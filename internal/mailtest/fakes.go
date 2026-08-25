// Package mailtest provides in-process fakes for perch's IMAP mailbox and
// SMTP reply paths, plus a Mailtest harness that wires them into a real
// *app.App. Use it to drive perch's email pipeline end-to-end without IMAP,
// SMTP, Docker, or a network.
//
// The fakes are not safe for concurrent use; the underlying *app.App already
// serializes ProcessUnseen on a single goroutine, so single-threaded callers
// (the typical test) are fine.
package mailtest

import (
	"context"
	"sync"

	"github.com/ChrisZhangJin/perch/internal/mailbox"
)

// FakeMailbox satisfies mailbox.Mailbox. FetchUnseen drains the queue and
// returns every queued Raw in enqueue order; MarkSeen records the UID.
// Close is a no-op. Not safe for concurrent use.
type FakeMailbox struct {
	mu      sync.Mutex
	msgs    []mailbox.Raw
	seen    []uint32
	nextUID uint32
}

// Enqueue appends data to the mailbox queue. If uid is 0, the next sequential
// UID is assigned; the first auto-assigned UID is 1 (UID 0 is reserved for
// "unassigned" by SendRaw's validation). Otherwise the caller-chosen uid is
// used and nextUID is advanced past it so subsequent auto-assignments don't
// collide.
func (f *FakeMailbox) Enqueue(uid uint32, data []byte) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if uid == 0 {
		if f.nextUID == 0 {
			f.nextUID = 1
		}
		uid = f.nextUID
		f.nextUID++
	} else if uid >= f.nextUID {
		f.nextUID = uid + 1
	}
	f.msgs = append(f.msgs, mailbox.Raw{UID: uid, Data: data})
	return uid
}

func (f *FakeMailbox) FetchUnseen(ctx context.Context) ([]mailbox.Raw, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.msgs
	f.msgs = nil
	return out, nil
}

func (f *FakeMailbox) MarkSeen(ctx context.Context, uid uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, uid)
	return nil
}

func (f *FakeMailbox) Close() error { return nil }

// Reply captures one outbound reply recorded by FakeSender. Returned from
// Mailtest.Replies. The body is the text the agent produced (post-greeting
// scan when applicable), not the raw agent stdout.
type Reply struct {
	To, Subject, InReplyTo string
	References             []string
	Body                   string
	Attachments            []string
}

// FakeSender satisfies app.ReplySender. Records every Reply call in
// send-order; Replies returns a snapshot copy.
type FakeSender struct {
	mu      sync.Mutex
	replies []Reply
}

func (s *FakeSender) Reply(to, subject, inReplyTo string, refs []string, body string, attachments []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, Reply{
		To: to, Subject: subject, InReplyTo: inReplyTo,
		References: append([]string{}, refs...),
		Body:       body, Attachments: append([]string{}, attachments...),
	})
	return nil
}

// Replies returns a deep-copy snapshot of the recorded replies. Mutating any
// field of a returned Reply (including inner slice fields like References and
// Attachments) does not affect later Replies calls.
func (s *FakeSender) Replies() []Reply {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Reply, len(s.replies))
	copy(out, s.replies)
	for i := range out {
		out[i].References = append([]string{}, out[i].References...)
		out[i].Attachments = append([]string{}, out[i].Attachments...)
	}
	return out
}

// ScriptedRunner satisfies app.TaskRunner. Outs is the canned stdout returned
// per Run call (call N returns Outs[N]); when N >= len(Outs), the last entry
// is reused (defensive — keeps miscounting tests from panicking). Prompts,
// SIDs, and IsNews record every call for assertions. When Native is non-empty
// it is returned as the second value of Run (the agent-minted session id
// perch adopts).
type ScriptedRunner struct {
	mu      sync.Mutex
	Outs    []string
	Native  string
	Prompts []string
	SIDs    []string
	IsNews  []bool
}

func (r *ScriptedRunner) Run(ctx context.Context, prompt, sid string, isNew bool) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Prompts = append(r.Prompts, prompt)
	r.SIDs = append(r.SIDs, sid)
	r.IsNews = append(r.IsNews, isNew)
	idx := len(r.Prompts) - 1
	if idx >= len(r.Outs) {
		idx = len(r.Outs) - 1
	}
	if idx < 0 {
		return "", r.Native, nil
	}
	return r.Outs[idx], r.Native, nil
}
