package replier

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
)

func TestComposeHeaders(t *testing.T) {
	msg := Compose("agent@163.com", Envelope{
		To:         "alice@163.com",
		Subject:    "Do X",
		InReplyTo:  "<root-1@163.com>",
		References: []string{"<thread-root@163.com>", "<root-1@163.com>"},
	}, "here is the result")
	s := string(msg)
	checks := []string{
		"From: agent@163.com",
		"To: alice@163.com",
		"Subject: Re: Do X",
		"In-Reply-To: <root-1@163.com>",
		"References: <thread-root@163.com> <root-1@163.com>",
		"Content-Type: text/plain; charset=utf-8",
		"here is the result",
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("composed message missing %q\n---\n%s", c, s)
		}
	}
}

func TestComposeDoesNotDoubleRe(t *testing.T) {
	msg := Compose("a@x", Envelope{To: "b@x", Subject: "Re: Already", InReplyTo: "<i@x>"}, "body")
	if strings.Contains(string(msg), "Subject: Re: Re: Already") {
		t.Error("should not double-prefix Re:")
	}
}

func TestSanitizeHeaderStripsCRLF(t *testing.T) {
	got := sanitizeHeader("evil\r\nBcc: victim@x")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("CRLF not stripped: %q", got)
	}
}

func TestComposeWithAttachments(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg, err := ComposeWithAttachments("agent@163.com", Envelope{
		To: "alice@163.com", Subject: "Do X", InReplyTo: "<root-1@163.com>",
		References: []string{"<thread-root@163.com>"},
	}, "here is the result", []string{f})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	for _, c := range []string{
		"Subject: Re: Do X",
		"Content-Type: multipart/mixed",
		"Content-Disposition: attachment; filename=report.txt",
		"ZGF0YQ==", // base64("data")
	} {
		if !strings.Contains(s, c) {
			t.Errorf("multipart message missing %q\n---\n%s", c, s)
		}
	}
}

// --- retry + failure-notification tests ------------------------------------

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("smtp data write: broken pipe"), true},
		{errors.New("read tcp: connection reset by peer"), true},
		{errors.New("dial tcp: connection refused"), true},
		{errors.New("read tcp: i/o timeout"), true},
		{errors.New("smtp mail: 421 Service not available"), true},
		{errors.New("smtp mail: 451 mailbox temporarily unavailable"), true},
		{errors.New("smtp auth: 535 Authentication credentials invalid"), false},
		{errors.New("smtp mail: 553 mail from syntax error"), false},
		{io.EOF, true},
	}
	for _, c := range cases {
		if got := isTransient(c.err); got != c.want {
			t.Errorf("isTransient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestComposeFailureBodyIncludesLastErr(t *testing.T) {
	body := composeFailureBody("alice@x", "Hi", 3,
		errors.New("smtp data close: broken pipe"), 1234, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"3 attempt(s)",
		"Original subject: Hi",
		"Reply size:       1234 bytes",
		"Last error:",
		"broken pipe",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failure body missing %q\n---\n%s", want, body)
		}
	}
}

func TestComposeFailurePrefixesSubject(t *testing.T) {
	msg := ComposeFailure("agent@x", "alice@x", "Hi", "<r@x>", []string{"<r@x>"}, "body")
	if !strings.Contains(string(msg), "Subject: Perch failed: Hi") {
		t.Errorf("expected Perch failed: prefix in subject; got:\n%s", msg)
	}
	// Idempotent: don't double-prefix
	msg2 := ComposeFailure("agent@x", "alice@x", "Perch failed: Hi", "<r@x>", nil, "body")
	if strings.Contains(string(msg2), "Subject: Perch failed: Perch failed:") {
		t.Errorf("should not double-prefix; got:\n%s", msg2)
	}
}

// smtpStep is a minimal in-process SMTP server for the retry tests. It
// speaks the bare subset perch uses (EHLO/AUTH/MAIL/RCPT/DATA/QUIT/RSET)
// and lets the test decide per-DATA-call whether to drop mid-stream or
// complete normally — modelling 163's mid-DATA RST (which produces the
// "broken pipe" we want perch to retry).
type smtpStep struct {
	ln       net.Listener
	failData func(call int) bool // true => drop the conn after the DATA payload
	failAuth func(call int) bool // true => answer this AUTH with 535, as 163 does
	calls    int32
	auths    int32
	closed   atomic.Bool
}

func startSMTPStep(t *testing.T, failData func(int) bool) *smtpStep {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpStep{ln: ln, failData: failData}
	go s.acceptLoop(t)
	return s
}

func (s *smtpStep) addr() string { return s.ln.Addr().String() }
func (s *smtpStep) Close()       { s.ln.Close(); s.closed.Store(true) }

func (s *smtpStep) acceptLoop(t *testing.T) {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(t, c)
	}
}

// handle is a minimal SMTP server. Replies 250/235 to the verbs perch uses.
// If failData(call) returns true for the current DATA call, we close the
// connection right after the client sends the message — modelling 163's
// mid-stream RST. The client side then sees "broken pipe" on Write/Close.
func (s *smtpStep) handle(t *testing.T, c net.Conn) {
	defer c.Close()
	br := newBufReader(c)
	bw := newBufWriter(c)

	writeLine := func(s string) { bw.writeString(s + "\r\n") }
	writeLine("220 perch-test ESMTP ready")

	// Smtp conn state machine.
	for {
		line, err := br.readLine()
		if err != nil {
			return
		}
		verb := line
		if i := strings.IndexByte(line, ' '); i >= 0 {
			verb = line[:i]
		}
		verb = strings.ToUpper(verb)
		switch verb {
		case "EHLO", "HELO":
			writeLine("250-perch-test")
			writeLine("250 OK")
		case "AUTH":
			if s.failAuth != nil && s.failAuth(int(atomic.AddInt32(&s.auths, 1))) {
				writeLine("535 Error: authentication failed")
				continue
			}
			writeLine("235 OK")
		case "MAIL":
			writeLine("250 OK")
		case "RCPT":
			writeLine("250 OK")
		case "DATA":
			writeLine("354 Send data, end with .")
			// Drain payload until "." terminator.
			for {
				l, err := br.readLine()
				if err != nil {
					return
				}
				if l == "." {
					break
				}
			}
			call := int(atomic.AddInt32(&s.calls, 1))
			if s.failData != nil && s.failData(call) {
				// Hard-close mid-DATA. Client sees broken pipe.
				c.Close()
				return
			}
			writeLine("250 OK queued")
		case "QUIT":
			writeLine("221 Bye")
			return
		case "RSET":
			writeLine("250 OK")
		case "NOOP":
			writeLine("250 OK")
		default:
			writeLine("500 Unknown")
		}
	}
}

// bufReader / bufWriter are minimal line helpers (net.Conn doesn't speak
// lines on its own). They avoid pulling in bufio's full Scan API.
type bufReader struct {
	conn net.Conn
	buf  []byte
}

func newBufReader(c net.Conn) *bufReader { return &bufReader{conn: c} }

func (b *bufReader) readLine() (string, error) {
	for {
		// Look for CRLF already buffered.
		for i := 0; i < len(b.buf)-1; i++ {
			if b.buf[i] == '\r' && b.buf[i+1] == '\n' {
				line := string(b.buf[:i])
				b.buf = b.buf[i+2:]
				return line, nil
			}
		}
		// Read more.
		tmp := make([]byte, 1024)
		n, err := b.conn.Read(tmp)
		if n > 0 {
			b.buf = append(b.buf, tmp[:n]...)
		}
		if err != nil {
			return "", err
		}
	}
}

type bufWriter struct{ conn net.Conn }

func newBufWriter(c net.Conn) *bufWriter  { return &bufWriter{conn: c} }
func (b *bufWriter) writeString(s string) { _, _ = b.conn.Write([]byte(s)) }

// plaintextDialer returns a fresh net.Conn to the fake SMTP listener. The
// production code uses tls.Dial; tests inject this dialer to skip TLS.
func plaintextDialer(srvAddr string) func(string, string, bool) (net.Conn, error) {
	return func(addr, _ string, _ bool) (net.Conn, error) {
		return net.Dial("tcp", srvAddr)
	}
}

// newReplierForFakeSMTP builds a Replier that talks plaintext to the fake.
// smtpAddr is set to the fake's listening address so splitHostPort works.
func newReplierForFakeSMTP(fakeAddr string, maxAttempts int) *Replier {
	return &Replier{
		cfg: &config.Config{
			Email:       "agent@perch.test",
			AuthCode:    "anything",
			TLSInsecure: true,
		},
		smtpAddr:    fakeAddr,
		MaxAttempts: maxAttempts,
		retryDelay:  5 * time.Millisecond,
		// Production defaults this to 5s (163's throttle window). Shrink it or
		// the auth-retry test below sleeps for five real seconds.
		authRetryDelay: 5 * time.Millisecond,
		dial:           plaintextDialer(fakeAddr),
	}
}

// TestReplyRetriesAuthRejection is the 2026-09-29 incident in miniature: 163
// answered the first AUTH with `535 Error: authentication failed`, then
// accepted the very same credentials on the next connection a second later.
// perch treated 535 as permanent and dropped the reply on the floor. It must
// now retry, on a longer backoff, and the reply must go out.
func TestReplyRetriesAuthRejection(t *testing.T) {
	srv := startSMTPStep(t, nil)
	srv.failAuth = func(call int) bool { return call == 1 } // reject the first login only
	defer srv.Close()

	r := newReplierForFakeSMTP(srv.addr(), 3)

	var attempts []int
	r.SetHook(func(attempt int, _ error, _ int) { attempts = append(attempts, attempt) })

	if err := r.Reply(Envelope{To: "alice@perch.test", Subject: "test", InReplyTo: "<r@x>"}, "hello", nil); err != nil {
		t.Fatalf("a 535 on the first login must not lose the reply, got: %v", err)
	}
	if len(attempts) != 2 {
		t.Errorf("hook fired %d times, want 2 (rejected login + successful retry)", len(attempts))
	}
	if got := atomic.LoadInt32(&srv.auths); got != 2 {
		t.Errorf("server saw %d AUTH commands, want 2", got)
	}
	if got := atomic.LoadInt32(&srv.calls); got != 1 {
		t.Errorf("server saw %d DATA calls, want 1 (only the retry gets that far)", got)
	}
	// An auth rejection must back off on authRetryDelay, not retryDelay.
	if r.backoff(fmt.Errorf("%w: 535", ErrAuth)) != r.authRetryDelay {
		t.Error("ErrAuth should pick the longer auth backoff")
	}
	if r.backoff(errors.New("smtp data write: broken pipe")) != r.retryDelay {
		t.Error("a network blip should keep the ordinary backoff")
	}
}

func TestReplyRetriesAcrossTransientDrop(t *testing.T) {
	srv := startSMTPStep(t, func(call int) bool { return call == 1 }) // drop first only
	defer srv.Close()

	r := newReplierForFakeSMTP(srv.addr(), 3)

	var hookCalls []int
	r.SetHook(func(attempt int, err error, size int) {
		hookCalls = append(hookCalls, attempt)
	})

	if err := r.Reply(Envelope{To: "alice@perch.test", Subject: "test", InReplyTo: "<r@x>"},
		"hello", nil); err != nil {
		t.Fatalf("expected success on 2nd attempt, got: %v", err)
	}
	if len(hookCalls) != 2 {
		t.Errorf("hook fired %d times, want 2 (attempts 1+2)", len(hookCalls))
	}
	if atomic.LoadInt32(&srv.calls) != 2 {
		t.Errorf("server saw %d DATA calls, want 2", srv.calls)
	}
}

func TestReplyExhaustsAndReturnsAggregatedError(t *testing.T) {
	srv := startSMTPStep(t, func(call int) bool { return true }) // always drop
	defer srv.Close()

	r := newReplierForFakeSMTP(srv.addr(), 3)

	var hookErrs []error
	r.SetHook(func(_ int, err error, _ int) { hookErrs = append(hookErrs, err) })

	err := r.Reply(Envelope{To: "alice@perch.test", Subject: "test", InReplyTo: "<r@x>"}, "hello", nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error should mention attempt count: %v", err)
	}
	// Last error message from a hard-closed server typically surfaces as
	// "EOF" or "broken pipe" depending on OS / timing; accept either.
	if !strings.Contains(err.Error(), "broken pipe") && !strings.Contains(err.Error(), "EOF") {
		t.Errorf("error should preserve a transient cause (broken pipe / EOF), got: %v", err)
	}
	if len(hookErrs) != 3 {
		t.Errorf("hook should fire 3 times, got %d", len(hookErrs))
	}
	for i, e := range hookErrs {
		if e == nil {
			t.Errorf("hook call %d had nil err (every attempt should fail)", i+1)
		}
	}
}

func TestReplySucceedsOnFirstTryNoRetry(t *testing.T) {
	srv := startSMTPStep(t, nil) // never drop
	defer srv.Close()

	r := newReplierForFakeSMTP(srv.addr(), 3)
	var calls int
	r.SetHook(func(_ int, _ error, _ int) { calls++ })

	if err := r.Reply(Envelope{To: "alice@perch.test", Subject: "test", InReplyTo: "<r@x>"}, "hello", nil); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("hook should fire exactly once on success, got %d", calls)
	}
}

// TestComposeStampsAutoSubmitted pins the outbound half of loop protection.
// Every composer must emit RFC 3834's Auto-Submitted: auto-replied, so a
// compliant counterparty does not answer perch's reply. Two robots that both
// omit it exchange mail indefinitely.
func TestComposeStampsAutoSubmitted(t *testing.T) {
	const want = "Auto-Submitted: auto-replied\r\n"

	plain := string(Compose("a@x", Envelope{To: "b@x", Subject: "hi", InReplyTo: "<i@x>", References: []string{"<i@x>"}}, "body"))
	if !strings.Contains(plain, want) {
		t.Errorf("Compose is missing the header:\n%s", plain)
	}

	f := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	multi, err := ComposeWithAttachments("a@x", Envelope{To: "b@x", Subject: "hi", InReplyTo: "<i@x>"}, "body", []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(multi), want) {
		t.Errorf("ComposeWithAttachments is missing the header:\n%s", multi)
	}

	fail := string(ComposeFailure("a@x", "b@x", "hi", "<i@x>", nil, "body"))
	if !strings.Contains(fail, want) {
		t.Errorf("ComposeFailure is missing the header:\n%s", fail)
	}
}

// TestReplySubjectPrefixes pins which subjects already count as replies. The
// ASCII-only check this replaced could not see 回复：/答复：, so a Chinese
// client's thread grew one prefix pair per round:
// "回复：x" → "Re: 回复：x" → "回复：Re: 回复：x" → …
func TestReplySubjectPrefixes(t *testing.T) {
	unchanged := []string{
		"Re: Already",
		"RE: shouty",
		"re: lower",
		"Re:no space",
		"Re : spaced colon",
		"Re[2]: counted",
		"Re[10]:counted",
		"Re：full width colon",
		"回复：检查状态！", // the one from the 2026-08-26 log
		"回复: half width colon",
		"回覆：traditional",
		"答复：another form",
		"答覆: and its traditional",
		"  Re: leading space",
	}
	for _, s := range unchanged {
		if got := replySubject(s); got != s {
			t.Errorf("replySubject(%q) = %q, want it unchanged", s, got)
		}
	}

	prefixed := map[string]string{
		"检查状态":             "Re: 检查状态",
		"Do X":             "Re: Do X",
		"":                 "Re: ",
		"Reminder: pay up": "Re: Reminder: pay up", // "Reminder" is not "Re"
		"研究一下 Re: 这个":      "Re: 研究一下 Re: 这个",      // marker must be at the start
	}
	for in, want := range prefixed {
		if got := replySubject(in); got != want {
			t.Errorf("replySubject(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestComposeDoesNotStackChinesePrefix is the end-to-end form: the composed
// header must carry exactly one reply marker.
func TestComposeDoesNotStackChinesePrefix(t *testing.T) {
	msg := string(Compose("a@x", Envelope{To: "b@x", Subject: "回复：检查状态！", InReplyTo: "<i@x>"}, "body"))
	if !strings.Contains(msg, "Subject: 回复：检查状态！") {
		t.Errorf("expected the subject passed through untouched, got:\n%s", msg)
	}
	if strings.Contains(msg, "Subject: Re: 回复：") {
		t.Errorf("perch stacked a second reply prefix:\n%s", msg)
	}
}

func TestComposeCcAndKind(t *testing.T) {
	msg := string(Compose("a@x", Envelope{To: "b@x", Cc: []string{"c@x", "d@x"}, Subject: "hi", Kind: KindAck}, "body"))
	for _, want := range []string{"Cc: c@x, d@x\r\n", "X-Perch-Kind: ack\r\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if got := (Envelope{To: "b@x", Cc: []string{"c@x"}}).recipients(); len(got) != 2 {
		t.Errorf("recipients = %v, want To+Cc", got)
	}
	if def := string(Compose("a@x", Envelope{To: "b@x", Subject: "hi"}, "body")); !strings.Contains(def, "X-Perch-Kind: reply\r\n") || strings.Contains(def, "\r\nCc:") {
		t.Errorf("default envelope:\n%s", def)
	}
}
