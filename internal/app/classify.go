package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
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
