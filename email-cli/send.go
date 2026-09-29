package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"syscall"
	"time"
)

// ErrAuth marks an SMTP authentication failure — the providers answer a bad
// account/authorization-code pair with "535 Error: authentication failed".
// It is permanent: retrying the same credentials cannot succeed, so the retry
// loop stops on it and main.go maps it to its own exit code.
var ErrAuth = errors.New("smtp authentication failed")

// sender holds one SMTP endpoint's connection settings.
type sender struct {
	Addr        string // host:port
	User        string // the mailbox address
	Pass        string // the authorization code
	STARTTLS    bool   // dial plaintext then upgrade, instead of implicit TLS
	Insecure    bool   // skip certificate verification (dev only)
	Timeout     time.Duration
	MaxAttempts int
	RetryDelay  time.Duration

	// dial and auth exist so the tests can drive a real send against an
	// in-process plaintext SMTP server. Production leaves both nil.
	dial func(addr string, timeout time.Duration) (net.Conn, error)
	auth func(host string) smtp.Auth

	// onAttempt, if set, is called after every attempt with its 1-based
	// number and outcome. Used for --verbose progress.
	onAttempt func(attempt int, err error)
}

// sendResult reports what the server accepted. Rejected is non-empty when
// some recipients were refused but the message went to the rest.
type sendResult struct {
	Accepted []string
	Rejected map[string]error
}

// send delivers msg, retrying transient failures with exponential backoff.
// Each attempt opens a fresh connection: a half-broken session cannot be
// reused, and the providers drop idle connections aggressively.
func (s *sender) send(from string, rcpts []string, msg []byte) (sendResult, error) {
	attempts := s.MaxAttempts
	if attempts < 1 {
		attempts = 3
	}
	delay := s.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err := s.sendOnce(from, rcpts, msg)
		if s.onAttempt != nil {
			s.onAttempt(attempt, err)
		}
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isTransient(err) {
			return res, err // permanent (auth, bad address, refused mail) — stop
		}
		if attempt == attempts {
			break
		}
		time.Sleep(delay << (attempt - 1)) // 1s, 2s, 4s, ...
	}
	return sendResult{}, fmt.Errorf("send failed after %d attempt(s): %w", attempts, lastErr)
}

// sendOnce runs a single SMTP transaction.
//
// Recipients are handled individually: one bad address returns a 5xx on its
// RCPT TO and would otherwise abort delivery to everybody else on the line.
// We record the rejection and carry on, and only fail the whole send when the
// server accepted nobody.
func (s *sender) sendOnce(from string, rcpts []string, msg []byte) (sendResult, error) {
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return sendResult{}, fmt.Errorf("smtp addr %q: %w", s.Addr, err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	conn, err := s.dialConn(host, timeout)
	if err != nil {
		return sendResult{}, fmt.Errorf("smtp dial %s: %w", s.Addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return sendResult{}, fmt.Errorf("smtp handshake: %w", err)
	}
	// On a failure inside DATA we must RSET, not QUIT — QUIT is only valid
	// from a command state. closeOK tracks which the deferred cleanup should
	// send so we never compound one error with a second protocol violation.
	closeOK := true
	defer func() {
		if closeOK {
			_ = c.Quit()
		}
		_ = conn.Close()
	}()

	if s.STARTTLS {
		if err := c.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: s.Insecure}); err != nil { //nolint:gosec // gated by --tls-insecure
			return sendResult{}, fmt.Errorf("starttls: %w", err)
		}
	}

	authFn := s.auth
	if authFn == nil {
		authFn = func(h string) smtp.Auth { return smtp.PlainAuth("", s.User, s.Pass, h) }
	}
	if err := c.Auth(authFn(host)); err != nil {
		return sendResult{}, fmt.Errorf("%w (account %s): %v", ErrAuth, s.User, err)
	}

	if err := c.Mail(from); err != nil {
		return sendResult{}, fmt.Errorf("smtp mail from %s: %w", from, err)
	}

	res := sendResult{Rejected: map[string]error{}}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			if isTransient(err) {
				return sendResult{}, fmt.Errorf("smtp rcpt %s: %w", r, err)
			}
			res.Rejected[r] = err
			continue
		}
		res.Accepted = append(res.Accepted, r)
	}
	if len(res.Accepted) == 0 {
		return res, fmt.Errorf("every recipient was rejected: %s", formatRejected(res.Rejected))
	}

	w, err := c.Data()
	if err != nil {
		return res, fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		closeOK = false
		_ = c.Reset()
		return res, fmt.Errorf("smtp data write: %w", err)
	}
	if err := w.Close(); err != nil {
		closeOK = false
		_ = c.Reset()
		return res, fmt.Errorf("smtp data close: %w", err)
	}
	return res, nil
}

// dialConn opens the transport: implicit TLS on :465 by default, or a
// plaintext socket the caller upgrades with STARTTLS on :587.
func (s *sender) dialConn(host string, timeout time.Duration) (net.Conn, error) {
	if s.dial != nil {
		return s.dial(s.Addr, timeout)
	}
	d := &net.Dialer{Timeout: timeout}
	if s.STARTTLS {
		return d.Dial("tcp", s.Addr)
	}
	return tls.DialWithDialer(d, "tcp", s.Addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: s.Insecure, //nolint:gosec // dev-only, gated by --tls-insecure
	})
}

func formatRejected(m map[string]error) string {
	parts := make([]string, 0, len(m))
	for addr, err := range m {
		parts = append(parts, fmt.Sprintf("%s (%v)", addr, err))
	}
	return strings.Join(parts, "; ")
}

// isTransient reports whether err is the kind of blip a retry might clear:
// a dropped connection, a timeout, a DNS flap, or a 4xx "try again" reply.
// Permanent failures — bad credentials, rejected address, malformed message —
// return false so the caller fails fast instead of burning the backoff.
func isTransient(err error) bool {
	if err == nil || errors.Is(err, ErrAuth) {
		return false
	}
	s := err.Error()
	for _, sub := range []string{
		"broken pipe", "connection reset", "connection refused", "EOF",
		"i/o timeout", "timeout", "temporary failure",
		"421", "450", "451", "452",
		"network is unreachable", "no such host",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF)
}
