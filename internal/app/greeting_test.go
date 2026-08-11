package app

import (
	"errors"
	"strings"
	"testing"
)

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

func TestExtractBodyAfterGreeting_Hey(t *testing.T) {
	in := "Hey Chris,\n\nOn it.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
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

func TestExtractBodyAfterGreeting_GoodMorning(t *testing.T) {
	in := "Good morning,\n\nFiles attached.\n"
	out, err := ExtractBodyAfterGreeting(in, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_GoodMorningWithName(t *testing.T) {
	in := "Good morning, Chris,\n\nFiles attached.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_GoodAfternoonEvening(t *testing.T) {
	for _, greeting := range []string{"Good afternoon,", "Good evening,", "GOOD EVENING,"} {
		in := greeting + "\n\nDone.\n"
		out, err := ExtractBodyAfterGreeting(in, "")
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", greeting, err)
		}
		if out != in {
			t.Errorf("%s: want %q, got %q", greeting, in, out)
		}
	}
}

func TestExtractBodyAfterGreeting_ChineseName(t *testing.T) {
	in := "Hi 张进,\n\n完成。\n"
	out, err := ExtractBodyAfterGreeting(in, "张进")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
	}
}

func TestExtractBodyAfterGreeting_LeadingWhitespace(t *testing.T) {
	in := "  hi Chris,  \n\nBody.\n"
	out, err := ExtractBodyAfterGreeting(in, "Chris")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != in {
		t.Errorf("want %q, got %q", in, out)
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
		"Root: /home/zhangjin/perch\n" +
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
		"Hi there is a regression in prod.\nNext line.\n",     // no comma after "Hi there"
		"First I'll greet: Hi Chris. Then continue.\n",         // no comma after "Chris"
		"Hello world, this is not a greeting.\n",               // comma in middle of sentence
		"Greetings Chris,\n",                                   // "Greetings" not in canonical list
		"Hi,\n",                                                 // empty name
	}
	for _, in := range cases {
		_, err := ExtractBodyAfterGreeting(in, "Chris")
		if !errors.Is(err, ErrNoGreeting) {
			t.Errorf("input %q: want ErrNoGreeting, got %v", in, err)
		}
	}
}
