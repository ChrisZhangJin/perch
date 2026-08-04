package replier

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
)

func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// Compose builds an RFC5322 threaded reply. All header values are CRLF-sanitized.
func Compose(fromAddr, to, subject, inReplyTo string, references []string, body string) []byte {
	subject = sanitizeHeader(subject)
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
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
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

type Replier struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Replier { return &Replier{cfg: cfg} }

// Reply composes and sends a threaded reply over implicit-TLS SMTP (163 :465).
func (r *Replier) Reply(to, subject, inReplyTo string, references []string, body string) error {
	msg := Compose(r.cfg.Email, to, subject, inReplyTo, references, body)

	host, _, err := splitHostPort(r.cfg.SMTPAddr)
	if err != nil {
		return err
	}
	conn, err := tls.Dial("tcp", r.cfg.SMTPAddr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: r.cfg.TLSInsecure, //nolint:gosec // dev/test-only, gated by TLS_INSECURE_SKIP_VERIFY
	})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Quit()

	auth := smtp.PlainAuth("", r.cfg.Email, r.cfg.AuthCode, host)
	if err := c.Auth(auth); err != nil {
		return err
	}
	if err := c.Mail(r.cfg.Email); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("bad addr %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}
