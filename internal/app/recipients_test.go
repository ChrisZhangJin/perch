package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
	"github.com/ChrisZhangJin/perch/internal/runner"
)

const testAgentEmail = "agent@163.com"

// rawMailWithCc builds a message with explicit To/Cc headers.
func rawMailWithCc(uid uint32, to, cc, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: alice@163.com\r\nTo: %s\r\n", to)
	if cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", cc)
	}
	fmt.Fprintf(&b, "Subject: the thing\r\nMessage-ID: <m%d@mailtest>\r\n", uid)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body + "\r\n")
	return []byte(b.String())
}

func newRecipientApp(t *testing.T, run *mailtest.ScriptedRunner) *mailtest.Mailtest {
	t.Helper()
	cfg := config.Defaults()
	cfg.MaxPromptBytes = 4096
	cfg.AgentWorkdir = t.TempDir()
	cfg.Email = testAgentEmail
	mt, err := mailtest.New(cfg, []string{"alice@163.com"}, run)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

// TestCcOnlyIsIgnored: being copied means "for your awareness". perch must
// not answer, must not spend an agent run, and must mark the mail seen so
// it stops being reconsidered on every poll.
func TestCcOnlyIsIgnored(t *testing.T) {
	run := &mailtest.ScriptedRunner{Outs: []string{"the answer"}}
	mt := newRecipientApp(t, run)

	if err := mt.SendRaw(11, rawMailWithCc(11, "bob@example.com", testAgentEmail,
		"Bob, please run the report.")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(run.Prompts) != 0 {
		t.Errorf("cc-only mail must not reach the agent, got %d runs", len(run.Prompts))
	}
	if rs := mt.Replies(); len(rs) != 0 {
		t.Errorf("cc-only mail must not be answered, got %d replies", len(rs))
	}
	if seen := mt.SeenUIDs(); len(seen) != 1 || seen[0] != 11 {
		t.Errorf("seen = %v, want [11]", seen)
	}
}

// TestBccIsProcessed: perch's address in neither header is Bcc, a mailing
// list, or a forward — ordinary mail addressed to us, not a Cc. Fails open
// on purpose; the cc-only rule needs positive evidence of both halves.
func TestBccIsProcessed(t *testing.T) {
	run := &mailtest.ScriptedRunner{Outs: []string{"the answer"}}
	mt := newRecipientApp(t, run)

	if err := mt.SendRaw(12, rawMailWithCc(12, "list@example.com", "", "do the thing")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Prompts) != 1 || len(mt.Replies()) != 1 {
		t.Fatalf("bcc'd mail must be processed: %d runs, %d replies",
			len(run.Prompts), len(mt.Replies()))
	}
	// Act-or-not says "treat as sole recipient"; the prompt still shows the
	// list, because somebody else visibly is on the email and the agent
	// should know the request may not be its own.
	if !strings.Contains(run.Prompts[0], "To: list@example.com") {
		t.Errorf("bcc'd mail should still show the visible recipients:\n%s", run.Prompts[0])
	}
}

// TestSoleRecipientPromptUnchanged is the no-regression half of the
// feature: the overwhelmingly common email must look identical to the
// agent.
func TestSoleRecipientPromptUnchanged(t *testing.T) {
	run := &mailtest.ScriptedRunner{Outs: []string{"the answer"}}
	mt := newRecipientApp(t, run)

	if err := mt.SendRaw(13, rawMailWithCc(13, testAgentEmail, "", "do the thing")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Prompts) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(run.Prompts))
	}
	if strings.Contains(run.Prompts[0], "RECIPIENTS") ||
		strings.Contains(run.Prompts[0], runner.MultiRecipientRule) {
		t.Errorf("sole-recipient prompt gained a recipients block:\n%s", run.Prompts[0])
	}
}

// TestMultiRecipientPromptCarriesBlock: in To alongside others, perch still
// processes the mail and hands the agent the recipient list plus the
// act-only-on-your-part rule. The rule is prompt-only — nothing enforces
// it in code — so the test pins that it is actually delivered.
func TestMultiRecipientPromptCarriesBlock(t *testing.T) {
	run := &mailtest.ScriptedRunner{Outs: []string{"the answer"}}
	mt := newRecipientApp(t, run)

	if err := mt.SendRaw(14, rawMailWithCc(14, "bob@example.com, "+testAgentEmail,
		"carol@example.com", "Bob: the report. Agent: summarise the log.")); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Prompts) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(run.Prompts))
	}
	p := run.Prompts[0]
	for _, want := range []string{
		"RECIPIENTS",
		"To: bob@example.com, " + testAgentEmail + " (you)",
		"Cc: carol@example.com",
		runner.MultiRecipientRule,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	// The block must sit immediately before the task it scopes.
	if i, j := strings.Index(p, "RECIPIENTS"), strings.Index(p, "Task:"); i < 0 || j < i {
		t.Errorf("RECIPIENTS must precede Task: (at %d and %d)", i, j)
	}
}
