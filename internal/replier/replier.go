package replier

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
)

func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// Compose builds an RFC5322 threaded text/plain reply. All header values are
// CRLF-sanitized.
func Compose(fromAddr, to, subject, inReplyTo string, references []string, body string) []byte {
	return []byte(composeHeaders(fromAddr, to, subject, inReplyTo, references, "text/plain; charset=utf-8") + body)
}

// replyPrefixRe matches the "this is a reply" marker a mail client prepends to
// a subject, in the forms perch actually meets on 163 / 126 / QQ / Foxmail /
// Lark as well as ASCII clients:
//
//	Re: x    RE：x    Re[2]: x    回复：x    回覆: x    答复：x    答覆: x
//
// Both the ASCII colon and the full-width one (：) count, and so does the
// bracketed counter some clients add.
//
// This used to be a plain strings.HasPrefix(lower(subject), "re:"), which does
// not see the CJK forms — so a thread that a Chinese client started as
// "回复：检查状态！" came back as "Re: 回复：检查状态！", the sender's client made
// that "回复：Re: 回复：…", and the ladder grew one pair per round. Observed
// 2026-08-26.
var replyPrefixRe = regexp.MustCompile(`^\s*(?i:re)\s*(?:\[\d+\])?\s*[:：]|^\s*(?:回复|回覆|答复|答覆)\s*[:：]`)

// replySubject returns subject with a single "Re: " prefix, adding one only
// when the subject does not already carry a reply marker.
func replySubject(subject string) string {
	if replyPrefixRe.MatchString(subject) {
		return subject
	}
	return "Re: " + subject
}

func composeHeaders(fromAddr, to, subject, inReplyTo string, references []string, contentType string) string {
	subject = replySubject(sanitizeHeader(subject))
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(fromAddr))
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Message-ID: <%d.%s>\r\n", time.Now().UnixNano(), sanitizeHeader(fromAddr))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", sanitizeHeader(inReplyTo))
	}
	if len(references) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", sanitizeHeader(strings.Join(references, " ")))
	}
	// RFC 3834: label our own mail as an automatic reply so a compliant
	// counterparty does not answer it. Two robots that both omit this reply
	// to each other indefinitely — which is exactly what happened in
	// production between two perch instances.
	b.WriteString("Auto-Submitted: auto-replied\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	b.WriteString("\r\n")
	return b.String()
}

// ComposeWithAttachments builds an RFC5322 threaded multipart/mixed reply:
// one text/plain body part plus one part per attachment file path. Used when
// the agent staged files in the reply/ directory.
func ComposeWithAttachments(fromAddr, to, subject, inReplyTo string, references []string, body string, attachments []string) ([]byte, error) {
	// Same header normalization as the plain path: sanitize + Re: prefix.
	subject = replySubject(sanitizeHeader(subject))

	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "From: %s\r\n", sanitizeHeader(fromAddr))
	fmt.Fprintf(&buf, "To: %s\r\n", sanitizeHeader(to))
	fmt.Fprintf(&buf, "Subject: %s\r\n", sanitizeHeader(subject))
	fmt.Fprintf(&buf, "Message-ID: <%d.%s>\r\n", time.Now().UnixNano(), sanitizeHeader(fromAddr))
	if inReplyTo != "" {
		fmt.Fprintf(&buf, "In-Reply-To: %s\r\n", sanitizeHeader(inReplyTo))
	}
	if len(references) > 0 {
		fmt.Fprintf(&buf, "References: %s\r\n", sanitizeHeader(strings.Join(references, " ")))
	}
	// RFC 3834: label our own mail as an automatic reply so a compliant
	// counterparty does not answer it. Two robots that both omit this reply
	// to each other indefinitely — which is exactly what happened in
	// production between two perch instances.
	buf.WriteString("Auto-Submitted: auto-replied\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; charset=utf-8; boundary=%q\r\n", mw.Boundary())
	buf.WriteString("\r\n")

	// body part
	bodyHdr := textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}}
	bp, err := mw.CreatePart(bodyHdr)
	if err != nil {
		return nil, err
	}
	if _, err := bp.Write([]byte(body)); err != nil {
		return nil, err
	}

	// attachment parts
	for _, path := range attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // unreadable (raced delete?) — skip, keep the rest
		}
		name := filepath.Base(path)
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		hdr := textproto.MIMEHeader{
			"Content-Type":              {ct},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
			"Content-Transfer-Encoding": {"base64"},
		}
		ap, err := mw.CreatePart(hdr)
		if err != nil {
			return nil, err
		}
		enc := base64.NewEncoder(base64.StdEncoding, ap)
		if _, err := enc.Write(data); err != nil {
			enc.Close()
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

type Replier struct {
	cfg      *config.Config
	smtpAddr string // resolved by provider registry in main; empty => legacy fallback (none after MVP)
	// MaxAttempts is the upper bound on SMTP send attempts (incl. the first).
	// Defaults to 3 if zero. Each retry opens a fresh TLS+SMTP session.
	MaxAttempts int
	// retryDelay is the base backoff between attempts. Defaults to 1s if zero.
	retryDelay time.Duration
	// authRetryDelay is the base backoff used when the previous attempt was
	// rejected at AUTH. Defaults to 5s if zero — deliberately much longer than
	// retryDelay, because a 535 from 163 is a risk-control throttle whose
	// window is measured in seconds (see ErrAuth). Retrying a throttle after a
	// millisecond just spends another of the attempts on the same refusal.
	// Tests shrink it, same as retryDelay.
	authRetryDelay time.Duration
	// onAttempt, if non-nil, is invoked after each attempt with (attempt #, err).
	// Used by app.go to log per-attempt details and to trigger failure
	// notifications when MaxAttempts is exhausted.
	onAttempt func(attempt int, err error, msgSize int)
	// dial overrides the SMTP dialer. nil => tls.Dial (implicit TLS, the
	// production path). Tests inject a plaintext dialer against an in-process
	// fake SMTP server.
	dial func(addr, host string, insecure bool) (net.Conn, error)
}

// New wires a Replier. smtpAddr comes from the provider registry
// (e.g. "smtp.163.com:465"); main.go resolves it after config.Load.
func New(cfg *config.Config, smtpAddr string) *Replier { return &Replier{cfg: cfg, smtpAddr: smtpAddr} }

// SetHook installs a per-attempt hook. Intended for app-level logging +
// failure-notification wiring. Pass nil to clear.
func (r *Replier) SetHook(fn func(attempt int, err error, msgSize int)) { r.onAttempt = fn }

// Reply composes and sends a threaded reply over implicit-TLS SMTP (163 :465)
// with up to MaxAttempts attempts (default 3) and exponential backoff on
// retryable errors (transient network blips, plus AUTH rejections — see
// ErrAuth). Permanent failures (bad address, malformed message) fail fast.
// If attachments is non-empty the message becomes multipart/mixed.
//
// The onAttempt hook fires after every attempt including the final one. When
// all attempts fail with a transient error, the final error is returned and
// the hook has been invoked once per attempt so the caller can react.
func (r *Replier) Reply(to, subject, inReplyTo string, references []string, body string, attachments []string) error {
	var msg []byte
	var err error
	if len(attachments) > 0 {
		msg, err = ComposeWithAttachments(r.cfg.Email, to, subject, inReplyTo, references, body, attachments)
	} else {
		msg = Compose(r.cfg.Email, to, subject, inReplyTo, references, body)
	}
	if err != nil {
		return err
	}

	maxAttempts := r.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := r.sendOnce(to, msg)
		if r.onAttempt != nil {
			r.onAttempt(attempt, err, len(msg))
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if !r.retryable(err) {
			return err // permanent — fail fast
		}
		if attempt == maxAttempts {
			break // last try exhausted
		}
		time.Sleep(r.backoff(err) << (attempt - 1)) // 1s, 2s, 4s, ... (5s, 10s on AUTH)
	}
	return fmt.Errorf("smtp send failed after %d attempts: %w", maxAttempts, lastErr)
}

// sendOnce dials SMTP + sends one DATA transaction with proper RSET on failure.
func (r *Replier) sendOnce(to string, msg []byte) error {
	host, _, err := splitHostPort(r.smtpAddr)
	if err != nil {
		return fmt.Errorf("smtp addr: %w", err)
	}
	dialer := r.dial
	if dialer == nil {
		dialer = defaultDial
	}
	conn, err := dialer(r.smtpAddr, host, r.cfg.TLSInsecure)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	// On any failure path below we call c.Reset() (DATA-state safe) instead of
	// c.Quit() (which sends QUIT only valid from a fresh state). Track whether
	// we own the close so the deferred Quit doesn't double-fire after a Reset.
	closeOK := true
	defer func() {
		if closeOK {
			_ = c.Quit()
		}
	}()

	if err := c.Auth(smtp.PlainAuth("", r.cfg.Email, r.cfg.AuthCode, host)); err != nil {
		return fmt.Errorf("%w (account %s): %v", ErrAuth, r.cfg.Email, err)
	}
	if err := c.Mail(r.cfg.Email); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		closeOK = false
		_ = c.Reset() // server is in DATA; Quit would compound the error
		return fmt.Errorf("smtp data write: %w", err)
	}
	if err := w.Close(); err != nil {
		closeOK = false
		_ = c.Reset()
		return fmt.Errorf("smtp data close: %w", err)
	}
	return nil
}

// defaultDial is the production SMTP dialer: implicit TLS to the configured
// host. Tests override r.dial to talk plaintext to an in-process fake.
func defaultDial(addr, host string, insecure bool) (net.Conn, error) {
	return tls.Dial("tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: insecure, //nolint:gosec // dev/test-only, gated by TLS_INSECURE_SKIP_VERIFY
	})
}

// NotifyFailure mails the original sender a plain-text "perch failed" report
// after Reply exhausted its retries. It uses an independent SMTP session
// (Retry attempts on it too, with the same MaxAttempts) and never recurses
// into the failure path: even if NotifyFailure itself fails, the error is
// returned without further notification attempts.
//
// Subject is prefixed with "Perch failed: " so the user can filter. The body
// includes the last error verbatim, attempt count, message size, and timestamp.
func (r *Replier) NotifyFailure(to, subject, inReplyTo string, references []string, attempts int, lastErr error, msgSize int) error {
	host, _, err := splitHostPort(r.smtpAddr)
	if err != nil {
		return err
	}
	body := composeFailureBody(to, subject, attempts, lastErr, msgSize, time.Now())
	msg := ComposeFailure(r.cfg.Email, to, subject, inReplyTo, references, body)

	maxAttempts := r.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	var lastSendErr error
	dialer := r.dial
	if dialer == nil {
		dialer = defaultDial
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := func() error {
			conn, err := dialer(r.smtpAddr, host, r.cfg.TLSInsecure)
			if err != nil {
				return fmt.Errorf("smtp dial: %w", err)
			}
			c, err := smtp.NewClient(conn, host)
			if err != nil {
				conn.Close()
				return fmt.Errorf("smtp handshake: %w", err)
			}
			defer func() { _ = c.Quit() }()
			if err := c.Auth(smtp.PlainAuth("", r.cfg.Email, r.cfg.AuthCode, host)); err != nil {
				return fmt.Errorf("%w (account %s): %v", ErrAuth, r.cfg.Email, err)
			}
			if err := c.Mail(r.cfg.Email); err != nil {
				return fmt.Errorf("smtp mail: %w", err)
			}
			if err := c.Rcpt(to); err != nil {
				return fmt.Errorf("smtp rcpt: %w", err)
			}
			w, err := c.Data()
			if err != nil {
				return fmt.Errorf("smtp data: %w", err)
			}
			if _, err := w.Write(msg); err != nil {
				_ = c.Reset()
				return fmt.Errorf("smtp data write: %w", err)
			}
			if err := w.Close(); err != nil {
				_ = c.Reset()
				return fmt.Errorf("smtp data close: %w", err)
			}
			return nil
		}()
		if err == nil {
			return nil
		}
		lastSendErr = err
		if !r.retryable(err) {
			return err
		}
		if attempt < maxAttempts {
			time.Sleep(r.backoff(err) << (attempt - 1))
		}
	}
	return fmt.Errorf("failure notification send failed after %d attempts: %w", maxAttempts, lastSendErr)
}

// ErrAuth marks an SMTP authentication failure (163 returns "535 Error:
// authentication failed").
//
// This was documented as permanent — "the credentials were rejected, so
// retrying them is futile" — and that turned out to be wrong. Observed
// 2026-09-29: a reply to vos_th@163.com died with
// `535 Error: authentication failed`, and the "Perch failed:" notice that
// followed reached the sender under a second later over a fresh connection
// using the byte-identical PlainAuth call, same account, same auth code, same
// smtp.163.com:465. The credential was correct the whole time; 163 reuses 535
// for risk-control / login-rate rejection, and a single flake permanently lost
// that reply because we gave up after one attempt.
//
// So it is retryable — but with authRetryDelay rather than retryDelay, since
// the throttle window is seconds. Note this is NOT done by making isTransient
// true for ErrAuth: isTransient describes network blips and has its own
// contract. Use (*Replier).retryable instead.
//
// The wrapped error names the auth account, since the message's from/to are
// irrelevant to a credential rejection; callers match it with errors.Is to log
// the account instead of the recipient.
var ErrAuth = errors.New("smtp authentication failed")

// retryable reports whether another attempt is worth making: a network blip,
// or an AUTH rejection (see ErrAuth — 163's 535 is not reliably permanent).
func (r *Replier) retryable(err error) bool {
	return isTransient(err) || errors.Is(err, ErrAuth)
}

// backoff returns the base delay to use after err, before the per-attempt
// doubling. An AUTH rejection gets the much longer authRetryDelay: retrying
// 163's risk-control throttle a second later just burns an attempt.
func (r *Replier) backoff(err error) time.Duration {
	if errors.Is(err, ErrAuth) {
		if r.authRetryDelay > 0 {
			return r.authRetryDelay
		}
		return 5 * time.Second
	}
	if r.retryDelay > 0 {
		return r.retryDelay
	}
	return time.Second
}

// isTransient reports whether err is the kind of network blip that might
// succeed on retry: broken pipe, connection reset, EOF, timeout, "421 try
// again later" / "450 mailbox unavailable" style SMTP replies, DNS hiccups.
// Permanent failures (bad address, malformed message) return false, and so
// does ErrAuth — see its doc comment for why that is not the same thing as
// "do not retry an auth failure".

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	transientSubstrings := []string{
		"broken pipe",
		"connection reset",
		"connection refused",
		"EOF",
		"i/o timeout",
		"timeout",
		"temporary failure",
		"421",
		"450",
		"451",
		"network is unreachable",
		"no such host", // DNS flap — sometimes transient
	}
	for _, t := range transientSubstrings {
		if strings.Contains(s, t) {
			return true
		}
	}
	// net.Error timeout also counts
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// syscall.ECONNRESET / EPIPE
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF)
}

// composeFailureBody is the human-readable body of the failure notification.
func composeFailureBody(to, subject string, attempts int, lastErr error, msgSize int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hello,\n\n")
	fmt.Fprintf(&b, "perch received your email but could not deliver a reply after %d attempt(s).\n\n", attempts)
	fmt.Fprintf(&b, "Original subject: %s\n", subject)
	fmt.Fprintf(&b, "Attempted at:     %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "Reply size:       %d bytes\n", msgSize)
	fmt.Fprintf(&b, "Last error:\n%s\n\n", indent(lastErr.Error(), "    "))
	fmt.Fprintf(&b, "No further action is required. This is an automated failure notice.\n")
	return b.String()
}

func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// ComposeFailure builds the RFC5322 message for the failure notification.
// Subject gets a "Perch failed: " prefix; threading is preserved so the
// notification stays in the same conversation in the user's mail client.
// Note: this does NOT add the "Re: " prefix composeHeaders would — a
// notification isn't a reply, it's a side-channel report.
func ComposeFailure(fromAddr, to, subject, inReplyTo string, references []string, body string) []byte {
	subject = sanitizeHeader(subject)
	if !strings.HasPrefix(subject, "Perch failed:") {
		subject = "Perch failed: " + subject
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(fromAddr))
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Message-ID: <%d.%s>\r\n", time.Now().UnixNano(), sanitizeHeader(fromAddr))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", sanitizeHeader(inReplyTo))
	}
	if len(references) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", sanitizeHeader(strings.Join(references, " ")))
	}
	// A failure notice is machine generated too — same reasoning as the
	// reply composers above.
	b.WriteString("Auto-Submitted: auto-replied\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	return []byte(b.String() + body)
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("bad addr %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}
