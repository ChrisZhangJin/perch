package message

import (
	"io"
	"strings"

	gomail "github.com/emersion/go-message/mail"
)

// Message is a parsed inbound email reduced to the fields the watcher needs.
type Message struct {
	UID        uint32
	From       string // lowercased addr-spec, e.g. "alice@163.com"
	MessageID  string // e.g. "<abc@163.com>"
	InReplyTo  string
	References []string
	Subject    string
	Body       string // text/plain, truncated to maxBody
}

// ThreadRoot returns the first References id if present, else the message's own id.
func (m *Message) ThreadRoot() string {
	if len(m.References) > 0 {
		return m.References[0]
	}
	return m.MessageID
}

// Parse reads an RFC5322 message, preferring the first text/plain part.
func Parse(r io.Reader, uid uint32, maxBody int) (*Message, error) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return nil, err
	}
	h := mr.Header
	m := &Message{UID: uid}

	if addrs, err := h.AddressList("From"); err == nil && len(addrs) > 0 {
		m.From = strings.ToLower(strings.TrimSpace(addrs[0].Address))
	}
	m.Subject, _ = h.Subject()
	m.MessageID = firstMsgID(h.Get("Message-Id"))
	m.InReplyTo = firstMsgID(h.Get("In-Reply-To"))
	m.References = allMsgIDs(h.Get("References"))

	var fallback string
	haveText := false
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		ih, ok := p.Header.(*gomail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := ih.ContentType()
		b, _ := io.ReadAll(p.Body)
		if strings.HasPrefix(ct, "text/plain") {
			m.Body = string(b)
			haveText = true
			break
		}
		if fallback == "" {
			fallback = string(b)
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
