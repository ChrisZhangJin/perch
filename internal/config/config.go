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

	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/llm"
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
	// LogFile, when set, sends logs to this file instead of stderr. The file
	// is rotated once it reaches LogMaxSizeMB, keeping LogMaxBackups old
	// copies (path.1 newest … path.N oldest). LogMaxSizeMB 0 disables rotation.
	LogFile       string
	LogMaxSizeMB  int
	LogMaxBackups int
	// LongTaskAck gates the two-stage inbound flow (classifier probe +
	// interim "processing, please wait" ack email for long tasks). Default
	// off: every long-task ack costs an extra agent invocation per email,
	// so opt-in only. See internal/app/classify.go for the contract.
	LongTaskAck bool
	// Classifier picks who answers the LongTaskAck duration probe:
	//
	//	"agent" — DEFAULT. Spawn the coding agent in a throwaway session and
	//	          parse its sentinel line (internal/app/classify.go).
	//	"jev"   — ask TypeSafe AI's System One model two typed questions over
	//	          HTTP (internal/jev). One round trip instead of one agent
	//	          invocation, which is the whole cost objection to LongTaskAck.
	//
	// "jev" degrades to "agent" on every failure — no key, transport error,
	// timeout, unrecognised answer — so setting it can slow the probe down
	// but can never stop the ack path working. Ignored when LongTaskAck is
	// off, since nothing calls the classifier then.
	Classifier string
	// JevAPIKey authenticates to TypeSafe AI. Env-only (TYPESAFE_API_KEY):
	// there is deliberately no YAML key, because perch.yaml is written by
	// the setup wizard, is not gitignored, and operators copy it around.
	// Empty means jev.New returns nil and the classifier stays on "agent".
	JevAPIKey string
	// JevAPIURL is the System One endpoint. Overridable only so tests can
	// point at an httptest server; in production leave it alone.
	JevAPIURL string
	// JevModel is the pinned model version. Pinned, not "jev-latest": the
	// alias moves under the prompt without warning. See internal/jev.
	JevModel string
	// JevTimeout bounds one classify call. Short (10s) on purpose — the
	// probe runs inline while ProcessUnseen holds the app lock, so a slow
	// call stalls every email behind it, and the agent fallback is the
	// faster path by then.
	JevTimeout time.Duration
	// EndDetect asks Jev whether an inbound thread continuation is just a
	// closing ("thanks, got it", "好的，谢谢") and, if so, leaves it
	// unanswered instead of spending an agent run on it.
	//
	// Default off, and opt-in for a reason: a false positive silently drops
	// a real request and nobody ever finds out, whereas a false negative
	// only costs one unnecessary reply. Requires a key; inert without one.
	// See internal/app/enddetect.go for the full set of guards.
	EndDetect bool
	// EndDetectMinProb is how sure Jev must be before perch stays silent —
	// the calibrated probability of "the thread is over", not the vendor's
	// `confidence` field and not merely "the model picked over". 0.9 by
	// default because of the asymmetry above; lower it only with evidence.
	EndDetectMinProb float64
	// LLM* configure the chat-model alternative to Jev (internal/llm). It is
	// asked whenever Jev is unconfigured or fails, for both end_detect and
	// the duration probe — leave TYPESAFE_API_KEY unset and it is the
	// replacement rather than the fallback. Built only when the key, base
	// URL and model are all set.
	//
	// LLMFormat is the wire format: "openai" (DEFAULT; /chat/completions,
	// DeepSeek, Ark, DashScope, MiMo) or "anthropic" (/v1/messages).
	LLMFormat  string
	LLMBaseURL string
	LLMModel   string
	LLMTimeout time.Duration
	// LLMMaxTokens is sent as max_tokens. Reasoning models count their
	// thinking against it, so it must be large; DEFAULT llm.DefaultMaxTokens.
	LLMMaxTokens int
	// LLMAPIKey is env-only (PERCH_LLM_API_KEY), same rule as JevAPIKey.
	LLMAPIKey string
	// AgentTaskOnly injects a SAFETY PROTOCOL section into the agent prompt
	// telling the agent to refuse destructive side-requests that aren't the
	// email's stated task. Read-only inspection and any operation inside
	// cwd stay allowed. Default true — this is a light guardrail against
	// prompt-injection-style side asks; a truly hostile prompt still needs
	// the agent's own permission mode to block it.
	AgentTaskOnly bool

	// SkipAutomated drops inbound mail that announces itself as machine
	// generated (RFC 3834 Auto-Submitted, Precedence: bulk/list/junk,
	// List-* headers) instead of running the agent on it. Default true.
	//
	// Turn it off only if something you WANT perch to answer sets those
	// headers — a monitoring system or CI job mailing in a task legitimately
	// sets Auto-Submitted: auto-generated.
	SkipAutomated bool
	// MaxRepliesPerHour caps replies per email thread per rolling hour.
	// 0 disables the cap.
	//
	// This is the backstop for the case the header checks cannot see: a robot
	// that sets no headers at all. Two such robots reply to each other
	// forever, each one burning an agent invocation per round. A human does
	// not round-trip one thread ten times an hour; a loop does it in minutes.
	MaxRepliesPerHour int
	// PeerAgents lists addresses of other mail agents (typically other perch
	// instances) that perch is meant to work with. Their mail is answered
	// even though it carries Auto-Submitted — which every perch reply does —
	// so two agents can hand work back and forth. Lowercased, exact match.
	//
	// What stops such a conversation is end_detect (a closing gets no reply)
	// and, as the hard backstop, MaxRepliesPerHour. Both stay in force.
	PeerAgents []string

	// AppendSystemPrompt is text layered onto the agent's own system prompt
	// on every run (--append-system-prompt), for a standing role the agent
	// keeps across emails and sessions: "you are the support desk, the task
	// definitions live in ./tasks, never invent policy".
	//
	// The value is text-or-path, matching what pi's flag accepts: a
	// single-line value naming a readable file is read as a file (and
	// re-read on every email, so edits need no restart), anything else is
	// used literally. Empty disables the flag.
	//
	// Agents that have not shipped the flag are detected at startup and the
	// value is ignored with a WARN rather than failing every email. See
	// runner.SystemPrompt and agent.SupportsAppendSystemPrompt.
	AppendSystemPrompt string
	// OnEmailHook is the path to a script perch runs for every inbound email
	// it accepts, before the agent runs. Empty (the default) disables it.
	//
	// The script is called with positional arguments:
	//
	//	<email_id> <subject> <body> <sender> <new_thread|reply_thread>
	//
	// email_id is the Message-Id header, empty for mail that has none — the
	// argument is passed anyway so later positions never shift. The hook is an
	// observer, never a gate: its exit status is logged and otherwise ignored,
	// and it only ever sees mail that already cleared the whitelist and the
	// loop guards. See internal/hook.
	OnEmailHook string
	// HookTimeout bounds a single hook run (SIGTERM, then SIGKILL after 5s).
	// Default 30s: long enough for a script that writes a file or curls a
	// local endpoint, short enough that a hung hook doesn't stall the email
	// queued behind it.
	HookTimeout time.Duration
}

// Duration-probe backends. See Config.Classifier.
const (
	ClassifierAgent = "agent"
	ClassifierJev   = "jev"
)

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
		TaskOnly           *bool  `yaml:"task_only"`
		AppendSystemPrompt string `yaml:"append_system_prompt"`
	} `yaml:"ai_agent"`
	LoopGuard struct {
		// Pointer so an explicit `false` is distinguishable from "unset",
		// which must keep the default of true.
		SkipAutomated     *bool    `yaml:"skip_automated"`
		MaxRepliesPerHour *int     `yaml:"max_replies_per_hour"`
		PeerAgents        []string `yaml:"peer_agents"`
	} `yaml:"loop_guard"`
	Hooks struct {
		OnEmail string `yaml:"on_email"`
		// Timeout is a pointer so an explicit 0 (or a negative) is
		// distinguishable from "unset" and can be rejected with a WARN
		// instead of silently meaning "no timeout".
		Timeout *time.Duration `yaml:"timeout"`
	} `yaml:"hooks"`
	AllowFrom          []string      `yaml:"allow_from"`
	PollInterval       time.Duration `yaml:"poll_interval"`
	TaskTimeout        time.Duration `yaml:"task_timeout"`
	MaxPromptBytes     int           `yaml:"max_prompt_bytes"`
	MaxAttachmentBytes int           `yaml:"max_attachment_bytes"`
	SessionStore       string        `yaml:"session_store"`
	TLSInsecure        bool          `yaml:"tls_insecure_skip_verify"`
	LogLevel           string        `yaml:"log_level"`
	Log                struct {
		Level      string `yaml:"level"`
		File       string `yaml:"file"`
		MaxSizeMB  *int   `yaml:"max_size_mb"`
		MaxBackups *int   `yaml:"max_backups"`
	} `yaml:"log"`
	LongTaskAck      bool          `yaml:"long_task_ack"`
	Classifier       string        `yaml:"classifier"`
	JevAPIURL        string        `yaml:"jev_api_url"`
	JevModel         string        `yaml:"jev_model"`
	JevTimeout       time.Duration `yaml:"jev_timeout"`
	EndDetect        bool          `yaml:"end_detect"`
	EndDetectMinProb float64       `yaml:"end_detect_min_prob"`
	LLM              struct {
		Format    string        `yaml:"format"`
		BaseURL   string        `yaml:"base_url"`
		Model     string        `yaml:"model"`
		Timeout   time.Duration `yaml:"timeout"`
		MaxTokens TokenCount    `yaml:"max_tokens"`
		// No api_key: env-only (PERCH_LLM_API_KEY).
	} `yaml:"llm"`
	// No jev_api_key here on purpose — the key is env-only
	// (TYPESAFE_API_KEY). See Config.JevAPIKey.
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
		LogMaxSizeMB:       100,
		LogMaxBackups:      5,
		AgentTaskOnly:      true,
		SkipAutomated:      true,
		MaxRepliesPerHour:  10,
		HookTimeout:        30 * time.Second,
		Classifier:         ClassifierAgent,
		JevAPIURL:          jev.DefaultAPIURL,
		JevModel:           jev.DefaultModel,
		JevTimeout:         jev.DefaultTimeout,
		EndDetectMinProb:   0.9,
		LLMFormat:          llm.FormatOpenAI,
		LLMTimeout:         llm.DefaultTimeout,
		LLMMaxTokens:       llm.DefaultMaxTokens,
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
		"prompt",
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
	// Not TrimSpace'd: a leading newline is normal for a YAML block scalar,
	// and trailing whitespace in a role definition is harmless. Only an
	// all-whitespace value counts as unset.
	if strings.TrimSpace(y.AIAgent.AppendSystemPrompt) != "" {
		c.AppendSystemPrompt = y.AIAgent.AppendSystemPrompt
	}
	if v := y.LoopGuard.SkipAutomated; v != nil {
		c.SkipAutomated = *v
	}
	if v := y.LoopGuard.MaxRepliesPerHour; v != nil {
		if *v < 0 {
			slog.Warn("loop_guard.max_replies_per_hour must not be negative; using default",
				"value", *v, "using", Defaults().MaxRepliesPerHour)
		} else {
			c.MaxRepliesPerHour = *v
		}
	}
	for _, v := range y.LoopGuard.PeerAgents {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			c.PeerAgents = append(c.PeerAgents, v)
		}
	}
	if v := strings.TrimSpace(y.Hooks.OnEmail); v != "" {
		c.OnEmailHook = v
	}
	if v := y.Hooks.Timeout; v != nil {
		if *v <= 0 {
			// A zero/negative timeout would mean "wait forever", which is the
			// one thing this knob exists to prevent — a hung hook holding the
			// mail loop. Keep the default and say so.
			slog.Warn("hooks.timeout must be positive; using default",
				"value", v.String(), "using", Defaults().HookTimeout.String())
		} else {
			c.HookTimeout = *v
		}
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
	// log.level wins over the legacy top-level log_level.
	if v := strings.TrimSpace(y.Log.Level); v != "" {
		c.LogLevel = v
	}
	if v := strings.TrimSpace(y.Log.File); v != "" {
		c.LogFile = expandHome(v)
	}
	if v := y.Log.MaxSizeMB; v != nil {
		c.LogMaxSizeMB = *v
	}
	if v := y.Log.MaxBackups; v != nil {
		c.LogMaxBackups = *v
	}
	if y.LongTaskAck {
		c.LongTaskAck = y.LongTaskAck
	}
	if v := strings.ToLower(strings.TrimSpace(y.Classifier)); v != "" {
		switch v {
		case ClassifierAgent, ClassifierJev:
			c.Classifier = v
		default:
			slog.Warn("classifier must be agent or jev; using current value",
				"value", y.Classifier, "using", c.Classifier)
		}
	}
	if y.JevAPIURL != "" {
		c.JevAPIURL = strings.TrimSpace(y.JevAPIURL)
	}
	if v := strings.TrimSpace(y.JevModel); v != "" {
		// A moving alias is the one value that must not be honoured
		// silently: it would swap the model out from under the rubric in
		// internal/app/classify.go with no signal anywhere. See internal/jev.
		if strings.EqualFold(v, "jev-latest") {
			slog.Warn("jev_model must be a pinned version, not a moving alias; using current value",
				"value", v, "using", c.JevModel)
		} else {
			c.JevModel = v
		}
	}
	if y.JevTimeout > 0 {
		c.JevTimeout = y.JevTimeout
	}
	if y.EndDetect {
		c.EndDetect = y.EndDetect
	}
	// A threshold of 0 would mean "drop every continuation", which nobody
	// can have meant, so a zero is read as unset rather than honoured —
	// same treatment as max_replies_per_hour. Anything above 1 is
	// unreachable and would silently disable the feature instead.
	if y.EndDetectMinProb != 0 {
		if y.EndDetectMinProb > 0 && y.EndDetectMinProb <= 1 {
			c.EndDetectMinProb = y.EndDetectMinProb
		} else {
			slog.Warn("end_detect_min_prob must be in (0,1]; using current value",
				"value", y.EndDetectMinProb, "using", c.EndDetectMinProb)
		}
	}
	if v := strings.ToLower(strings.TrimSpace(y.LLM.Format)); v != "" {
		switch v {
		case llm.FormatOpenAI, llm.FormatAnthropic:
			c.LLMFormat = v
		default:
			slog.Warn("llm.format must be openai or anthropic; using current value",
				"value", y.LLM.Format, "using", c.LLMFormat)
		}
	}
	if v := strings.TrimSpace(y.LLM.BaseURL); v != "" {
		c.LLMBaseURL = v
	}
	if v := strings.TrimSpace(y.LLM.Model); v != "" {
		c.LLMModel = v
	}
	if y.LLM.Timeout > 0 {
		c.LLMTimeout = y.LLM.Timeout
	}
	if y.LLM.MaxTokens != 0 {
		if y.LLM.MaxTokens > 0 {
			c.LLMMaxTokens = int(y.LLM.MaxTokens)
		} else {
			slog.Warn("llm.max_tokens must be positive; using current value",
				"value", y.LLM.MaxTokens, "using", c.LLMMaxTokens)
		}
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
	case "prompt":
		return "none; prompt.contracts and prompt.strip_quoted are now fixed: full contracts and quoted history on a new session, compact contracts and no quoted history on a resumed one"
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
	if v := os.Getenv("LOG_FILE"); strings.TrimSpace(v) != "" {
		c.LogFile = expandHome(strings.TrimSpace(v))
	}
	if v := os.Getenv("LOG_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			c.LogMaxSizeMB = n
		}
	}
	if v := os.Getenv("LOG_MAX_BACKUPS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			c.LogMaxBackups = n
		}
	}
	if v := os.Getenv("APPEND_SYSTEM_PROMPT"); strings.TrimSpace(v) != "" {
		c.AppendSystemPrompt = v
	}
	if v := os.Getenv("ON_EMAIL_HOOK"); v != "" {
		c.OnEmailHook = strings.TrimSpace(v)
	}
	if v := os.Getenv("HOOK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
			c.HookTimeout = d
		} else {
			slog.Warn("HOOK_TIMEOUT must be a positive duration; using current value",
				"value", v, "using", c.HookTimeout.String())
		}
	}
	if v := os.Getenv("LONG_TASK_ACK"); v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			c.LongTaskAck = b
		}
	}
	// Env-only, no YAML counterpart: perch.yaml is wizard-written, not
	// gitignored, and gets copied between machines. Secrets stay out of it,
	// same rule as AGENT_AUTH_CODE. Never logged — not even its length.
	if v := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); v != "" {
		c.JevAPIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("PERCH_LLM_API_KEY")); v != "" {
		c.LLMAPIKey = v
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

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
