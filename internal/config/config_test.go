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
		"AGENT_EMAIL", "AGENT_AUTH_CODE",
		"ALLOW_FROM",
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