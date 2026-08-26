package app

import (
	"errors"
	"strings"
	"testing"
)

// TestIsDegenerateReply pins the guard that stops perch from emailing a
// bare greeting. Regression for the 2026-08-12 run whose entire output was
// "Hi Chris," (a weak model ended its turn on the greeting before doing the
// task). A reply is degenerate iff it has no content beyond blank lines and
// an optional single greeting line; anything with a real body is not.
func TestIsDegenerateReply(t *testing.T) {
	degenerate := []string{
		"",
		"   \n\t\n",
		"Hi Chris,",
		"Hi Chris,\n",
		"  Hi Chris,  \n\n\n",
		"Hello Chris,\n",
		"Hi there,\n\n",
	}
	for _, s := range degenerate {
		if !IsDegenerateReply(s) {
			t.Errorf("want degenerate, got body-bearing for %q", s)
		}
	}
	healthy := []string{
		"Hi Chris,\n\nThe answer is 42.\n",
		"Hi Chris,\nDone.",
		"Hello Chris,\n\nReport attached.\n",
		"No greeting but real content here.", // not degenerate; sent as-is
	}
	for _, s := range healthy {
		if IsDegenerateReply(s) {
			t.Errorf("want body-bearing, got degenerate for %q", s)
		}
	}
}

func TestExtractBodyAfterGreeting_Standard(t *testing.T) {
	in := "Hi Chris,\n\nThe answer is 42.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_Hello(t *testing.T) {
	in := "Hello Chris Zhang Jin,\n\nDone.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris Zhang Jin")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_HiThere(t *testing.T) {
	in := "Hi there,\n\nThe result is below.\n"
	out, err := ExtractBodyAfterGreeting(in, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

// TestExtractBodyAfterGreeting_RejectsDroppedForms pins that greetings we
// used to accept — Hey, Good morning/afternoon/evening — are no longer
// recognized. The shrunken protocol accepts only Hi/Hello + name and Hi
// there. Regression guard: if someone re-widens the regex, these will
// silently start matching again and drift the contract.
func TestExtractBodyAfterGreeting_RejectsDroppedForms(t *testing.T) {
	dropped := []string{
		"Hey Chris,\n\nOn it.\n",
		"Good morning,\n\nFiles attached.\n",
		"Good morning, Chris,\n\nFiles attached.\n",
		"Good afternoon,\n\nDone.\n",
		"Good evening,\n\nDone.\n",
	}
	for _, in := range dropped {
		_, err := ExtractBodyAfterGreeting(in, "Chris")
		if !errors.Is(err, ErrNoGreeting) {
			t.Errorf("input %q: want ErrNoGreeting after shrinking greeting set, got %v", in, err)
		}
	}
}

func TestExtractBodyAfterGreeting_ChineseName(t *testing.T) {
	in := "Hi 小明,\n\n完成。\n"
	out, err := ExtractBodyAfterGreeting(in, "小明")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_LeadingWhitespace(t *testing.T) {
	in := "  hi Chris,  \n\nBody.\n"
	// New contract 2026-08-12: leading horizontal whitespace before the
	// greeting is trimmed so the reply doesn't ship with awkward indentation.
	want := "hi Chris,  \n\nBody.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != want {
		t.Errorf("want %q, got %q", want, out)
	}
}

// TestExtractBodyAfterGreeting_RunOnGreeting pins the 2026-08-12 splitter
// relaxation: a weak model sometimes runs the greeting onto the end of the
// previous sentence ("...report attached.Hi Chris,\n"). The splitter must
// still recognise the greeting and drop the run-on prefix.
func TestExtractBodyAfterGreeting_RunOnGreeting(t *testing.T) {
	in := "Body: polite email, mention audit executed.Hi Chris Zhang Jin,\n\nI've executed the audit.\n"
	want := "Hi Chris Zhang Jin,\n\nI've executed the audit.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris Zhang Jin")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != want {
		t.Errorf("want %q, got %q", want, out)
	}
}

func TestExtractBodyAfterGreeting_DropsAuditPreamble(t *testing.T) {
	// This is the regression test: the agent did an audit, wrote a
	// classification report, then a greeting, then the verdict. Only the
	// greeting onwards should reach the human.
	in := "Let me check the files first.\n" +
		"Both files have content after frontmatter (no `#` / `##` heading, but they have substantive prose — this is a format issue per the checklist's weak check for first-line `#`/`##`).\n" +
		"Let me report this honestly per the schema.\n" +
		"== Memory Audit Report ==\n" +
		"Root: /home/agent/perch\n" +
		"Total entries: 2\n" +
		"Valid: 2\n\n" +
		"Hi Chris Zhang Jin,\n\n" +
		"Here's the verdict.\n"

	out, err := ExtractBodyAfterGreeting(in, "Chris Zhang Jin")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(out, "Hi Chris Zhang Jin,") {
		t.Errorf("output should start with greeting, got %q", out)
	}
	if strings.Contains(out, "Let me check") || strings.Contains(out, "Both files have") || strings.Contains(out, "Audit Report") {
		t.Errorf("output should not contain preamble, got %q", out)
	}
	expected := "Hi Chris Zhang Jin,\n\nHere's the verdict.\n"
	if out != expected {
		t.Errorf("want %q, got %q", expected, out)
	}
}

func TestExtractBodyAfterGreeting_NoGreetingReturnsAsIs(t *testing.T) {
	in := "Just the answer, no greeting. Sorry.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
	if !errors.Is(err, ErrNoGreeting) {
		t.Fatalf("want ErrNoGreeting, got %v", err)
	}
	if out != in {
		t.Errorf("want %q (unchanged), got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_EmptyStdout(t *testing.T) {
	out, err := ExtractBodyAfterGreeting("", "Chris")
	if !errors.Is(err, ErrNoGreeting) {
		t.Fatalf("want ErrNoGreeting, got %v", err)
	}
	if out != "" {
		t.Errorf("want empty, got %q", out)
	}
}

// Regression guard: the agent must not be able to slip past the gate by
// writing "Hi" inside body prose. We require the greeting to be a complete
// line (followed by \n) and to look like a salutation, not an in-line
// phrase.
func TestExtractBodyAfterGreeting_RejectsProseHi(t *testing.T) {
	cases := []string{
		"Hi there is a regression in prod.\nNext line.\n", // no comma after "Hi there"
		"First I'll greet: Hi Chris. Then continue.\n",    // no comma after "Chris"
		"Hello world, this is not a greeting.\n",          // comma in middle of sentence
		"Greetings Chris,\n",                              // "Greetings" not in canonical list
		"Hi,\n",                                           // empty name
	}
	for _, in := range cases {
		_, err := ExtractBodyAfterGreeting(in, "Chris")
		if !errors.Is(err, ErrNoGreeting) {
			t.Errorf("input %q: want ErrNoGreeting, got %v", in, err)
		}
	}
}

// --- Chinese salutations ----------------------------------------------------
//
// A Chinese email must get a Chinese reply (the LANGUAGE contract in
// runner.BuildPrompt), and a Chinese reply opens with 您好/你好 — not "Hi".
// If the scanner only knew the English forms, every Chinese reply would take
// the missing-greeting path: perch logs a WARN and ships the raw stdout,
// preamble and all. Reported 2026-08-25.

func TestExtractBodyAfterGreeting_Chinese(t *testing.T) {
	cases := []string{
		"您好 小明，\n\n您反馈的问题已经记录。\n",
		"你好，\n\n已经收到您的邮件。\n",
		"您好 Chris：\n\n工单号 01a03867。\n",
		"你好 小明:\n\n收到。\n", // half-width colon is accepted too
	}
	for _, in := range cases {
		out, err := ExtractBodyAfterGreeting(in, "小明")
		if err != nil {
			t.Errorf("input %q: unexpected err %v", in, err)
			continue
		}
		if out != in {
			t.Errorf("input %q: want the whole reply back, got %q", in, out)
		}
	}
}

// TestExtractBodyAfterGreeting_ChineseDropsPreamble is the same contract the
// English forms get: everything before the greeting is the agent's scratch
// space and never reaches the human.
func TestExtractBodyAfterGreeting_ChineseDropsPreamble(t *testing.T) {
	in := "我先看一下工单系统的记录。\n检查完毕，可以回复了。\n\n您好 小明，\n\n" +
		"您的投诉已经登记，工单号 01a03867。\n\nBest,\nkulink_support\n"
	out, err := ExtractBodyAfterGreeting(in, "小明")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(out, "您好 小明，") {
		t.Errorf("reply must start at the Chinese greeting, got %q", out)
	}
	if strings.Contains(out, "我先看一下") {
		t.Errorf("pre-greeting narration leaked into the reply: %q", out)
	}
}

// TestExtractBodyAfterGreeting_RejectsChineseProse is why the Chinese branch
// is anchored to the start of a line and requires the comma to END the line:
// 您好/你好 are short, common substrings, and matching them mid-sentence would
// truncate a legitimate reply at an arbitrary clause.
func TestExtractBodyAfterGreeting_RejectsChineseProse(t *testing.T) {
	cases := []string{
		"如果您好奇，可以查看文档。\n", // 您好 inside a word, mid-line
		"您好世界，这不是问候。\n",   // comma does not end the line
		"我说：你好。\n",        // no comma/colon terminator
	}
	for _, in := range cases {
		if _, err := ExtractBodyAfterGreeting(in, "小明"); !errors.Is(err, ErrNoGreeting) {
			t.Errorf("input %q: want ErrNoGreeting, got %v", in, err)
		}
	}
}

// TestIsDegenerateReplyChinese: the bare-greeting guard has to understand the
// Chinese forms too, or a Chinese "您好，"-and-nothing-else run would be
// emailed out instead of retried.
func TestIsDegenerateReplyChinese(t *testing.T) {
	for _, s := range []string{"您好 小明，", "您好 小明，\n", "你好，\n\n", "  您好 Chris：  \n"} {
		if !IsDegenerateReply(s) {
			t.Errorf("want degenerate for %q", s)
		}
	}
	for _, s := range []string{"您好 小明，\n\n已经处理完成。\n", "你好，\n收到。"} {
		if IsDegenerateReply(s) {
			t.Errorf("want body-bearing for %q", s)
		}
	}
}
