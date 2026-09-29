package app_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
)

func TestParseEndDetect(t *testing.T) {
	// The probability returned is always P(over), never "the probability of
	// whichever option won" — a confident "reply" must come back as a low
	// number so the caller's threshold rejects it, not as a high one.
	prob, err := app.ParseEndDetect(map[string]jev.Answer{
		"ended": {Type: jev.TypeChoice, Choice: "reply",
			Probabilities: map[string]float64{"over": 0.04, "reply": 0.96}},
	})
	if err != nil || prob != 0.04 {
		t.Errorf("got (%v, %v), want (0.04, nil)", prob, err)
	}

	prob, err = app.ParseEndDetect(map[string]jev.Answer{
		"ended": {Type: jev.TypeChoice, Choice: "over",
			Probabilities: map[string]float64{"over": 0.97, "reply": 0.03}},
	})
	if err != nil || prob != 0.97 {
		t.Errorf("got (%v, %v), want (0.97, nil)", prob, err)
	}

	// An unrecognised choice is an error, never coerced. Coercing toward
	// "over" would drop a real request on a value nobody understood.
	if _, err := app.ParseEndDetect(map[string]jev.Answer{
		"ended": {Type: jev.TypeChoice, Choice: "maybe",
			Probabilities: map[string]float64{"maybe": 1}},
	}); err == nil {
		t.Error("unrecognised choice must be an error")
	}

	// No probabilities at all is also an error: Probabilities["over"] would
	// otherwise read 0, which happens to be safe here but only by accident.
	if _, err := app.ParseEndDetect(map[string]jev.Answer{
		"ended": {Type: jev.TypeChoice, Choice: "over"},
	}); err == nil {
		t.Error("a missing probability map must be an error")
	}

	if _, err := app.ParseEndDetect(map[string]jev.Answer{}); err == nil {
		t.Error("a missing answer must be an error")
	}
}

// endDetectServer answers every call with the given P(over), and counts
// calls so a test can assert that Jev was never consulted at all.
func endDetectServer(t *testing.T, probOver float64, calls *int) *httptest.Server {
	t.Helper()
	choice := "reply"
	if probOver >= 0.5 {
		choice = "over"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		fmt.Fprintf(w, `{"answers":{"ended":{"type":"choice","choice":%q,
			"probabilities":{"over":%v,"reply":%v},"confidence":0.5}}}`,
			choice, probOver, 1-probOver)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newEndDetectApp(t *testing.T, url string) *mailtest.Mailtest {
	t.Helper()
	cfg := config.Defaults()
	cfg.MaxPromptBytes = 4096
	cfg.AgentWorkdir = t.TempDir()
	cfg.EndDetect = true
	cfg.JevAPIKey = "test-key"
	cfg.JevAPIURL = url
	mt, err := mailtest.New(cfg, []string{"alice@163.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

// rawMail builds an RFC822 message. When inReplyTo is empty the message is a
// thread root; otherwise it is a continuation, which is the only shape
// end-detection is allowed to act on.
func rawMail(msgID, subject, inReplyTo, body string) []byte {
	var b strings.Builder
	b.WriteString("From: alice@163.com\r\nTo: agent@163.com\r\n")
	fmt.Fprintf(&b, "Subject: %s\r\nMessage-ID: %s\r\n", subject, msgID)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\nReferences: %s\r\n", inReplyTo, inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body + "\r\n")
	return []byte(b.String())
}

// TestEndDetectSkipsClosingReply is the happy path: a Chinese sign-off on a
// thread continuation, Jev confident it is over, so perch answers nothing
// and marks the mail seen. Chinese on purpose — the closings perch actually
// receives are as often "好的，谢谢" as "thanks".
func TestEndDetectSkipsClosingReply(t *testing.T) {
	var calls int
	mt := newEndDetectApp(t, endDetectServer(t, 0.97, &calls).URL)

	if err := mt.SendRaw(7, rawMail("<c1@mailtest>", "Re: the report",
		"<root@mailtest>", "好的，谢谢！")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Errorf("jev calls = %d, want 1", calls)
	}
	if rs := mt.Replies(); len(rs) != 0 {
		t.Errorf("a closing must get no reply, got %d: %#v", len(rs), rs)
	}
	// Marked seen, or the same "thanks" is re-examined on every poll for
	// as long as it sits in the inbox.
	if seen := mt.SeenUIDs(); len(seen) != 1 || seen[0] != 7 {
		t.Errorf("seen = %v, want [7]", seen)
	}
}

// TestEndDetectBelowThresholdStillReplies pins the asymmetry. 0.85 is the
// model leaning "over", and perch still answers: the cost of being wrong in
// the quiet direction is a silently dropped request.
func TestEndDetectBelowThresholdStillReplies(t *testing.T) {
	var calls int
	mt := newEndDetectApp(t, endDetectServer(t, 0.85, &calls).URL)

	if err := mt.SendRaw(8, rawMail("<c2@mailtest>", "Re: the report",
		"<root@mailtest>", "thanks — and can you check the totals?")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Errorf("jev calls = %d, want 1", calls)
	}
	if rs := mt.Replies(); len(rs) != 1 {
		t.Fatalf("below the threshold perch must still reply, got %d", len(rs))
	}
}

// TestEndDetectIgnoresThreadRoots is the blast-radius bound: identical
// content, but no In-Reply-To/References, so Jev is never even asked. A
// first contact reading "ok" is far more likely to be a terse instruction
// than a goodbye, and there is no prior turn for a "thanks" to thank.
func TestEndDetectIgnoresThreadRoots(t *testing.T) {
	var calls int
	mt := newEndDetectApp(t, endDetectServer(t, 0.97, &calls).URL)

	if err := mt.SendRaw(9, rawMail("<r1@mailtest>", "the report", "", "好的，谢谢！")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls != 0 {
		t.Errorf("a thread root must not reach jev at all, got %d calls", calls)
	}
	if rs := mt.Replies(); len(rs) != 1 {
		t.Fatalf("a new email is always processed, got %d replies", len(rs))
	}
}

// TestEndDetectErrorRepliesAnyway: a 500 from the API must never turn into
// silence. Every failure mode lands on "process normally".
func TestEndDetectErrorRepliesAnyway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	mt := newEndDetectApp(t, srv.URL)

	if err := mt.SendRaw(10, rawMail("<c3@mailtest>", "Re: x", "<root@mailtest>", "ok")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rs := mt.Replies(); len(rs) != 1 {
		t.Fatalf("a failed check must fall through to a normal reply, got %d", len(rs))
	}
}
