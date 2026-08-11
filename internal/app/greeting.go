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

// greetingRe matches a salutation line that the agent must open its reply
// with, per the perch protocol. We accept the canonical English forms
// (Hi/Hello/Hey + name + comma, or Good morning/afternoon/evening) and the
// generic "Hi there," form that works when the sender's name is unknown.
// The trailing comma is required so we don't false-positive on body
// prose like "Hi there is a regression" — a greeting is a complete line
// that ends with punctuation.
//
// Examples that match (case-insensitive, leading/trailing whitespace ignored):
//
//	Hi Chris,
//	hello chris zhang jin,
//	Hey 张进,
//	Hi there,
//	Good morning,
//	Good morning, Chris,
//	Good afternoon, Chris,
//	GOOD EVENING,
//	"  hi Chris,  "
var greetingRe = regexp.MustCompile(
	`^\s*(?i:hi|hello|hey)\s+(?:there|[^\s,](?:[^\n,]*[^\s,])?)\s*,\s*$|^\s*(?i:good\s+(?:morning|afternoon|evening))(?:\s*,?\s+[^\n,]+)?\s*,?\s*$`,
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

	scanner := strings.SplitAfter(agentStdout, "\n")
	offset := 0
	for _, line := range scanner {
		// Strip the trailing \n that SplitAfter leaves in so the regex
		// sees just the line content. We trim leading/trailing whitespace
		// inside the regex itself.
		trimmed := strings.TrimRight(line, "\n")
		if greetingRe.MatchString(trimmed) {
			return agentStdout[offset:], nil
		}
		offset += len(line)
	}
	return agentStdout, ErrNoGreeting
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
