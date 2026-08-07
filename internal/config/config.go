// Package config loads perch settings from three layers, in this order of
// precedence (higher overrides lower):
//
//  1. Environment variables (e.g. AGENT_EMAIL, POLL_INTERVAL).
//  2. A YAML file (path from --config / PERCH_CONFIG; default ./perch.yaml,
//     then ~/.config/perch/perch.yaml).
//  3. Built-in defaults (see defaults()).
//
// Secrets (the 163 auth code) MUST be set via env and are never committed to
// the YAML file. The required-AGENT_EMAIL / AGENT_AUTH_CODE check still runs
// after layering, so the YAML can hold non-secret defaults and the env can
// inject the credentials.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime settings. Field names are the source of truth;
// YAML tags map snake_case keys, env tags map the legacy AGENT_* / CLAUDE_*
// / IMAP_* / SMTP_ADDR / POLL_INTERVAL / TASK_TIMEOUT / MAX_PROMPT_BYTES /
// SESSION_STORE / TLS_INSECURE_SKIP_VERIFY names.
type Config struct {
	Email              string
	AuthCode           string
	IMAPAddr           string
	SMTPAddr           string
	AllowFrom          []string
	ClaudeBin          string
	ClaudeWorkdir      string
	ClaudePermMode     string
	PollInterval       time.Duration
	TaskTimeout        time.Duration
	MaxPromptBytes     int
	MaxAttachmentBytes int
	SessionStore       string
	TLSInsecure        bool
}

// yamlConfig mirrors Config with snake_case keys + a few non-secret
// only fields (no AuthCode is ever read from disk).
type yamlConfig struct {
	IMAPAddr            string        `yaml:"imap_addr"`
	SMTPAddr            string        `yaml:"smtp_addr"`
	AllowFrom           []string      `yaml:"allow_from"`
	AgentBin            string        `yaml:"agent_bin"`
	AgentWorkdir        string        `yaml:"agent_workdir"`
	AgentPermissionMode string        `yaml:"agent_permission_mode"`
	PollInterval        time.Duration `yaml:"poll_interval"`
	TaskTimeout         time.Duration `yaml:"task_timeout"`
	MaxPromptBytes      int           `yaml:"max_prompt_bytes"`
	MaxAttachmentBytes  int           `yaml:"max_attachment_bytes"`
	SessionStore        string        `yaml:"session_store"`
	TLSInsecure         bool          `yaml:"tls_insecure_skip_verify"`
}

// Load reads the YAML file at cfgPath (or the default search path), layers
// env vars on top, then applies built-in defaults for anything still empty.
// cfgPath == "" means: search default locations.
//
// Required fields (AGENT_EMAIL, AGENT_AUTH_CODE) are still taken from the env
// only — they never come from the YAML file. This is intentional: a committed
// YAML must not be able to leak credentials.
func Load(cfgPath string) (*Config, error) {
	// 1. Built-in defaults.
	c := defaults()

	// 2. YAML layer (if a file is found).
	if cfgPath == "" {
		cfgPath = firstExisting(defaultConfigPaths())
	}
	if cfgPath != "" {
		if err := applyYAML(c, cfgPath); err != nil {
			return nil, fmt.Errorf("load config %s: %w", cfgPath, err)
		}
	}

	// 3. Env layer (highest priority; overrides YAML + defaults).
	applyEnv(c)

	// 4. Required-field gate. Secrets come from env only.
	if c.Email == "" || c.AuthCode == "" {
		return nil, fmt.Errorf(
			"AGENT_EMAIL and AGENT_AUTH_CODE are required. " +
				"perch reads credentials from env vars only — never from the YAML file. " +
				"Run it like:\n" +
				"  AGENT_EMAIL=you@163.com AGENT_AUTH_CODE='<your 163 authcode>' ./bin/perch\n" +
				"(tip: keep the auth code in an untracked file like ./grant.code, then " +
				"AGENT_AUTH_CODE=\"$(cat ./grant.code)\" ./bin/perch)")
	}
	return c, nil
}

// defaults returns the built-in fallback values. Anything zero here is
// overridden by YAML or env; this is the bottom of the stack.
func defaults() *Config {
	return &Config{
		IMAPAddr:           "imap.163.com:993",
		SMTPAddr:           "smtp.163.com:465",
		ClaudeBin:          "claude",
		ClaudeWorkdir:      ".",
		ClaudePermMode:     "acceptEdits",
		PollInterval:       60 * time.Second,
		TaskTimeout:        30 * time.Minute,
		MaxPromptBytes:     65536,
		MaxAttachmentBytes: 50 << 20, // 50 MB per attachment
		SessionStore:       filepath.Join(os.TempDir(), "perch-sessions.json"),
	}
}

// defaultConfigPaths returns the search order for the YAML file. The first
// existing file is used; if none exist, Load silently proceeds with defaults.
func defaultConfigPaths() []string {
	paths := []string{"./perch.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "perch", "perch.yaml"))
	}
	return paths
}

// firstExisting returns the first path in `paths` that is a regular file,
// or "" if none exist. Missing files are not an error at this stage — only
// an unreadable or malformed file is.
func firstExisting(paths []string) string {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// applyYAML decodes the file at path and merges its values into c. Empty /
// zero values in the YAML are left alone (don't clobber defaults with blanks).
func applyYAML(c *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var y yamlConfig
	if err := yaml.Unmarshal(data, &y); err != nil {
		return err
	}
	if y.IMAPAddr != "" {
		c.IMAPAddr = y.IMAPAddr
	}
	if y.SMTPAddr != "" {
		c.SMTPAddr = y.SMTPAddr
	}
	if len(y.AllowFrom) > 0 {
		c.AllowFrom = normalizeList(y.AllowFrom)
	}
	if y.AgentBin != "" {
		c.ClaudeBin = y.AgentBin
	}
	if y.AgentWorkdir != "" {
		c.ClaudeWorkdir = y.AgentWorkdir
	}
	if y.AgentPermissionMode != "" {
		c.ClaudePermMode = y.AgentPermissionMode
	}
	if y.PollInterval != 0 {
		c.PollInterval = y.PollInterval
	}
	if y.TaskTimeout != 0 {
		c.TaskTimeout = y.TaskTimeout
	}
	if y.MaxPromptBytes != 0 {
		c.MaxPromptBytes = y.MaxPromptBytes
	}
	if y.MaxAttachmentBytes != 0 {
		c.MaxAttachmentBytes = y.MaxAttachmentBytes
	}
	if y.SessionStore != "" {
		c.SessionStore = y.SessionStore
	}
	if y.TLSInsecure {
		c.TLSInsecure = y.TLSInsecure
	}
	return nil
}

// applyEnv overlays env vars onto c. This is the highest-precedence layer.
func applyEnv(c *Config) {
	if v := os.Getenv("AGENT_EMAIL"); v != "" {
		c.Email = v
	}
	if v := os.Getenv("AGENT_AUTH_CODE"); v != "" {
		c.AuthCode = v
	}
	if v := os.Getenv("IMAP_ADDR"); v != "" {
		c.IMAPAddr = v
	}
	if v := os.Getenv("SMTP_ADDR"); v != "" {
		c.SMTPAddr = v
	}
	if v := os.Getenv("ALLOW_FROM"); v != "" {
		c.AllowFrom = normalizeList(strings.Split(v, ","))
	}
	if v := os.Getenv("CLAUDE_BIN"); v != "" {
		c.ClaudeBin = v
	}
	if v := os.Getenv("CLAUDE_WORKDIR"); v != "" {
		c.ClaudeWorkdir = v
	}
	if v := os.Getenv("CLAUDE_PERMISSION_MODE"); v != "" {
		c.ClaudePermMode = v
	}
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.PollInterval = d
		}
	}
	if v := os.Getenv("TASK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.TaskTimeout = d
		}
	}
	if v := os.Getenv("MAX_PROMPT_BYTES"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			c.MaxPromptBytes = n
		}
	}
	if v := os.Getenv("MAX_ATTACHMENT_BYTES"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			c.MaxAttachmentBytes = n
		}
	}
	if v := os.Getenv("SESSION_STORE"); v != "" {
		c.SessionStore = v
	}
	if v := os.Getenv("TLS_INSECURE_SKIP_VERIFY"); v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			c.TLSInsecure = b
		}
	}
}

// normalizeList lower-cases + trims + drops empties. Shared by the YAML and
// env paths so a YAML list and an ALLOW_FROM env var look the same to gate.
func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ResolveConfigPath picks the YAML path from the CLI flag, the env, or the
// default search. Exported so cmd/perch can log which file was actually used.
func ResolveConfigPath(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("PERCH_CONFIG"); v != "" {
		return v
	}
	return firstExisting(defaultConfigPaths())
}
