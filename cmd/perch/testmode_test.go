//go:build testmode

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	plog "github.com/ChrisZhangJin/perch/internal/log"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// quietLogger returns a slog.Logger that discards all output, so test
// output stays clean when the app logs warnings.
func quietLogger() *slog.Logger {
	return slog.New(plog.New(io.Discard, slog.LevelError))
}

// newTestApp builds a *app.App with QueuedMailbox, real gate, and an
// in-memory session registry. The caller swaps the mailbox/sender as needed.
func newTestApp(t *testing.T, cfg *config.Config, run app.TaskRunner) (*app.App, *QueuedMailbox, *InjectSender) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{MaxPromptBytes: 4096, AgentWorkdir: t.TempDir()}
	}
	g, err := gate.New([]string{"*"})
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	sess, err := session.Load(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatalf("session.Load: %v", err)
	}
	if run == nil {
		run = &fakeRunner{out: "the answer"}
	}
	qm := &QueuedMailbox{}
	is := &InjectSender{}
	a := app.New(cfg, qm, g, sess, run, is, quietLogger())
	a.SetMailboxForTest(qm)
	a.SetReplySenderForTest(is)
	return a, qm, is
}

func TestQueuedMailboxAutoUIDStartsAtOne(t *testing.T) {
	q := &QueuedMailbox{}
	q.Enqueue(0, []byte("a"))
	q.Enqueue(0, []byte("b"))

	got, _ := q.FetchUnseen(context.Background())
	if len(got) != 2 || got[0].UID != 1 || got[1].UID != 2 {
		t.Errorf("got UIDs (%d,%d), want (1,2)", got[0].UID, got[1].UID)
	}
}

func TestQueuedMailboxCallerUIDAdvancesNextUID(t *testing.T) {
	q := &QueuedMailbox{nextUID: 5}
	q.Enqueue(42, []byte("a"))
	q.Enqueue(0, []byte("b"))
	got, _ := q.FetchUnseen(context.Background())
	if len(got) != 2 || got[0].UID != 42 || got[1].UID != 43 {
		t.Errorf("got UIDs (%d,%d), want (42,43)", got[0].UID, got[1].UID)
	}
}

func TestQueuedMailboxDrains(t *testing.T) {
	q := &QueuedMailbox{nextUID: 1}
	q.Enqueue(0, []byte("a"))
	q.Enqueue(0, []byte("b"))
	got1, _ := q.FetchUnseen(context.Background())
	if len(got1) != 2 {
		t.Fatalf("first fetch got %d, want 2", len(got1))
	}
	got2, _ := q.FetchUnseen(context.Background())
	if len(got2) != 0 {
		t.Errorf("second fetch should drain, got %d", len(got2))
	}
}

func TestQueuedMailboxMarkSeenAndCloseNoOp(t *testing.T) {
	q := &QueuedMailbox{}
	if err := q.MarkSeen(context.Background(), 99); err != nil {
		t.Errorf("MarkSeen: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestInjectSenderCaptures(t *testing.T) {
	is := &InjectSender{}
	if err := is.Reply("alice@x", "Re: hi", "<r@x>", []string{"<r@x>"}, "body", []string{"/tmp/a"}); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	rp := is.LastReply()
	if rp == nil {
		t.Fatal("LastReply should be non-nil")
	}
	if rp.to != "alice@x" || rp.body != "body" || rp.subject != "Re: hi" {
		t.Errorf("captured: %+v", rp)
	}
}

// TestInjectSenderSendsNothing is the guard for the whole point of
// testmode: InjectSender must satisfy app.ReplySender without holding any
// outbound transport, so an injected message cannot reach a real inbox.
// If someone re-adds a delegate field, this stops compiling or the
// zero-value construction below stops being safe.
func TestInjectSenderSendsNothing(t *testing.T) {
	var s app.ReplySender = &InjectSender{} // zero value must be usable
	if err := s.Reply("alice@x", "s", "<i@x>", nil, "b", nil); err != nil {
		t.Fatalf("zero-value InjectSender must accept replies without a transport: %v", err)
	}
}

// TestInjectSenderCapturesEveryReply covers the long-task flow: the ack and
// the real answer are two separate Reply calls, and both must survive --
// with SMTP gone, a dropped reply would vanish without a trace.
func TestInjectSenderCapturesEveryReply(t *testing.T) {
	is := &InjectSender{}
	_ = is.Reply("a@x", "s", "<i@x>", nil, "the ack", nil)
	_ = is.Reply("a@x", "s", "<i@x>", nil, "the answer", nil)

	rs := is.Replies()
	if len(rs) != 2 {
		t.Fatalf("Replies() = %d, want 2", len(rs))
	}
	if rs[0].body != "the ack" || rs[1].body != "the answer" {
		t.Errorf("out of order: %q then %q", rs[0].body, rs[1].body)
	}
	if last := is.LastReply(); last == nil || last.body != "the answer" {
		t.Errorf("LastReply should be the real answer, got %+v", last)
	}
}

func TestInjectSenderClearResets(t *testing.T) {
	is := &InjectSender{}
	_ = is.Reply("a", "s", "i", nil, "b", nil)
	is.Clear()
	if is.LastReply() != nil {
		t.Errorf("Clear should reset; got %+v", is.LastReply())
	}
	if len(is.Replies()) != 0 {
		t.Errorf("Clear should empty Replies, got %d", len(is.Replies()))
	}
}

func TestHandleInjectReturnsUIDAndReply(t *testing.T) {
	run := &fakeRunner{out: "Hi Chris,\nanswer"}
	a, qm, is := newTestApp(t, nil, run)

	body := `{"from":"chris@x","to":"agent@x","subject":"hi","body":"do the thing"}`
	req := httptest.NewRequest("POST", "/inject", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleInject(qm, is, a)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp injectResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.UID != 1 {
		t.Errorf("UID=%d, want 1", resp.UID)
	}
	if resp.Reply == nil {
		t.Fatal("reply should be set")
	}
	if resp.Reply.To != "chris@x" {
		t.Errorf("Reply.To=%q, want chris@x", resp.Reply.To)
	}
	if resp.Reply.Body != "Hi Chris,\nanswer" {
		t.Errorf("Reply.Body=%q, want Hi Chris,\\nanswer", resp.Reply.Body)
	}
	if len(resp.Replies) != 1 {
		t.Errorf("Replies should carry the single reply, got %d", len(resp.Replies))
	}
	if len(is.Replies()) != 1 {
		t.Errorf("sender should have captured one reply, got %d", len(is.Replies()))
	}
}

func TestHandleInjectNoReplyForNonWhitelisted(t *testing.T) {
	cfg := &config.Config{MaxPromptBytes: 4096, AgentWorkdir: t.TempDir()}
	g, err := gate.New([]string{"alice@x"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Load(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	qm := &QueuedMailbox{}
	is := &InjectSender{}
	a := app.New(cfg, qm, g, sess, &fakeRunner{out: ""}, is, quietLogger())
	a.SetMailboxForTest(qm)
	a.SetReplySenderForTest(is)

	body := `{"from":"mallory@evil","subject":"pwn","body":"x"}`
	req := httptest.NewRequest("POST", "/inject", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleInject(qm, is, a)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var resp injectResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.UID != 1 {
		t.Errorf("UID=%d, want 1", resp.UID)
	}
	if resp.Reply != nil {
		t.Errorf("non-whitelisted should produce no reply, got %+v", resp.Reply)
	}
	if len(is.Replies()) != 0 {
		t.Errorf("sender should not be called for non-whitelisted, got %d", len(is.Replies()))
	}
}

func TestHandleInjectRejectsBadJSON(t *testing.T) {
	a, qm, is := newTestApp(t, nil, nil)

	req := httptest.NewRequest("POST", "/inject", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	handleInject(qm, is, a)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rec.Code)
	}
}

func TestHandleInjectRejectsEmptyFrom(t *testing.T) {
	a, qm, is := newTestApp(t, nil, nil)

	req := httptest.NewRequest("POST", "/inject", strings.NewReader(`{"from":"","body":"x"}`))
	rec := httptest.NewRecorder()
	handleInject(qm, is, a)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rec.Code)
	}
}

// fakeRunner is a minimal app.TaskRunner that returns canned output.
type fakeRunner struct {
	out string
}

func (f *fakeRunner) Run(_ context.Context, _, _ string, _ bool) (string, string, error) {
	return f.out, "", nil
}

// ensure mailbox.Raw compiles in this file
var _ = mailbox.Raw{}