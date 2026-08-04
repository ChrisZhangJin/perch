package config

import (
	"testing"
	"time"
)

func TestLoadReadsEnvAndDefaults(t *testing.T) {
	t.Setenv("AGENT_EMAIL", "agent@163.com")
	t.Setenv("AGENT_AUTH_CODE", "secret-code")
	t.Setenv("ALLOW_FROM", "Alice@163.com, bob@126.com")
	t.Setenv("CLAUDE_WORKDIR", "/tmp/work")

	cfg, err := Load()
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
	t.Setenv("AGENT_EMAIL", "")
	t.Setenv("AGENT_AUTH_CODE", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when required env missing")
	}
}
