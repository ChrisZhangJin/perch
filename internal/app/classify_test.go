package app

import (
	"strings"
	"testing"
)

func TestParseClassifyOutput_LongWithETA(t *testing.T) {
	in := "some analysis...\n<<<PERCH_CLASSIFY>>> long 10\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 10 {
		t.Errorf("got (%q, %d), want (long, 10)", rt, eta)
	}
}

func TestParseClassifyOutput_ShortNoTrailingNewline(t *testing.T) {
	in := "quick task.\n<<<PERCH_CLASSIFY>>> short 1"
	rt, eta := ParseClassifyOutput(in)
	if rt != "short" || eta != 1 {
		t.Errorf("got (%q, %d), want (short, 1)", rt, eta)
	}
}

func TestParseClassifyOutput_Garbage_DefaultsToShort(t *testing.T) {
	// No sentinel → safe default: skip the ack path.
	in := "who knows\n\nLet me think about it..."
	rt, eta := ParseClassifyOutput(in)
	if rt != "short" || eta != 0 {
		t.Errorf("got (%q, %d), want (short, 0)", rt, eta)
	}
}

func TestParseClassifyOutput_CaseInsensitive(t *testing.T) {
	in := "analysis\n<<<perch_classify>>> LONG 5\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 5 {
		t.Errorf("got (%q, %d), want (long, 5)", rt, eta)
	}
}

// The agent may quote its own examples early ("emit like:
// <<<PERCH_CLASSIFY>>> long 15") before writing the real verdict at the
// tail. Only the last sentinel line is the real answer.
func TestParseClassifyOutput_LastSentinelWins(t *testing.T) {
	in := "" +
		"I'll follow the contract and emit e.g. <<<PERCH_CLASSIFY>>> short 1 at the end.\n" +
		"...long analysis...\n" +
		"<<<PERCH_CLASSIFY>>> long 20\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 20 {
		t.Errorf("got (%q, %d), want (long, 20)", rt, eta)
	}
}

// A well-formed sentinel with ETA=0 falls back to etaMin=0 (no ETA line
// in the ack), but the runtime verdict still counts.
func TestParseClassifyOutput_ZeroETAKept(t *testing.T) {
	in := "<<<PERCH_CLASSIFY>>> long 0\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 0 {
		t.Errorf("got (%q, %d), want (long, 0)", rt, eta)
	}
}

func TestBuildLongAckBody_WithETA(t *testing.T) {
	body := BuildLongAckBody("小明", "tommy", 8, true)
	if !strings.Contains(body, "您好 小明，") {
		t.Errorf("missing greeting: %s", body)
	}
	if !strings.Contains(body, "预计需要 8 分钟") {
		t.Errorf("missing ETA line: %s", body)
	}
	if !strings.Contains(body, "我先处理一下") {
		t.Errorf("missing ack line: %s", body)
	}
	if !strings.Contains(body, "tommy") {
		t.Errorf("ack must sign off as the agent mailbox: %s", body)
	}
}

func TestBuildLongAckBody_NoETA_NoName(t *testing.T) {
	body := BuildLongAckBody("", "tommy", 0, true)
	if !strings.Contains(body, "您好，") {
		t.Errorf("expected generic greeting: %s", body)
	}
	if strings.Contains(body, "预计需要") {
		t.Errorf("ETA line should be absent when etaMin=0: %s", body)
	}
}

// TestBuildLongAckBody_English pins the language switch. This body was
// Chinese unconditionally until 2026-08-25, so an English sender waiting on a
// long task got an interim ack they could not read.
func TestBuildLongAckBody_English(t *testing.T) {
	body := BuildLongAckBody("Chris", "tommy", 8, false)
	if !strings.Contains(body, "Hi Chris,") {
		t.Errorf("missing greeting: %s", body)
	}
	if !strings.Contains(body, "around 8 minutes") {
		t.Errorf("missing ETA: %s", body)
	}
	for _, zh := range []string{"预计", "我先处理", "此致"} {
		if strings.Contains(body, zh) {
			t.Errorf("English ack must not contain %q: %s", zh, body)
		}
	}
}

func TestBuildClassifyPrompt_ContainsContract(t *testing.T) {
	p := BuildClassifyPrompt("alice@163.com", "hi", "do the thing")
	for _, want := range []string{"<<<PERCH_CLASSIFY>>>", "short", "long", "Do NOT actually run it", "do the thing"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
