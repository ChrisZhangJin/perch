// Package config loads perch settings from three layers, in this order of
// precedence (higher overrides lower):
//
//  1. Environment variables (e.g. AGENT_EMAIL, POLL_INTERVAL).
//  2. A YAML file (path from --config / PERCH_CONFIG; default ./perch.yaml,
//     then ~/.config/perch/perch.yaml).
//  3. Built-in defaults (see defaults()).
//
// Secrets (the 163 auth code) MUST be set via env and are never committed to
// the YAML file. IMAP/SMTP endpoints and the agent binary are resolved at
// startup via internal/provider and internal/agent — this package stays pure
// data.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime settings. Field names are the source of truth;
// YAML tags map the new email_provider / ai_agent blocks; env tags map the
// AGENT_* / ALLOW_FROM / *_INTERVAL / *_TIMEOUT / MAX_* / SESSION_STORE /
// TLS_INSECURE_SKIP_VERIFY names. The IMAP/SMTP endpoints and the agent
// binary are NOT here — they're resolved from cfg.ProviderName /
// cfg.AgentName in main.go via the provider and agent packages.
type Config struct {
	Email              string
	AuthCode           string
	ProviderName       string // "163" | "126" | "qq"
	AgentName          string // "claude" | "nanopi" | "pi"
	AgentWorkdir       string
	AgentPermMode      string // claude only; ignored by nanopi/pi
	AllowFrom          []string
	PollInterval       time.Duration
	TaskTimeout        time.Duration
	MaxPromptBytes     int
	MaxAttachmentBytes int
	SessionStore       string
	TLSInsecure        bool
}

// yamlConfig mirrors Config with snake_case keys. Old-style endpoint / agent
// keys (imap_addr, smtp_addr, agent_bin, claude_workdir, claude_permission_mode)
// are intentionally NOT here — applyYAML detects them via a raw yaml.Node
// pass and emits a WARN (see Task 7). AuthCode is never read from disk.
type yamlConfig struct {
	EmailProvider struct {
		Name string `yaml:"name"`
	} `yaml:"email_provider"`
	AIAgent struct {
		Name           string `yaml:"name"`
		Workdir        string `yaml:"workdir"`
		PermissionMode string `yaml:"permission_mode"`
	} `yaml:"ai_agent"`
	AllowFrom          []string      `yaml:"allow_from"`
	PollInterval       time.Duration `yaml:"poll_interval"`
	TaskTimeout        time.Duration `yaml:"task_timeout"`
	MaxPromptBytes     int           `yaml:"max_prompt_bytes"`
	MaxAttachmentBytes int           `yaml:"max_attachment_bytes"`
	SessionStore       string        `yaml:"session_store"`
	TLSInsecure        bool          `yaml:"tls_insecure_skip_verify"`
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
		ProviderName:       "163",
		AgentName:          "claude",
		AgentWorkdir:       ".",
		AgentPermMode:      "acceptEdits",
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

	// Deprecated-key scan: walk the raw yaml.Node tree, log a WARN per hit.
	// New-schema decode below ignores these keys, but we want the user to
	// know they should clean up their file.
	deprecated := []string{
		"imap_addr", "smtp_addr",
		"agent_bin", "claude_workdir", "claude_permission_mode",
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	warnDeprecated(&node, deprecated)

	var y yamlConfig
	if err := yaml.Unmarshal(data, &y); err != nil {
		return err
	}
	if y.EmailProvider.Name != "" {
		c.ProviderName = y.EmailProvider.Name
	}
	if y.AIAgent.Name != "" {
		c.AgentName = y.AIAgent.Name
	}
	if y.AIAgent.Workdir != "" {
		c.AgentWorkdir = y.AIAgent.Workdir
	}
	if y.AIAgent.PermissionMode != "" {
		c.AgentPermMode = y.AIAgent.PermissionMode
	}
	if len(y.AllowFrom) > 0 {
		c.AllowFrom = normalizeList(y.AllowFrom)
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

// warnDeprecated walks the yaml.Node tree (which is a DocumentNode at the
// root when parsed from raw bytes) and emits a slog.Warn for every mapping
// key whose name appears in `keys`. Nested mappings are scanned recursively.
func warnDeprecated(n *yaml.Node, keys []string) {
	if n == nil {
		return
	}
	keyset := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keyset[k] = struct{}{}
	}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil {
			return
		}
		if node.Kind == yaml.DocumentNode {
			walk(node.Content[0])
			return
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				k := node.Content[i]
				v := node.Content[i+1]
				if ks := k.Value; ks != "" {
					if _, hit := keyset[ks]; hit {
						slog.Warn("deprecated config key ignored; remove and use new schema",
							"key", ks, "replacement", replacement(ks))
					}
				}
				walk(v)
			}
			return
		}
		if node.Kind == yaml.SequenceNode {
			for _, c := range node.Content {
				walk(c)
			}
		}
	}
	walk(n)
}

// replacement maps each deprecated key to a one-line hint for the WARN log.
func replacement(k string) string {
	switch k {
	case "imap_addr", "smtp_addr":
		return "email_provider.name (163 / 126 / qq)"
	case "agent_bin":
		return "ai_agent.name (claude / nanopi / pi)"
	case "claude_workdir":
		return "ai_agent.workdir"
	case "claude_permission_mode":
		return "ai_agent.permission_mode"
	}
	return ""
}

// applyEnv overlays env vars onto c. This is the highest-precedence layer.
func applyEnv(c *Config) {
	if v := os.Getenv("AGENT_EMAIL"); v != "" {
		c.Email = v
	}
	if v := os.Getenv("AGENT_AUTH_CODE"); v != "" {
		c.AuthCode = v
	}
	if v := os.Getenv("ALLOW_FROM"); v != "" {
		c.AllowFrom = normalizeList(strings.Split(v, ","))
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