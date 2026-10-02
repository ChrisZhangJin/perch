package app

import (
	"strings"
	"time"
)

// QuoteHistory renders the inbound email as the quoted block a mail client
// puts under a reply, so perch's replies carry the thread's history the way
// a human's would. The agent never sees or writes this: it is appended to
// the agent's answer after the run, which keeps the email complete while the
// agent's context stays small (quoted history is stripped from the prompt on
// resumed sessions).
//
// body is the email as received, so it already carries the earlier quotes and
// the history accumulates round by round. The attribution line uses the same
// shape as Gmail ("On <date>, <who> wrote:") or the Chinese clients
// ("在 <date>，<who> 写道："), both of which message.StripQuoted recognizes —
// so when the next message quotes this one, perch strips it again.
func QuoteHistory(from, fromName string, date time.Time, body string, zh bool) string {
	body = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	if body == "" {
		return ""
	}
	who := from
	if fromName != "" {
		who = fromName + " <" + from + ">"
	}
	var b strings.Builder
	b.WriteString("\n\n")
	switch {
	case zh && !date.IsZero():
		b.WriteString("在 " + date.Format("2006-01-02 15:04") + "，" + who + " 写道：\n")
	case zh:
		b.WriteString(who + " 写道：\n")
	case !date.IsZero():
		b.WriteString("On " + date.Format("2006-01-02 15:04") + ", " + who + " wrote:\n")
	default:
		b.WriteString(who + " wrote:\n")
	}
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, ">") {
			b.WriteString(">" + line + "\n")
		} else {
			b.WriteString("> " + line + "\n")
		}
	}
	return b.String()
}
