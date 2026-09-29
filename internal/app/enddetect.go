package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/message"
)

// End-of-conversation detection (cfg.EndDetect).
//
// A thread often ends with a message that is not a request: "thanks, got
// it", "ok 👍", "好的，谢谢". perch answers those, which costs an agent run
// and invites the other side to be polite back — a two-robot politeness
// loop that the reply cap eventually stops, but only after several wasted
// runs.
//
// One Jev call decides whether the latest message closes the thread. It
// runs after the whitelist and the loop guards (so a skip is never a
// security decision) and before the duration probe and the agent run (so a
// skip actually saves the expensive part).
//
// The two failure modes are wildly asymmetric, and the design follows that:
//
//	false "over"     — a real request is silently dropped. The sender gets
//	                   nothing, forever, and nobody finds out.
//	false "not over" — perch answers a "thanks". Costs one agent run.
//
// So this path is deliberately reluctant. It is off by default; it only
// ever looks at thread continuations, never at a new email; it requires the
// probability of "over" to clear a high bar (cfg.EndDetectMinProb, 0.9) and
// not merely to be the model's pick; and every error, timeout, missing key
// or unrecognised answer means "process normally". The only way an email
// gets dropped is an explicit, confident, positive answer.

// Question key and option names for the end-of-conversation call.
const (
	jevQEnded = "ended"
	// endOver is the option perch gates on: the thread is finished.
	endOver = "over"
	// endReply is its counterpart, named so the model has a concrete
	// alternative to put mass on rather than a bare negation.
	endReply = "reply"
)

// endDetectMaxBytes caps the body sent to Jev. A closing is a sentence; a
// long body is itself evidence the thread is not over, so there is nothing
// to learn from the tail. Much smaller than MaxPromptBytes on purpose —
// this is billed per input token on every continuation.
const endDetectMaxBytes = 4096

// BuildEndDetect frames the "is this thread finished?" question.
//
// The subject is included here (unlike the duration probe, which ignores
// it): on a continuation the subject is the thread's own "Re: ..." and
// gives the model the topic that the one-line body is a reply *to*.
//
// The wording names both languages explicitly. perch's senders write
// Chinese and English, and a closing is exactly the kind of short,
// idiomatic utterance where a model given an English-only frame starts
// treating "好的" as unparseable rather than as "ok".
func BuildEndDetect(subject, body string) (state any, questions map[string]jev.Question) {
	state = map[string]any{
		"subject":        subject,
		"latest_message": body,
	}
	questions = map[string]jev.Question{
		jevQEnded: {
			Type: jev.TypeChoice,
			Question: "This is the latest message in an ongoing email thread with an AI assistant. " +
				"The message may be written in English or Chinese. " +
				"Does the sender still expect a reply?",
			Criteria: map[string]string{
				endOver: "No reply is expected. The message is purely an acknowledgement, a thank-you, " +
					"a confirmation of receipt or a sign-off (\"thanks, got it\", \"ok\", \"好的\", " +
					"\"收到，谢谢\", \"辛苦了\"). It contains no question, no new request, and no " +
					"correction or disagreement that calls for a response.",
				endReply: "A reply is expected. The message asks a question, makes a request, reports a " +
					"problem, disagrees, or adds information the assistant is meant to act on — even " +
					"if it also contains thanks or pleasantries.",
			},
		},
	}
	return state, questions
}

// ParseEndDetect returns the calibrated probability that the thread is over.
//
// An unrecognised choice is a hard error, never folded into either option.
// Note what is returned: the probability of endOver specifically, not of
// whichever option the model picked, and emphatically not the vendor's
// `confidence` field (a rescaling that reads 0.84 for a 0.92 answer). The
// caller compares it against a high threshold, so a "reply" answer simply
// yields a low number and falls through — which is the safe direction.
func ParseEndDetect(ans map[string]jev.Answer) (probOver float64, err error) {
	a, ok := ans[jevQEnded]
	if !ok {
		return 0, fmt.Errorf("jev: answer %q missing", jevQEnded)
	}
	switch strings.ToLower(strings.TrimSpace(a.Choice)) {
	case endOver, endReply:
	default:
		return 0, fmt.Errorf("jev: unrecognised %s choice %q", jevQEnded, a.Choice)
	}
	if a.Probabilities == nil {
		return 0, fmt.Errorf("jev: %s answer carried no probabilities", jevQEnded)
	}
	return a.Probabilities[endOver], nil
}

// conversationEnded reports whether this message closes its thread and can
// be left unanswered. False for every uncertain case; see the file comment.
func (a *App) conversationEnded(ctx context.Context, m *message.Message) bool {
	// The "didn't run" reasons are logged at DEBUG, not INFO: two of the
	// three are the steady state (feature off, or every thread root), and
	// an INFO line per email saying "the disabled feature stayed disabled"
	// is the chatter that makes operators stop reading logs.
	if !a.cfg.EndDetect {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "disabled",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return false
	}
	if a.jev == nil {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "no_client",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return false
	}
	// Thread continuations only. A brand-new email is always processed: a
	// first contact that merely *looks* like a closing is far more likely
	// to be a terse request ("ok, do the migration") than a goodbye, and
	// there is no prior turn for "thanks" to be thanking.
	if m.InReplyTo == "" && len(m.References) == 0 {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "thread_root",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return false
	}

	// Quoted history would drown the one line the sender actually wrote —
	// the whole signal here is "short acknowledgement above a long quote".
	// Stripped unconditionally, independent of cfg.StripQuoted: that knob
	// governs what the agent sees, and leaving the quote in would make this
	// question unanswerable rather than merely verbose.
	body, _ := message.StripQuoted(m.Body)
	body = message.TruncateUTF8(body, endDetectMaxBytes)

	state, questions := BuildEndDetect(m.Subject, body)
	ans, err := a.jev.Ask(ctx, "end_detect", state, questions)
	if err == nil {
		var prob float64
		if prob, err = ParseEndDetect(ans); err == nil {
			// One line either way, carrying both numbers that produced the
			// verdict — and only one line: a skip is invisible downstream
			// (no reply, no agent run, nothing else logs), so this is the
			// sole record that an email was deliberately left unanswered.
			outcome, ended := "processed", prob >= a.cfg.EndDetectMinProb
			if ended {
				outcome = "skipped"
			}
			a.log.Info("end_detect outcome", "outcome", outcome, "prob_over", prob,
				"threshold", a.cfg.EndDetectMinProb, "from", m.From,
				"subject", m.Subject, "message_id", m.MessageID)
			return ended
		}
	}
	a.log.Warn("end_detect failed; processing normally",
		"outcome", "processed", "reason", "error",
		"from", m.From, "subject", m.Subject, "message_id", m.MessageID, "err", err)
	return false
}
