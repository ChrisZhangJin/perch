package runner

import (
	"bytes"
	"context"
	"errors"
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
	p := BuildPrompt("alice@163.com", "Alice", "Do X", "please do X", nil, "", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
	if !strings.Contains(p, "alice@163.com") || !strings.Contains(p, "Do X") || !strings.Contains(p, "please do X") {
		t.Errorf("prompt missing fields: %q", p)
	}
}

func TestBuildPromptAttachmentHints(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Alice", "Do X", "body", []string{"/tmp/att/app.log", "/tmp/att/notes.txt"}, "/home/agent/reply", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
	for _, want := range []string{"/tmp/att/app.log", "/tmp/att/notes.txt", "/home/agent/reply"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

// TestBuildPromptMentionsEmailBody was removed on 2026-08-25, resolving the
// 2026-08-12 "re-enable or delete" note. It pinned the words "stdout" and
// "concise". The framing it protected still exists in different wording —
// "write a polite email reply, not a CLI transcript", plus the GREETING
// PROTOCOL paragraph that explains perch discards everything before the
// greeting line — so the test was pinning vocabulary, not behaviour.

// TestBuildPromptForbidsMetaCommentary was removed on 2026-08-25, resolving
// the 2026-08-12 "re-enable or delete" note. Its rules ("do not narrate",
// zero-thought, the named regression phrases) really are gone from the
// prompt, but enforcement moved rather than vanished: ExtractBodyAfterGreeting
// discards everything the model writes before the greeting line, so a
// "Let me check..." preamble never reaches the human. That mechanism is
// covered by app.TestExtractBodyAfterGreeting_DropsAuditPreamble.
//
// Contrast with GROUNDING, which WAS restored: a fabricated fact is silently
// wrong and no downstream stage can detect it, whereas leaked narration is
// merely ugly and the splitter removes it. That asymmetry is why only one of
// these four came back.

// TestBuildPromptRequiresGrounding pins the anti-fabrication contract: the
// prompt must tell the agent that the output-shaping rules govern what it
// PRINTS, not whether it runs tools, and that factual claims must come from
// a tool it actually ran.
//
// Regression for 2026-08-12, where minimax-M3 under the "emit only the
// conclusion" framing skipped every tool call and fabricated command output,
// including "No such file or directory" for a directory that existed.
//
// This test was commented out on 2026-08-12 in the same change that dropped
// the rule, so nothing was left to catch the recurrence — on 2026-08-25
// nanopi answered "What is the current date?" with a date five months stale,
// having never run `date`. Re-enabled with the rule; if the prompt is
// simplified again, delete the rule and this test together and say why.
func TestBuildPromptRequiresGrounding(t *testing.T) {
	// Contracts:true — this pins the VERBOSE rule text sent on a cold
	// session. The compacted resume form is covered by
	// TestCompactGuardrailsKeepTheOperativeRules.
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
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
	// The observed failure was a stale date, so name dates specifically.
	if !strings.Contains(low, "date") {
		t.Errorf("prompt must call out dates/times as things to look up, not recall, got:\n%s", p)
	}
}

// TestGroundingSurvivesResume is the placement contract. GROUNDING is an
// anti-fabrication guardrail, not a format convention, so compact
// resume contracts must not drop it: a model deep into a thread is more likely to answer from
// memory, not less. Same reasoning as SAFETY.
func TestGroundingSurvivesResume(t *testing.T) {
	warm := BuildPrompt("x@y", "X", "subj", "task", nil, "/reply", "agent_tommy@163.com", "/wd",
		PromptOpts{TaskOnly: true, Contracts: false})
	low := strings.ToLower(warm)
	for _, want := range []string{"grounding", "safety protocol", "never invent"} {
		if !strings.Contains(low, want) {
			t.Errorf("resumed prompt must still carry %q — it is a guardrail, not a contract", want)
		}
	}
	// Sanity: the format contracts really are gone in this same prompt, so
	// the assertions above are not passing because nothing was stripped.
	if strings.Contains(warm, "GREETING PROTOCOL") {
		t.Fatal("test is vacuous: contracts were not stripped")
	}
}

// TestGroundingHasNoConfigKnob documents a deliberate omission: unlike
// task_only, grounding is always on. Fabricating facts is never a mode an
// operator wants, and a knob would invite turning it off to save tokens.
func TestGroundingHasNoConfigKnob(t *testing.T) {
	for _, opts := range []PromptOpts{
		{}, {TaskOnly: true}, {Contracts: true}, {TaskOnly: true, Contracts: true},
	} {
		if p := BuildPrompt("x@y", "X", "s", "t", nil, "", "a@b", "", opts); !strings.Contains(p, "GROUNDING") {
			t.Errorf("GROUNDING missing for opts %+v; it must not be switchable", opts)
		}
	}
}

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

// TestRunNanopiResumeReportsSessionLost pins the contract for a resume whose
// stored session id no longer maps to a real nanopi session: Run reports
// ErrSessionLost and does NOT silently retry.
//
// Run used to retry here itself, reusing the same prompt. That became unsafe
// once prompts started omitting the format contracts on resume
// (compact contracts on resume): the retry would open a COLD session with a prompt
// built for a warm one, so the agent never saw the ATTACHMENT PROTOCOL and
// file replies vanished. Rebuilding the prompt is app.ProcessUnseen's job —
// see TestProcessRetriesColdWithFullContracts.
//
// The stub greps the whole argv for --session. The previous version tested
// only "$1", which is always "-p" for nanopi, so the lost-session branch never
// executed and the test passed even with the fallback deleted.
func TestRunNanopiResumeReportsSessionLost(t *testing.T) {
	bin, argfile := writeStub(t, `for a in "$@"; do
  if [ "$a" = "--session" ]; then
    echo "error: resolve session: first line must be a session header" >&2
    exit 1
  fi
done
echo "REPLY-FROM-COLD-SESSION"`)

	r := runnerFromNanopiStub(t, bin)
	out, _, err := r.Run(context.Background(), "hi", "lost-uuid", false)
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("err = %v, want ErrSessionLost so the caller can rebuild the prompt", err)
	}
	if out != "" {
		t.Errorf("out = %q, want empty — Run must not return a reply it did not get", out)
	}
	// The underlying agent error is preserved for the log.
	if !strings.Contains(err.Error(), "first line must be a session header") {
		t.Errorf("wrapped error lost the agent's message: %v", err)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "lost-uuid") {
		t.Errorf("the attempted resume should have carried lost-uuid, got: %s", args)
	}
}

// TestRunNanopiFreshSessionSucceedsAfterLoss is the other half: once the
// caller retries cold (no session id, isNew=true), the same stub serves the
// request normally. Together with the test above this covers what the old
// single test only claimed to cover.
func TestRunNanopiFreshSessionSucceedsAfterLoss(t *testing.T) {
	bin, _ := writeStub(t, `for a in "$@"; do
  if [ "$a" = "--session" ]; then
    echo "error: resolve session: first line must be a session header" >&2
    exit 1
  fi
done
echo "REPLY-FROM-COLD-SESSION"`)

	r := runnerFromNanopiStub(t, bin)
	out, _, err := r.Run(context.Background(), "hi", "", true)
	if err != nil {
		t.Fatalf("cold retry should succeed, got: %v", err)
	}
	if out != "REPLY-FROM-COLD-SESSION" {
		t.Errorf("out = %q, want REPLY-FROM-COLD-SESSION", out)
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
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "The 5th attempt", "body", nil, "", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
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
	p2 := BuildPrompt("chris.zhang@wiz.ai", "Chris", "audit", "body", nil, "", "agent_phillip@163.com", "", PromptOpts{Contracts: true})
	if !strings.Contains(p2, "Best,\\nphillip") {
		t.Errorf("prompt should sign off with derived name 'phillip' for agent_phillip@163.com, got:\n%s", p2)
	}
	if strings.Contains(p2, "tommy") {
		t.Errorf("prompt for agent_phillip@163.com must not mention 'tommy', got:\n%s", p2)
	}
}

// TestBuildPromptGreetingFallback was removed on 2026-08-25, resolving the
// 2026-08-12 "re-enable or delete" note: it pinned the exact phrase "polite
// salutation", which the prompt no longer uses. The behaviour it cared about
// is now asserted by TestBuildPromptNameLessSenderStillGetsSalutation below.

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
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "audit", "body", nil, "", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
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
	p := BuildPrompt("chris.zhang@wiz.ai", "Chris", "send report", "body", nil, "/home/agent/reply", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
	must := []string{
		"ATTACHMENT PROTOCOL",
		"/home/agent/reply",                // dir path appears in the numbered steps
		"Perch scans",                      // perch does the attaching, not the agent
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
	p := BuildPrompt("x@y", "X", "subj", "body", nil, "", "agent_tommy@163.com", "", PromptOpts{Contracts: true})
	if strings.Contains(p, "ATTACHMENT PROTOCOL") {
		t.Errorf("prompt must NOT include ATTACHMENT PROTOCOL when replyDir is empty, got:\n%s", p)
	}
}

// TestBuildPromptSafetyProtocolWhenTaskOnly pins the SAFETY PROTOCOL section's
// presence and its key contract phrases when taskOnly=true. The section tells
// the agent to refuse destructive side-requests that aren't the stated task —
// a light guardrail against email-body-borne injection. If the phrases below
// silently drop, the guardrail is gone.
//
// Also pins the strengthened contract added 2026-08-20 after pi cheerfully
// executed a `rm ~/foo.rpm` side-request against a workdir of
// /home/chris/perch: (a) the section is placed BEFORE the Task body so the
// model reads it before the imperative task text takes over; (b) the exact
// cwd absolute path is injected so the model has a concrete boundary string
// to compare against; (c) the "delete outside cwd" failure mode is spelled
// out as a concrete example the model must not replicate.
func TestBuildPromptSafetyProtocolWhenTaskOnly(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "body", nil, "/home/agent/reply", "agent_tommy@163.com", "/home/chris/perch", PromptOpts{TaskOnly: true, Contracts: true})
	for _, want := range []string{
		"SAFETY PROTOCOL",
		"Always allowed",
		"You MUST refuse",
		"Judgment rule",
		"REFUSE that part only",
		"/home/chris/perch", // cwd absolute path is injected
		"~/foo.rpm",         // concrete failure-mode example
		"Concrete example",  // header for the example block
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing SAFETY PROTOCOL phrase %q, got:\n%s", want, p)
		}
	}
	// SAFETY must come BEFORE the Task body so the model reads guardrails
	// before it locks onto the imperative task text.
	safetyAt := strings.Index(p, "SAFETY PROTOCOL")
	taskAt := strings.Index(p, "Task:")
	if safetyAt < 0 || taskAt < 0 {
		t.Fatalf("prompt missing SAFETY PROTOCOL or Task: markers, got:\n%s", p)
	}
	if safetyAt >= taskAt {
		t.Errorf("SAFETY PROTOCOL must appear before Task: (safety at %d, task at %d), got:\n%s", safetyAt, taskAt, p)
	}
}

// TestBuildPromptNoSafetyProtocolWhenTaskOnlyOff confirms the section is
// gated: an operator who set task_only: false in yaml must not see the
// SAFETY PROTOCOL block injected.
func TestBuildPromptNoSafetyProtocolWhenTaskOnlyOff(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "body", nil, "/home/agent/reply", "agent_tommy@163.com", "/tmp/wd", PromptOpts{Contracts: true})
	if strings.Contains(p, "SAFETY PROTOCOL") {
		t.Errorf("prompt must NOT include SAFETY PROTOCOL when taskOnly=false, got:\n%s", p)
	}
}

// TestBuildPromptNameLessSenderStillGetsSalutation replaces the deleted
// TestBuildPromptGreetingFallback. When the From header carries no display
// name (mailing lists, automated senders) the prompt must still ask for a
// salutation and must offer the name-less "Hi there," form, otherwise the
// model has no valid greeting to emit and ExtractBodyAfterGreeting finds
// nothing to split on.
func TestBuildPromptNameLessSenderStillGetsSalutation(t *testing.T) {
	p := BuildPrompt("noreply@example.com", "", "subj", "body", nil, "", "agent_alice@163.com", "",
		PromptOpts{Contracts: true})
	if !strings.Contains(strings.ToLower(p), "salutation") {
		t.Errorf("prompt must still ask for a salutation when fromName is empty, got:\n%s", p)
	}
	if !strings.Contains(p, "Hi there,") {
		t.Errorf("prompt must offer the name-less greeting form, got:\n%s", p)
	}
}

// TestBuildPromptGreetingTimingAfterWork was removed on 2026-08-25, resolving
// the 2026-08-12 "re-enable or delete" note. The prompt no longer tells the
// agent to finish its tool work before greeting; enforcement moved to runtime,
// where app.IsDegenerateReply catches a greeting-only reply, retries once, and
// refuses to email a bare greeting. Covered by app.TestIsDegenerateReply.
// The prompt rule would still be cheaper than a second agent spawn, so this is
// a cost tradeoff rather than a gap.

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
		if got := AgentDisplayName(c.in); got != c.want {
			t.Errorf("AgentDisplayName(%q) = %q, want %q", c.in, got, c.want)
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

// TestWarnIfSensitiveWorkdir pins the sensitive-workdir warning: exact
// top-level paths trigger a WARN, subpaths and normal user dirs stay quiet.
// A "log gets a WARN entry" assertion via a buffered handler catches the
// message; equality-not-prefix is verified by testing /root/workspace which
// must NOT trigger.
func TestWarnIfSensitiveWorkdir(t *testing.T) {
	cases := []struct {
		workdir string
		warn    bool
	}{
		{"/", true},
		{"/home", true},
		{"/root", true},
		{"/tmp", true},
		{"/etc", true},
		{"/root/workspace/perch", false}, // subpath, safe
		{"/home/alice/agent", false},
		{"/opt/agent", false},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		warnIfSensitiveWorkdir(c.workdir, log)
		got := strings.Contains(buf.String(), "sensitive top-level path")
		if got != c.warn {
			t.Errorf("workdir=%q warn=%v, want %v; log:\n%s", c.workdir, got, c.warn, buf.String())
		}
	}
}

// TestCompactGuardrailsKeepTheOperativeRules pins what may and may not be
// dropped when a session is resumed.
//
// The guardrails are compacted, not removed. What goes is teaching material
// the agent read on turn 1 — the rationale, the worked `rm ~/foo.rpm` story,
// the print-vs-do explanation. What stays is every rule the agent has to
// apply: the cwd definition, the full refusal list, and the grounding
// obligation. 813 of SAFETY's 2497 bytes were that one story, re-sent on
// every email in a thread.
func TestCompactGuardrailsKeepTheOperativeRules(t *testing.T) {
	warm := BuildPrompt("x@y", "X", "subj", "task", nil, "/wd/reply", "agent_tommy@163.com", "/wd",
		PromptOpts{TaskOnly: true, Contracts: false})
	low := strings.ToLower(warm)

	// Every refusal category must survive verbatim — this is the operative
	// part of the guardrail, and dropping any line silently permits it.
	for _, want := range []string{
		"outside cwd", "installing or removing packages", "sudo",
		"network calls to external hosts", "killing processes",
		"credentials or ssh keys", "hard-to-reverse",
	} {
		if !strings.Contains(low, want) {
			t.Errorf("compact SAFETY dropped a refusal rule: %q", want)
		}
	}
	// The cwd definition is what makes "outside cwd" mean anything.
	if !strings.Contains(warm, "/wd") {
		t.Error("compact SAFETY must still state the working directory")
	}
	// Grounding's obligation survives even though its explanation does not.
	for _, want := range []string{"grounding", "actually ran", "never invent", "date"} {
		if !strings.Contains(low, want) {
			t.Errorf("compact GROUNDING dropped %q", want)
		}
	}

	// The teaching material is what we came to remove.
	for _, gone := range []string{"Concrete example", "foo.rpm", "Judgment rule", "what you PRINT"} {
		if strings.Contains(warm, gone) {
			t.Errorf("compact form still carries teaching material: %q", gone)
		}
	}

	// And it has to actually be smaller, or none of this bought anything.
	cold := BuildPrompt("x@y", "X", "subj", "task", nil, "/wd/reply", "agent_tommy@163.com", "/wd",
		PromptOpts{TaskOnly: true, Contracts: true})
	if saved := len(cold) - len(warm); saved < 3000 {
		t.Errorf("resume saved only %d bytes; expected ~4 KB", saved)
	}
}

// TestAttachmentProtocolForbidsWritingTheBody pins the fix for a duplicate
// attachment on every email: nanopi wrote its whole reply into the reply dir,
// so each message arrived with an attachment repeating the body. Its own
// summary of the directory read "reply/ — where my reply body goes first", so
// the conditional "if the task calls for sending a file back" was not enough.
func TestAttachmentProtocolForbidsWritingTheBody(t *testing.T) {
	p := BuildPrompt("x@y", "X", "subj", "task", nil, "/wd/reply", "agent_tommy@163.com", "/wd",
		PromptOpts{Contracts: true})
	low := strings.ToLower(p)
	for _, want := range []string{
		"only for files the sender asked for",
		"your reply body is your stdout, never a file",
		"same content twice",
		"leave the directory empty",
	} {
		if !strings.Contains(low, want) {
			t.Errorf("attachment protocol is missing %q:\n%s", want, p)
		}
	}
}

// TestBuildPromptRequiresLanguageMirroring pins the LANGUAGE contract added
// 2026-08-25: a 126.com sender wrote a complaint in Chinese ("kulink的服务有
// 严重的bug，我要投诉") and nanopi replied entirely in English. Nothing in the
// prompt asked for the sender's language, and the surrounding framing ("Hi
// <name>," / "Best,") is English, which reads as an instruction to write it.
func TestBuildPromptRequiresLanguageMirroring(t *testing.T) {
	p := BuildPrompt("sender@126.com", "小明", "我要投诉！", "服务有bug",
		nil, "/reply", "kulink_support@163.com", "/wd",
		PromptOpts{TaskOnly: true, Contracts: true})
	for _, want := range []string{"LANGUAGE (hard contract", "same language"} {
		if !strings.Contains(p, want) {
			t.Errorf("cold prompt must contain %q, got:\n%s", want, p)
		}
	}
	// The English examples in the prompt must be disclaimed, or they read as
	// the instruction they contradict.
	if !strings.Contains(p, "not a request to answer in English") {
		t.Errorf("prompt must disclaim its own English examples, got:\n%s", p)
	}
	// A Chinese reply opens with 您好/你好, so the greeting protocol has to
	// offer those forms — otherwise LANGUAGE and GREETING PROTOCOL conflict
	// and the agent has to pick one to violate.
	for _, want := range []string{"您好 <name>，", "你好 <name>，"} {
		if !strings.Contains(p, want) {
			t.Errorf("GREETING PROTOCOL must list %q alongside the English forms, got:\n%s", want, p)
		}
	}
}

// TestLanguageSurvivesResume: unlike the format contracts, LANGUAGE must be
// re-sent on every turn — and for a reason the other guardrails don't have.
// The answer can CHANGE mid-thread: the same thread can arrive in Chinese
// today and English tomorrow, so a rule stated once in the cold turn's
// history is the wrong shape.
func TestLanguageSurvivesResume(t *testing.T) {
	warm := BuildPrompt("sender@126.com", "小明", "我要投诉！", "服务有bug",
		nil, "/reply", "kulink_support@163.com", "/wd",
		PromptOpts{TaskOnly: true, Contracts: false})
	if !strings.Contains(warm, "LANGUAGE (hard contract") {
		t.Errorf("resumed prompt must still carry LANGUAGE, got:\n%s", warm)
	}
	// It must point at THIS email's body, not at what the thread started in.
	if !strings.Contains(warm, "may differ from earlier turns") {
		t.Errorf("resumed LANGUAGE must tell the agent to re-check this email's language, got:\n%s", warm)
	}
	if strings.Contains(warm, "GREETING PROTOCOL") {
		t.Fatal("test is vacuous: contracts were not stripped")
	}
}

// TestLanguageHasNoConfigKnob mirrors GROUNDING's rule: answering a human in
// a language they did not write in is never a mode an operator wants.
func TestLanguageHasNoConfigKnob(t *testing.T) {
	for _, opts := range []PromptOpts{
		{}, {TaskOnly: true}, {Contracts: true}, {TaskOnly: true, Contracts: true},
	} {
		if p := BuildPrompt("x@y", "X", "s", "t", nil, "", "a@b", "", opts); !strings.Contains(p, "LANGUAGE") {
			t.Errorf("LANGUAGE missing for opts %+v; it must not be switchable", opts)
		}
	}
}
