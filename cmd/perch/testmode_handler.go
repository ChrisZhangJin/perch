//go:build testmode

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/replier"
)

// QueuedMailbox is an in-memory mailbox that satisfies app.Mailbox.
// FetchUnseen drains the queue and returns every queued Raw in enqueue order.
// MarkSeen and Close are no-ops. Not safe for concurrent use by multiple
// callers of FetchUnseen; the app's ProcessUnseen mutex serializes that path.
type QueuedMailbox struct {
	mu      sync.Mutex
	msgs    []mailbox.Raw
	nextUID uint32
}

func (q *QueuedMailbox) Enqueue(uid uint32, data []byte) uint32 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if uid == 0 {
		if q.nextUID == 0 {
			q.nextUID = 1
		}
		uid = q.nextUID
		q.nextUID++
	} else if uid >= q.nextUID {
		q.nextUID = uid + 1
	}
	q.msgs = append(q.msgs, mailbox.Raw{UID: uid, Data: data})
	return uid
}

func (q *QueuedMailbox) FetchUnseen(_ context.Context) ([]mailbox.Raw, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out, nil
}

func (q *QueuedMailbox) MarkSeen(_ context.Context, _ uint32) error { return nil }
func (q *QueuedMailbox) Close() error                               { return nil }

// replyCapture holds one outbound reply recorded by InjectSender.
type replyCapture struct {
	to, subject, inReplyTo, body, kind string
	cc, refs, attachments              []string
}

// InjectSender satisfies app.ReplySender by recording replies in memory and
// sending nothing at all. testmode builds wire this in place of the SMTP
// replier, so a message injected over HTTP can never produce a real outbound
// email -- the reply comes back in the POST /inject response instead.
//
// There is deliberately no delegate field: "testmode never sends SMTP" is a
// structural property of this type, not a configuration choice a future
// caller could flip.
type InjectSender struct {
	mu      sync.Mutex
	replies []replyCapture
}

func (i *InjectSender) Reply(env replier.Envelope, body string, attachments []string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.replies = append(i.replies, replyCapture{
		to: env.To, subject: env.Subject, inReplyTo: env.InReplyTo, kind: env.Kind,
		cc:          append([]string{}, env.Cc...),
		refs:        append([]string{}, env.References...),
		body:        body,
		attachments: append([]string{}, attachments...),
	})
	return nil
}

// Replies returns a snapshot of every reply captured since the last Clear,
// in send order. One ProcessUnseen can emit more than one: a long task sends
// the interim ack first, then the real answer.
func (i *InjectSender) Replies() []replyCapture {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]replyCapture, len(i.replies))
	copy(out, i.replies)
	return out
}

// LastReply returns the most recently captured reply (or nil).
func (i *InjectSender) LastReply() *replyCapture {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.replies) == 0 {
		return nil
	}
	last := i.replies[len(i.replies)-1]
	return &last
}

// Clear resets the captured replies.
func (i *InjectSender) Clear() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.replies = nil
}

// injectRequest is the JSON body for POST /inject.
//
// MessageID and References exist to build multi-turn threads. Without them
// every injection mints a fresh id and carries no References, so each one is
// a brand-new thread and the agent session is always opened cold — there is
// no way to exercise a resume, which is most of perch's threading behaviour.
//
// Turn 1 opens a thread; later turns reference its id:
//
//	{"from":"a@b","body":"...","message_id":"<t1@x>"}
//	{"from":"a@b","body":"...","message_id":"<t2@x>","references":["<t1@x>"]}
//
// Angle brackets are optional on input; they are added if missing.
type injectRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// MessageID overrides the synthetic <testmode-N@testmode> id.
	MessageID string `json:"message_id"`
	// References is the thread chain. Entry 0 is the thread root, which is
	// what message.ThreadRoot keys the session registry on.
	References []string `json:"references"`
}

// normalizeMsgID trims s and wraps it in angle brackets when the caller left
// them off. message.allMsgIDs only recognises <...>-wrapped ids, so a bare
// "t1@x" in References parses to nothing, ThreadRoot silently falls back to
// the message's own id, and the injection opens a NEW thread instead of
// resuming the one the caller named — a wrong result that looks like a
// working request. Returns "" for empty input.
func normalizeMsgID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "<") {
		s = "<" + s
	}
	if !strings.HasSuffix(s, ">") {
		s += ">"
	}
	return s
}

// injectResponse is the JSON response from POST /inject. Replies carries
// every reply the run produced, in send order; Reply points at the last one
// (the real answer, when a long-task ack preceded it).
type injectResponse struct {
	UID uint32 `json:"uid"`
	// MessageID is the id this injection was sent under (echoed back so a
	// script can chain it into the next turn's references).
	MessageID string `json:"message_id"`
	// ThreadRoot is the key the session registry uses for this message:
	// references[0] when present, else message_id. Two injections sharing a
	// thread root share an agent session.
	ThreadRoot string          `json:"thread_root"`
	Reply      *replyResponse  `json:"reply,omitempty"`
	Replies    []replyResponse `json:"replies,omitempty"`
}

type replyResponse struct {
	To      string   `json:"to"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	Refs    []string `json:"refs,omitempty"`
}

func handleInject(qm *QueuedMailbox, is *InjectSender, a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req injectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"bad JSON: %s"}`, err), http.StatusBadRequest)
			return
		}
		if req.From == "" {
			http.Error(w, `{"error":"from is required"}`, http.StatusBadRequest)
			return
		}
		if req.To == "" {
			req.To = "agent@perch.local"
		}
		if req.Subject == "" {
			req.Subject = "(no subject)"
		}

		// An id with whitespace would split across fields in the header and
		// be dropped by the parser, silently detaching the message from its
		// thread. Reject instead.
		for _, raw := range append([]string{req.MessageID}, req.References...) {
			if strings.ContainsAny(raw, " \t\r\n") {
				http.Error(w, `{"error":"message_id/references must not contain whitespace"}`, http.StatusBadRequest)
				return
			}
		}
		msgID := normalizeMsgID(req.MessageID)
		if msgID == "" {
			seq++
			msgID = fmt.Sprintf("<testmode-%d@testmode>", seq)
		}
		refs := make([]string, 0, len(req.References))
		for _, r := range req.References {
			if n := normalizeMsgID(r); n != "" {
				refs = append(refs, n)
			}
		}

		eml := composeRFC822(req.From, req.To, req.Subject, msgID, refs, req.Body)
		uid := qm.Enqueue(0, eml)

		is.Clear()
		if err := a.ProcessUnseen(r.Context()); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"process: %s"}`, err), http.StatusInternalServerError)
			return
		}

		threadRoot := msgID
		if len(refs) > 0 {
			threadRoot = refs[0]
		}
		resp := injectResponse{UID: uid, MessageID: msgID, ThreadRoot: threadRoot}
		for _, rp := range is.Replies() {
			resp.Replies = append(resp.Replies, replyResponse{
				To: rp.to, Subject: rp.subject, Body: rp.body, Refs: rp.refs,
			})
		}
		if n := len(resp.Replies); n > 0 {
			resp.Reply = &resp.Replies[n-1]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// seq is a monotonic counter for synthetic Message-IDs in testmode.
var seq uint64

// composeRFC822 builds a minimal RFC822 message with CRLF line endings.
// refs, when non-empty, becomes the References header (space-separated, the
// form message.allMsgIDs parses) plus an In-Reply-To pointing at the last
// entry, which is what a real client sends. perch threads on References
// alone; In-Reply-To is there so the fixture looks like real mail.
func composeRFC822(from, to, subject, msgID string, refs []string, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Message-ID: %s\r\n", msgID)
	if len(refs) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", strings.Join(refs, " "))
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", refs[len(refs)-1])
	}
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}
