package message

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// TruncateUTF8 caps s at max bytes without splitting a multi-byte rune.
//
// A plain s[:max] cuts on a byte boundary, which for CJK text lands mid-rune
// roughly two times in three and yields invalid UTF-8 in the agent prompt.
// max <= 0 means no limit.
func TruncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Walk back from the cut until it sits on a rune boundary. A rune is at
	// most 4 bytes, so this steps back 3 times at worst.
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// quoteMarkers are line-anchored patterns that begin a quoted block. Every
// one is a convention meaning "everything below is the message being replied
// to", so matching one lets us cut to the end of the body.
//
// Case-insensitive, anchored at line start (with leading whitespace allowed)
// so a marker mentioned mid-sentence does not trigger a cut.
var quoteMarkers = []*regexp.Regexp{
	// Foxmail / QQ / 163: ------------------ 原始邮件 ------------------
	regexp.MustCompile(`(?im)^[ \t]*-{2,}[ \t]*原始邮件[ \t]*-{2,}`),
	// Outlook English: -----Original Message-----
	regexp.MustCompile(`(?im)^[ \t]*-{2,}[ \t]*original message[ \t]*-{2,}`),
	// Outlook / 网易 forward banner: ---------- 转发邮件 ----------
	regexp.MustCompile(`(?im)^[ \t]*-{2,}[ \t]*转发邮件[ \t]*-{2,}`),
	regexp.MustCompile(`(?im)^[ \t]*-{2,}[ \t]*forwarded message[ \t]*-{2,}`),
	// Chinese attribution: 在 2026年8月23日 ... 写道： / 于 ... 写道:
	// A digit is required between the opener and 写道 so that ordinary prose
	// ("在会议上他写道：这个可行") is not mistaken for an attribution line.
	// Client-generated attributions always carry a date.
	regexp.MustCompile(`(?im)^[ \t]*(在|于)[ \t]*\S.*\d.*写道[ \t]*[:：]`),
	// Gmail English attribution, incl. the common wrap where "wrote:" lands
	// on the following line. A digit is required for the same reason as the
	// Chinese form above ("On reflection I wrote: ..." must not match).
	regexp.MustCompile(`(?im)^[ \t]*on[ \t].*\d.*wrote[ \t]*:`),
	regexp.MustCompile(`(?im)^[ \t]*on[ \t].*\d.*\n[ \t]*wrote[ \t]*:`),
	// Header blocks pasted by Outlook / Foxmail. 发件人/From must be followed
	// closely by another header line, otherwise a body sentence starting
	// "From: " would trigger a cut.
	regexp.MustCompile(`(?im)^[ \t]*发件人[ \t]*[:：].*\n(.*\n){0,3}?[ \t]*(收件人|发送时间|主题|抄送)[ \t]*[:：]`),
	regexp.MustCompile(`(?im)^[ \t]*from[ \t]*:.*\n(.*\n){0,3}?[ \t]*(sent|to|subject|date)[ \t]*:`),
	// Outlook's underscore rule.
	regexp.MustCompile(`(?m)^[ \t]*_{5,}[ \t]*$`),
}

// StripQuoted removes the quoted history from an email body and reports how
// many bytes went away.
//
// Two passes, in order of confidence:
//
//  1. A quote marker (see quoteMarkers). Everything from the earliest marker
//     to the end of the body is quoted by convention, so it is cut.
//  2. Failing that, a trailing run of ">"-prefixed lines. Only a TRAILING run
//     is removed: cutting at the first ">" line would destroy an inline reply,
//     where the sender answers between the quoted paragraphs.
//
// If the result would be blank, the original is returned unchanged — the
// quote was the whole message (a bare forward), and an empty prompt is worse
// than a redundant one. There is deliberately no "removed too much" ratio
// guard beyond that: a one-line reply above a long quote is the case this
// function exists for, and a ratio test would skip exactly that.
//
// The markers are tuned for precision, not recall, because the two failure
// modes are not symmetric: a false positive silently deletes something the
// human wrote and the agent answers the wrong question, while a false
// negative merely leaves redundant text in the prompt and costs tokens. When
// a pattern is ambiguous, it does not cut.
func StripQuoted(body string) (string, int) {
	if body == "" {
		return body, 0
	}

	cut := len(body)
	for _, re := range quoteMarkers {
		if loc := re.FindStringIndex(body); loc != nil && loc[0] < cut {
			cut = loc[0]
		}
	}

	out := body
	if cut < len(body) {
		out = body[:cut]
	} else {
		out = stripTrailingQuotedLines(body)
	}

	if strings.TrimSpace(out) == "" {
		return body, 0
	}
	out = strings.TrimRight(out, " \t\r\n") + "\n"
	if len(out) >= len(body) {
		return body, 0
	}
	return out, len(body) - len(out)
}

// stripTrailingQuotedLines drops a trailing block of ">"-quoted lines, along
// with any blank lines and a single attribution-looking line directly above
// it ("Chris <x@y> 写道：" without the leading 在/于 that quoteMarkers wants).
func stripTrailingQuotedLines(body string) string {
	lines := strings.Split(body, "\n")
	i := len(lines)
	sawQuote := false
	for i > 0 {
		l := strings.TrimSpace(lines[i-1])
		if l == "" {
			i--
			continue
		}
		if strings.HasPrefix(l, ">") {
			sawQuote = true
			i--
			continue
		}
		break
	}
	if !sawQuote {
		return body
	}
	// Pull in a trailing attribution line if one sits just above the quote.
	if i > 0 {
		if l := strings.TrimSpace(lines[i-1]); attributionLine.MatchString(l) {
			i--
		}
	}
	return strings.Join(lines[:i], "\n")
}

// attributionLine matches the "<someone> wrote:" line that introduces a
// quoted block when it is not already covered by quoteMarkers.
var attributionLine = regexp.MustCompile(`(写道|wrote)[ \t]*[:：]$`)
