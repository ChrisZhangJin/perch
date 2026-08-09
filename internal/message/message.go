package message

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	// Register decoders for common charsets (GBK/GB2312/GB18030/Big5/Shift_JIS/…)
	// so Chinese/CJK emails parse. Without this, go-message errors on non-UTF-8.
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

// Attachment is a file carried by an inbound email, held in memory until
// the app decides where to persist it.
type Attachment struct {
	Name        string // sanitized base filename; "" if unnameable
	ContentType string
	Size        int64
	Data        []byte
}

// Message is a parsed inbound email reduced to the fields the watcher needs.
type Message struct {
	UID         uint32
	From        string // lowercased addr-spec, e.g. "alice@163.com"
	FromName    string // display name from the From header, "" if absent
	MessageID   string // e.g. "<abc@163.com>"
	InReplyTo   string
	References  []string
	Subject     string
	Date        time.Time // RFC5322 Date: header; zero if absent/unparseable
	Body        string    // text/plain, truncated to maxBody
	Attachments []Attachment
}

// ThreadRoot returns the first References id if present, else the message's own id.
func (m *Message) ThreadRoot() string {
	if len(m.References) > 0 {
		return m.References[0]
	}
	return m.MessageID
}

// Parse reads an RFC5322 message, collecting the first text/plain part as
// Body and every attachment part into Attachments. maxBody truncates the
// body; maxAttach (0 = unlimited) caps a single attachment so a huge file
// can't exhaust memory — oversized attachments are dropped, not fatal.
func Parse(r io.Reader, uid uint32, maxBody, maxAttach int) (*Message, error) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return nil, err
	}
	h := mr.Header
	m := &Message{UID: uid}

	if addrs, err := h.AddressList("From"); err == nil && len(addrs) > 0 {
		m.From = strings.ToLower(strings.TrimSpace(addrs[0].Address))
		m.FromName = strings.TrimSpace(addrs[0].Name)
	}
	m.Subject, _ = h.Subject()
	m.MessageID = firstMsgID(h.Get("Message-Id"))
	m.InReplyTo = firstMsgID(h.Get("In-Reply-To"))
	m.References = allMsgIDs(h.Get("References"))
	if d, err := h.Date(); err == nil {
		m.Date = d
	}

	var fallback string
	haveText := false
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		switch hdr := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := hdr.ContentType()
			b, _ := io.ReadAll(p.Body)
			if strings.HasPrefix(ct, "text/plain") {
				if !haveText {
					m.Body = string(b)
					haveText = true
				}
			} else if fallback == "" {
				fallback = string(b)
			}
		case *gomail.AttachmentHeader:
			att, err := readAttachment(hdr, p.Body, maxAttach)
			if err != nil {
				continue // drop oversized/unreadable attachments, keep the rest
			}
			m.Attachments = append(m.Attachments, att)
		}
	}
	if !haveText {
		m.Body = fallback
	}
	if maxBody > 0 && len(m.Body) > maxBody {
		m.Body = m.Body[:maxBody]
	}
	return m, nil
}

func readAttachment(h *gomail.AttachmentHeader, r io.Reader, max int) (Attachment, error) {
	name, _ := h.Filename()
	name = sanitizeFilename(name)
	if name == "" {
		name = "attachment"
	}
	var limit io.Reader = r
	if max > 0 {
		limit = io.LimitReader(r, int64(max)+1) // +1 so we can detect oversize
	}
	b, err := io.ReadAll(limit)
	if err != nil {
		return Attachment{}, err
	}
	if max > 0 && len(b) > max {
		return Attachment{}, fmt.Errorf("attachment %q exceeds %d bytes", name, max)
	}
	ct, _, _ := h.ContentType()
	return Attachment{Name: name, ContentType: ct, Size: int64(len(b)), Data: b}, nil
}

// sanitizeFilename reduces an attacker-controlled MIME filename to a plain
// basename. The FROM whitelist is the real trust boundary (perch's design),
// but a name like "../../etc/x" would otherwise escape the attachments dir
// and write into the agent's workspace — cheap to prevent, no downsides.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || name == "" {
		return ""
	}
	return name
}

func firstMsgID(s string) string {
	ids := allMsgIDs(s)
	if len(ids) > 0 {
		return ids[0]
	}
	return strings.TrimSpace(s)
}

func allMsgIDs(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.TrimSpace(f)
		if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") {
			out = append(out, f)
		}
	}
	return out
}
