package replier

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
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

func composeHeaders(fromAddr, to, subject, inReplyTo string, references []string, contentType string) string {
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
	fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	b.WriteString("\r\n")
	return b.String()
}

// ComposeWithAttachments builds an RFC5322 threaded multipart/mixed reply:
// one text/plain body part plus one part per attachment file path. Used when
// the agent staged files in the reply/ directory.
func ComposeWithAttachments(fromAddr, to, subject, inReplyTo string, references []string, body string, attachments []string) ([]byte, error) {
	// Same header normalization as the plain path: sanitize + Re: prefix.
	subject = sanitizeHeader(subject)
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

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
	cfg *config.Config
}

func New(cfg *config.Config) *Replier { return &Replier{cfg: cfg} }

// Reply composes and sends a threaded reply over implicit-TLS SMTP (163 :465).
// If attachments is non-empty the message becomes multipart/mixed.
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
