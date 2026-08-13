package app

import (
	"strings"
	"testing"
)

func TestParseClassifyOutput_LongWithETA(t *testing.T) {
	in := "RUNTIME: long\nETA_MIN: 10\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 10 {
		t.Errorf("got (%q, %d), want (long, 10)", rt, eta)
	}
}

func TestParseClassifyOutput_ShortNoETA(t *testing.T) {
	in := "RUNTIME: short\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "short" || eta != 0 {
		t.Errorf("got (%q, %d), want (short, 0)", rt, eta)
	}
}

func TestParseClassifyOutput_Garbage_DefaultsToShort(t *testing.T) {
	// Malformed classifier output must degrade to short/0 so the ack path
	// is skipped rather than firing on noise.
	in := "who knows\n\nLet me think about it..."
	rt, eta := ParseClassifyOutput(in)
	if rt != "short" || eta != 0 {
		t.Errorf("got (%q, %d), want (short, 0)", rt, eta)
	}
}

func TestParseClassifyOutput_CaseInsensitive(t *testing.T) {
	in := "runtime: LONG\neta_min: 5\n"
	rt, eta := ParseClassifyOutput(in)
	if rt != "long" || eta != 5 {
		t.Errorf("got (%q, %d), want (long, 5)", rt, eta)
	}
}

func TestBuildLongAckBody_WithETA(t *testing.T) {
	body := BuildLongAckBody("Chris", 8)
	if !strings.Contains(body, "Hi Chris,") {
		t.Errorf("missing greeting: %s", body)
	}
	if !strings.Contains(body, "预计需要 8 分钟") {
		t.Errorf("missing ETA line: %s", body)
	}
	if !strings.Contains(body, "我先处理一下") {
		t.Errorf("missing ack line: %s", body)
	}
}

func TestBuildLongAckBody_NoETA_NoName(t *testing.T) {
	body := BuildLongAckBody("", 0)
	if !strings.Contains(body, "Hi there,") {
		t.Errorf("expected generic greeting: %s", body)
	}
	if strings.Contains(body, "预计需要") {
		t.Errorf("ETA line should be absent when etaMin=0: %s", body)
	}
}

func TestBuildClassifyPrompt_ContainsContract(t *testing.T) {
	p := BuildClassifyPrompt("alice@163.com", "hi", "do the thing")
	for _, want := range []string{"RUNTIME:", "ETA_MIN:", "Do NOT do the task", "do the thing"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
