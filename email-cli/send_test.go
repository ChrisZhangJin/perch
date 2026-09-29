package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is an in-process plaintext SMTP server. It is enough of RFC5321
// for net/smtp's client to complete a transaction, plus knobs for the failure
// modes the retry logic cares about.
//
// It listens on 127.0.0.1 deliberately: net/smtp's PlainAuth refuses to send
// credentials over an unencrypted link unless the server name is a loopback
// address, so this exercises the real production auth path rather than a stub.
type fakeSMTP struct {
	ln net.Listener

	mu       sync.Mutex
	conns    int
	sessions []fakeSession

	// authOK false makes AUTH answer 535 (bad authorization code).
	authOK bool
	// rejectRcpt maps a recipient to the reply line it gets instead of 250.
	rejectRcpt map[string]string
	// transientMailBefore makes MAIL FROM answer "421 try later" on every
	// connection numbered below it, so attempt N finally succeeds.
	transientMailBefore int
}

type fakeSession struct {
	from string
	rcpt []string
	data string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSMTP{ln: ln, authOK: true, rejectRcpt: map[string]string{}}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeSMTP) snapshot() (int, []fakeSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, append([]fakeSession(nil), f.sessions...)
}

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	f.mu.Lock()
	f.conns++
	connNo := f.conns
	f.mu.Unlock()

	br := bufio.NewReader(c)
	w := func(s string) { fmt.Fprint(c, s+"\r\n") }
	w("220 fake ESMTP ready")

	var sess fakeSession
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			w("250-fake greets you")
			w("250-AUTH PLAIN LOGIN")
			w("250 8BITMIME")
		case strings.HasPrefix(upper, "HELO"):
			w("250 fake greets you")
		case strings.HasPrefix(upper, "AUTH"):
			if f.authOK {
				w("235 2.7.0 Authentication successful")
			} else {
				w("535 Error: authentication failed")
			}
		case strings.HasPrefix(upper, "MAIL FROM"):
			if connNo < f.transientMailBefore {
				w("421 4.7.0 try again later")
				return
			}
			sess.from = between(cmd, "<", ">")
			w("250 2.1.0 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			addr := between(cmd, "<", ">")
			if reply, bad := f.rejectRcpt[addr]; bad {
				w(reply)
				continue
			}
			sess.rcpt = append(sess.rcpt, addr)
			w("250 2.1.5 Ok")
		case upper == "DATA":
			w("354 End data with <CR><LF>.<CR><LF>")
			var b strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				b.WriteString(l)
			}
			sess.data = b.String()
			f.mu.Lock()
			f.sessions = append(f.sessions, sess)
			f.mu.Unlock()
			sess = fakeSession{}
			w("250 2.0.0 Ok: queued")
		case upper == "RSET":
			sess = fakeSession{}
			w("250 2.0.0 Ok")
		case upper == "QUIT":
			w("221 2.0.0 Bye")
			return
		default:
			w("502 5.5.2 Command not implemented")
		}
	}
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	j := strings.LastIndex(s, close)
	if i < 0 || j <= i {
		return ""
	}
	return s[i+1 : j]
}

// testSender wires a sender at the fake server with a plaintext dialer and a
// retry delay short enough not to slow the suite down.
func testSender(f *fakeSMTP) *sender {
	return &sender{
		Addr:        f.addr(),
		User:        "sender@163.com",
		Pass:        "secret-grant-code",
		Timeout:     5 * time.Second,
		MaxAttempts: 3,
		RetryDelay:  time.Millisecond,
		dial: func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		},
	}
}

func TestSendDeliversMessage(t *testing.T) {
	f := newFakeSMTP(t)
	s := testSender(f)

	res, err := s.send("sender@163.com", []string{"a@example.com", "b@example.com"}, []byte("Subject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(res.Accepted) != 2 {
		t.Errorf("accepted = %v", res.Accepted)
	}

	conns, sessions := f.snapshot()
	if conns != 1 {
		t.Errorf("connections = %d, want 1", conns)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	got := sessions[0]
	if got.from != "sender@163.com" {
		t.Errorf("MAIL FROM = %q", got.from)
	}
	if strings.Join(got.rcpt, ",") != "a@example.com,b@example.com" {
		t.Errorf("RCPT TO = %v", got.rcpt)
	}
	if !strings.Contains(got.data, "Subject: hi") {
		t.Errorf("DATA = %q", got.data)
	}
}

func TestSendAuthFailureIsPermanent(t *testing.T) {
	f := newFakeSMTP(t)
	f.authOK = false
	s := testSender(f)

	var attempts int
	s.onAttempt = func(int, error) { attempts++ }

	_, err := s.send("sender@163.com", []string{"a@example.com"}, []byte("x"))
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	// Retrying rejected credentials can only ever fail again, and each retry
	// costs the provider's rate limit.
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on a permanent failure)", attempts)
	}
	if !strings.Contains(err.Error(), "sender@163.com") {
		t.Errorf("auth error should name the account, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-grant-code") {
		t.Fatal("the authorization code leaked into the error message")
	}
}

func TestSendRetriesTransientFailure(t *testing.T) {
	f := newFakeSMTP(t)
	f.transientMailBefore = 3 // connections 1 and 2 get a 421
	s := testSender(f)

	res, err := s.send("sender@163.com", []string{"a@example.com"}, []byte("x"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(res.Accepted) != 1 {
		t.Errorf("accepted = %v", res.Accepted)
	}
	if conns, _ := f.snapshot(); conns != 3 {
		t.Errorf("connections = %d, want 3 (each retry dials fresh)", conns)
	}
}

func TestSendGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFakeSMTP(t)
	f.transientMailBefore = 99
	s := testSender(f)
	s.MaxAttempts = 2

	if _, err := s.send("sender@163.com", []string{"a@example.com"}, []byte("x")); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "2 attempt") {
		t.Errorf("err = %v, want it to name the attempt count", err)
	}
	if conns, _ := f.snapshot(); conns != 2 {
		t.Errorf("connections = %d, want 2", conns)
	}
}

func TestSendContinuesPastRejectedRecipient(t *testing.T) {
	f := newFakeSMTP(t)
	f.rejectRcpt["bad@example.com"] = "550 5.1.1 User unknown"
	s := testSender(f)

	res, err := s.send("sender@163.com", []string{"bad@example.com", "good@example.com"}, []byte("x"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// One bad address on a ten-recipient line must not cost the other nine
	// their copy.
	if len(res.Accepted) != 1 || res.Accepted[0] != "good@example.com" {
		t.Errorf("accepted = %v", res.Accepted)
	}
	if _, ok := res.Rejected["bad@example.com"]; !ok {
		t.Errorf("rejected = %v", res.Rejected)
	}
	if _, sessions := f.snapshot(); len(sessions) != 1 {
		t.Fatalf("message was not delivered to the surviving recipient")
	}
}

func TestSendFailsWhenEveryRecipientRejected(t *testing.T) {
	f := newFakeSMTP(t)
	f.rejectRcpt["bad@example.com"] = "550 5.1.1 User unknown"
	s := testSender(f)

	_, err := s.send("sender@163.com", []string{"bad@example.com"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "every recipient") {
		t.Fatalf("err = %v", err)
	}
}

func TestIsTransient(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"421 greylist": {errors.New("421 4.7.0 try again later"), true},
		"broken pipe":  {errors.New("write tcp: broken pipe"), true},
		"i/o timeout":  {errors.New("dial tcp: i/o timeout"), true},
		"550 unknown":  {errors.New("550 5.1.1 User unknown"), false},
		"auth":         {fmt.Errorf("%w: 535", ErrAuth), false},
		"nil":          {nil, false},
	}
	for name, c := range cases {
		if got := isTransient(c.err); got != c.want {
			t.Errorf("%s: isTransient = %v, want %v", name, got, c.want)
		}
	}
}
