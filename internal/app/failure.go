package app

import (
	"fmt"
	"strings"
	"unicode"
)

// This file holds the bodies perch writes ITSELF, without an agent: the
// out-of-office notice when the agent could not run at all, and the
// can't-answer notice when it ran but produced nothing.
//
// Two rules govern them.
//
// They say nothing technical. Until 2026-08-25 an agent failure mailed the
// sender the raw Go error — "nanopi failed: exec: \"nanopi\": executable file
// not found in $PATH; stderr:" was what a customer complaining about a bug
// actually received. That is both unhelpful to a human and an information
// leak: the error text carries binary names, argv fragments, filesystem paths
// and the agent's stderr. The operator still gets all of it, at ERROR, in the
// log where it belongs.
//
// They mirror the sender's language, for the same reason the agent prompt has
// a LANGUAGE contract: answering a Chinese complaint in English reads as a
// machine brushing the sender off. perch has no model to delegate this to, so
// it ships one phrasing per language and picks with looksChinese.

// looksChinese reports whether text is written in Chinese.
//
// Han runes are counted against the other letters rather than merely being
// looked for: an English email that quotes a Chinese name or carries a Chinese
// signature block must not flip the whole reply into Chinese. The 20% share is
// a threshold with a lot of daylight on either side — real Chinese prose runs
// far above it, an incidental name or two far below.
//
// Kana and Hangul veto the decision: Japanese text is full of kanji (which are
// Han), so a Japanese email would otherwise be answered in Chinese. English is
// the safer fallback there — a language perch was not built to handle should
// get the neutral notice, not a confidently wrong one.
func looksChinese(text string) bool {
	var han, letters, otherCJK int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r),
			unicode.Is(unicode.Hangul, r):
			otherCJK++
		case unicode.IsLetter(r):
			letters++
		}
	}
	if han == 0 || han <= otherCJK {
		return false
	}
	return han*4 >= letters
}

// greetingFor returns the salutation line for a perch-authored body, in the
// sender's language. It follows the same forms the GREETING PROTOCOL asks the
// agent for, so a failure notice renders in a thread like any other reply.
func greetingFor(fromName string, zh bool) string {
	name := strings.TrimSpace(fromName)
	if zh {
		if name == "" {
			return "您好，"
		}
		return "您好 " + name + "，"
	}
	if name == "" {
		return "Hi there,"
	}
	return "Hi " + name + ","
}

// signOffFor returns the closing lines. agentName is the agent mailbox's
// display name (runner.AgentDisplayName), so the notice signs off exactly as a
// successful reply would.
func signOffFor(agentName string, zh bool) string {
	if agentName == "" {
		agentName = "perch"
	}
	if zh {
		return "\n\n此致\n" + agentName + "\n"
	}
	return "\n\nBest,\n" + agentName + "\n"
}

// BuildAgentFailureBody is what perch mails when the agent could not be run or
// died before producing anything — a missing binary, a crash, a timeout, an
// exhausted API quota. From the sender's side these are all the same event:
// nobody is at the desk right now.
//
// It deliberately promises nothing about when: perch does not know whether the
// operator will fix the cause in a minute or a week, and a made-up ETA is
// worse than none. It also does not tell the sender their email is queued,
// because it is not — the message is marked seen and nothing will retry it.
// Asking them to send again is the honest instruction.
func BuildAgentFailureBody(fromName, agentName string, zh bool) string {
	var b strings.Builder
	b.WriteString(greetingFor(fromName, zh))
	b.WriteString("\n\n")
	if zh {
		b.WriteString("抱歉，我现在不在工位上，暂时没法处理这封邮件。\n\n")
		b.WriteString("你的邮件我收到了，但这次没能开始处理，所以也没有结果可以回复你。麻烦稍后在这个邮件线程里再发一次，我恢复之后会接着处理。")
	} else {
		b.WriteString("Sorry — I'm out of office at the moment and can't pick this up.\n\n")
		b.WriteString("Your email reached me, but I wasn't able to start on it, so there's no result to send back yet. Please ping me again in this thread a little later and I'll take it from there.")
	}
	b.WriteString(signOffFor(agentName, zh))
	return b.String()
}

// BuildNoReplyBody is the sibling case: the agent ran, twice, and produced
// nothing usable either time (see IsDegenerateReply). The cause is usually the
// request rather than the machine, so unlike the out-of-office notice this one
// asks for a rephrase.
func BuildNoReplyBody(fromName, agentName string, zh bool) string {
	var b strings.Builder
	b.WriteString(greetingFor(fromName, zh))
	b.WriteString("\n\n")
	if zh {
		b.WriteString("抱歉，这封邮件我没能整理出有效的回复。\n\n")
		b.WriteString("麻烦你补充一点细节，或者换个说法再发一次，我再试一遍。")
	} else {
		b.WriteString("Sorry — I wasn't able to put together an answer for this one.\n\n")
		b.WriteString("Could you resend it with a little more detail, or rephrase the request? I'll have another go.")
	}
	b.WriteString(signOffFor(agentName, zh))
	return b.String()
}

// failureLanguageSample is the text looksChinese judges: the subject plus the
// body as RECEIVED. Kept as one place so every perch-authored body decides the
// language from the same evidence.
func failureLanguageSample(subject, body string) string {
	return fmt.Sprintf("%s\n%s", subject, body)
}
