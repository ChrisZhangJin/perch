// Package config loads perch settings from three layers, in this order of
// precedence (higher overrides lower):
//
//  1. Environment variables (e.g. AGENT_EMAIL, POLL_INTERVAL).
//  2. A YAML file (path from --config / PERCH_CONFIG; default ./perch.yaml,
//     then ~/.perch/perch.yaml).
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
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime settings. Field names are the source of truth;
// YAML tags map the new email_provider / ai_agent blocks; env tags map the
// AGENT_* / ALLOW_FROM / *_INTERVAL / *_TIMEOUT / MAX_* / SESSION_STORE /
// TLS_INSECURE_SKIP_VERIFY / LOG_LEVEL names. The IMAP/SMTP endpoints and
// the agent binary are NOT here — they're resolved from cfg.ProviderName /
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
	LogLevel           string // "debug" | "info" | "warn" | "error"
	// LongTaskAck gates the two-stage inbound flow (classifier probe +
	// interim "processing, please wait" ack email for long tasks). Default
	// off: every long-task ack costs an extra agent invocation per email,
	// so opt-in only. See internal/app/classify.go for the contract.
	LongTaskAck bool
	// AgentTaskOnly injects a SAFETY PROTOCOL section into the agent prompt
	// telling the agent to refuse destructive side-requests that aren't the
	// email's stated task. Read-only inspection and any operation inside
	// cwd stay allowed. Default true — this is a light guardrail against
	// prompt-injection-style side asks; a truly hostile prompt still needs
	// the agent's own permission mode to block it.
	AgentTaskOnly bool
}

// yamlConfig mirrors Config with snake_case keys. Old-style endpoint / agent
// keys (imap_addr, smtp_addr, agent_bin, claude_workdir, claude_permission_mode)
// are intentionally NOT here — applyYAML detects them via a raw yaml.Node
// pass and emits a WARN (see Task 7). AuthCode is never read from disk.
type yamlConfig struct {
	Email         string `yaml:"email"`
	EmailProvider struct {
		Name string `yaml:"name"`
	} `yaml:"email_provider"`
	AIAgent struct {
		Name           string `yaml:"name"`
		Workdir        string `yaml:"workdir"`
		PermissionMode string `yaml:"permission_mode"`
		// TaskOnly is a pointer so we can tell "unset" (leave the default)
		// apart from "explicit false" (disable). The default is true, so a
		// non-pointer would swallow the user's `task_only: false`.
		TaskOnly *bool `yaml:"task_only"`
	} `yaml:"ai_agent"`
	AllowFrom          []string      `yaml:"allow_from"`
	PollInterval       time.Duration `yaml:"poll_interval"`
	TaskTimeout        time.Duration `yaml:"task_timeout"`
	MaxPromptBytes     int           `yaml:"max_prompt_bytes"`
	MaxAttachmentBytes int           `yaml:"max_attachment_bytes"`
	SessionStore       string        `yaml:"session_store"`
	TLSInsecure        bool          `yaml:"tls_insecure_skip_verify"`
	LogLevel           string        `yaml:"log_level"`
	LongTaskAck        bool          `yaml:"long_task_ack"`
}

// YAMLKeys returns every YAML key Load understands, nested keys in dotted
// form (e.g. "ai_agent.task_only"). Derived from yamlConfig's struct tags by
// reflection, so it cannot drift from what Load actually reads.
//
// This exists as a seam for the setup wizard's completeness test. The wizard
// promises to write a fully populated perch.yaml — every knob visible, even
// ones it never prompts for — and that promise is only checkable against the
// real key set. Adding a field to yamlConfig without teaching setup.persist
// to write it will fail that test.
func YAMLKeys() []string {
	return yamlKeysOf(reflect.TypeOf(yamlConfig{}), "")
}

// yamlKeysOf walks a struct's yaml tags, recursing into nested structs and
// prefixing their keys. time.Duration is an int64, not a struct, so it falls
// through to the leaf case like any other scalar.
func yamlKeysOf(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, yamlKeysOf(ft, prefix+name+".")...)
			continue
		}
		out = append(out, prefix+name)
	}
	return out
}

// Load reads the YAML file at cfgPath (or the default search path), layers
// env vars on top, then applies built-in defaults for anything still empty.
// cfgPath == "" means: search default locations.
//
// AuthCode is taken from the env only — it never comes from the YAML file.
// Email CAN be read from YAML (written there by the setup wizard) but env
// `AGENT_EMAIL` always overrides it.
func Load(cfgPath string) (*Config, error) {
	// 1. Built-in defaults.
	c := Defaults()

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

	// No required-field gate here. setup.Ensure owns the missing-creds policy:
	// it runs the wizard interactively, returns ErrMissingFields when
	// non-interactive + missing, and is a no-op when both fields are set.
	// Gating here would block first-time users from ever reaching the wizard.
	return c, nil
}

// Defaults returns the built-in fallback values. Anything zero here is
// overridden by YAML or env; this is the bottom of the stack. Exported
// so other packages (notably setup's persist) can render the same defaults
// into the wizard-written config file.
func Defaults() *Config {
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
		LogLevel:           "info",
		AgentTaskOnly:      true,
	}
}

// defaultConfigPaths returns the search order for the YAML file. The first
// existing file is used; if none exist, Load silently proceeds with defaults.
func defaultConfigPaths() []string {
	paths := []string{"./perch.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".perch", "perch.yaml"))
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
	if y.Email != "" {
		c.Email = y.Email
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
	if y.AIAgent.TaskOnly != nil {
		c.AgentTaskOnly = *y.AIAgent.TaskOnly
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
	if y.LogLevel != "" {
		c.LogLevel = y.LogLevel
	}
	if y.LongTaskAck {
		c.LongTaskAck = y.LongTaskAck
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
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("LONG_TASK_ACK"); v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			c.LongTaskAck = b
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

// ParseLogLevel maps a config.LogLevel string to a slog.Level. Unknown
// values fall back to slog.LevelInfo and return an explanatory error so the
// caller can WARN-log it without crashing on a typo.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q (want debug/info/warn/error); falling back to info", s)
}
