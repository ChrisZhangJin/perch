package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plog "github.com/ChrisZhangJin/perch/internal/log"
)

func TestApplyYAMLWarnsOnDeprecatedKeys(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte(`
imap_addr: imap.163.com:993
smtp_addr: smtp.163.com:465
agent_bin: myagent
claude_workdir: /tmp
claude_permission_mode: bypassPermissions
email_provider:
  name: 163
ai_agent:
  name: claude
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(plog.New(&buf, slog.LevelInfo)))
	defer slog.SetDefault(prev)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// New-schema fields should still apply.
	if cfg.ProviderName != "163" {
		t.Errorf("ProviderName = %q", cfg.ProviderName)
	}
	if cfg.AgentName != "claude" {
		t.Errorf("AgentName = %q", cfg.AgentName)
	}
	logged := buf.String()
	for _, key := range []string{"imap_addr", "smtp_addr", "agent_bin", "claude_workdir", "claude_permission_mode"} {
		if !strings.Contains(logged, key) {
			t.Errorf("expected WARN for deprecated key %q, log was:\n%s", key, logged)
		}
	}
}

func TestApplyYAMLSilentOnNewSchema(t *testing.T) {
	withCleanEnv(t)
	yaml := []byte(`
email_provider:
  name: 163
ai_agent:
  name: claude
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "perch.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EMAIL", "agent@x")
	t.Setenv("AGENT_AUTH_CODE", "secret")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(plog.New(&buf, slog.LevelInfo)))
	defer slog.SetDefault(prev)

	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("no warnings expected on new schema, got:\n%s", buf.String())
	}
}
