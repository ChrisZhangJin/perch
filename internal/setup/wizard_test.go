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

func TestMissingFieldsWrapper(t *testing.T) {
	// Empty config: all required fields missing.
	empty := &config.Config{}
	got := MissingFields(empty)
	if len(got) == 0 {
		t.Fatalf("empty config: expected missing fields, got none")
	}
	want := map[string]bool{
		"email": false, "authcode": false, "provider.name": false,
		"agent.name": false, "agent.workdir": false,
		"agent.permission_mode": false, "log_level": false,
	}
	for _, f := range got {
		want[f] = true
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("empty config: expected %q in missing list", f)
		}
	}

	// Fully populated: empty list (AllowFrom omitted by design).
	full := &config.Config{
		Email: "a@163.com", AuthCode: "x",
		ProviderName: "163", AgentName: "claude",
		AgentWorkdir: ".", AgentPermMode: "plan", LogLevel: "info",
	}
	if got := MissingFields(full); len(got) != 0 {
		t.Fatalf("full config: expected no missing, got %v", got)
	}
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
		LogLevel:      "info",
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
		// AllowFrom empty by design — not required for headless start; an
		// empty allow_from means "deny all" and is the safe default.
	}
	in := &bytes.Buffer{} // empty => "not a TTY" path
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	err := Ensure(cfg, in, out, errOut, func(int) ([]byte, error) { return nil, nil })
	if !errors.Is(err, ErrMissingFields) {
		t.Fatalf("expected ErrMissingFields, got %v", err)
	}
	msg := errOut.String()
	if !strings.Contains(strings.ToLower(msg), "authcode") {
		t.Errorf("error output missing %q: %s", "authcode", msg)
	}
	if strings.Contains(strings.ToLower(msg), "allow_from") {
		t.Errorf("error should NOT mention allow_from (no longer required for startup): %s", msg)
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
	// wizard prompts (in order): provider, agent, workdir, perm_mode,
	// log_level, allow_from, email. Defaults for the first five are set,
	// so empty lines accept them.
	script := "\n\n\n\n\nbob@qq.com\nagent@qq.com\n"
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

// TestPersistWritesEveryYAMLKey enforces persist's documented promise: the
// generated perch.yaml shows every knob that exists, so an operator never has
// to learn a key name from the source. It reflects over the real yamlConfig
// key set, so adding a field to config without teaching persist to write it
// fails here instead of shipping an invisible setting.
//
// Regression: task_only and long_task_ack were both added to config and to
// perch.yaml.example but never to persist, so wizard-generated configs
// omitted them entirely.
func TestPersistWritesEveryYAMLKey(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Seed from Defaults, matching the production path (config.Load then
	// setup.Ensure) that persist's INVARIANT comment requires.
	cfg := config.Defaults()
	cfg.Email = "agent@163.com"
	cfg.ProviderName = "163"
	cfg.AgentName = "claude"
	cfg.AgentWorkdir = "."
	cfg.AgentPermMode = "acceptEdits"
	cfg.AllowFrom = []string{"alice@163.com"}

	if err := persist(cfg); err != nil {
		t.Fatalf("persist: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(tmpHome, ".perch", "perch.yaml"))
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}

	for _, key := range config.YAMLKeys() {
		// Nested keys are written indented under their parent, so match on
		// the leaf; the parent block key is checked by its own entry.
		leaf := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			leaf = key[i+1:]
		}
		if !strings.Contains(string(body), leaf+":") {
			t.Errorf("persisted config is missing key %q — add it to persist() in wizard.go\nfile:\n%s", key, body)
		}
	}
}

// TestPersistedConfigRoundTrips closes the loop the key-presence check
// cannot: the file persist writes must parse back through config.Load with
// the same values. A key can be present but misspelled, wrongly indented, or
// carry a value the strict parser rejects.
func TestPersistedConfigRoundTrips(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg := config.Defaults()
	cfg.Email = "agent@163.com"
	cfg.ProviderName = "qq"
	cfg.AgentName = "claude"
	cfg.AgentWorkdir = "/tmp/wd"
	cfg.AgentPermMode = "plan"
	cfg.AllowFrom = []string{"alice@163.com"}
	cfg.LongTaskAck = true
	cfg.AgentTaskOnly = false // explicitly disabled must survive the round trip

	if err := persist(cfg); err != nil {
		t.Fatalf("persist: %v", err)
	}
	path := filepath.Join(tmpHome, ".perch", "perch.yaml")

	// config.Load layers env on top of YAML; clear the ones that would win
	// so this test reads the file, not the ambient environment.
	for _, k := range []string{"AGENT_EMAIL", "ALLOW_FROM", "POLL_INTERVAL",
		"TASK_TIMEOUT", "MAX_PROMPT_BYTES", "MAX_ATTACHMENT_BYTES",
		"SESSION_STORE", "TLS_INSECURE_SKIP_VERIFY", "LOG_LEVEL", "LONG_TASK_ACK"} {
		t.Setenv(k, "")
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load on wizard-written file: %v", err)
	}
	if got.AgentTaskOnly != false {
		t.Errorf("AgentTaskOnly = %v, want false — an explicit opt-out must survive persist→Load", got.AgentTaskOnly)
	}
	if got.LongTaskAck != true {
		t.Errorf("LongTaskAck = %v, want true", got.LongTaskAck)
	}
	if got.AgentPermMode != "plan" {
		t.Errorf("AgentPermMode = %q, want plan", got.AgentPermMode)
	}
	if got.ProviderName != "qq" {
		t.Errorf("ProviderName = %q, want qq", got.ProviderName)
	}
	if len(got.AllowFrom) != 1 || got.AllowFrom[0] != "alice@163.com" {
		t.Errorf("AllowFrom = %#v", got.AllowFrom)
	}
}

func TestEnsureInteractiveAuthcodeNotEchoed(t *testing.T) {
	interactiveStdin(t)
	cfg := &config.Config{} // everything missing => wizard runs
	in := strings.NewReader("163\nclaude\n.\nacceptEdits\ninfo\nalice@x\nagent@x\n")
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

// TestEnsureInteractiveAllowFromDefaultIsWildlet pins the onboarding
// default: when the operator hits Enter at the allow_from prompt with no
// current value, the wizard writes ["*"] (the gate's accept-everyone
// sentinel) — not "[]" — so perch actually accepts mail on a fresh install.
//
// Regression for the user-reported bug: prior behaviour wrote
// allow_from: [] to disk, which the gate then treated as "deny everyone"
// and silently dropped every inbound message.
func TestEnsureInteractiveAllowFromDefaultIsWildlet(t *testing.T) {
	interactiveStdin(t)
	cfg := &config.Config{} // fresh install: every field missing
	// Wizard prompts (in order): provider, agent, workdir, perm_mode,
	// log_level, allow_from, email. Six Enter's accept the default for
	// each, then we type the email on the seventh line.
	in := strings.NewReader("\n\n\n\n\n\nagent@x.com\n")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	pw := func(int) ([]byte, error) { return []byte("pw"), nil }
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v\nstderr:\n%s", err, errOut.String())
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "*" {
		t.Errorf("cfg.AllowFrom = %#v, want [\"*\"] (wizard default for empty current value)", cfg.AllowFrom)
	}

	// Persisted file must contain `allow_from:\n  - "*"`, NOT `allow_from: []` —
	// the whole point of this fix is what hits disk. The entry is quoted
	// in the YAML because bare `*` is the YAML alias indicator and the
	// strict parser rejects it; gate.New sees it as the literal string "*".
	cfgPath := filepath.Join(tmpHome, ".perch", "perch.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `allow_from:`) || !strings.Contains(body, `"*"`) {
		t.Errorf("persisted file missing `allow_from: [\"*\"]` line, got:\n%s", body)
	}
	if strings.Contains(body, "allow_from: []") {
		t.Errorf("persisted file must NOT contain `allow_from: []` (that's deny-all), got:\n%s", body)
	}
}
