package app

import (
	"errors"
	"regexp"
	"strings"
)

// ErrNoGreeting is returned by ExtractBodyAfterGreeting when the agent's
// stdout does not contain a recognized greeting line. The caller is
// expected to log the absence and send the agent's stdout as-is.
var ErrNoGreeting = errors.New("agent stdout missing greeting line")

// greetingRe matches a salutation line the agent uses to open its reply,
// per the perch protocol. We accept only the two canonical forms
// (Hi/Hello + name + comma) and the generic "Hi there," fallback for
// senders with no known display name. The trailing comma is required so
// we don't false-positive on body prose like "Hi there is a regression"
// — a greeting ends with punctuation.
//
// Anchoring: the greeting must be terminated by a newline (or end of
// stdout). It need NOT sit on its own line — a weak model sometimes runs
// the salutation into the previous sentence ("...report attached.Hi
// Chris,\n"). We tolerate that by not requiring a newline immediately
// before the greeting; the splitter still returns everything from the
// greeting onward, dropping the run-on prefix.
//
// The full-line variant is preferred (it's what the prompt asks for);
// the run-on variant is the fallback. Both live in the same regex.
//
// Examples that match (case-insensitive):
//
//	Hi Chris,
//	hello chris zhang jin,
//	Hi 张进,
//	Hi there,
//	"  hi Chris,  "
//	"...report attached.Hi Chris Zhang Jin,\n"   ← run-on fallback
var greetingRe = regexp.MustCompile(
	`(?i:hi|hello)[ \t]+(?:there|[^\s,](?:[^\n,]*[^\s,])?)[ \t]*,[ \t]*\n`,
)

// greetingLineRe matches a greeting that occupies an ENTIRE line (whitespace
// on either side is OK). Used by IsDegenerateReply to spot "reply is just a
// greeting" — a separate, stricter matcher than greetingRe (which tolerates
// run-on prefixes).
var greetingLineRe = regexp.MustCompile(
	`^\s*(?i:hi|hello)\s+(?:there|[^\s,](?:[^\n,]*[^\s,])?)\s*,\s*$`,
)

// ExtractBodyAfterGreeting locates the first line of agentStdout that
// matches a recognized greeting pattern and returns the substring starting
// at that line. Anything the agent wrote BEFORE the greeting (audit notes,
// classification reports, tool-call narration) is dropped — this is the
// whole point of the protocol: perch only ships content past the greeting,
// so the agent has no reason to invest tokens in a preamble that will be
// silently discarded.
//
// senderName is the display name from the email's From header (e.g. "Chris"
// or "Chris Zhang Jin"); it is informational here — we accept any
// well-formed greeting even if the name doesn't match senderName exactly,
// because agents sometimes greet by the local-part or a short form. The
// strict constraint lives in BuildPrompt, not in this parser.
//
// Returns ErrNoGreeting when no greeting line is found. The caller decides
// what to do (we log and send as-is; we never block the reply on a missing
// greeting, since that would let a single prompt-drift incident stall the
// daemon).
func ExtractBodyAfterGreeting(agentStdout, senderName string) (string, error) {
	_ = senderName // reserved for a future strict-mode; kept for API stability

	// Append a trailing newline so a greeting that ends the stdout (no
	// final \n) still terminates the regex.
	haystack := agentStdout
	if !strings.HasSuffix(haystack, "\n") {
		haystack = haystack + "\n"
	}
	loc := greetingRe.FindStringIndex(haystack)
	if loc == nil {
		return agentStdout, ErrNoGreeting
	}
	// loc[0] points at the first char of the greeting itself (e.g. the "H"
	// in "Hi Chris,"). Skip any leading horizontal whitespace on the same
	// line so the reply looks clean, but keep the greeting itself intact.
	start := loc[0]
	// Walk backward past leading spaces/tabs on the same line — but stop
	// at the previous newline (or start of string). This trims "   Hi
	// Chris," down to "Hi Chris," without swallowing preceding content on
	// a different line.
	for start > 0 {
		c := agentStdout[start-1]
		if c == ' ' || c == '\t' {
			start--
			continue
		}
		break
	}
	// Re-trim any leading whitespace we may have kept from the beginning
	// of the extracted region.
	body := agentStdout[start:]
	body = strings.TrimLeft(body, " \t")
	return body, nil
}

// IsDegenerateReply reports whether body carries no actual content — it is
// empty (or only whitespace), or it consists solely of the mandatory
// greeting line with nothing after it. Such a reply is the signature of a
// run that terminated prematurely: the agent emitted the salutation the
// greeting protocol demands and then stopped before doing the task. Observed
// 2026-08-12 when a weak model opened a resumed thread with "Hi Chris," as a
// tool-call-free message, ending its turn on the spot — perch then shipped a
// bare "Hi Chris," to the human. The caller must never email such a reply;
// it retries the run, then flags it, instead.
func IsDegenerateReply(body string) bool {
	sawGreeting := false
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue // blank lines carry no content
		}
		if !sawGreeting && greetingLineRe.MatchString(line) {
			sawGreeting = true
			continue // the greeting itself is not "content"
		}
		return false // a real, non-greeting line — this reply has a body
	}
	return true // nothing but blanks and/or a lone greeting
}

// preview returns the first maxLen bytes of s, collapsing newlines so a
// multi-line agent stdout fits on a single log line. Used by the caller
// to attach a short diagnostic to the missing-greeting warning.
func preview(s string) string {
	const maxLen = 120
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}
