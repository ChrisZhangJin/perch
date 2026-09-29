package message

import (
	"fmt"
	"io"
	netmail "net/mail"
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
	To          []string // lowercased addr-specs, in header order
	Cc          []string // same
	Subject     string
	Date        time.Time // RFC5322 Date: header; zero if absent/unparseable
	Body        string    // text/plain, in full (see Parse)
	Attachments []Attachment

	// Loop-protection headers, kept verbatim (lowercased, trimmed) so the
	// caller decides policy. See IsAutomated.
	AutoSubmitted string // RFC 3834 Auto-Submitted; "" when absent
	Precedence    string // de-facto Precedence: bulk|list|junk
	ListID        string // List-Id / List-Unsubscribe / List-Post presence
}

// IsAutomated reports whether this message announces itself as machine
// generated, and why. Auto-replying to such mail is how two mail robots end
// up in an unbounded exchange, each politely answering the other.
//
// RFC 3834: Auto-Submitted is absent or "no" for human-generated mail;
// anything else ("auto-generated", "auto-replied", ...) means do not
// auto-reply. Precedence and the List-* family are not standardised for this
// but are what mailing lists and bulk senders actually set.
//
// This is a claim by the sender, not proof. A robot that sets no headers at
// all — which is the common case — passes this check, so it cannot be the
// only defence.
func (m *Message) IsAutomated() (bool, string) {
	if v := m.AutoSubmitted; v != "" && v != "no" {
		return true, "Auto-Submitted: " + v
	}
	switch m.Precedence {
	case "bulk", "list", "junk":
		return true, "Precedence: " + m.Precedence
	}
	if m.ListID != "" {
		return true, "mailing-list header: " + m.ListID
	}
	return false, ""
}

// ThreadRoot returns the first References id if present, else the message's own id.
func (m *Message) ThreadRoot() string {
	if len(m.References) > 0 {
		return m.References[0]
	}
	return m.MessageID
}

// Parse reads an RFC5322 message, collecting the first text/plain part as
// Body and every attachment part into Attachments. maxAttach (0 = unlimited)
// caps a single attachment so a huge file can't exhaust memory — oversized
// attachments are dropped, not fatal.
//
// Body is returned in full. Parse used to also truncate it to MaxPromptBytes,
// which forced the wrong order of operations once quoted-history stripping
// arrived: the cap was spent on quoted text that was about to be thrown away,
// so on a long thread the quote ate the budget and the sender's actual
// request was what got cut. Callers now compose the transformations
// themselves — StripQuoted first, then TruncateUTF8 — see app.ProcessUnseen.
//
// This costs nothing in memory safety: Parse already reads each part fully
// into memory before any cap could apply. Only maxAttach ever bounded
// allocation.
func Parse(r io.Reader, uid uint32, maxAttach int) (*Message, error) {
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
	m.To = addressList(h, "To")
	m.Cc = addressList(h, "Cc")
	m.Subject, _ = h.Subject()
	m.MessageID = firstMsgID(h.Get("Message-Id"))
	m.InReplyTo = firstMsgID(h.Get("In-Reply-To"))
	m.References = allMsgIDs(h.Get("References"))
	if d, err := h.Date(); err == nil {
		m.Date = d
	}
	// Auto-Submitted carries optional parameters after a semicolon
	// (RFC 3834 §5), e.g. "auto-replied; owner-token=...". Only the keyword
	// matters here.
	m.AutoSubmitted = headerKeyword(h.Get("Auto-Submitted"))
	m.Precedence = headerKeyword(h.Get("Precedence"))
	for _, k := range []string{"List-Id", "List-Unsubscribe", "List-Post"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			m.ListID = k
			break
		}
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

// addressList returns the lowercased addr-specs from an address header, in
// header order, normalised the same way From is.
//
// Display names are dropped: the only consumers are "is the agent in To?"
// (an address comparison) and the recipients block in the agent prompt,
// where a list of bare addresses is both shorter and less ambiguous than
// "Chris <chris@163.com>".
//
// One malformed entry must not cost the whole header. go-message parses the
// list atomically and fails all-or-nothing, so on error we fall back to
// splitting on commas and keeping whatever parses individually — a real
// Outlook footgun ("Chris; chris@163.com" and friends) should degrade to a
// partial recipient list, not to an empty one, because an empty To is what
// the cc-only check reads as "not addressed to me".
//
// The comma split is naive and will mangle a display name that contains a
// comma ("Zhang, Chris" <c@163.com>) — but that half already round-tripped
// through the strict parser, so this path only ever sees headers the strict
// parser rejected, where a best-effort salvage beats nothing.
func addressList(h gomail.Header, key string) []string {
	if addrs, err := h.AddressList(key); err == nil {
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			if v := strings.ToLower(strings.TrimSpace(a.Address)); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	raw := strings.TrimSpace(h.Get(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		a, err := netmail.ParseAddress(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if v := strings.ToLower(strings.TrimSpace(a.Address)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// HasRecipient reports whether addr appears in list (case-insensitive).
func HasRecipient(list []string, addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	for _, v := range list {
		if strings.EqualFold(v, addr) {
			return true
		}
	}
	return false
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

// headerKeyword lowercases a header value and drops any ";"-delimited
// parameters, leaving the bare keyword.
func headerKeyword(v string) string {
	kw, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(kw))
}
