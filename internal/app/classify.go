package app

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/message"
)

// classifySentinel is the marker the classifier agent must emit on its
// final answer line. Everything before it is free-form — the agent may
// read files, run tools, think out loud, whatever helps it decide.
// Only the sentinel-tail line is parsed by perch.
const classifySentinel = "<<<PERCH_CLASSIFY>>>"

// BuildClassifyPrompt frames a permissive classification prompt. The agent
// is invited to analyze freely (read files, use tools, reason) and is only
// asked to close its answer with a single line of the form:
//
//	<<<PERCH_CLASSIFY>>> <short|long> <eta_minutes>
//
// Locking the model behind an "output only two lines, no explanation"
// contract makes weaker models skip tool calls and hallucinate — better
// to let them work, then pluck the verdict off the last line.
// subject is intentionally unused: the classifier judges purely on the
// task body — user-written subjects are often terse or misleading and
// would only distort the verdict.
func BuildClassifyPrompt(from, _ /*subject*/, body string) string {
	var b strings.Builder
	b.WriteString("Evaluate the operation below. Classify it as short or long, and estimate how long it would take to finish. Do NOT actually run it — only estimate the duration. At the end of your reply, give a brief summary. Prefix the summary with ")
	b.WriteString(classifySentinel)
	b.WriteString(". Example: ")
	fmt.Fprintf(&b, "%s short 1min\n\n", classifySentinel)
	fmt.Fprintf(&b, "Task from %s:\n", from)
	b.WriteString(body)
	return b.String()
}

// classifyTailRe matches the sentinel line: sentinel + short|long + integer.
// (?i) so weaker models can emit "SHORT" / "Long"; leading whitespace
// tolerated so the sentinel can sit inside a bullet or block.
var classifyTailRe = regexp.MustCompile(
	`(?i)` + regexp.QuoteMeta(classifySentinel) + `\s+(short|long)\s+(\d+)`,
)

// ParseClassifyOutput extracts (runtime, etaMinutes) from the classifier
// agent's stdout. Scans from the tail so if the sentinel appears more
// than once (agent quoted its own examples earlier), the last occurrence
// — the real verdict — wins.
//
// Missing sentinel or malformed value → ("short", 0), the safe default:
// skip the ack path entirely so a classifier hiccup can never delay the
// real reply.
func ParseClassifyOutput(s string) (runtime string, etaMin int) {
	runtime = "short"
	etaMin = 0
	matches := classifyTailRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return runtime, etaMin
	}
	last := matches[len(matches)-1]
	runtime = strings.ToLower(last[1])
	if n, err := strconv.Atoi(last[2]); err == nil && n > 0 {
		etaMin = n
	}
	return runtime, etaMin
}

// ---------------------------------------------------------------------------
// Jev classifier (cfg.Classifier == "jev")
//
// Same two outputs as the agent probe above — short|long and an ETA — but
// asked of a model that answers typed questions directly instead of writing
// a paragraph we then regex. The agent probe costs a full agent invocation
// per email, which is the documented reason cfg.LongTaskAck is off by
// default; this path costs one HTTP round trip.
//
// The agent probe stays as the fallback for every failure mode, so nothing
// here needs to be defensive about availability — only about meaning.
// ---------------------------------------------------------------------------

// Question keys in the Jev request. Answers come back in a map under these
// same keys, so they are named once here.
const (
	jevQRuntime = "runtime"
	jevQETA     = "eta"
)

// jevETALevels is the ordered rubric for the ETA score, paired with the
// minute figure perch quotes to the sender for each level. A Score answer
// is a position in this list, not a number of minutes — asking for
// free-form minutes would be asking a typed model to guess a continuous
// value, which is exactly what it is not for.
//
// Positions are ZERO-based and fractional. Verified against the live API on
// 2026-09-29: a task obviously in the top band came back as score 3.99 with
// probabilities {"4": 1.0}, i.e. the score is the probability-weighted
// expectation over rubric indices, not the argmax and not a 1-based rank.
// Rounding recovers the intended level; guessing 1-based here would have
// quoted every sender one band too fast.
//
// The minute values are deliberately round: the ack text says "around N
// minutes", and a human reading "around 17 minutes" would reasonably expect
// that to mean something.
var jevETALevels = []struct {
	desc    string
	minutes int
}{
	{"under a minute: a question answered from memory, or a single file read", 1},
	{"a few minutes: a handful of file edits, a short search, one build", 5},
	{"a quarter of an hour: a focused change across several files, with tests", 15},
	{"half an hour: a feature touching many files, or a long-running build or test suite", 30},
	{"an hour or more: a migration, a sweep across a whole codebase, or repeated build-test cycles", 60},
}

// BuildJevClassify frames the duration probe as two typed questions.
//
// subject is intentionally absent for the same reason BuildClassifyPrompt
// ignores it: user-written subjects are terse or misleading often enough
// that including them only adds noise to the verdict.
func BuildJevClassify(from, body string) (state any, questions map[string]jev.Question) {
	state = map[string]any{
		"sender": from,
		"task":   body,
	}
	rubric := make([]string, len(jevETALevels))
	for i, l := range jevETALevels {
		rubric[i] = l.desc
	}
	questions = map[string]jev.Question{
		jevQRuntime: {
			Type:     jev.TypeChoice,
			Question: "An AI coding agent has been emailed the task above. Will finishing it take long enough that the sender should get an interim 'working on it' acknowledgement first?",
			Criteria: map[string]string{
				"short": "the agent can answer within a couple of minutes; an interim ack would arrive alongside the real reply and just be noise",
				"long":  "the agent will be busy long enough that silence would leave the sender wondering whether the email arrived",
			},
		},
		jevQETA: {
			Type:     jev.TypeScore,
			Question: "How long will the agent take to finish the task above?",
			Criteria: rubric,
		},
	}
	return state, questions
}

// ParseJevAnswers turns Jev's answers into the same (runtime, etaMin) pair
// ParseClassifyOutput produces.
//
// The two fields get deliberately different error severity, because they
// carry different weight:
//
//   - runtime decides whether an email is sent at all, so an unrecognised
//     value is a hard error. It is never quietly folded into "short". A
//     sibling integration mapped unknown choices onto a default and
//     silently discarded 5125 of 7412 verdicts for weeks.
//   - etaMin only decorates the ack text, and BuildLongAckBody already
//     renders a sensible sentence when it is 0. A missing or out-of-range
//     score therefore degrades to 0 rather than throwing away a correct
//     runtime verdict over a cosmetic field.
//
// The returned probability is the calibrated probability of the chosen
// runtime — Answer.Prob, NOT the vendor's `confidence` field, which is a
// rescaling that reports 0.24 for a decisive 62/38 answer. It is returned
// for logging only; perch does not threshold on it.
func ParseJevAnswers(ans map[string]jev.Answer) (runtime string, etaMin int, prob float64, err error) {
	rt, ok := ans[jevQRuntime]
	if !ok {
		return "", 0, 0, fmt.Errorf("jev: answer %q missing", jevQRuntime)
	}
	switch strings.ToLower(strings.TrimSpace(rt.Choice)) {
	case "short":
		runtime = "short"
	case "long":
		runtime = "long"
	default:
		return "", 0, 0, fmt.Errorf("jev: unrecognised %s choice %q", jevQRuntime, rt.Choice)
	}
	prob = rt.Prob()

	// Score is a *float64 so "the model didn't answer" stays distinct from
	// "the model answered 0" — decoding a missing field to 0.0 would invent
	// a rubric level that was never chosen.
	if s := ans[jevQETA].Score; s != nil {
		if i := int(math.Round(*s)); i >= 0 && i < len(jevETALevels) {
			etaMin = jevETALevels[i].minutes
		}
	}
	return runtime, etaMin, prob, nil
}

// classifyTask answers "will this run long, and roughly how long?" for one
// inbound message. ok is false when nothing could answer, which the caller
// reads as "skip the ack path entirely".
//
// Jev first when configured, the agent probe otherwise — and the agent
// probe is also the fallback for every Jev failure: no key, transport
// error, timeout, rate limit, unrecognised choice. That ordering is the
// whole safety story. Turning the classifier to "jev" can make the probe
// slower in the worst case (one timed-out HTTP call, then the agent run it
// would have done anyway) but can never make the ack path stop working.
func (a *App) classifyTask(ctx context.Context, m *message.Message) (runtime string, etaMin int, ok bool) {
	// Distinguishes "the agent probe was the configured classifier" from
	// "the agent probe caught a Jev failure" in the outcome line — the two
	// look identical in the logs otherwise, and only one of them is a
	// problem worth chasing.
	fellBack := false
	if a.jev != nil {
		state, questions := BuildJevClassify(m.From, m.Body)
		ans, err := a.jev.Ask(ctx, "classify", state, questions)
		if err == nil {
			rt, eta, prob, perr := ParseJevAnswers(ans)
			if perr == nil {
				// prob is the calibrated probability of the chosen option.
				// Logged, never thresholded — and note it is NOT the API's
				// `confidence` field, which is a rescaling that reads far
				// lower than the real split. See internal/jev.
				a.log.Info("classify outcome", "source", "jev", "runtime", rt,
					"eta_min", eta, "prob", prob, "model", a.jev.Model(),
					"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
				return rt, eta, true
			}
			err = perr
		}
		// The fallback is named in the line itself: a WARN that only says
		// what broke leaves the reader guessing whether the email stalled.
		a.log.Warn("jev classify failed; using agent probe",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID, "err", err)
		fellBack = true
	}

	// The agent probe uses IsNew=true with sessionID="" so it never pollutes
	// the thread's actual session; agents that persist state (nanopi) will
	// mint a throwaway session id we deliberately drop on the floor.
	out, _, err := a.run.Run(ctx, BuildClassifyPrompt(m.From, m.Subject, m.Body), "", true)
	if err != nil {
		a.log.Warn("classifier run failed; skipping ack path",
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID, "err", err)
		return "", 0, false
	}
	runtime, etaMin = ParseClassifyOutput(out)
	source := "agent"
	if fellBack {
		source = "agent(fallback)"
	}
	a.log.Info("classify outcome", "source", source, "runtime", runtime,
		"eta_min", etaMin, "from", m.From, "subject", m.Subject, "message_id", m.MessageID)
	return runtime, etaMin, true
}

// BuildLongAckBody is the interim acknowledgement perch sends when a task is
// classified as long. It includes the ETA (in minutes) if the classifier
// supplied one; otherwise it omits the duration. A greeting and sign-off are
// included so the ack renders in the thread like any other reply.
//
// zh selects the language, from the same looksChinese check the failure
// notices use: this body used to be Chinese unconditionally, which mailed
// Chinese to English senders. perch has no agent in the loop here, so it
// carries one phrasing per language itself. agentName signs the message
// (runner.AgentDisplayName of the agent mailbox).
func BuildLongAckBody(fromName, agentName string, etaMin int, zh bool) string {
	var b strings.Builder
	b.WriteString(greetingFor(fromName, zh))
	b.WriteString("\n\n")
	if zh {
		b.WriteString("这个任务执行时间比较长，我先处理一下，请稍等。")
		if etaMin > 0 {
			fmt.Fprintf(&b, "预计需要 %d 分钟左右。", etaMin)
		}
		b.WriteString("\n\n处理完成后我会把结果邮件回复给你。")
	} else {
		b.WriteString("This one will take a while to run — I've started on it, so no need to do anything.")
		if etaMin > 0 {
			fmt.Fprintf(&b, " It should take around %d minutes.", etaMin)
		}
		b.WriteString("\n\nI'll email you the result in this thread as soon as it's done.")
	}
	b.WriteString(signOffFor(agentName, zh))
	return b.String()
}
