package app

import (
	"strings"
	"testing"
)

// TestLooksChinese pins the language heuristic that decides which phrasing
// every perch-authored body uses.
func TestLooksChinese(t *testing.T) {
	chinese := []string{
		"kulink的服务有严重的bug，我要投诉。请让你们的主管联系我。",
		"我要投诉！",
		"你好，帮我看一下昨天的日志",
		"服务挂了", // short, but unambiguous
	}
	for _, s := range chinese {
		if !looksChinese(s) {
			t.Errorf("want Chinese for %q", s)
		}
	}

	english := []string{
		"",
		"Hi Support Team, kulink has a serious bug. I want to complain.",
		"Please summarize yesterday's logs and send me the report.",
		// A quoted Chinese name or a signature block must NOT flip an
		// otherwise-English email: this is why Han runes are weighed against
		// the other letters instead of merely being looked for.
		"Hi team, please forward this to 王芳 and copy the support desk when you reply, thanks.",
		// Japanese: kanji are Han, so without the kana veto this would be
		// answered in Chinese. English is the safer fallback.
		"サービスにバグがあります。対応をお願いします。",
		// Korean.
		"서비스에 심각한 버그가 있습니다.",
	}
	for _, s := range english {
		if looksChinese(s) {
			t.Errorf("want non-Chinese for %q", s)
		}
	}
}

// TestBuildAgentFailureBodyLeaksNothing is the core of the change: whatever
// went wrong inside perch, the sender must not learn about binaries, paths or
// stderr. Reported 2026-08-25, when a missing nanopi on the host mailed a
// complaining customer `exec: "nanopi": executable file not found in $PATH`.
func TestBuildAgentFailureBodyLeaksNothing(t *testing.T) {
	for _, zh := range []bool{true, false} {
		body := BuildAgentFailureBody("Chris", "kulink_support", zh)
		for _, leak := range []string{
			"exec:", "$PATH", "stderr", "nanopi", "claude", "err=",
			"exit status", "/root", "goroutine",
		} {
			if strings.Contains(body, leak) {
				t.Errorf("zh=%v: failure body must not contain %q, got:\n%s", zh, leak, body)
			}
		}
		// It must still read as a human reply: greeting, apology, sign-off.
		if !strings.Contains(body, "kulink_support") {
			t.Errorf("zh=%v: body must sign off as the agent mailbox:\n%s", zh, body)
		}
	}
}

// TestBuildAgentFailureBodyLanguage pins the two phrasings, and that neither
// bleeds into the other.
func TestBuildAgentFailureBodyLanguage(t *testing.T) {
	zh := BuildAgentFailureBody("小明", "kulink_support", true)
	if !strings.HasPrefix(zh, "您好 小明，") {
		t.Errorf("Chinese body must open with a Chinese greeting, got:\n%s", zh)
	}
	if !strings.Contains(zh, "不在工位上") {
		t.Errorf("Chinese body missing the out-of-office line:\n%s", zh)
	}
	if strings.Contains(zh, "out of office") {
		t.Errorf("Chinese body must not carry the English phrasing:\n%s", zh)
	}

	en := BuildAgentFailureBody("Chris", "kulink_support", false)
	if !strings.HasPrefix(en, "Hi Chris,") {
		t.Errorf("English body must open with an English greeting, got:\n%s", en)
	}
	if !strings.Contains(en, "out of office") {
		t.Errorf("English body missing the out-of-office line:\n%s", en)
	}
	for _, han := range []string{"抱歉", "您好", "此致"} {
		if strings.Contains(en, han) {
			t.Errorf("English body must not contain %q:\n%s", han, en)
		}
	}
}

// TestBuildNoReplyBodyAsksForRephrase: the agent DID run here, so the sender
// gets a different instruction than the out-of-office notice — the request is
// the more likely cause than the machine.
func TestBuildNoReplyBodyAsksForRephrase(t *testing.T) {
	en := BuildNoReplyBody("Chris", "tommy", false)
	if !strings.Contains(en, "rephrase") {
		t.Errorf("English no-reply body should ask for a rephrase:\n%s", en)
	}
	if strings.Contains(en, "out of office") {
		t.Errorf("no-reply body must not claim the agent was away:\n%s", en)
	}
	zh := BuildNoReplyBody("小明", "tommy", true)
	if !strings.Contains(zh, "换个说法") {
		t.Errorf("Chinese no-reply body should ask for a rephrase:\n%s", zh)
	}
	// No technical wording — the old text named "the agent" and "an empty
	// response", which means nothing to the person who sent the email.
	for _, leak := range []string{"agent", "empty response", "stdout"} {
		if strings.Contains(strings.ToLower(en+zh), leak) {
			t.Errorf("no-reply body must stay non-technical, found %q", leak)
		}
	}
}

// TestGreetingForNamelessSender: perch's own bodies must satisfy the same
// greeting shapes the agent is held to, including when the From header
// carried no display name.
func TestGreetingForNamelessSender(t *testing.T) {
	if got := greetingFor("", false); got != "Hi there," {
		t.Errorf("greetingFor(\"\", en) = %q", got)
	}
	if got := greetingFor("  ", true); got != "您好，" {
		t.Errorf("greetingFor(\"  \", zh) = %q", got)
	}
	// The bodies are sent straight to the replier, bypassing the greeting
	// scanner — but they still have to LOOK like replies, and the scanner is
	// the definition of that shape.
	for _, body := range []string{
		BuildAgentFailureBody("", "tommy", false),
		BuildAgentFailureBody("", "tommy", true),
		BuildNoReplyBody("小明", "tommy", true),
	} {
		if _, err := ExtractBodyAfterGreeting(body, ""); err != nil {
			t.Errorf("perch-authored body does not satisfy the greeting protocol: %v\n%s", err, body)
		}
	}
}
