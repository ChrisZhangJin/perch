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
	"unicode/utf8"

	"github.com/ChrisZhangJin/perch/internal/agent"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestNewSystemPromptOff pins the disabled case: no value means a nil
// *SystemPrompt whose methods are safe, so no caller needs a branch.
func TestNewSystemPromptOff(t *testing.T) {
	for _, v := range []string{"", "   ", "\n\t\n"} {
		if sp := NewSystemPrompt(v, discardLogger()); sp != nil {
			t.Errorf("NewSystemPrompt(%q) = %v, want nil", v, sp)
		}
	}
	var nilSP *SystemPrompt
	if nilSP.Text() != "" || nilSP.Bytes() != 0 || nilSP.Describe() != "(none)" {
		t.Error("nil *SystemPrompt accessors must be zero-valued")
	}
}

// TestSystemPromptInlineText: a multi-line value is prose, never a filename.
func TestSystemPromptInlineText(t *testing.T) {
	const text = "You are the support desk.\nTask definitions live in ./tasks/.\n"
	sp := NewSystemPrompt(text, discardLogger())
	if sp == nil {
		t.Fatal("NewSystemPrompt returned nil for real text")
	}
	if sp.Text() != text {
		t.Errorf("Text() = %q, want %q", sp.Text(), text)
	}
	if sp.Describe() != "inline text" {
		t.Errorf("Describe() = %q, want inline text", sp.Describe())
	}
}

// TestSystemPromptFile: a single-line value naming a readable file is read as
// a file — the same text-or-path rule pi's own flag follows.
func TestSystemPromptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte("You are the support desk.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := NewSystemPrompt(path, discardLogger())
	if sp == nil {
		t.Fatal("nil for a valid path")
	}
	if !strings.Contains(sp.Text(), "support desk") {
		t.Errorf("Text() did not read the file: %q", sp.Text())
	}
	if !strings.HasPrefix(sp.Describe(), "file ") {
		t.Errorf("Describe() = %q, want a file description", sp.Describe())
	}
}

// TestSystemPromptFileRereadPerCall is the operational promise: editing the
// role definition takes effect on the next email, not the next restart.
func TestSystemPromptFileRereadPerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte("first version"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := NewSystemPrompt(path, discardLogger())
	if got := sp.Text(); got != "first version" {
		t.Fatalf("Text() = %q", got)
	}
	if err := os.WriteFile(path, []byte("second version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sp.Text(); got != "second version" {
		t.Errorf("Text() after an edit = %q, want the new content", got)
	}
}

// TestSystemPromptFallsBackToLastGood: a file that vanishes mid-edit must not
// silently turn a support desk back into a generic assistant.
func TestSystemPromptFallsBackToLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte("the role"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := NewSystemPrompt(path, discardLogger())
	if got := sp.Text(); got != "the role" {
		t.Fatalf("Text() = %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := sp.Text(); got != "the role" {
		t.Errorf("Text() after the file vanished = %q, want the last known content", got)
	}
}

// TestSystemPromptMissingPathIsLiteral: a path typo must not silently become
// an empty system prompt. It is used as literal text (what pi would do) and
// Describe() says so, which is how the operator spots it in the startup log.
func TestSystemPromptMissingPathIsLiteral(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.md")
	sp := NewSystemPrompt(missing, discardLogger())
	if sp == nil {
		t.Fatal("nil for a non-empty value")
	}
	if sp.Text() != missing {
		t.Errorf("Text() = %q, want the value used literally", sp.Text())
	}
	if sp.Describe() != "inline text" {
		t.Errorf("Describe() = %q, want inline text so the typo is visible", sp.Describe())
	}
}

// TestSystemPromptDirIsLiteral: a directory is not a prompt file.
func TestSystemPromptDirIsLiteral(t *testing.T) {
	dir := t.TempDir()
	if got := NewSystemPrompt(dir, discardLogger()).Describe(); got != "inline text" {
		t.Errorf("a directory should not be read as a prompt file, got %q", got)
	}
}

// TestSystemPromptTruncates guards the argv limit: the text rides as one argv
// entry, and Linux rejects the whole exec above MAX_ARG_STRLEN — which would
// break every email rather than trimming one prompt.
func TestSystemPromptTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.md")
	// Multi-byte runes so a naive cut would split one.
	if err := os.WriteFile(path, []byte(strings.Repeat("汉", MaxSystemPromptBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := NewSystemPrompt(path, discardLogger()).Text()
	if len(got) > MaxSystemPromptBytes {
		t.Errorf("text is %d bytes, want <= %d", len(got), MaxSystemPromptBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a rune")
	}
}

// capturingLogger returns a logger writing into buf, so a test can assert on
// what an operator would actually see in the log.
func capturingLogger(buf *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestSystemPromptLogsReloadOnChange pins the operator-visible signal for the
// per-email re-read: editing the role file must announce itself, or there is
// no way to confirm perch picked the edit up short of diffing the DEBUG argv
// dump. Unchanged content stays silent — one line per email saying "nothing
// happened" would bury the line that matters.
func TestSystemPromptLogsReloadOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte("role one"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	sp := NewSystemPrompt(path, capturingLogger(&buf))

	// First read: content matches what the constructor already loaded, so
	// nothing to announce.
	sp.Text()
	if strings.Contains(buf.String(), "reloaded") {
		t.Errorf("unchanged content must not log a reload:\n%s", buf.String())
	}

	if err := os.WriteFile(path, []byte("role two, which is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp.Text()
	log := buf.String()
	if !strings.Contains(log, "append_system_prompt reloaded") {
		t.Errorf("an edited role file must log a reload:\n%s", log)
	}
	if !strings.Contains(log, "bytes=25") || !strings.Contains(log, "previous_bytes=8") {
		t.Errorf("reload line should carry both sizes:\n%s", log)
	}

	// Reading again with no further edit is silent.
	buf.Reset()
	sp.Text()
	if strings.Contains(buf.String(), "reloaded") {
		t.Errorf("a second read with no edit must be silent:\n%s", buf.String())
	}
}

// TestSystemPromptLogsUnreadableFile: a path that stats but cannot be read
// must say so at startup rather than silently yielding an empty role.
func TestSystemPromptLogsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable")
	}
	path := filepath.Join(t.TempDir(), "locked.md")
	if err := os.WriteFile(path, []byte("secret role"), 0o000); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	NewSystemPrompt(path, capturingLogger(&buf))
	if !strings.Contains(buf.String(), "unreadable at startup") {
		t.Errorf("an unreadable file must warn at startup:\n%s", buf.String())
	}
}

// TestSystemPromptWarnsOnUnfilledTemplate is the guard for what a live deploy
// actually did on 2026-08-25: helpdesk.md.example was copied verbatim, so the
// agent was told to escalate to a literal "[[ESCALATION CONTACT …]]" and the
// operator's setup comment reached it as instructions.
func TestSystemPromptWarnsOnUnfilledTemplate(t *testing.T) {
	body, err := os.ReadFile("../../helpdesk.md.example")
	if err != nil {
		t.Fatalf("read the shipped example: %v", err)
	}
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	NewSystemPrompt(path, capturingLogger(&buf))

	log := buf.String()
	if !strings.Contains(log, "unfilled template placeholders") {
		t.Errorf("copying the example verbatim must warn about placeholders:\n%s", log)
	}
	if !strings.Contains(log, "[[COMPANY]]") {
		t.Errorf("the warning should name the placeholders it found:\n%s", log)
	}
	if !strings.Contains(log, "setup comment") {
		t.Errorf("the leftover operator comment must warn too:\n%s", log)
	}
}

// TestSystemPromptQuietOnFilledTemplate: a properly edited copy must be
// silent, or the warning becomes noise an operator learns to ignore.
func TestSystemPromptQuietOnFilledTemplate(t *testing.T) {
	body, err := os.ReadFile("../../helpdesk.md.example")
	if err != nil {
		t.Fatal(err)
	}
	// What an operator does: drop the comment block, fill every slot.
	text := string(body)
	if i := strings.Index(text, "-->"); i >= 0 {
		text = strings.TrimLeft(text[i+3:], "\n")
	}
	text = placeholderRe.ReplaceAllString(text, "kulink")

	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	NewSystemPrompt(path, capturingLogger(&buf))
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("a filled-in copy must load without warnings:\n%s", buf.String())
	}
}

// TestPlaceholderReIgnoresWikiLinks: [[double brackets]] are also markdown
// wiki-link syntax, and a role definition may legitimately use them. Only an
// uppercase-initial slot counts as a placeholder.
func TestPlaceholderReIgnoresWikiLinks(t *testing.T) {
	if placeholderRe.MatchString("see [[the runbook]] for details") {
		t.Error("lowercase wiki-link must not read as a placeholder")
	}
	if !placeholderRe.MatchString("escalate to [[ESCALATION CONTACT]]") {
		t.Error("uppercase slot must read as a placeholder")
	}
}

// --- --append-system-prompt plumbing ---------------------------------------

// writeStubAdvertising builds a stub agent that answers `--help` with helpOut
// (so the capability probe can see, or not see, the flag) and otherwise
// records its argv and prints REPLY-OK.
func writeStubAdvertising(t *testing.T, helpOut string) (bin, argfile string) {
	t.Helper()
	dir := t.TempDir()
	argfile = filepath.Join(dir, "args.txt")
	bin = filepath.Join(dir, "stubagent")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = --help ]; then printf '%s\\n' '" + helpOut + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + argfile + "\n" +
		"echo REPLY-OK\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argfile
}

func runnerWithSystemPrompt(t *testing.T, bin, value string) *Runner {
	t.Helper()
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(&ag, t.TempDir(), "acceptEdits", 5*time.Second, log).
		WithAppendSystemPrompt(NewSystemPrompt(value, log))
}

// TestRunPassesAppendSystemPrompt is the end of the wire: a configured value
// reaches the agent's argv as --append-system-prompt <text>.
func TestRunPassesAppendSystemPrompt(t *testing.T) {
	bin, argfile := writeStubAdvertising(t, "  --append-system-prompt <text>  Append to system prompt")
	const role = "You are the support desk. Task definitions live in ./tasks/."

	if _, _, err := runnerWithSystemPrompt(t, bin, role).
		Run(context.Background(), "hi", "sid", true); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "--append-system-prompt") {
		t.Errorf("flag missing from argv:\n%s", args)
	}
	if !strings.Contains(string(args), role) {
		t.Errorf("role text missing from argv:\n%s", args)
	}
}

// TestRunSkipsAppendSystemPromptWhenUnsupported pins the gate. nanopi had no
// such flag as of 2026-08-25; passing it anyway would kill every email on an
// unknown flag, so an agent that does not advertise it runs unchanged.
func TestRunSkipsAppendSystemPromptWhenUnsupported(t *testing.T) {
	bin, argfile := writeStubAdvertising(t, "  -p <prompt>  run headless")

	out, _, err := runnerWithSystemPrompt(t, bin, "You are the support desk.").
		Run(context.Background(), "hi", "sid", true)
	if err != nil {
		t.Fatalf("an unsupported flag must not break the run: %v", err)
	}
	if strings.TrimSpace(out) != "REPLY-OK" {
		t.Errorf("out = %q", out)
	}
	args, _ := os.ReadFile(argfile)
	if strings.Contains(string(args), "--append-system-prompt") {
		t.Errorf("flag passed to an agent that does not advertise it:\n%s", args)
	}
}

// TestRunRereadsAppendSystemPromptFile: editing the file changes what the NEXT
// email carries, with no restart.
func TestRunRereadsAppendSystemPromptFile(t *testing.T) {
	bin, argfile := writeStubAdvertising(t, "--append-system-prompt <text>")
	path := filepath.Join(t.TempDir(), "helpdesk.md")
	if err := os.WriteFile(path, []byte("ROLE-ONE"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runnerWithSystemPrompt(t, bin, path)

	if _, _, err := r.Run(context.Background(), "hi", "sid", true); err != nil {
		t.Fatal(err)
	}
	if args, _ := os.ReadFile(argfile); !strings.Contains(string(args), "ROLE-ONE") {
		t.Fatalf("first run missing the initial role:\n%s", args)
	}

	if err := os.WriteFile(path, []byte("ROLE-TWO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(context.Background(), "hi", "sid", false); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "ROLE-TWO") {
		t.Errorf("second run did not pick up the edited file:\n%s", args)
	}
	if strings.Contains(string(args), "ROLE-ONE") {
		t.Errorf("second run still carried the stale role:\n%s", args)
	}
}

// TestRunNoAppendSystemPromptByDefault: the default path is untouched — no
// flag, and no --help probe (WithAppendSystemPrompt is never reached with a
// non-nil value, so nothing is spawned).
func TestRunNoAppendSystemPromptByDefault(t *testing.T) {
	bin, argfile := writeStubAdvertising(t, "--append-system-prompt <text>")
	if _, _, err := runnerWithSystemPrompt(t, bin, "").
		Run(context.Background(), "hi", "sid", true); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argfile)
	if strings.Contains(string(args), "--append-system-prompt") {
		t.Errorf("flag present with nothing configured:\n%s", args)
	}
}
