package setup

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/config"
)

// interactiveStdin overrides isTTYFn so the wizard runs as if stdin is a TTY.
// Tests that exercise the interactive path restore the default on cleanup.
func interactiveStdin(t *testing.T) {
	t.Helper()
	prev := isTTYFn
	isTTYFn = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTTYFn = prev })
}

func TestEnsureSilentWhenComplete(t *testing.T) {
	cfg := &config.Config{
		Email:         "agent@163.com",
		AuthCode:      "secret",
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		AllowFrom:     []string{"alice@163.com"},
	}
	// No I/O expected — pass buffers and a stub password fn that must not be called.
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	pwCalled := false
	pw := func(fd int) ([]byte, error) { pwCalled = true; return nil, nil }
	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if pwCalled {
		t.Error("password fn should not be called when cfg is complete")
	}
	if out.Len() != 0 {
		t.Errorf("no output expected, got: %q", out.String())
	}
}

func TestEnsureNonInteractiveFailsLoud(t *testing.T) {
	cfg := &config.Config{
		Email:         "agent@163.com",
		AuthCode:      "", // missing
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		// AllowFrom also missing
	}
	in := &bytes.Buffer{} // empty => "not a TTY" path
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	err := Ensure(cfg, in, out, errOut, func(int) ([]byte, error) { return nil, nil })
	if !errors.Is(err, ErrMissingFields) {
		t.Fatalf("expected ErrMissingFields, got %v", err)
	}
	msg := errOut.String()
	for _, want := range []string{"authcode", "allow_from"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("error output missing %q: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "AGENT_EMAIL") {
		t.Errorf("error should mention AGENT_EMAIL env var: %s", msg)
	}
}

func TestEnsureInteractiveHappyPath(t *testing.T) {
	interactiveStdin(t)
	cfg := &config.Config{
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		// Email, AuthCode, AllowFrom — wizard fills these.
	}
	// Answers in order: email, authcode (via pw fn), allow_from literal.
	// wizard prompts (in order): provider, agent, workdir, allow_from, email.
	// Defaults for provider/agent/workdir are set, so empty lines accept them.
	script := "\n\n\n\nbob@qq.com\nagent@qq.com\n"
	in := strings.NewReader(script)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	var pwCount int
	pw := func(fd int) ([]byte, error) {
		pwCount++
		return []byte("supersecret"), nil
	}
	// HOME override so we write to a temp config dir, not the user's real one.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v\nstderr:\n%s", err, errOut.String())
	}
	if pwCount != 1 {
		t.Errorf("password fn called %d times, want 1", pwCount)
	}
	if cfg.Email != "agent@qq.com" {
		t.Errorf("Email = %q", cfg.Email)
	}
	if cfg.AuthCode != "supersecret" {
		t.Errorf("AuthCode = %q", cfg.AuthCode)
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "bob@qq.com" {
		t.Errorf("AllowFrom = %#v", cfg.AllowFrom)
	}

	// Persisted file should exist with 0600 mode.
	cfgPath := filepath.Join(tmpHome, ".perch", "perch.yaml")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("expected persisted config at %s: %v", cfgPath, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("persisted file mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "email_provider") {
		t.Errorf("persisted file missing email_provider:\n%s", data)
	}
}

func TestEnsureInteractiveAuthcodeNotEchoed(t *testing.T) {
	interactiveStdin(t)
	cfg := &config.Config{} // everything missing => wizard runs
	in := strings.NewReader("163\nclaude\n.\nacceptEdits\nalice@x\nagent@x\n")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	var usedReadPassword bool
	pw := func(fd int) ([]byte, error) {
		usedReadPassword = true
		return []byte("authcode-value"), nil
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !usedReadPassword {
		t.Error("wizard must use the injected password function (ReadPassword-style), not Fscanln, for the auth code")
	}
}
