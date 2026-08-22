package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCleanEnv clears every perch env var so tests don't leak between cases,
// and redirects HOME to a fresh tmp dir so the default-search path
// (~/.perch/perch.yaml) never picks up a real wizard-written file on the
// developer's machine. Without the HOME override, TestLogLevelDefault
// (and any other "no YAML present" test) reads the operator's real
// ~/.perch/perch.yaml and asserts the wrong thing.
func withCleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AGENT_EMAIL", "AGENT_AUTH_CODE",
		"ALLOW_FROM",
		"POLL_INTERVAL", "TASK_TIMEOUT", "MAX_PROMPT_BYTES",
		"SESSION_STORE", "TLS_INSECURE_SKIP_VERIFY", "LOG_LEVEL", "LONG_TASK_ACK", "PERCH_CONFIG",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
}

func TestLoadReadsEnvAndDefaults(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@163.com")
	t.Setenv("AGENT_AUTH_CODE", "secret-code")
	t.Setenv("ALLOW_FROM", "Alice@163.com, bob@126.com")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Email != "agent@163.com" {
		t.Errorf("Email = %q", cfg.Email)
	}
	if len(cfg.AllowFrom) != 2 || cfg.AllowFrom[0] != "alice@163.com" || cfg.AllowFrom[1] != "bob@126.com" {
		t.Errorf("AllowFrom = %#v", cfg.AllowFrom)
	}
	if cfg.ProviderName != "163" {
		t.Errorf("ProviderName default = %q", cfg.ProviderName)
	}
	if cfg.AgentName != "claude" {
		t.Errorf("AgentName default = %q", cfg.AgentName)
	}
	if cfg.AgentPermMode != "acceptEdits" {
		t.Errorf("AgentPermMode default = %q", cfg.AgentPermMode)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval default = %v", cfg.PollInterval)
	}
}

func TestLoadAppliesYAMLNewSchema(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte(`
email_provider:
  name: qq
ai_agent:
  name: nanopi
  workdir: /tmp/work
  permission_mode: bypassPermissions
poll_interval: 5s
task_timeout: 10m
allow_from:
  - Carol@Example.com
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProviderName != "qq" {
		t.Errorf("ProviderName = %q", cfg.ProviderName)
	}
	if cfg.AgentName != "nanopi" {
		t.Errorf("AgentName = %q", cfg.AgentName)
	}
	if cfg.AgentWorkdir != "/tmp/work" {
		t.Errorf("AgentWorkdir = %q", cfg.AgentWorkdir)
	}
	if cfg.AgentPermMode != "bypassPermissions" {
		t.Errorf("AgentPermMode = %q", cfg.AgentPermMode)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.TaskTimeout != 10*time.Minute {
		t.Errorf("TaskTimeout = %v", cfg.TaskTimeout)
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "carol@example.com" {
		t.Errorf("AllowFrom = %#v (expected lowercased)", cfg.AllowFrom)
	}
}

// TestLoadAcceptAllWildlet pins the YAML round-trip for the `*` literal
// sentinel (gate's accept-everyone marker). Bare `*` is the YAML alias
// indicator and the strict parser rejects it — the wizard writes it
// quoted as `"*"` so this test stays green. If yaml.v3 ever changes
// behaviour around the alias character, this fails loudly instead of
// silently breaking the onboarding default.
func TestLoadAcceptAllWildlet(t *testing.T) {
	withCleanEnv(t)
	for _, body := range []string{
		"allow_from:\n  - \"*\"\n", // wizard output (quoted, safe)
		"allow_from:\n  - '*'\n",   // single-quoted form, equally safe
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "perch.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", body, err)
		}
		if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "*" {
			t.Errorf("Load(%s): AllowFrom = %#v, want [*]", body, cfg.AllowFrom)
		}
	}
}

func TestLoadEnvOverridesYAML(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte(`
email_provider:
  name: 163
ai_agent:
  name: claude
poll_interval: 5s
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")
	t.Setenv("POLL_INTERVAL", "2m")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 2*time.Minute {
		t.Errorf("env should win over YAML, got PollInterval = %v", cfg.PollInterval)
	}
}

func TestLoadMalformedYAMLFails(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	bad := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(bad, []byte("email_provider: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("expected error on malformed YAML")
	}
}

func TestLoadEmptyYAMLFallsBackToDefaults(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	empty := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(empty, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(empty)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProviderName != "163" {
		t.Errorf("empty YAML should keep defaults, got ProviderName = %q", cfg.ProviderName)
	}
	if cfg.AgentName != "claude" {
		t.Errorf("empty YAML should keep defaults, got AgentName = %q", cfg.AgentName)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("empty YAML should keep defaults, got PollInterval = %v", cfg.PollInterval)
	}
}

func TestResolveConfigPathPrefersFlag(t *testing.T) {
	withCleanEnv(t)
	flagPath := filepath.Join(t.TempDir(), "flag.yaml")
	if err := os.WriteFile(flagPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERCH_CONFIG", "/should/be/ignored")
	if got := ResolveConfigPath(flagPath); got != flagPath {
		t.Errorf("flag should win, got %q", got)
	}
	if got := ResolveConfigPath(""); got != "/should/be/ignored" {
		t.Errorf("env should win over defaults, got %q", got)
	}
}

func TestLogLevelDefault(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLogLevelFromYAML(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte("log_level: debug\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestLogLevelEnvOverridesYAML(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte("log_level: debug\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")
	t.Setenv("LOG_LEVEL", "warn")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("env LOG_LEVEL should override YAML, got %q", cfg.LogLevel)
	}
}

func TestLongTaskAckDefaultOff(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LongTaskAck {
		t.Errorf("LongTaskAck default = true, want false (opt-in only)")
	}
}

func TestLongTaskAckFromYAML(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte("long_task_ack: true\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LongTaskAck {
		t.Errorf("YAML long_task_ack: true should enable, got false")
	}
}

func TestLongTaskAckEnvOverridesYAML(t *testing.T) {
	withCleanEnv(t)
	// YAML says off; env says on. Env must win.
	yaml := []byte("long_task_ack: false\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")
	t.Setenv("LONG_TASK_ACK", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LongTaskAck {
		t.Errorf("env LONG_TASK_ACK=true should override YAML, got false")
	}
}

func TestAgentTaskOnlyDefaultOn(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AgentTaskOnly {
		t.Errorf("AgentTaskOnly default = false, want true (safety on by default)")
	}
}

func TestAgentTaskOnlyExplicitFalseFromYAML(t *testing.T) {
	withCleanEnv(t)
	// Default is true; verify an explicit `task_only: false` disables it.
	yaml := []byte("ai_agent:\n  task_only: false\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentTaskOnly {
		t.Errorf("YAML task_only: false should disable, got true")
	}
}

func TestAgentTaskOnlyUnsetKeepsDefault(t *testing.T) {
	withCleanEnv(t)
	// YAML mentions ai_agent but not task_only — must not clobber the default.
	yaml := []byte("ai_agent:\n  name: claude\n")
	path := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AgentTaskOnly {
		t.Errorf("YAML without task_only key should preserve default true, got false")
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
		err  bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"  debug ", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"trace", slog.LevelInfo, true}, // unknown → falls back to info
	}
	for _, c := range cases {
		got, err := ParseLogLevel(c.in)
		if got != c.want {
			t.Errorf("ParseLogLevel(%q) level = %v, want %v", c.in, got, c.want)
		}
		if (err != nil) != c.err {
			t.Errorf("ParseLogLevel(%q) err = %v, want err=%v", c.in, err, c.err)
		}
	}
}
