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

// --- threaded injection ----------------------------------------------------

// inject posts one message and returns the decoded response.
func inject(t *testing.T, qm *QueuedMailbox, is *InjectSender, a *app.App, body string) injectResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/inject", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleInject(qm, is, a)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp injectResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestNormalizeMsgID(t *testing.T) {
	for in, want := range map[string]string{
		"<t1@x>":  "<t1@x>",
		"t1@x":    "<t1@x>", // the whole point: bare ids must still thread
		"<t1@x":   "<t1@x>",
		"t1@x>":   "<t1@x>",
		"  t1@x ": "<t1@x>",
		"":        "",
		"   ":     "",
	} {
		if got := normalizeMsgID(in); got != want {
			t.Errorf("normalizeMsgID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInjectDefaultsToNewThreadEachTime pins the pre-existing behaviour that
// made resumes untestable, so it stays a documented default rather than a
// surprise: with no message_id, every injection is its own thread.
func TestInjectDefaultsToNewThreadEachTime(t *testing.T) {
	a, qm, is := newTestApp(t, nil, nil)
	r1 := inject(t, qm, is, a, `{"from":"chris@x","body":"one"}`)
	r2 := inject(t, qm, is, a, `{"from":"chris@x","body":"two"}`)
	if r1.ThreadRoot == r2.ThreadRoot {
		t.Errorf("unthreaded injections should not share a thread root, both = %q", r1.ThreadRoot)
	}
}

// TestInjectReferencesSharesThreadRoot is the feature: a follow-up naming the
// first message in references lands in the same thread, so the agent session
// is resumed rather than opened cold. Without this the /inject endpoint can
// only ever exercise the isNew=true path.
func TestInjectReferencesSharesThreadRoot(t *testing.T) {
	run := &fakeRunner{out: "Hi Chris,\nanswer"}
	a, qm, is := newTestApp(t, nil, run)

	r1 := inject(t, qm, is, a, `{"from":"chris@x","body":"turn 1","message_id":"<t1@x>"}`)
	if r1.MessageID != "<t1@x>" {
		t.Errorf("MessageID = %q, want <t1@x>", r1.MessageID)
	}
	if r1.ThreadRoot != "<t1@x>" {
		t.Errorf("turn 1 ThreadRoot = %q, want its own id", r1.ThreadRoot)
	}

	r2 := inject(t, qm, is, a, `{"from":"chris@x","body":"turn 2","message_id":"<t2@x>","references":["<t1@x>"]}`)
	if r2.ThreadRoot != "<t1@x>" {
		t.Errorf("turn 2 ThreadRoot = %q, want <t1@x> (the root, not its own id)", r2.ThreadRoot)
	}

	// Same thread root => same session id in the registry, which is what
	// makes turn 2 a resume.
	sid1, _, _ := a.SessForTest().Resolve("<t1@x>")
	sid2, isNew, _ := a.SessForTest().Resolve(r2.ThreadRoot)
	if sid1 != sid2 || isNew {
		t.Errorf("turn 2 should resolve to the existing session (sid1=%q sid2=%q isNew=%v)", sid1, sid2, isNew)
	}
}

// TestInjectBareIDsStillThread covers the forgiving input path end to end:
// angle brackets omitted on both sides must still produce a shared thread.
func TestInjectBareIDsStillThread(t *testing.T) {
	a, qm, is := newTestApp(t, nil, nil)
	inject(t, qm, is, a, `{"from":"chris@x","body":"one","message_id":"t1@x"}`)
	r2 := inject(t, qm, is, a, `{"from":"chris@x","body":"two","message_id":"t2@x","references":["t1@x"]}`)
	if r2.ThreadRoot != "<t1@x>" {
		t.Errorf("ThreadRoot = %q, want <t1@x> — bare ids must be normalized, not dropped", r2.ThreadRoot)
	}
}

// TestInjectRejectsWhitespaceInIDs: a space would split the header field and
// the parser would drop the id, silently detaching the message from its
// thread. Better to fail the request than to return a wrong-looking success.
func TestInjectRejectsWhitespaceInIDs(t *testing.T) {
	a, qm, is := newTestApp(t, nil, nil)
	for _, body := range []string{
		`{"from":"chris@x","body":"x","message_id":"<t 1@x>"}`,
		`{"from":"chris@x","body":"x","references":["<t 1@x>"]}`,
	} {
		req := httptest.NewRequest("POST", "/inject", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleInject(qm, is, a)(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status=%d, want 400 for %s", rec.Code, body)
		}
	}
}

// TestComposeRFC822EmitsThreadHeaders checks the wire format the parser sees.
func TestComposeRFC822EmitsThreadHeaders(t *testing.T) {
	eml := string(composeRFC822("a@x", "b@x", "s", "<t2@x>", []string{"<t1@x>", "<t15@x>"}, "body"))
	if !strings.Contains(eml, "References: <t1@x> <t15@x>\r\n") {
		t.Errorf("References header malformed:\n%s", eml)
	}
	if !strings.Contains(eml, "In-Reply-To: <t15@x>\r\n") {
		t.Errorf("In-Reply-To should name the last reference:\n%s", eml)
	}
	// No refs => no thread headers at all, not empty ones.
	bare := string(composeRFC822("a@x", "b@x", "s", "<t1@x>", nil, "body"))
	if strings.Contains(bare, "References:") || strings.Contains(bare, "In-Reply-To:") {
		t.Errorf("unthreaded message should carry no thread headers:\n%s", bare)
	}
}
