package runner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChrisZhangJin/perch/internal/agent"
)

// writeStub writes an executable shell script that records its args and runs body.
func writeStub(t *testing.T, body string) (bin, argfile string) {
	t.Helper()
	dir := t.TempDir()
	argfile = filepath.Join(dir, "args.txt")
	bin = filepath.Join(dir, "stubclaude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argfile + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argfile
}

func TestBuildPrompt(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Alice", "Do X", "please do X", nil, "", "agent_tommy@163.com")
	if !strings.Contains(p, "alice@163.com") || !strings.Contains(p, "Do X") || !strings.Contains(p, "please do X") {
		t.Errorf("prompt missing fields: %q", p)
	}
}

func TestBuildPromptAttachmentHints(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Alice", "Do X", "body", []string{"/tmp/att/app.log", "/tmp/att/notes.txt"}, "/home/agent/reply", "agent_tommy@163.com")
	for _, want := range []string{"/tmp/att/app.log", "/tmp/att/notes.txt", "/home/agent/reply"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

// TestBuildPromptMentionsEmailBody pins the framing: the agent must know
// its stdout IS the email body, otherwise it narrates CLI-style instead
// of replying. Regression for the screenshot incident where the agent
// wrote "I'll help you respond to Chris's email" as the reply.
//
// TEMPORARILY DISABLED 2026-08-12: prompt simplified to trust the greeting-
// based splitter as the sole enforcement point. Re-enable or delete after
// the simplified-prompt trial concludes.
/*
func TestBuildPromptMentionsEmailBody(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "", "agent_tommy@163.com")
	for _, want := range []string{
		"email",
		"stdout",
		"reply",
		"concise",
		"narrate",
	} {
		if !strings.Contains(strings.ToLower(p), want) {
			t.Errorf("prompt should mention %q to set email-body framing, got:\n%s", want, p)
		}
	}
}
*/

// TestBuildPromptForbidsMetaCommentary pins an explicit anti-narration
// rule in the prompt so a future edit can't quietly drop it. The rule
// names the exact phrases the screenshot regression hit ("let me check",
// "i will respond", etc.) — those phrases must appear in a "do NOT" rule.
// Also pins the ZERO-thought rule (output of the work, not the work
// itself) and the explicit ban on internal-reasoning summaries ("I'll
// start by...", "Now I have...") — the second screenshot regression.
// Without these, the model defaults to writing its scratchpad into
// stdout and the email recipient reads the agent's thinking.
//
// TEMPORARILY DISABLED 2026-08-12: prompt simplified to trust the greeting-
// based splitter as the sole enforcement point. Re-enable or delete after
// the simplified-prompt trial concludes.
/*
func TestBuildPromptForbidsMetaCommentary(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "", "agent_tommy@163.com")
	low := strings.ToLower(p)
	if !strings.Contains(low, "do not narrate") {
		t.Errorf("prompt must contain a 'do not narrate' rule, got:\n%s", p)
	}
	// At least one of the regression phrases must be called out as banned.
	hit := false
	for _, phrase := range []string{"let me check", "i will respond", "let me first"} {
		if strings.Contains(low, phrase) {
			hit = true
			break
		}
	}
	if !hit {
		t.Errorf("prompt should name a regression phrase ('let me check' or 'i will respond') as forbidden, got:\n%s", p)
	}
	// ZERO-thought rule — the second-screenshot fix. Without this, the
	// model writes reasoning summaries ("I'll start by...", "Now I have...")
	// into stdout and the human reads them as the agent's reply.
	if !strings.Contains(low, "zero-thought") {
		t.Errorf("prompt must contain the 'zero-thought' rule (output of the work, not the work itself), got:\n%s", p)
	}
	// Internal-reasoning ban — at least one of the screenshot phrases
	// must be named as forbidden so a future edit can't quietly drop it.
	hit = false
	for _, phrase := range []string{"i'll start by", "now i have", "i need to check"} {
		if strings.Contains(low, phrase) {
			hit = true
			break
		}
	}
	if !hit {
		t.Errorf("prompt should name an internal-reasoning phrase ('i'll start by' or 'now i have') as forbidden, got:\n%s", p)
	}
}
*/

// TestBuildPromptRequiresGrounding pins the anti-fabrication contract: the
// prompt must tell the agent that the output-shaping rules govern what it
// PRINTS, not whether it runs tools, and that factual claims must come from
// a tool it actually ran. Regression for the 2026-08-12 incident where a
// weak model (minimax-M3), under the "emit only the conclusion" framing,
// skipped every tool call and fabricated command output — including a false
// "No such file or directory" for a directory that existed. Without this
// rule the prompt is pure output-shaping and never demands grounding.
//
// TEMPORARILY DISABLED 2026-08-12: prompt simplified to trust the greeting-
// based splitter as the sole enforcement point. Re-enable or delete after
// the simplified-prompt trial concludes.
/*
func TestBuildPromptRequiresGrounding(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "", "agent_tommy@163.com")
	low := strings.ToLower(p)
	// The print-vs-do distinction must be explicit.
	if !strings.Contains(low, "what you print") || !strings.Contains(low, "what you do") {
		t.Errorf("prompt must distinguish what the agent PRINTS from what it DOES, got:\n%s", p)
	}
	// Claims must be tied to a tool the agent actually ran.
	if !strings.Contains(low, "actually ran") {
		t.Errorf("prompt must require factual claims come from a tool actually run, got:\n%s", p)
	}
	// Fabrication must be named as forbidden.
	if !strings.Contains(low, "never invent") {
		t.Errorf("prompt must forbid inventing command output, got:\n%s", p)
	}
}
*/

// runnerFromStub wires a Runner that points at a stub binary instead of the
// real claude binary, so the tests stay hermetic.
func runnerFromStub(t *testing.T, bin string) *Runner {
	t.Helper()
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin // override the registry default for testing
	return New(&ag, t.TempDir(), "acceptEdits", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRunNewSessionPassesSessionID(t *testing.T) {
	bin, argfile := writeStub(t, `echo "REPLY-OK"`)
	out, _, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "11111111-1111-4111-8111-111111111111", true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "REPLY-OK" {
		t.Errorf("out = %q", out)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "--session-id") || !strings.Contains(string(args), "11111111-1111-4111-8111-111111111111") {
		t.Errorf("expected --session-id in args: %s", args)
	}
	if strings.Contains(string(args), "--resume") {
		t.Errorf("new session should not use --resume: %s", args)
	}
}

func TestRunResumePassesResume(t *testing.T) {
	bin, argfile := writeStub(t, `echo "R"`)
	_, _, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "22222222-2222-4222-8222-222222222222", false)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "--resume") {
		t.Errorf("resume should use --resume: %s", args)
	}
}

func TestRunNonZeroExitReturnsError(t *testing.T) {
	bin, _ := writeStub(t, `echo "boom" >&2; exit 3`)
	_, _, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "id", false)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include stderr: %v", err)
	}
}

// TestDiscoverNanopiSessionID exercises the post-run path where perch reads
// ~/.nanopi/sessions/active to find the UUIDv7 nanopi minted for a fresh
// session. The fixture mimics what nanopi's set_active_session writes.
func TestDiscoverNanopiSessionID(t *testing.T) {
	workdir := "/home/u/proj"
	nanopiUUID := "019fd2a7-812a-73c0-9052-c07bee77dabf"

	home := t.TempDir()
	t.Setenv("NANOPI_HOME", home)

	// Write the active pointer file: <workdir>\t<session_path>
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessFile := filepath.Join(sessDir, nanopiUUID+".jsonl")
	header := `{"type":"session","version":2,"id":"` + nanopiUUID + `","timestamp":"2026-08-09T15:00:00Z","cwd":"` + workdir + `","model":"m","base_url":""}`
	if err := os.WriteFile(sessFile, []byte(header+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(sessDir, "active")
	if err := os.WriteFile(active, []byte(workdir+"\t"+sessFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverNanopiSessionID(workdir)
	if got != nanopiUUID {
		t.Errorf("discoverNanopiSessionID = %q, want %q", got, nanopiUUID)
	}
}

// TestDiscoverNanopiSessionIDMissing returns "" gracefully when the active
// file or session is absent — caller treats empty as "no update needed".
func TestDiscoverNanopiSessionIDMissing(t *testing.T) {
	t.Setenv("NANOPI_HOME", t.TempDir())
	if got := discoverNanopiSessionID("/no/such/workdir"); got != "" {
		t.Errorf("expected empty when active missing, got %q", got)
	}
}

// TestDiscoverNanopiSessionIDWrongWorkdir returns "" when the active file
// exists but doesn't carry our workdir key.
func TestDiscoverNanopiSessionIDWrongWorkdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NANOPI_HOME", home)
	active := filepath.Join(home, "sessions", "active")
	if err := os.MkdirAll(filepath.Dir(active), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active, []byte("/other/cwd\t/sessions/x.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := discoverNanopiSessionID("/not/listed"); got != "" {
		t.Errorf("expected empty for unrelated workdir, got %q", got)
	}
}

// TestReadSessionHeaderIDRejectsGarbage returns "" on malformed JSON so the
// discovery path fails soft rather than aborting the watcher loop.
func TestReadSessionHeaderIDRejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(p, []byte("not json\n{\"id\":\"ok\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionHeaderID(p); got != "" {
		t.Errorf("expected empty on malformed first line, got %q", got)
	}
}

// TestCleanAgentOutput_Nanopi reproduces the screenshot shape: nanopi's
// StdoutRenderer writes color codes + tool_call / tool_result markers
// around the actual reply. The reply should arrive clean.
func TestCleanAgentOutput_Nanopi(t *testing.T) {
	raw := "\x1b[1;32mI'll help you respond to Chris's email.\x1b[0m\n" +
		"\x1b[33m\n[tool_call: bash call_019fe60a66da7431afe0d59e]\x1b[0m\n" +
		"\x1b[2m[bash \xe2\x86\x92 283 bytes  Took 2ms]\x1b[0m\n" +
		"\x1b[1;32mThe reply directory is empty.\x1b[0m\n" +
		"\x1b[33m\n[tool_call: bash call_019fe60a821c77308857f547]\x1b[0m\n" +
		"\x1b[2m[bash \xe2\x86\x92 552 bytes  Took 2ms]\x1b[0m\n" +
		"\x1b[1;32mHere is the file you asked for.\x1b[0m\n"
	got := cleanAgentOutput(raw, "nanopi")
	want := "I'll help you respond to Chris's email.\n\nThe reply directory is empty.\n\nHere is the file you asked for."
	if got != want {
		t.Errorf("cleanAgentOutput mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestCleanAgentOutput_NanopiEmpty(t *testing.T) {
	if got := cleanAgentOutput("", "nanopi"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestCleanAgentOutput_NanopiOnlyControls(t *testing.T) {
	// All lines are tool_call / tool_result — the agent never produced a
	// reply (e.g. failed mid-tool). We should return "" rather than leak
	// control markers into the email.
	raw := "\x1b[33m[tool_call: bash c1]\x1b[0m\n\x1b[2m[bash \xe2\x86\x92 0 bytes  Took 0ms]\x1b[0m\n"
	if got := cleanAgentOutput(raw, "nanopi"); got != "" {
		t.Errorf("expected empty when only controls present, got %q", got)
	}
}

func TestCleanAgentOutput_OtherAgentPassthrough(t *testing.T) {
	// claude and pi print plain text — the helper should not mangle it.
	raw := "Here's the answer you asked for.\n\nTwo paragraphs even."
	if got := cleanAgentOutput(raw, "claude"); got != raw {
		t.Errorf("non-nanopi should pass through, got %q", got)
	}
}

// TestNewResolvesWorkdir pins the workdir resolution: nanopi writes
// cwd as std::env::current_dir() in ~/.nanopi/sessions/active, which is
// the resolved absolute path — not the literal "." from a config file.
// If we stored the workdir un-resolved, discoverNanopiSessionID would
// look up "." in active and never match nanopi's stored
// "/root/workspace/perch", silently breaking every resume on the
// perch-minted UUID.
func TestNewResolvesWorkdir(t *testing.T) {
	bin, _ := writeStub(t, `echo ok`)
	ag, err := agent.Lookup("nanopi")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin

	// "." should be resolved to the test runner's cwd (an absolute path).
	r := New(&ag, ".", "acceptEdits", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !filepath.IsAbs(r.workdir) {
		t.Errorf("workdir should be absolute, got %q", r.workdir)
	}

	// An already-absolute path should pass through unchanged.
	abs := filepath.Join(t.TempDir(), "workdir")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	r2 := New(&ag, abs, "acceptEdits", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if r2.workdir != abs {
		t.Errorf("workdir should pass through unchanged, got %q want %q", r2.workdir, abs)
	}
}

// runnerFromNanopiStub wires a Runner against a stub binary using the
// nanopi adapter, so the resume-fallback tests can exercise the nanopi
// error path without depending on the real nanopi CLI.
func runnerFromNanopiStub(t *testing.T, bin string) *Runner {
	t.Helper()
	ag, err := agent.Lookup("nanopi")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin
	return New(&ag, t.TempDir(), "acceptEdits", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestRunNanopiResumeFallback pins the soft-fallback when the perch-
// minted UUID stored in the registry no longer maps to a real nanopi
// session file. Without the fallback, every email after the drift fails
// with "first line must be a session header" and the user gets a "task
// failed" email instead of a reply.
func TestRunNanopiResumeFallback(t *testing.T) {
	// First invocation (resume) writes the lost-session error to stderr
	// and exits non-zero. Second invocation (fresh, no --session) writes
	// a reply and exits 0. The stub writes which invocation it was to
	// argfile so the test can assert the fallback actually happened.
	bin, argfile := writeStub(t, `if [ "$1" = "--session" ]; then
  echo "error: resolve session: first line must be a session header" >&2
  exit 1
fi
echo "REPLY-AFTER-FALLBACK"`)

	r := runnerFromNanopiStub(t, bin)
	out, _, err := r.Run(context.Background(), "hi", "lost-uuid", false)
	if err != nil {
		t.Fatalf("Run should fall back, got error: %v", err)
	}
	if out != "REPLY-AFTER-FALLBACK" {
		t.Errorf("out = %q, want REPLY-AFTER-FALLBACK", out)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "lost-uuid") {
		t.Errorf("first invocation should have carried lost-uuid, got: %s", args)
	}
}

// TestRunNanopiResumeNoFallback confirms a normal nanopi resume error
// (NOT "first line must be a session header") does NOT trigger the
// fresh-session fallback. We don't want to mask real bugs by silently
// throwing away the perch UUID.
func TestRunNanopiResumeNoFallback(t *testing.T) {
	bin, _ := writeStub(t, `echo "some other error" >&2; exit 7`)
	_, _, err := runnerFromNanopiStub(t, bin).Run(context.Background(), "hi", "uuid", false)
	if err == nil {
		t.Fatal("expected error to surface, got nil")
	}
	if !strings.Contains(err.Error(), "some other error") {
		t.Errorf("error should pass through unchanged, got: %v", err)
	}
}

// TestRunClaudeResumeDoesNotFallback confirms the soft-fallback is
// nanopi-only. claude surfaces its own resume errors and perch should
// not paper over them by silently starting fresh.
func TestRunClaudeResumeDoesNotFallback(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "invocations")
	bin := filepath.Join(dir, "stubclaude")
	script := "#!/bin/sh\n" +
		"echo $(( $(cat " + counter + " 2>/dev/null || echo 0) + 1 )) > " + counter + "\n" +
		"echo \"first line must be a session header\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin
	r := New(&ag, t.TempDir(), "acceptEdits", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, _, err = r.Run(context.Background(), "hi", "uuid", false)
	if err == nil {
		t.Fatal("expected error to surface for claude, got nil")
	}
	// Stub ran exactly once — no fallback invocation.
	count, _ := os.ReadFile(counter)
	if strings.TrimSpace(string(count)) != "1" {
		t.Errorf("claude should not retry; invocations = %q", count)
	}
}

// TestBuildPromptIncludesGreeting pins the politeness framing: the agent
// must know it's replying to a real human and must include a salutation
// and sign-off in its reply. Without this rule the model defaults to a
// terse CLI tone and the email recipient reads it as rude. Regression for
// the 2026-08-09 screenshot where the agent replied with no greeting and
// no sign-off.
//
// The sign-off derives the agent's name from the agent's own email
// (cfg.Email). For agent_tommy@163.com → "tommy"; for agent_phillip@163.com
// → "phillip". The sign-off name must MATCH the agent's mailbox — the
// hardcoded "Tommy" in the old prompt was wrong when the agent ran on a
// different mailbox.
func TestBuildPromptIncludesGreeting(t *testing.T) {
	// Default case: agent owns agent_tommy@163.com.
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "The 5th attempt", "body", nil, "", "agent_tommy@163.com")
	low := strings.ToLower(p)
	if !strings.Contains(low, "hi chris") {
		t.Errorf("prompt should instruct greeting using fromName=Chris, got:\n%s", p)
	}
	if !strings.Contains(p, "Best,\\ntommy") {
		t.Errorf("prompt should sign off with derived name 'tommy' for agent_tommy@163.com, got:\n%s", p)
	}
	// Cross-check: when the agent's mailbox is agent_phillip@163.com the
	// sign-off must use "phillip" and must NOT contain "tommy". This is
	// the regression the user reported: agent on phillip's mailbox was
	// signing off as "Tommy" because the prompt hardcoded the name.
	p2 := BuildPrompt("chris.zhang@wiz.ai", "Chris", "audit", "body", nil, "", "agent_phillip@163.com")
	if !strings.Contains(p2, "Best,\\nphillip") {
		t.Errorf("prompt should sign off with derived name 'phillip' for agent_phillip@163.com, got:\n%s", p2)
	}
	if strings.Contains(p2, "tommy") {
		t.Errorf("prompt for agent_phillip@163.com must not mention 'tommy', got:\n%s", p2)
	}
}

// TestBuildPromptGreetingFallback covers the no-From-name case (mailing
// list, automated sender): the salutation guidance should still be there,
// just without a specific name to drop in.
//
// TEMPORARILY DISABLED 2026-08-12: pinned the exact phrase "polite salutation",
// which the simplified prompt no longer uses. Re-enable or delete after the
// simplified-prompt trial concludes.
/*
func TestBuildPromptGreetingFallback(t *testing.T) {
	p := BuildPrompt("noreply@example.com", "", "subj", "body", nil, "", "agent_alice@163.com")
	if !strings.Contains(p, "polite salutation") {
		t.Errorf("prompt should still mention politeness when fromName is empty, got:\n%s", p)
	}
}
*/

// TestBuildPromptEnforcesGreetingProtocol pins the new hard contract: the
// prompt must list the canonical greeting forms, name the rule as a hard
// contract, and tell the agent that pre-greeting content is silently
// dropped by perch on receipt. This is the prompt-side of the greeting
// protocol — the parser-side enforcement lives in internal/app/greeting.go
// (TestExtractBodyAfterGreeting_*).
//
// Regression for the 2026-08-11 incident where the agent did an audit,
// wrote a "Memory Audit Report" preamble, then a greeting, then the
// verdict — only the verdict (well, only the greeting-onwards) should
// reach the human.
func TestBuildPromptEnforcesGreetingProtocol(t *testing.T) {
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "audit", "body", nil, "", "agent_tommy@163.com")
	must := []string{
		"GREETING PROTOCOL",
		"Hi <name>,",
		"Hello <name>,",
		"Hi there,",
		"silently discarded",
	}
	for _, s := range must {
		if !strings.Contains(p, s) {
			t.Errorf("prompt must contain %q, got:\n%s", s, p)
		}
	}
	// Dropped forms must NOT appear in the prompt — regression guard for
	// the 2026-08-12 greeting-set shrink (Hi/Hello + Hi there only).
	forbidden := []string{"Hey <name>,", "Good morning,", "Good afternoon,", "Good evening,"}
	for _, s := range forbidden {
		if strings.Contains(p, s) {
			t.Errorf("prompt must NOT contain dropped form %q, got:\n%s", s, p)
		}
	}
}

// TestBuildPromptEnforcesAttachmentProtocol pins the attachment contract added
// after the 2026-08-13 incident where nanopi (on a resumed thread) replied
// "I'll read the file and attach it to the reply." then ended its turn —
// files=0, no tool_call, the narration shipped as the email body. The fix is
// a hard-contract section in the prompt that spells out the sequence: write
// the file to the reply dir, say it's attached, perch handles the rest —
// paired with an explicit ban on the "I'll do X" acknowledgment-then-stop
// failure mode. A regression here means a future edit dropped the contract
// and the failure will drift back.
func TestBuildPromptEnforcesAttachmentProtocol(t *testing.T) {
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "send report", "body", nil, "/home/agent/reply", "agent_tommy@163.com")
	must := []string{
		"ATTACHMENT PROTOCOL",
		"/home/agent/reply", // dir path appears in the numbered steps
		"Perch scans",       // perch does the attaching, not the agent
		"I'll read the file and attach it", // the exact failure phrase is named as forbidden
		"Complete steps 1 and 2 in THIS turn",
	}
	for _, s := range must {
		if !strings.Contains(p, s) {
			t.Errorf("prompt must contain %q, got:\n%s", s, p)
		}
	}
}

// TestBuildPromptNoAttachmentSectionWhenReplyDirEmpty confirms the section is
// scoped to runs where a reply dir was actually staged. Adding the protocol
// when the caller passed replyDir="" (unusual, but possible in tests) would
// point the agent at a non-existent path.
func TestBuildPromptNoAttachmentSectionWhenReplyDirEmpty(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "body", nil, "", "agent_tommy@163.com")
	if strings.Contains(p, "ATTACHMENT PROTOCOL") {
		t.Errorf("prompt must NOT include ATTACHMENT PROTOCOL when replyDir is empty, got:\n%s", p)
	}
}

// TestBuildPromptGreetingTimingAfterWork pins the timing clarification that
// resolves the tension between "begin with a greeting" and "do the work
// first". Regression for the 2026-08-12 run where a weak model read "begin
// with the greeting line" literally, emitted "Hi Chris," as its first
// tool-call-free message, and ended its turn before doing the task — perch
// then shipped a bare greeting. The prompt must say the greeting belongs on
// the FINAL result-bearing message and that a greeting-only reply is a
// failure.
//
// TEMPORARILY DISABLED 2026-08-12: prompt simplified to trust the greeting-
// based splitter as the sole enforcement point. Re-enable or delete after
// the simplified-prompt trial concludes.
/*
func TestBuildPromptGreetingTimingAfterWork(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "", "agent_tommy@163.com")
	low := strings.ToLower(p)
	for _, want := range []string{"do all of your tool work first", "only a greeting"} {
		if !strings.Contains(low, want) {
			t.Errorf("prompt must contain %q to fix greeting timing, got:\n%s", want, p)
		}
	}
}
*/

// TestAgentDisplayName verifies the sign-off name derivation. The perp
// convention is agent_<name>@<domain>; the helper strips the agent_
// prefix and returns the rest. Other addresses fall through unchanged.
// Empty / malformed input falls back to "there" so the prompt never
// produces a literal "Best,\n" with nothing after.
func TestAgentDisplayName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"agent_tommy@163.com", "tommy"},
		{"agent_phillip@163.com", "phillip"},
		{"alice@163.com", "alice"},
		{"agent_@163.com", "agent_"}, // empty after strip → keep original
		{"", "there"},
		{"   ", "there"},
		{"noatsign", "noatsign"},
	}
	for _, c := range cases {
		if got := agentDisplayName(c.in); got != c.want {
			t.Errorf("agentDisplayName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"\x1b[1;32mgreen\x1b[0m", "green"},
		{"\x1b[2mdim\x1b[0m and \x1b[33myellow\x1b[0m", "dim and yellow"},
		{"", ""},
		{"no escape here", "no escape here"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
