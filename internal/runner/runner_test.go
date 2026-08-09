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
	p := BuildPrompt("alice@163.com", "Do X", "please do X", nil, "")
	if !strings.Contains(p, "alice@163.com") || !strings.Contains(p, "Do X") || !strings.Contains(p, "please do X") {
		t.Errorf("prompt missing fields: %q", p)
	}
}

func TestBuildPromptAttachmentHints(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Do X", "body", []string{"/tmp/att/app.log", "/tmp/att/notes.txt"}, "/home/agent/reply")
	for _, want := range []string{"/tmp/att/app.log", "/tmp/att/notes.txt", "/home/agent/reply"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
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
