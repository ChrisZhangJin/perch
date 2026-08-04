package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings, loaded from environment variables.
type Config struct {
	Email          string
	AuthCode       string
	IMAPAddr       string
	SMTPAddr       string
	AllowFrom      []string
	ClaudeBin      string
	ClaudeWorkdir  string
	ClaudePermMode string
	PollInterval   time.Duration
	TaskTimeout    time.Duration
	MaxPromptBytes int
	SessionStore   string
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	c := &Config{
		Email:          os.Getenv("AGENT_EMAIL"),
		AuthCode:       os.Getenv("AGENT_AUTH_CODE"),
		IMAPAddr:       envOr("IMAP_ADDR", "imap.163.com:993"),
		SMTPAddr:       envOr("SMTP_ADDR", "smtp.163.com:465"),
		AllowFrom:      parseList(os.Getenv("ALLOW_FROM")),
		ClaudeBin:      envOr("CLAUDE_BIN", "claude"),
		ClaudeWorkdir:  envOr("CLAUDE_WORKDIR", "."),
		ClaudePermMode: envOr("CLAUDE_PERMISSION_MODE", "acceptEdits"),
		PollInterval:   durOr("POLL_INTERVAL", 60*time.Second),
		TaskTimeout:    durOr("TASK_TIMEOUT", 30*time.Minute),
		MaxPromptBytes: intOr("MAX_PROMPT_BYTES", 65536),
		SessionStore:   envOr("SESSION_STORE", filepath.Join(os.TempDir(), "aamp-watcher-sessions.json")),
	}
	if c.Email == "" || c.AuthCode == "" {
		return nil, errors.New("AGENT_EMAIL and AGENT_AUTH_CODE are required")
	}
	return c, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durOr(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func intOr(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func parseList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
