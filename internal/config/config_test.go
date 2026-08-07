package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCleanEnv clears every perch env var so tests don't leak between cases.
func withCleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AGENT_EMAIL", "AGENT_AUTH_CODE", "IMAP_ADDR", "SMTP_ADDR",
		"ALLOW_FROM", "CLAUDE_BIN", "CLAUDE_WORKDIR", "CLAUDE_PERMISSION_MODE",
		"POLL_INTERVAL", "TASK_TIMEOUT", "MAX_PROMPT_BYTES",
		"SESSION_STORE", "TLS_INSECURE_SKIP_VERIFY", "PERCH_CONFIG",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadReadsEnvAndDefaults(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "agent@163.com")
	t.Setenv("AGENT_AUTH_CODE", "secret-code")
	t.Setenv("ALLOW_FROM", "Alice@163.com, bob@126.com")
	t.Setenv("CLAUDE_WORKDIR", "/tmp/work")

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
	if cfg.IMAPAddr != "imap.163.com:993" {
		t.Errorf("IMAPAddr default = %q", cfg.IMAPAddr)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval default = %v", cfg.PollInterval)
	}
	if cfg.ClaudePermMode != "acceptEdits" {
		t.Errorf("ClaudePermMode default = %q", cfg.ClaudePermMode)
	}
}

func TestLoadMissingRequiredFails(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "")
	t.Setenv("AGENT_AUTH_CODE", "")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when required env missing")
	}
}

func TestLoadAppliesYAMLDefaults(t *testing.T) {
	withCleanEnv(t)
	// Non-default YAML values that no built-in default uses.
	yaml := []byte(`
imap_addr: imap.example.com:143
smtp_addr: smtp.example.com:587
poll_interval: 5s
task_timeout: 10m
agent_bin: myagent
agent_permission_mode: bypassPermissions
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
	if cfg.IMAPAddr != "imap.example.com:143" {
		t.Errorf("IMAPAddr = %q", cfg.IMAPAddr)
	}
	if cfg.SMTPAddr != "smtp.example.com:587" {
		t.Errorf("SMTPAddr = %q", cfg.SMTPAddr)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.TaskTimeout != 10*time.Minute {
		t.Errorf("TaskTimeout = %v", cfg.TaskTimeout)
	}
	if cfg.ClaudeBin != "myagent" {
		t.Errorf("ClaudeBin = %q", cfg.ClaudeBin)
	}
	if cfg.ClaudePermMode != "bypassPermissions" {
		t.Errorf("ClaudePermMode = %q", cfg.ClaudePermMode)
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "carol@example.com" {
		t.Errorf("AllowFrom = %#v (expected lowercased)", cfg.AllowFrom)
	}
}

func TestLoadEnvOverridesYAML(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte(`
imap_addr: imap.yaml.example.com:993
poll_interval: 5s
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")
	t.Setenv("IMAP_ADDR", "imap.env.example.com:993")
	t.Setenv("POLL_INTERVAL", "2m")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IMAPAddr != "imap.env.example.com:993" {
		t.Errorf("env should win over YAML, got IMAPAddr = %q", cfg.IMAPAddr)
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
	if err := os.WriteFile(bad, []byte("imap_addr: [unclosed"), 0o600); err != nil {
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
	if cfg.IMAPAddr != "imap.163.com:993" {
		t.Errorf("empty YAML should keep defaults, got IMAPAddr = %q", cfg.IMAPAddr)
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
