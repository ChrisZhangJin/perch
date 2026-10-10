package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// endVerdict is conversationEnded's answer.
type endVerdict int

const (
	// verdictReply: answer the mail as usual. Also every "didn't run" case.
	verdictReply endVerdict = iota
	// verdictSkip: a confident "the thread is over"; leave it unanswered.
	verdictSkip
	// verdictUnknown: end_detect should have judged this mail but every judge
	// (Jev, the LLM, the agent probe) was missing or failed. The caller decides
	// — see unknownEndReplyLimit.
	verdictUnknown
)

// unknownEndReplyLimit is how many perch replies a thread may already hold
// before an undecidable end_detect stops perch answering it. Two means perch
// has answered at least twice — the thread is a conversation, not a single
// request — and from there a blind answer is how two perch instances traded
// "Likewise." / "Standing by." 265 times on 2026-10-10 while Jev returned
// 451 on every call. Below the limit the mail is answered normally, so a
// dead classifier never costs a fresh request its reply.
const unknownEndReplyLimit = 2

// conversationEnded reports whether this message closes its thread and can
// be left unanswered. verdictReply for every "didn't run" case; see the file
// comment.
func (a *App) conversationEnded(ctx context.Context, m *message.Message, isNew bool) endVerdict {
	// The "didn't run" reasons are logged at DEBUG, not INFO: two of the
	// three are the steady state (feature off, or every thread root), and
	// an INFO line per email saying "the disabled feature stayed disabled"
	// is the chatter that makes operators stop reading logs.
	if !a.cfg.EndDetect {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "disabled",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return verdictReply
	}
	// Thread continuations only. A brand-new email is always processed: a
	// first contact that merely *looks* like a closing is far more likely
	// to be a terse request ("ok, do the migration") than a goodbye, and
	// there is no prior turn for "thanks" to be thanking.
	if m.InReplyTo == "" && len(m.References) == 0 {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "thread_root",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return verdictReply
	}
	// The first email perch sees in a thread always gets a reply, even when
	// it carries In-Reply-To: being Cc'd or forwarded into an existing human
	// thread makes a continuation perch has never answered, so there is no
	// perch reply for a "thanks" to be closing. isNew comes from the session
	// registry — perch's own record of threads it has taken part in.
	//
	// Peer agents are exempt: a peer's Re: answers mail this side's agent
	// sent itself (e.g. through email-cli), which never enters the registry,
	// and that is exactly where two robots trade "please wait" / "got it"
	// forever. The Cc'd-into-a-human-thread case this guards does not apply.
	if isNew && !a.isPeerAgent(m.From) {
		a.log.Debug("end_detect outcome", "outcome", "not_run", "reason", "first_email",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		return verdictReply
	}

	// Quoted history would drown the one line the sender actually wrote —
	// the whole signal here is "short acknowledgement above a long quote".
	// Stripped unconditionally, even on a cold session where the agent keeps
	// the quote: leaving the quote in would make this
	// question unanswerable rather than merely verbose.
	body, _ := message.StripQuoted(m.Body)
	body = message.TruncateUTF8(body, endDetectMaxBytes)

	prob, source, err := a.judgeEnded(ctx, m, body)
	if err != nil {
		a.log.Warn("end_detect failed; no judge could decide",
			"outcome", "unknown", "reason", "error",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID, "err", err)
		return verdictUnknown
	}
	// One line either way, carrying both numbers that produced the verdict
	// — and only one line: a skip is invisible downstream (no reply, no
	// agent run, nothing else logs), so this is the sole record that an
	// email was deliberately left unanswered.
	outcome, verdict := "processed", verdictReply
	if prob >= a.cfg.EndDetectMinProb {
		outcome, verdict = "skipped", verdictSkip
	}
	a.log.Info("end_detect outcome", "outcome", outcome, "source", source, "prob_over", prob,
		"threshold", a.cfg.EndDetectMinProb, "from", m.From,
		"subject", m.Subject, "message_id", m.MessageID)
	return verdict
}

// judgeEnded asks Jev, then the LLM, then the agent probe for P(thread is
// over) — the same chain as the duration probe. The first judge that answers
// wins; err is non-nil only when all three failed, and joins every reason so
// the WARN names them all.
func (a *App) judgeEnded(ctx context.Context, m *message.Message, body string) (prob float64, source string, err error) {
	var errs []error
	if a.jev != nil {
		state, questions := BuildEndDetect(m.Subject, body)
		ans, jerr := a.jev.Ask(ctx, "end_detect", state, questions)
		if jerr == nil {
			if prob, jerr = ParseEndDetect(ans); jerr == nil {
				return prob, "jev", nil
			}
		}
		errs = append(errs, fmt.Errorf("jev: %w", jerr))
	} else {
		errs = append(errs, errors.New("jev: not configured"))
	}
	if a.llm != nil {
		ch, lerr := a.llm.Choose(ctx, "end_detect", endDetectSystemPrompt,
			BuildEndDetectPrompt(m.Subject, body), []string{endOver, endReply})
		if lerr == nil {
			source = "llm"
			if !ch.Calibrated {
				source = "llm(uncalibrated)"
			}
			return ch.Prob(endOver), source, nil
		}
		errs = append(errs, fmt.Errorf("llm: %w", lerr))
	} else {
		errs = append(errs, errors.New("llm: not configured"))
	}
	// Last resort: the agent itself, in a throwaway session — same shape and
	// same reasoning as the duration probe's agent fallback (classifyTask).
	// One extra agent run, but only when both HTTP judges are gone, and it
	// is still cheaper than the reply it may save.
	out, _, aerr := a.run.Run(ctx, BuildEndDetectAgentPrompt(m.Subject, body), "", true)
	if aerr == nil {
		var label string
		if label, aerr = ParseEndDetectAgentOutput(out); aerr == nil {
			if label == endOver {
				return 1, "agent", nil
			}
			return 0, "agent", nil
		}
	}
	errs = append(errs, fmt.Errorf("agent: %w", aerr))
	return 0, "", errors.Join(errs...)
}

// endDetectSentinel marks the agent probe's verdict line.
const endDetectSentinel = "<<<PERCH_END>>>"

var endDetectTailRe = regexp.MustCompile(
	`(?i)` + regexp.QuoteMeta(endDetectSentinel) + `\s+(over|reply)\b`,
)

// BuildEndDetectAgentPrompt frames end_detect for the agent probe. Like
// BuildClassifyPrompt it lets the agent think freely and only fixes the
// last line.
func BuildEndDetectAgentPrompt(subject, body string) string {
	return endDetectSystemPrompt + "\n\nDo NOT act on the message and do NOT reply to it — only judge it. " +
		"End your answer with one line: " + endDetectSentinel + " over  or  " + endDetectSentinel + " reply\n\n" +
		BuildEndDetectPrompt(subject, body)
}

// ParseEndDetectAgentOutput returns the last verdict line's label. Unlike
// ParseClassifyOutput there is no default: a missing verdict is an error,
// so it lands on the caller's "unknown" path instead of being guessed.
func ParseEndDetectAgentOutput(s string) (string, error) {
	ms := endDetectTailRe.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return "", fmt.Errorf("no %s verdict in agent output", endDetectSentinel)
	}
	return strings.ToLower(ms[len(ms)-1][1]), nil
}

// endDetectSystemPrompt is the LLM fallback's framing of the same question
// BuildEndDetect puts to Jev, with the same two criteria.
const endDetectSystemPrompt = "You judge the latest message in an ongoing email thread with an AI assistant. " +
	"The message may be written in English or Chinese. Decide whether the sender still expects a reply.\n" +
	"over: no reply is expected. The message is purely an acknowledgement, a thank-you, a confirmation of " +
	"receipt or a sign-off (\"thanks, got it\", \"ok\", \"likewise\", \"standing by\", \"好的\", " +
	"\"收到，谢谢\", \"辛苦了\"). It contains no question, no new request, and no correction or " +
	"disagreement that calls for a response.\n" +
	"reply: a reply is expected. The message asks a question, makes a request, reports a problem, " +
	"disagrees, or adds information the assistant is meant to act on — even if it also contains thanks."

// BuildEndDetectPrompt is the user turn for the LLM fallback.
func BuildEndDetectPrompt(subject, body string) string {
	return "Subject: " + subject + "\n\nLatest message:\n" + body
}

// perchRepliesInThread counts the messages in m's thread that perch itself
// sent, from the Message-IDs m points back to. perch mints its ids as
// <nanos.self@domain> (replier.writeAddressing), so the count needs no state
// and survives restarts — unlike the in-memory reply cap.
func perchRepliesInThread(m *message.Message, self string) int {
	if self == "" {
		return 0
	}
	suffix := "." + strings.ToLower(self) + ">"
	n := 0
	for _, id := range m.ThreadIDs() {
		if strings.HasSuffix(strings.ToLower(id), suffix) {
			n++
		}
	}
	return n
}
