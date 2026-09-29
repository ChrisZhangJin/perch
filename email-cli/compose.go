package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
)

// Attachment is one file to hang off the message. Data is held in memory;
// main.go caps the total with --max-attach-bytes before we get here.
type Attachment struct {
	Filename string
	MIMEType string
	Data     []byte
}

// Header is an extra RFC5322 header from --header "Name: value".
type Header struct{ Name, Value string }

// defaultAttachmentType is the fallback when the extension is unknown or the
// media type will not format.
const defaultAttachmentType = "application/octet-stream"

// orDefault guards against mime.FormatMediaType's habit of signalling failure
// with an empty string: an empty Content-Type header is worse than a generic
// one, because clients differ on what they assume in its absence.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Message is everything needed to build one outbound mail.
//
// Note what is NOT here, compared with internal/replier: no "Re: " subject
// prefix and no "Auto-Submitted: auto-replied". Those belong to perch's
// auto-reply semantics — a reply robot must mark itself so the counterparty's
// robot does not answer back forever. email-cli sends mail a human asked for,
// so marking it auto-replied would tell compliant recipients to ignore it.
type Message struct {
	From       *mail.Address
	ReplyTo    *mail.Address
	To         []*mail.Address
	Cc         []*mail.Address
	Bcc        []*mail.Address // envelope only; deliberately never a header
	Subject    string
	Text       string // text/plain body
	HTML       string // text/html body
	Attach     []Attachment
	Extra      []Header
	InReplyTo  string   // optional threading
	References []string // optional threading

	// Date and MessageID are injectable so tests can assert on a byte-exact
	// message. Zero/empty means "generate now".
	Date      time.Time
	MessageID string
}

// Recipients is the SMTP envelope: every address the server must be told
// about, including Bcc, deduplicated and in a stable order.
func (m *Message) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]*mail.Address{m.To, m.Cc, m.Bcc} {
		for _, a := range group {
			key := strings.ToLower(a.Address)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a.Address)
		}
	}
	return out
}

// forbiddenExtra are headers --header must not set, because Build owns them
// and a second copy produces a message clients render inconsistently.
var forbiddenExtra = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "subject": true,
	"date": true, "message-id": true, "mime-version": true,
	"content-type": true, "content-transfer-encoding": true,
}

// Build renders the message as RFC5322 bytes with CRLF line endings.
//
// Structure is chosen from what is present:
//
//	text only                 -> text/plain
//	html only                 -> text/html
//	text + html               -> multipart/alternative
//	... + attachments         -> multipart/mixed wrapping the above
//
// Bodies are quoted-printable and attachments base64, both wrapped to legal
// line lengths: a raw UTF-8 body would be 8-bit content that plenty of relays
// still refuse, and an unwrapped base64 blob exceeds RFC5322's 998-octet line
// limit on the first attachment of any size.
func (m *Message) Build() ([]byte, error) {
	if m.From == nil {
		return nil, fmt.Errorf("message has no From address")
	}
	if len(m.To)+len(m.Cc)+len(m.Bcc) == 0 {
		return nil, fmt.Errorf("message has no recipients")
	}
	if m.Text == "" && m.HTML == "" {
		return nil, fmt.Errorf("message has no body")
	}
	for _, h := range m.Extra {
		if forbiddenExtra[strings.ToLower(strings.TrimSpace(h.Name))] {
			return nil, fmt.Errorf("--header %s: that header is built from the other flags, set it there", h.Name)
		}
	}

	body, contentType, cte, err := m.buildBody()
	if err != nil {
		return nil, err
	}

	var h bytes.Buffer
	writeHeader(&h, "From", m.From.String())
	if len(m.To) > 0 {
		writeHeader(&h, "To", addrList(m.To))
	}
	if len(m.Cc) > 0 {
		writeHeader(&h, "Cc", addrList(m.Cc))
	}
	if m.ReplyTo != nil {
		writeHeader(&h, "Reply-To", m.ReplyTo.String())
	}
	writeHeader(&h, "Subject", mime.BEncoding.Encode("utf-8", sanitizeHeaderValue(m.Subject)))
	writeHeader(&h, "Date", m.date().Format(time.RFC1123Z))
	writeHeader(&h, "Message-ID", m.messageID())
	if m.InReplyTo != "" {
		writeHeader(&h, "In-Reply-To", sanitizeHeaderValue(m.InReplyTo))
	}
	if len(m.References) > 0 {
		writeHeader(&h, "References", sanitizeHeaderValue(strings.Join(m.References, " ")))
	}
	for _, e := range m.Extra {
		writeHeader(&h, sanitizeHeaderName(e.Name), sanitizeHeaderValue(e.Value))
	}
	writeHeader(&h, "MIME-Version", "1.0")
	writeHeader(&h, "X-Mailer", "email-cli/"+version+" (perch)")
	writeHeader(&h, "Content-Type", contentType)
	if cte != "" {
		writeHeader(&h, "Content-Transfer-Encoding", cte)
	}
	h.WriteString("\r\n")

	return append(h.Bytes(), body...), nil
}

// buildBody renders everything below the blank line, and reports the
// Content-Type / Content-Transfer-Encoding the top-level headers need. cte is
// empty for multipart bodies, which carry per-part encodings instead.
func (m *Message) buildBody() (body []byte, contentType, cte string, err error) {
	switch {
	case len(m.Attach) > 0:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := m.writeBodyParts(mw); err != nil {
			return nil, "", "", err
		}
		for _, a := range m.Attach {
			if err := writeAttachmentPart(mw, a); err != nil {
				return nil, "", "", err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), `multipart/mixed; boundary="` + mw.Boundary() + `"`, "", nil

	case m.Text != "" && m.HTML != "":
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := writeAlternative(mw, m.Text, m.HTML); err != nil {
			return nil, "", "", err
		}
		if err := mw.Close(); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), `multipart/alternative; boundary="` + mw.Boundary() + `"`, "", nil

	default:
		text, ct := m.Text, "text/plain; charset=utf-8"
		if text == "" {
			text, ct = m.HTML, "text/html; charset=utf-8"
		}
		var buf bytes.Buffer
		qw := quotedprintable.NewWriter(&buf)
		if _, err := qw.Write([]byte(normalizeNewlines(text))); err != nil {
			return nil, "", "", err
		}
		if err := qw.Close(); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), ct, "quoted-printable", nil
	}
}

// writeBodyParts emits the human-readable part(s) of a multipart/mixed: one
// text part, or a nested multipart/alternative when both bodies are present.
func (m *Message) writeBodyParts(mw *multipart.Writer) error {
	if m.Text != "" && m.HTML != "" {
		// The nested boundary has to be known before CreatePart, because it
		// goes in the part's own Content-Type header — so generate it, then
		// pin the inner writer to it with SetBoundary.
		boundary := randomBoundary()
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		p, err := mw.CreatePart(hdr)
		if err != nil {
			return err
		}
		inner := multipart.NewWriter(p)
		if err := inner.SetBoundary(boundary); err != nil {
			return err
		}
		if err := writeAlternative(inner, m.Text, m.HTML); err != nil {
			return err
		}
		return inner.Close()
	}
	text, ct := m.Text, "text/plain"
	if text == "" {
		text, ct = m.HTML, "text/html"
	}
	return writeTextPart(mw, ct, text)
}

// randomBoundary returns a MIME boundary that cannot collide with body
// content, in the same shape multipart.Writer generates for itself.
func randomBoundary() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeAlternative writes the text-then-html pair into mw, in the RFC2046
// order: least-rich first, so a client that understands both picks the HTML.
func writeAlternative(mw *multipart.Writer, text, html string) error {
	if err := writeTextPart(mw, "text/plain", text); err != nil {
		return err
	}
	return writeTextPart(mw, "text/html", html)
}

func writeTextPart(mw *multipart.Writer, mimeType, body string) error {
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Type", mimeType+"; charset=utf-8")
	hdr.Set("Content-Transfer-Encoding", "quoted-printable")
	p, err := mw.CreatePart(hdr)
	if err != nil {
		return err
	}
	qw := quotedprintable.NewWriter(p)
	if _, err := qw.Write([]byte(normalizeNewlines(body))); err != nil {
		return err
	}
	return qw.Close()
}

func writeAttachmentPart(mw *multipart.Writer, a Attachment) error {
	ct := a.MIMEType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(a.Filename))
	}
	if ct == "" {
		ct = defaultAttachmentType
	}
	// mime.TypeByExtension hands back parameters as well ("text/plain;
	// charset=utf-8"), and FormatMediaType rejects a type that already
	// carries them — by returning "" rather than an error, which ships an
	// empty Content-Type. Split first, then re-merge with the filename.
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType, params = defaultAttachmentType, nil
	}
	if params == nil {
		params = map[string]string{}
	}
	// mime.FormatMediaType emits RFC2231 (name*=utf-8''...) when the value is
	// not ASCII, which is what mail clients expect for 中文文件名.pdf.
	params["name"] = a.Filename

	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Type", orDefault(mime.FormatMediaType(mediaType, params), defaultAttachmentType))
	hdr.Set("Content-Disposition", orDefault(
		mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}), "attachment"))
	hdr.Set("Content-Transfer-Encoding", "base64")
	p, err := mw.CreatePart(hdr)
	if err != nil {
		return err
	}
	return writeWrappedBase64(p, a.Data)
}

// writeWrappedBase64 emits base64 in 76-character CRLF-terminated lines.
// base64.NewEncoder does not wrap, and a single unwrapped line past 998
// octets is an illegal RFC5322 line that some relays reject outright.
func writeWrappedBase64(w io.Writer, data []byte) error {
	const lineLen = 76
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > lineLen {
		if _, err := w.Write([]byte(enc[:lineLen] + "\r\n")); err != nil {
			return err
		}
		enc = enc[lineLen:]
	}
	if len(enc) > 0 {
		if _, err := w.Write([]byte(enc + "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

func (m *Message) date() time.Time {
	if m.Date.IsZero() {
		return time.Now()
	}
	return m.Date
}

// messageID returns the Message-ID header value, generating a
// <timestamp.random@sender-domain> one if the caller did not supply it.
func (m *Message) messageID() string {
	if m.MessageID != "" {
		return sanitizeHeaderValue(m.MessageID)
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	domain := domainOf(m.From.Address)
	if domain == "" {
		domain = "localhost"
	}
	return fmt.Sprintf("<%d.%s@%s>", m.date().UnixNano(), hex.EncodeToString(b[:]), domain)
}

// addrList renders addresses as a comma-separated RFC5322 list. Each name is
// RFC2047-encoded by mail.Address.String(), so 张三 <a@b.com> comes out right.
func addrList(addrs []*mail.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// writeHeader emits one header, folding long address lists across
// continuation lines so no line approaches RFC5322's 998-octet hard limit.
//
// Folding happens only at ", " boundaries. Long non-address values are left
// alone on purpose: the long ones are RFC2047 encoded-words, which may not be
// broken mid-word — and mime.WordEncoder already splits those into multiple
// encoded-words joined by CRLF+space before we see them.
func writeHeader(b *bytes.Buffer, name, value string) {
	const limit = 78
	if len(name)+2+len(value) <= limit || !strings.Contains(value, ", ") {
		b.WriteString(name + ": " + value + "\r\n")
		return
	}
	parts := strings.Split(value, ", ")
	cur := name + ":"
	for i, p := range parts {
		if i < len(parts)-1 {
			p += ","
		}
		if cur != name+":" && len(cur)+1+len(p) > limit {
			b.WriteString(cur + "\r\n")
			cur = "" // continuation lines start with the folding space below
		}
		cur += " " + p
	}
	b.WriteString(cur + "\r\n")
}

// sanitizeHeaderValue strips CR and LF. Without this, a --subject containing
// a newline would let the caller inject arbitrary headers (Bcc: attacker@…)
// into the message.
func sanitizeHeaderValue(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// sanitizeHeaderName keeps only characters legal in a field name (RFC5322
// printable US-ASCII except colon).
func sanitizeHeaderName(n string) string {
	n = strings.TrimSpace(n)
	var b strings.Builder
	for _, r := range n {
		if r > 32 && r < 127 && r != ':' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeNewlines collapses CRLF and lone CR to LF. The quoted-printable
// writer turns every LF into a CRLF itself, so handing it CRLF input would
// double the line endings.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// parseAddrList parses and validates a list of addresses, accepting both bare
// "a@b.com" and "Name <a@b.com>" forms.
func parseAddrList(vals []string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		a, err := mail.ParseAddress(v)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", v, err)
		}
		out = append(out, a)
	}
	return out, nil
}
