package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// BuildClassifyPrompt frames a short classification prompt asking the agent to
// judge whether the incoming task is long-running or short. Output contract
// (single line, first non-empty line of stdout):
//
//	RUNTIME: <short|long>
//	ETA_MIN: <integer>
//
// The two-line form is a hard contract; anything else is treated as "short"
// (the safe default — the human just gets the normal reply without an ack).
// The subject/body are quoted so the agent can see what task it is judging
// without executing it.
func BuildClassifyPrompt(from, subject, body string) string {
	var b strings.Builder
	b.WriteString("You are perch's task-duration classifier. Do NOT perform the task described below — only judge how long it would take you to complete it.\n\n")
	b.WriteString("Output contract (EXACTLY two lines, no salutation, no explanation, no markdown):\n")
	b.WriteString("    RUNTIME: <short|long>\n")
	b.WriteString("    ETA_MIN: <integer minutes>\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- \"short\" means you can finish in under 1 minute (a quick read, a lookup, a small edit).\n")
	b.WriteString("- \"long\" means it will take at least 1 minute — anything that needs multi-step work, running commands, exploring a codebase, or repeated agent turns.\n")
	b.WriteString("- ETA_MIN is your best integer estimate; use 1 for short tasks and any positive integer for long ones.\n")
	b.WriteString("- Do NOT do the task. Do NOT produce a greeting. Emit only the two contract lines.\n\n")
	fmt.Fprintf(&b, "Task from %s (subject: %q):\n", from, subject)
	b.WriteString(body)
	return b.String()
}

// classifyRuntimeRe / classifyETARe pull the two fields out of the classifier
// output. Matching is case-insensitive on the label; the value is trimmed.
var (
	classifyRuntimeRe = regexp.MustCompile(`(?im)^\s*RUNTIME\s*:\s*(short|long)\s*$`)
	classifyETARe     = regexp.MustCompile(`(?im)^\s*ETA_MIN\s*:\s*(\d+)\s*$`)
)

// ParseClassifyOutput extracts (runtime, etaMinutes) from the classifier
// agent's stdout. Missing/malformed fields fall back to ("short", 0) — the
// safe default that skips the ack path entirely, so a classifier hiccup can
// never delay the real reply.
func ParseClassifyOutput(s string) (runtime string, etaMin int) {
	runtime = "short"
	etaMin = 0
	if m := classifyRuntimeRe.FindStringSubmatch(s); len(m) == 2 {
		runtime = strings.ToLower(m[1])
	}
	if m := classifyETARe.FindStringSubmatch(s); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			etaMin = n
		}
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
