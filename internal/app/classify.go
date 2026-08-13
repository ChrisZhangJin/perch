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
func BuildClassifyPrompt(from, subject, body string) string {
	var b strings.Builder
	b.WriteString("You are perch's task-duration classifier. Judge how long the task below would take *you* to complete — do NOT complete it. You may read files, run commands, or think out loud; whatever helps you decide.\n\n")
	b.WriteString("Definitions:\n")
	b.WriteString("- short: you can finish in under 1 minute (a quick read, a lookup, a small edit).\n")
	b.WriteString("- long: it will take at least 1 minute — anything with multi-step work, batch operations, network round-trips, or repeated agent turns.\n\n")
	b.WriteString("At the very end of your reply, on its OWN line, emit:\n")
	fmt.Fprintf(&b, "    %s <short|long> <eta_minutes>\n", classifySentinel)
	b.WriteString("Examples:\n")
	fmt.Fprintf(&b, "    %s long 15\n", classifySentinel)
	fmt.Fprintf(&b, "    %s short 1\n\n", classifySentinel)
	b.WriteString("Perch discards everything you write and only reads that final sentinel line, so your analysis above is for your own benefit only.\n\n")
	fmt.Fprintf(&b, "Task from %s (subject: %q):\n", from, subject)
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

// BuildLongAckBody is the fixed Chinese acknowledgement body perch sends
// when a task is classified as long. It includes the ETA (in minutes) if
// the classifier supplied one; otherwise it omits the duration line.
// A greeting line is prepended so the reply still satisfies perch's own
// greeting protocol and clients render the thread cleanly.
func BuildLongAckBody(fromName string, etaMin int) string {
	greeting := "Hi there,"
	if fromName != "" {
		greeting = "Hi " + fromName + ","
	}
	var b strings.Builder
	b.WriteString(greeting)
	b.WriteString("\n\n")
	b.WriteString("这个任务执行时间比较长，我先处理一下，请稍等。")
	if etaMin > 0 {
		fmt.Fprintf(&b, "预计需要 %d 分钟左右。", etaMin)
	}
	b.WriteString("\n\n处理完成后我会把结果邮件回复给你。\n\nBest,\nperch\n")
	return b.String()
}
