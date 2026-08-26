package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentBuildArgs_ClaudeNew(t *testing.T) {
	a, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "11111111-1111-4111-8111-111111111111",
		IsNew: true, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--session-id", "11111111-1111-4111-8111-111111111111"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_ClaudeResume(t *testing.T) {
	a, _ := Lookup("claude")
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "22222222-2222-4222-8222-222222222222",
		IsNew: false, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--resume", "22222222-2222-4222-8222-222222222222"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiAcceptEdits(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "acceptEdits"})
	want := []string{"-p", "p", "--output", "text", "--approve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi acceptEdits = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiBypassPermissions(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "bypassPermissions"})
	want := []string{"-p", "p", "--output", "text", "--approve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi bypassPermissions = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiResume(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--output", "text", "--session", "sid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiNewOmitsSession(t *testing.T) {
	// Regression: passing --session <new-uuid> to nanopi fails with
	// "first line must be a session header" because the flag is resume-only.
	// On IsNew=true we must omit the flag and let nanopi create the file.
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "acceptEdits"})
	for _, a := range got {
		if a == "--session" {
			t.Errorf("nanopi IsNew=true must not pass --session, got argv: %v", got)
		}
	}
}

func TestAgentBuildArgs_PiNew(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-new", IsNew: true, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session-id", "sid-new"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_PiResume(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-old", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session", "sid-old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi resume = %v, want %v", got, want)
	}
}

func TestAgentLookupUnknown(t *testing.T) {
	_, err := Lookup("not-an-agent")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should list valid names, got: %v", err)
	}
}

func TestBinaryPath_UnknownName(t *testing.T) {
	_, err := BinaryPath("not-an-agent")
	if err == nil {
		t.Fatal("expected error for unknown agent name")
	}
	if strings.Contains(err.Error(), ErrBinaryNotFound.Error()) {
		t.Errorf("unknown-name error must not be ErrBinaryNotFound, got: %v", err)
	}
}

func TestBinaryPath_KnownButMissing(t *testing.T) {
	// Probe the registry's binary for "pi" directly via exec.LookPath.
	// If it exists on this machine (developer env), skip — we can't
	// deterministically force the missing case without mutating the
	// package's private table. The wrapping contract is the same
	// either way: if LookPath fails, BinaryPath must return
	// ErrBinaryNotFound with the binary name in the message.
	a, _ := Lookup("pi")
	if _, err := exec.LookPath(a.Binary); err == nil {
		t.Skip("pi binary exists on this machine; cannot test missing-PATH path")
	}
	_, bpErr := BinaryPath("pi")
	if bpErr == nil {
		t.Fatal("expected ErrBinaryNotFound when binary missing on PATH")
	}
	if !errors.Is(bpErr, ErrBinaryNotFound) {
		t.Errorf("expected ErrBinaryNotFound, got: %v", bpErr)
	}
	if !strings.Contains(bpErr.Error(), "pi") {
		t.Errorf("error should mention the binary name, got: %v", bpErr)
	}
}

// --- --append-system-prompt -------------------------------------------------

// TestBuildArgsAppendSystemPrompt pins the flag on every adapter: the same
// flag name and the same text, because perch resolves a configured file
// itself and hands all three agents identical bytes (claude's flag takes text
// only, so a path would not travel).
func TestBuildArgsAppendSystemPrompt(t *testing.T) {
	const sys = "You are the support desk. Task definitions live in ./tasks/."
	for _, name := range []string{"claude", "nanopi", "pi"} {
		a, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, isNew := range []bool{true, false} {
			got := a.BuildArgs(Args{
				Prompt: "p", SessionID: "sid", IsNew: isNew,
				Workdir: "/w", PermMode: "acceptEdits", AppendSystemPrompt: sys,
			})
			idx := -1
			for i, v := range got {
				if v == "--append-system-prompt" {
					idx = i
					break
				}
			}
			if idx < 0 {
				t.Errorf("%s (isNew=%v): --append-system-prompt missing from %v", name, isNew, got)
				continue
			}
			if idx+1 >= len(got) || got[idx+1] != sys {
				t.Errorf("%s (isNew=%v): flag not followed by the text: %v", name, isNew, got)
			}
		}
	}
}

// TestBuildArgsNoAppendSystemPromptWhenEmpty: the flag must be absent, not
// present-with-empty-string, when nothing is configured. An empty value would
// blank out whatever the agent's own default appends.
func TestBuildArgsNoAppendSystemPromptWhenEmpty(t *testing.T) {
	for _, name := range []string{"claude", "nanopi", "pi"} {
		a, _ := Lookup(name)
		got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, PermMode: "acceptEdits"})
		for _, v := range got {
			if v == "--append-system-prompt" {
				t.Errorf("%s: flag present with no text configured: %v", name, got)
			}
		}
	}
}

// TestSupportsAppendSystemPrompt covers the capability probe. It asks the
// binary instead of consulting a table, so the answer tracks reality: as of
// 2026-08-25 claude and pi have the flag and nanopi does not, and perch must
// start using it the moment nanopi ships it — without a perch release.
func TestSupportsAppendSystemPrompt(t *testing.T) {
	dir := t.TempDir()

	supports := filepath.Join(dir, "supports")
	writeExec(t, supports, "#!/bin/sh\necho '  --append-system-prompt <text>  Append to system prompt'\n")
	if !SupportsAppendSystemPrompt(supports) {
		t.Error("binary whose --help lists the flag should be reported as supporting it")
	}

	// Help on stderr is just as valid — CLI frameworks differ.
	stderrHelp := filepath.Join(dir, "stderr-help")
	writeExec(t, stderrHelp, "#!/bin/sh\necho '--append-system-prompt <text>' >&2\nexit 1\n")
	if !SupportsAppendSystemPrompt(stderrHelp) {
		t.Error("help on stderr (and a non-zero exit) must still count")
	}

	lacks := filepath.Join(dir, "lacks")
	writeExec(t, lacks, "#!/bin/sh\necho '  -p <prompt>   run headless'\n")
	if SupportsAppendSystemPrompt(lacks) {
		t.Error("binary without the flag must not be reported as supporting it")
	}

	// A missing binary must answer false rather than panicking: perch has to
	// keep serving mail when the agent is not installed.
	if SupportsAppendSystemPrompt(filepath.Join(dir, "nope")) {
		t.Error("missing binary must report false")
	}
	if SupportsAppendSystemPrompt("") {
		t.Error("empty binary name must report false")
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
