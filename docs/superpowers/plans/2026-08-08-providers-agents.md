# Built-in Providers and Agents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace free-form `imap_addr` / `smtp_addr` / `agent_bin` / `claude_workdir` / `claude_permission_mode` strings in `perch.yaml` with a built-in registry of named email providers (163, 126, qq) and AI agents (claude, nanopi, pi); add a first-run interactive wizard that writes non-secret fields to `~/.config/perch/perch.yaml` with `0600` permissions.

**Architecture:** Three new packages (`internal/provider`, `internal/agent`, `internal/setup`) decouple endpoint resolution from config. `internal/config` becomes pure data (no endpoints). `cmd/perch/main.go` orchestrates: load config → resolve provider + agent → dial mailbox / build runner → call `setup.Ensure` → run. Backward-compat: old YAML keys trigger a one-line WARN and are ignored.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, `golang.org/x/term` (new dep for password input), existing `log/slog` for warnings.

## Global Constraints

- TDD throughout: every task starts with a failing test, then implementation, then refactor.
- Each task ends with a single `git commit` (or skip if the repo state is dirty from prior tasks — see task headers).
- All package additions must compile standalone (`go build ./...` green) before moving on.
- Run `go test ./... -count=1` after every task. Tree must stay green.
- No new third-party deps except `golang.org/x/term` (added in Task 6).
- Provider name ↔ endpoint values MUST be exactly as in the spec table — copy verbatim.
- Agent arg vectors MUST exactly match the spec's "Adapter table" — byte-for-byte.
- Config-layer precedence stays `env > yaml > defaults`.
- Deprecated keys (`imap_addr` / `smtp_addr` / `agent_bin` / `claude_workdir` / `claude_permission_mode`) emit a single WARN per key on startup, then are ignored.
- Task 6 (wizard) and Tasks 1/2 can run in parallel — they touch independent packages.
- Order: 1, 2, 6 first (parallel-friendly). Then 3, then 5, then 4 (because runner needs new config types, and main wires them). Then 7 (deprecation). Then 8 (docs).

---

### Task 1: `internal/provider` package (registry + Lookup) [parallelizable]

**Files:**
- Create: `internal/provider/registry.go`
- Create: `internal/provider/registry_test.go`

**Interfaces:**
- Consumes: a string name (`"163"` / `"126"` / `"qq"`)
- Produces: `Provider{ Name, IMAPAddr, SMTPAddr, NeedsIMAPID, SupportsIDLE }` and `error` for unknown names.

- [ ] Step 1: Write the failing test

`internal/provider/registry_test.go`:

```go
package provider

import (
	"strings"
	"testing"
)

func TestProviderTableConsistent(t *testing.T) {
	for name, p := range providers {
		if p.Name != name {
			t.Errorf("map key %q != provider.Name %q", name, p.Name)
		}
	}
}

func TestProviderEndpoints(t *testing.T) {
	cases := []struct {
		name, imap, smtp            string
		needsIMAPID, supportsIDLE   bool
	}{
		{"163", "imap.163.com:993", "smtp.163.com:465", true, false},
		{"126", "imap.126.com:993", "smtp.126.com:465", true, false},
		{"qq", "imap.qq.com:993", "smtp.qq.com:465", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Lookup(c.name)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", c.name, err)
			}
			if got.IMAPAddr != c.imap {
				t.Errorf("IMAPAddr = %q, want %q", got.IMAPAddr, c.imap)
			}
			if got.SMTPAddr != c.smtp {
				t.Errorf("SMTPAddr = %q, want %q", got.SMTPAddr, c.smtp)
			}
			if got.NeedsIMAPID != c.needsIMAPID {
				t.Errorf("NeedsIMAPID = %v, want %v", got.NeedsIMAPID, c.needsIMAPID)
			}
			if got.SupportsIDLE != c.supportsIDLE {
				t.Errorf("SupportsIDLE = %v, want %v", got.SupportsIDLE, c.supportsIDLE)
			}
		})
	}
}

func TestProviderLookupUnknown(t *testing.T) {
	_, err := Lookup("gmail")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "163") || !strings.Contains(err.Error(), "126") || !strings.Contains(err.Error(), "qq") {
		t.Errorf("error should list valid names, got: %v", err)
	}
}
```

- [ ] Step 2: Run it

```
go test ./internal/provider -run TestProvider -count=1
```

Expected: FAIL — `provider: No such file or directory`

- [ ] Step 3: Implement

`internal/provider/registry.go`:

```go
// Package provider names the email transports perch supports and ships their
// endpoints + per-provider transport quirks so the rest of the codebase does
// not hard-code IMAP/SMTP addresses.
package provider

import "fmt"

// Provider is a built-in email transport.
type Provider struct {
	Name         string
	IMAPAddr     string // host:port for implicit TLS IMAP
	SMTPAddr     string // host:port for implicit TLS SMTP
	NeedsIMAPID  bool   // 163/126/qq all require IMAP ID before login
	SupportsIDLE bool   // 163/126 do not advertise IDLE
}

// providers is the built-in table. Order is stable; Lookup preserves the
// insertion order in its error message.
var providers = map[string]Provider{
	"163": {Name: "163", IMAPAddr: "imap.163.com:993", SMTPAddr: "smtp.163.com:465", NeedsIMAPID: true, SupportsIDLE: false},
	"126": {Name: "126", IMAPAddr: "imap.126.com:993", SMTPAddr: "smtp.126.com:465", NeedsIMAPID: true, SupportsIDLE: false},
	"qq":  {Name: "qq", IMAPAddr: "imap.qq.com:993", SMTPAddr: "smtp.qq.com:465", NeedsIMAPID: true, SupportsIDLE: true},
}

// Lookup resolves a provider name to its built-in transport. Unknown names
// return an error listing the valid choices.
func Lookup(name string) (Provider, error) {
	if p, ok := providers[name]; ok {
		return p, nil
	}
	return Provider{}, fmt.Errorf("unknown email provider %q (valid: 163, 126, qq)", name)
}
```

- [ ] Step 4: Run it

```
go test ./internal/provider -count=1
```

Expected: PASS

- [ ] Step 5: Commit

```
git add internal/provider
git commit -m "feat(provider): add built-in provider registry

163/126/qq endpoints baked in; Lookup returns error listing valid
names for anything else. No transport behaviour change yet."
```

---

### Task 2: `internal/agent` package (registry + BuildArgs adapters) [parallelizable]

**Files:**
- Create: `internal/agent/registry.go`
- Create: `internal/agent/adapters.go`
- Create: `internal/agent/registry_test.go`

**Interfaces:**
- Consumes: `Args{ Prompt, SessionID, IsNew, Workdir, PermMode }`
- Produces: `[]string` argv; `Lookup(name) (Agent, error)` for unknown names.

- [ ] Step 1: Write the failing test

`internal/agent/registry_test.go`:

```go
package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentBuildArgs_ClaudeNew(t *testing.T) {
	a, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "11111111-1111-4111-8111-111111111111",
		IsNew: true, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--session-id", "11111111-1111-4111-8111-111111111111"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_ClaudeResume(t *testing.T) {
	a, _ := Lookup("claude")
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "22222222-2222-4222-8222-222222222222",
		IsNew: false, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--resume", "22222222-2222-4222-8222-222222222222"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiNew(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w"})
	want := []string{"-p", "p", "--yolo", "--output", "text", "--session", "sid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiResume(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--yolo", "--output", "text", "--session", "sid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_PiNew(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-new", IsNew: true, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session-id", "sid-new"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_PiResume(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-old", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session", "sid-old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi resume = %v, want %v", got, want)
	}
}

func TestAgentLookupUnknown(t *testing.T) {
	_, err := Lookup("not-an-agent")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should list valid names, got: %v", err)
	}
}
```

- [ ] Step 2: Run it

```
go test ./internal/agent -run TestAgent -count=1
```

Expected: FAIL — package does not exist

- [ ] Step 3: Implement

`internal/agent/registry.go`:

```go
// Package agent names the AI agents perch can spawn and ships each one's
// argv adapter so the runner does not hard-code Claude flags.
package agent

import "fmt"

// Args is everything the runner passes to an agent. Workdir is informational
// here (runner sets cmd.Dir); PermMode is claude-only and ignored by nanopi/pi.
type Args struct {
	Prompt    string
	SessionID string
	IsNew     bool // true => create; false => resume
	Workdir   string
	PermMode  string
}

// Agent is a built-in CLI runner.
type Agent struct {
	Name      string
	Binary    string
	BuildArgs func(Args) []string
}

// agents is the built-in table. Order is stable; Lookup's error preserves it.
var agents = map[string]Agent{
	"claude": {Name: "claude", Binary: "claude", BuildArgs: buildClaudeArgs},
	"nanopi": {Name: "nanopi", Binary: "nanopi", BuildArgs: buildNanopiArgs},
	"pi":     {Name: "pi", Binary: "pi", BuildArgs: buildPiArgs},
}

// Lookup resolves an agent name to its built-in adapter. Unknown names return
// an error listing the valid choices.
func Lookup(name string) (Agent, error) {
	if a, ok := agents[name]; ok {
		return a, nil
	}
	return Agent{}, fmt.Errorf("unknown AI agent %q (valid: claude, nanopi, pi)", name)
}
```

`internal/agent/adapters.go`:

```go
package agent

// buildClaudeArgs preserves today's behaviour exactly: --session-id on first
// run, --resume on subsequent runs. PermMode is required for claude.
func buildClaudeArgs(a Args) []string {
	out := []string{"-p", a.Prompt, "--output-format", "text", "--permission-mode", a.PermMode}
	if a.IsNew {
		out = append(out, "--session-id", a.SessionID)
	} else {
		out = append(out, "--resume", a.SessionID)
	}
	return out
}

// buildNanopiArgs uses --session <sid> for both new and resume (per spec);
// IsNew is ignored. nanopi has no permission-mode flag.
func buildNanopiArgs(a Args) []string {
	return []string{"-p", a.Prompt, "--yolo", "--output", "text", "--session", a.SessionID}
}

// buildPiArgs distinguishes new (--session-id) vs resume (--session). PermMode
// is not part of pi's CLI; the --mode flag controls output format only.
func buildPiArgs(a Args) []string {
	if a.IsNew {
		return []string{"-p", a.Prompt, "--mode", "text", "--session-id", a.SessionID}
	}
	return []string{"-p", a.Prompt, "--mode", "text", "--session", a.SessionID}
}
```

- [ ] Step 4: Run it

```
go test ./internal/agent -count=1
```

Expected: PASS

- [ ] Step 5: Commit

```
git add internal/agent
git commit -m "feat(agent): add built-in agent registry with BuildArgs adapters

claude/nanopi/pi argv vectors baked in per spec. runner still
hard-codes Claude flags; refactor is a follow-up."
```

---

### Task 3: Config refactor — remove endpoint fields, add name fields [parallelizable after Task 1/2/6 land]

**Files:**
- Modify: `internal/config/config.go:30-45` (Config struct)
- Modify: `internal/config/config.go:49-62` (yamlConfig struct)
- Modify: `internal/config/config.go:103-116` (defaults)
- Modify: `internal/config/config.go:142-191` (applyYAML)
- Modify: `internal/config/config.go:194-247` (applyEnv — drop CLAUDE_*/IMAP_ADDR/SMTP_ADDR)
- Modify: `internal/config/config.go:9-12` (package doc)
- Modify: `internal/config/config_test.go` (replace IMAPAddr/SMTPAddr/ClaudeBin assertions)

**Interfaces:**
- Config gains `ProviderName`, `AgentName`, `AgentWorkdir`, `AgentPermMode`.
- Config loses `IMAPAddr`, `SMTPAddr`, `ClaudeBin`, `ClaudeWorkdir`, `ClaudePermMode`.
- yamlConfig gains `EmailProvider struct{Name string} yaml:"email_provider"` and `AIAgent struct{Name, Workdir, PermissionMode string} yaml:"ai_agent"`.
- yamlConfig loses `IMAPAddr`/`SMTPAddr`/`AgentBin`/`AgentWorkdir`/`AgentPermissionMode`.
- applyEnv drops `IMAP_ADDR`, `SMTP_ADDR`, `CLAUDE_BIN`, `CLAUDE_WORKDIR`, `CLAUDE_PERMISSION_MODE`.

- [ ] Step 1: Write the failing test (update existing config_test.go)

Replace the contents of `internal/config/config_test.go` with:

```go
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

func TestLoadMissingRequiredFails(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("AGENT_EMAIL", "")
	t.Setenv("AGENT_AUTH_CODE", "")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when required env missing")
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
```

- [ ] Step 2: Run it

```
go test ./internal/config -count=1
```

Expected: FAIL — `cfg.IMAPAddr` / `cfg.ClaudeBin` etc. no longer exist; tests won't compile.

- [ ] Step 3: Implement — refactor `internal/config/config.go`

Replace the entire file with:

```go
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
```

- [ ] Step 4: Run it

```
go build ./... && go test ./internal/config -count=1
```

Expected: BUILD FAIL — runner.go, replier.go, mailbox.go still reference removed fields. That's expected (Tasks 4-5 fix it). For now, just confirm config tests PASS:

```
go test ./internal/config -count=1
```

Expected: PASS

- [ ] Step 5: Commit (the broken build is OK — runner/replier fix comes in Tasks 4-5)

```
git add internal/config
git commit -m "refactor(config): replace endpoints with provider/agent names

Config drops IMAPAddr/SMTPAddr/ClaudeBin/ClaudeWorkdir/ClaudePermMode
and gains ProviderName/AgentName/AgentWorkdir/AgentPermMode. yamlConfig
gains email_provider/ai_agent blocks; old keys are detected via raw
yaml.Node pass and emit a WARN (separate commit). runner/replier/main
updates follow."
```

---

### Task 6: `internal/setup` package — first-run wizard [parallelizable, MUST land before Task 7]

**Files:**
- Create: `internal/setup/wizard.go`
- Create: `internal/setup/wizard_test.go`
- Modify: `go.mod` (add `golang.org/x/term`)
- Modify: `go.sum`

**Interfaces:**
- `Ensure(cfg *config.Config, stdin io.Reader, stdout io.Writer, stderr io.Writer, passwordFn func(fd int) ([]byte, error)) error`
- On success or all-fields-populated, returns nil.
- Returns `ErrMissingFields` (sentinel) when fields are missing AND stdin is not a TTY.
- Persists non-secret fields to `~/.config/perch/perch.yaml` with mode `0600`.

**Subtask 6a — `Ensure` decision tree + non-interactive failure (no stdin I/O yet)**

- [ ] Step 6a.1: Write the failing test

`internal/setup/wizard_test.go`:

```go
package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/config"
)

func TestEnsureSilentWhenComplete(t *testing.T) {
	cfg := &config.Config{
		Email:         "agent@163.com",
		AuthCode:      "secret",
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		AllowFrom:     []string{"alice@163.com"},
	}
	// No I/O expected — pass buffers and a stub password fn that must not be called.
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	pwCalled := false
	pw := func(fd int) ([]byte, error) { pwCalled = true; return nil, nil }
	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if pwCalled {
		t.Error("password fn should not be called when cfg is complete")
	}
	if out.Len() != 0 {
		t.Errorf("no output expected, got: %q", out.String())
	}
}

func TestEnsureNonInteractiveFailsLoud(t *testing.T) {
	cfg := &config.Config{
		Email:         "agent@163.com",
		AuthCode:      "", // missing
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		// AllowFrom also missing
	}
	in := &bytes.Buffer{} // empty => "not a TTY" path
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	err := Ensure(cfg, in, out, errOut, func(int) ([]byte, error) { return nil, nil })
	if !errors.Is(err, ErrMissingFields) {
		t.Fatalf("expected ErrMissingFields, got %v", err)
	}
	msg := errOut.String()
	for _, want := range []string{"authcode", "allow_from"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("error output missing %q: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "AGENT_EMAIL") {
		t.Errorf("error should mention AGENT_EMAIL env var: %s", msg)
	}
}

func TestEnsureInteractiveHappyPath(t *testing.T) {
	cfg := &config.Config{
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
		// Email, AuthCode, AllowFrom — wizard fills these.
	}
	// Answers in order: email, authcode (via pw fn), allow_from literal.
	// wizard will call readLine for: provider (default 163, hit enter), agent (claude, enter),
	// workdir (., enter), allow_from ("bob@qq.com"), email ("agent@qq.com")
	script := "\n\n\nbob@qq.com\nagent@qq.com\n"
	in := strings.NewReader(script)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	var pwCount int
	pw := func(fd int) ([]byte, error) {
		pwCount++
		return []byte("supersecret"), nil
	}
	// HOME override so we write to a temp config dir, not the user's real one.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v\nstderr:\n%s", err, errOut.String())
	}
	if pwCount != 1 {
		t.Errorf("password fn called %d times, want 1", pwCount)
	}
	if cfg.Email != "agent@qq.com" {
		t.Errorf("Email = %q", cfg.Email)
	}
	if cfg.AuthCode != "supersecret" {
		t.Errorf("AuthCode = %q", cfg.AuthCode)
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "bob@qq.com" {
		t.Errorf("AllowFrom = %#v", cfg.AllowFrom)
	}

	// Persisted file should exist with 0600 mode.
	cfgPath := filepath.Join(tmpHome, ".config", "perch", "perch.yaml")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("expected persisted config at %s: %v", cfgPath, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("persisted file mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "email_provider") {
		t.Errorf("persisted file missing email_provider:\n%s", data)
	}
}

func TestEnsureInteractiveAuthcodeNotEchoed(t *testing.T) {
	cfg := &config.Config{} // everything missing => wizard runs
	in := strings.NewReader("163\nclaude\n.\nalice@x\nagent@x\n")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	var usedReadPassword bool
	pw := func(fd int) ([]byte, error) {
		usedReadPassword = true
		return []byte("authcode-value"), nil
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := Ensure(cfg, in, out, errOut, pw); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !usedReadPassword {
		t.Error("wizard must use the injected password function (ReadPassword-style), not Fscanln, for the auth code")
	}
}
```

- [ ] Step 6a.2: Run it

```
go test ./internal/setup -count=1
```

Expected: FAIL — package does not exist

- [ ] Step 6a.3: Implement — `internal/setup/wizard.go`

```go
// Package setup runs the first-run interactive wizard. Ensure decides
// whether to prompt based on whether required fields are already populated;
// it never prompts twice in a row if a config file already exists.
package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ChrisZhangJin/perch/internal/config"
)

// ErrMissingFields is returned by Ensure when required fields are not
// populated and stdin is not interactive. Callers should print stderr +
// exit non-zero (typically 2).
var ErrMissingFields = errors.New("missing required fields")

// PasswordFn reads a password from the given file descriptor without echoing.
// In production this wraps golang.org/x/term.ReadPassword; tests pass a stub.
type PasswordFn func(fd int) ([]byte, error)

// Ensure fills any missing cfg fields. If all required fields are already
// populated, it returns nil silently. If stdin is non-interactive and fields
// are missing, it writes a hint to stderr and returns ErrMissingFields.
// Otherwise it runs the wizard, persists non-secret fields to
// ~/.config/perch/perch.yaml with mode 0600, and updates cfg in place.
func Ensure(cfg *config.Config, stdin io.Reader, stdout io.Writer, stderr io.Writer, pw PasswordFn) error {
	missing := missingFields(cfg)
	if len(missing) == 0 {
		return nil
	}
	// Non-interactive path: cannot prompt; emit hint and error.
	if !isInteractive(stdin) {
		fmt.Fprintf(stderr, "missing required fields: %s\n", strings.Join(missing, ", "))
		fmt.Fprintln(stderr, "hint: run interactively (./perch) or pre-fill via env vars:")
		fmt.Fprintln(stderr, "  AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=xxxxxxxx ./perch")
		fmt.Fprintln(stderr, "  or edit ~/.config/perch/perch.yaml")
		return ErrMissingFields
	}
	return runWizard(cfg, stdin, stdout, pw)
}

// missingFields returns the cfg field names that still need to be populated.
func missingFields(cfg *config.Config) []string {
	var m []string
	if cfg.Email == "" {
		m = append(m, "email")
	}
	if cfg.AuthCode == "" {
		m = append(m, "authcode")
	}
	if cfg.ProviderName == "" {
		m = append(m, "provider.name")
	}
	if cfg.AgentName == "" {
		m = append(m, "agent.name")
	}
	if cfg.AgentWorkdir == "" {
		m = append(m, "agent.workdir")
	}
	if cfg.AgentPermMode == "" {
		m = append(m, "agent.permission_mode")
	}
	if len(cfg.AllowFrom) == 0 {
		m = append(m, "allow_from")
	}
	return m
}

// isInteractive reports whether stdin looks like a TTY. We can't call
// golang.org/x/term.IsTerminal here without leaking that dep through tests
// that pass a bytes.Buffer — so we approximate: stdin is a *os.File whose
// device is a character device. Everything else (bytes.Buffer, strings.Reader,
// pipes in some CI envs) is treated as non-interactive.
func isInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// runWizard prompts for each missing field in order, with bracketed defaults.
// Plain inputs use Fscanln; the auth code uses the injected PasswordFn so
// it does not echo. Non-secret fields are persisted to ~/.config/perch/perch.yaml.
func runWizard(cfg *config.Config, in io.Reader, out io.Writer, pw PasswordFn) error {
	fmt.Fprintln(out, "Welcome to perch.")
	fmt.Fprintln(out, "Press Enter to accept the default shown in [brackets]; type to override.")
	fmt.Fprintln(out)

	cfg.ProviderName = prompt(in, out, "Email provider", cfg.ProviderName, "163 / 126 / qq")
	cfg.AgentName = prompt(in, out, "AI agent", cfg.AgentName, "claude / nanopi / pi")
	cfg.AgentWorkdir = prompt(in, out, "Agent workdir", cfg.AgentWorkdir, ".")
	cfg.AgentPermMode = prompt(in, out, "Agent permission mode (claude only)", cfg.AgentPermMode, "acceptEdits")
	allowRaw := prompt(in, out, "Allow senders (comma-separated, empty = deny all)", joinList(cfg.AllowFrom), "")
	if allowRaw != "" {
		cfg.AllowFrom = splitAndTrim(allowRaw)
	}
	cfg.Email = prompt(in, out, "Agent email (env var AGENT_EMAIL; can't write to disk)", cfg.Email, "")

	fmt.Fprint(out, "Mailbox authorization code (env var AGENT_AUTH_CODE; not written to disk):\nPassword: ")
	pwBytes, err := pw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	cfg.AuthCode = strings.TrimSpace(string(pwBytes))
	fmt.Fprintln(out)

	if err := persist(cfg); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}
	return nil
}

// prompt writes "<label>: [<default>] (<hint>)\n" and reads one line. Empty
// input keeps the default; anything else replaces it.
func prompt(in io.Reader, out io.Writer, label, def, hint string) string {
	if hint != "" {
		fmt.Fprintf(out, "%s: [%s] (%s)\n", label, def, hint)
	} else {
		fmt.Fprintf(out, "%s: [%s]\n", label, def)
	}
	var got string
	_, err := fmt.Fscanln(in, &got)
	if err != nil && err != io.EOF {
		// On EOF or scan error, keep the default.
		got = ""
	}
	if got == "" {
		return def
	}
	return got
}

func joinList(xs []string) string {
	return strings.Join(xs, ", ")
}

func splitAndTrim(s string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// persist writes the non-secret fields of cfg to ~/.config/perch/perch.yaml
// with mode 0600. The file gets a header comment listing the env vars that
// should hold the secrets.
func persist(cfg *config.Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "perch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "perch.yaml")

	type persisted struct {
		EmailProvider struct {
			Name string `yaml:"name"`
		} `yaml:"email_provider"`
		AIAgent struct {
			Name           string `yaml:"name"`
			Workdir        string `yaml:"workdir"`
			PermissionMode string `yaml:"permission_mode"`
		} `yaml:"ai_agent"`
		AllowFrom []string `yaml:"allow_from"`
	}
	var p persisted
	p.EmailProvider.Name = cfg.ProviderName
	p.AIAgent.Name = cfg.AgentName
	p.AIAgent.Workdir = cfg.AgentWorkdir
	p.AIAgent.PermissionMode = cfg.AgentPermMode
	p.AllowFrom = cfg.AllowFrom

	body, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	header := []byte("# perch configuration (written by setup wizard).\n" +
		"# Secrets live in env vars, not here: AGENT_EMAIL, AGENT_AUTH_CODE.\n\n")
	return os.WriteFile(path, append(header, body...), 0o600)
}
```

- [ ] Step 6a.4: Run it

```
go test ./internal/setup -count=1
```

Expected: PASS

- [ ] Step 6a.5: Add `golang.org/x/term` dep and wire production password fn

Edit `cmd/perch/main.go` (provisional — Task 4 will adjust more):

No edit yet — just confirm `x/term` is importable. Update `go.mod`:

```
go get golang.org/x/term
```

This will populate `go.mod` and `go.sum`.

Then verify:

```
go build ./... && go test ./... -count=1
```

Expected: PASS (Tasks 1, 2, 3, 6a all green; runner/replier/main still broken from Task 3 — that's fixed in Task 4-5).

- [ ] Step 6a.6: Commit

```
git add internal/setup go.mod go.sum
git commit -m "feat(setup): first-run wizard with non-interactive failure path

Ensure prompts only when required fields are missing AND stdin is a
TTY; otherwise emits a hint and returns ErrMissingFields. Persists
non-secret fields to ~/.config/perch/perch.yaml with 0600 mode.
Password is read via injected PasswordFn (golang.org/x/term.ReadPassword
in production, stub in tests)."
```

---

### Task 7: Deprecation warnings for old YAML keys [depends on Task 6]

**Files:**
- Modify: `internal/config/config.go` (extend `applyYAML` with a raw `yaml.Node` pass that emits slog.Warn for deprecated keys)
- Create or modify: `internal/config/deprecation_test.go`

**Interfaces:**
- applyYAML detects `imap_addr`, `smtp_addr`, `agent_bin`, `claude_workdir`, `claude_permission_mode` in the raw YAML node tree and logs a single `slog.Warn` per key.

- [ ] Step 7.1: Write the failing test

`internal/config/deprecation_test.go`:

```go
package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
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
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("no warnings expected on new schema, got:\n%s", buf.String())
	}
}
```

- [ ] Step 7.2: Run it

```
go test ./internal/config -run TestApplyYAML -count=1
```

Expected: FAIL — no warnings emitted yet.

- [ ] Step 7.3: Implement — extend `applyYAML`

Replace the body of `applyYAML` in `internal/config/config.go` (keep its signature) with:

```go
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
	warnDeprecated(node, deprecated)

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
```

Add `"log/slog"` to the import block at the top of `internal/config/config.go`.

- [ ] Step 7.4: Run it

```
go test ./internal/config -count=1
```

Expected: PASS

- [ ] Step 7.5: Commit

```
git add internal/config
git commit -m "feat(config): WARN on deprecated YAML keys

imap_addr, smtp_addr, agent_bin, claude_workdir, claude_permission_mode
in the raw YAML node tree now log a single slog.Warn each, with a
replacement hint. The keys are ignored by the new-schema decode."
```

---

### Task 5: Refactor `internal/runner` to use `agent.Args` + `agent.BuildArgs`

**Files:**
- Modify: `internal/runner/runner.go:15-63` (struct, constructor, Run)
- Modify: `internal/runner/runner_test.go:43-85` (use new constructor)
- Modify: `internal/app/app.go:106-117` (`a.cfg.ClaudeWorkdir` → `a.cfg.AgentWorkdir`)
- Modify: `internal/app/app.go:117` (BuildPrompt usage unchanged)

**Interfaces:**
- `New(ag *agent.Agent, workdir, permMode string, taskTimeout time.Duration) *Runner`
- Runner no longer reads `cfg.ClaudeBin` / `cfg.ClaudeWorkdir` / `cfg.ClaudePermMode`.

- [ ] Step 5.1: Update failing tests

Replace `internal/runner/runner_test.go` with:

```go
package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChrisZhangJin/perch/internal/agent"
)

// writeStub writes an executable shell script that records its args and runs body.
func writeStub(t *testing.T, body string) (bin, argfile string) {
	t.Helper()
	dir := t.TempDir()
	argfile = filepath.Join(dir, "args.txt")
	bin = filepath.Join(dir, "stubclaude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argfile + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argfile
}

func TestBuildPrompt(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Do X", "please do X", nil, "")
	if !strings.Contains(p, "alice@163.com") || !strings.Contains(p, "Do X") || !strings.Contains(p, "please do X") {
		t.Errorf("prompt missing fields: %q", p)
	}
}

func TestBuildPromptAttachmentHints(t *testing.T) {
	p := BuildPrompt("alice@163.com", "Do X", "body", []string{"/tmp/att/app.log", "/tmp/att/notes.txt"}, "/home/agent/reply")
	for _, want := range []string{"/tmp/att/app.log", "/tmp/att/notes.txt", "/home/agent/reply"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

// runnerFromStub wires a Runner that points at a stub binary instead of the
// real claude binary, so the tests stay hermetic.
func runnerFromStub(t *testing.T, bin string) *Runner {
	t.Helper()
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	ag.Binary = bin // override the registry default for testing
	return New(ag, t.TempDir(), "acceptEdits", 5*time.Second)
}

func TestRunNewSessionPassesSessionID(t *testing.T) {
	bin, argfile := writeStub(t, `echo "REPLY-OK"`)
	out, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "11111111-1111-4111-8111-111111111111", true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "REPLY-OK" {
		t.Errorf("out = %q", out)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "--session-id") || !strings.Contains(string(args), "11111111-1111-4111-8111-111111111111") {
		t.Errorf("expected --session-id in args: %s", args)
	}
	if strings.Contains(string(args), "--resume") {
		t.Errorf("new session should not use --resume: %s", args)
	}
}

func TestRunResumePassesResume(t *testing.T) {
	bin, argfile := writeStub(t, `echo "R"`)
	_, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "22222222-2222-4222-8222-222222222222", false)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argfile)
	if !strings.Contains(string(args), "--resume") {
		t.Errorf("resume should use --resume: %s", args)
	}
}

func TestRunNonZeroExitReturnsError(t *testing.T) {
	bin, _ := writeStub(t, `echo "boom" >&2; exit 3`)
	_, err := runnerFromStub(t, bin).Run(context.Background(), "hi", "id", false)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include stderr: %v", err)
	}
}
```

- [ ] Step 5.2: Run it

```
go test ./internal/runner -count=1
```

Expected: FAIL — `runner.New` still takes `*config.Config`.

- [ ] Step 5.3: Implement — refactor `internal/runner/runner.go`

Replace the file with:

```go
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisZhangJin/perch/internal/agent"
)

// Runner spawns an AI agent subprocess for one task. The agent is selected by
// name at construction (claude / nanopi / pi); per-run argv is built by the
// agent's BuildArgs adapter.
type Runner struct {
	ag          *agent.Agent
	workdir     string
	permMode    string
	taskTimeout time.Duration
}

// New wires a Runner around an already-resolved agent. workdir becomes cmd.Dir
// for every spawned process; permMode is forwarded to the agent's argv
// adapter (claude only).
func New(ag *agent.Agent, workdir, permMode string, taskTimeout time.Duration) *Runner {
	return &Runner{ag: ag, workdir: workdir, permMode: permMode, taskTimeout: taskTimeout}
}

// BuildPrompt frames an email as a task prompt for the agent, listing any
// inbound attachments already saved to disk and the outbound reply/ staging
// directory the agent should write files into.
func BuildPrompt(from, subject, body string, attachments []string, replyDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You received a task via email and must act on it, then produce a reply that will be emailed back to the sender.\n\nFrom: %s\nSubject: %s\n\n%s", from, subject, body)
	if len(attachments) > 0 {
		fmt.Fprintf(&b, "\n\nThis email has %d attachment(s). They were saved under:\n%s\nRead them with your file tools if the task requires it.", len(attachments), strings.Join(attachments, "\n"))
	}
	if replyDir != "" {
		fmt.Fprintf(&b, "\n\nTo send files back to the sender, write them into %s — they will be attached to your reply email automatically.", replyDir)
	}
	return b.String()
}

// Run spawns the agent binary for one task. The arg vector is built by the
// agent's BuildArgs adapter; a brand-new session uses --session-id and a
// continuing session uses --resume (for adapters that distinguish them).
// On ctx timeout the process gets SIGTERM, then SIGKILL after a 5s grace.
func (r *Runner) Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, error) {
	args := r.ag.BuildArgs(agent.Args{
		Prompt:    prompt,
		SessionID: sessionID,
		IsNew:     isNew,
		Workdir:   r.workdir,
		PermMode:  r.permMode,
	})

	ctx, cancel := context.WithTimeout(ctx, r.taskTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.ag.Binary, args...)
	cmd.Dir = r.workdir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w; stderr: %s", r.ag.Name, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
```

- [ ] Step 5.4: Fix `internal/app/app.go` references

In `internal/app/app.go`, replace `a.cfg.ClaudeWorkdir` (lines ~106 and ~112) with `a.cfg.AgentWorkdir`:

```go
		// Inbound attachments: persist so the agent can read them with its
		// file tools. Failure is non-fatal — the task still runs on the body.
		saved, err := saveAttachments(a.cfg.AgentWorkdir, m)
		if err != nil {
			a.log.Warn("attachment save failed", "from", m.From, "err", err)
		}

		// Outbound staging dir: the agent writes files here to send back.
		rpDir := replyDir(a.cfg.AgentWorkdir)
```

(Also update the doc-comment block above ProcessUnseen that says "<workdir>/attachments/<msg-id>/" — no code change required, the comment stays accurate.)

- [ ] Step 5.5: Run it

```
go build ./internal/runner ./internal/app && go test ./internal/runner -count=1
```

Expected: PASS. (The full repo still won't build — Task 4 fixes `cmd/perch/main.go` and `internal/replier/replier.go`.)

- [ ] Step 5.6: Commit

```
git add internal/runner internal/app
git commit -m "refactor(runner): accept *agent.Agent instead of *config.Config

Runner now delegates argv construction to agent.BuildArgs and
cmd.Dir / permMode / taskTimeout come from explicit constructor args.
app.go uses cfg.AgentWorkdir for attachments/reply staging."
```

---

### Task 4: Wire `cmd/perch/main.go` and `internal/replier` to use provider + agent

**Files:**
- Modify: `cmd/perch/main.go:1-71` (full rewiring)
- Modify: `internal/replier/replier.go:126-143` (struct + constructor + sendOnce)
- Modify: `internal/replier/replier.go:209, 256, 271-340` (use `r.smtpAddr` instead of `r.cfg.SMTPAddr`)

**Interfaces:**
- `mailbox.Dial(cfg, imapAddr string)` — extra parameter from provider
- `replier.New(cfg, smtpAddr string)` — extra parameter from provider
- `runner.New(ag, workdir, permMode, taskTimeout)` — already done in Task 5
- `app.New` already takes `TaskRunner` and `ReplySender` interfaces; signatures unchanged
- main calls `setup.Ensure(cfg, os.Stdin, os.Stdout, os.Stderr, realReadPassword)` between config.Load and the rest

- [ ] Step 4.1: Update mailbox.Dial signature

Modify `internal/mailbox/mailbox.go`. Replace the existing `Dial` signature/body (lines ~40-83) with:

```go
// Dial connects with implicit TLS to imapAddr (resolved by the provider
// registry in cmd/perch/main.go), sends the IMAP ID command (163 requires it),
// logs in with the authorization code, and selects INBOX.
func Dial(cfg *config.Config, imapAddr string) (*IMAPMailbox, error) {
	m := &IMAPMailbox{cfg: cfg}
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{InsecureSkipVerify: cfg.TLSInsecure}, //nolint:gosec // dev/test-only, gated by TLS_INSECURE_SKIP_VERIFY
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					m.signal()
				}
			},
		},
	}
	c, err := imapclient.DialTLS(imapAddr, opts)
	if err != nil {
		return nil, err
	}
	m.c = c

	// 163 rejects sessions ("Unsafe Login") unless the client sends ID first.
	if _, err := c.ID(&imap.IDData{Name: "perch", Version: "0.1"}).Wait(); err != nil {
		// Non-fatal: some servers do not require/allow ID. Continue to login.
		_ = err
	}
	if err := c.Login(cfg.Email, cfg.AuthCode).Wait(); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		c.Close()
		return nil, err
	}

	// Detect IDLE support. 163 does not support IDLE and desyncs the connection
	// if we send it, so we must know up front and stay in poll-only mode there.
	caps := c.Caps()
	if len(caps) == 0 {
		if fetched, err := c.Capability().Wait(); err == nil {
			caps = fetched
		}
	}
	m.idleSupported = caps.Has(imap.CapIdle)

	return m, nil
}
```

- [ ] Step 4.2: Update replier to accept smtpAddr

In `internal/replier/replier.go`:

Replace the struct + constructor (lines 126-147):

```go
type Replier struct {
	cfg      *config.Config
	smtpAddr string // resolved by provider registry in main; empty => legacy fallback (none after MVP)
	// MaxAttempts is the upper bound on SMTP send attempts (incl. the first).
	// Defaults to 3 if zero. Each retry opens a fresh TLS+SMTP session.
	MaxAttempts int
	// retryDelay is the base backoff between attempts. Defaults to 1s if zero.
	retryDelay time.Duration
	// onAttempt, if non-nil, is invoked after each attempt with (attempt #, err).
	// Used by app.go to log per-attempt details and to trigger failure
	// notifications when MaxAttempts is exhausted.
	onAttempt func(attempt int, err error, msgSize int)
	// dial overrides the SMTP dialer. nil => tls.Dial (implicit TLS, the
	// production path). Tests inject a plaintext dialer against an in-process
	// fake SMTP server.
	dial func(addr, host string, insecure bool) (net.Conn, error)
}

// New wires a Replier. smtpAddr comes from the provider registry
// (e.g. "smtp.163.com:465"); main.go resolves it after config.Load.
func New(cfg *config.Config, smtpAddr string) *Replier { return &Replier{cfg: cfg, smtpAddr: smtpAddr} }
```

Replace `r.cfg.SMTPAddr` with `r.smtpAddr` everywhere in `sendOnce`, `defaultDial`, and `NotifyFailure` (lines ~201, 209, 272, 295):

In `sendOnce` (replace `r.cfg.SMTPAddr`):

```go
	host, _, err := splitHostPort(r.smtpAddr)
	if err != nil {
		return fmt.Errorf("smtp addr: %w", err)
	}
	dialer := r.dial
	if dialer == nil {
		dialer = defaultDial
	}
	conn, err := dialer(r.smtpAddr, host, r.cfg.TLSInsecure)
```

In `NotifyFailure` (replace `r.cfg.SMTPAddr`):

```go
	host, _, err := splitHostPort(r.smtpAddr)
	if err != nil {
		return err
	}
	body := composeFailureBody(to, subject, attempts, lastErr, msgSize, time.Now())
	msg := ComposeFailure(r.cfg.Email, to, subject, inReplyTo, references, body)
	...
		conn, err := dialer(r.smtpAddr, host, r.cfg.TLSInsecure)
```

(Use `Edit` with `replace_all: true` for the file: replace every occurrence of `r.cfg.SMTPAddr` with `r.smtpAddr`.)

- [ ] Step 4.3: Update existing replier tests if any reference SMTPAddr

```
grep -n 'SMTPAddr' internal/replier
```

If tests reference `cfg.SMTPAddr`, update them to set the new field directly:

```go
r := &Replier{cfg: cfg, smtpAddr: "smtp.test:465"}
```

(Apply the same change pattern as in `internal/replier/replier_test.go` if it exists.)

- [ ] Step 4.4: Rewire `cmd/perch/main.go`

Replace `cmd/perch/main.go` with:

```go
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
	"github.com/ChrisZhangJin/perch/internal/setup"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// realReadPassword is the production PasswordFn: it delegates to
// golang.org/x/term so the auth code is not echoed. Test code passes its own.
func realReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

func main() {
	configPath := flag.String("config", "", "path to YAML config file (default: ./perch.yaml, then ~/.config/perch/perch.yaml). Env: PERCH_CONFIG.")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("perch starting", "version", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if used := config.ResolveConfigPath(*configPath); used != "" {
		log.Info("config loaded", "path", used)
	} else {
		log.Info("config loaded", "path", "(built-in defaults only — no YAML file found)")
	}

	// First-run wizard: prompt for missing fields if stdin is a TTY,
	// otherwise return ErrMissingFields and exit non-zero with a hint.
	if err := setup.Ensure(cfg, os.Stdin, os.Stdout, os.Stderr, realReadPassword); err != nil {
		log.Error("setup", "err", err)
		os.Exit(2)
	}

	// Resolve named providers/agents.
	p, err := provider.Lookup(cfg.ProviderName)
	if err != nil {
		log.Error("provider lookup", "err", err)
		os.Exit(1)
	}
	ag, err := agent.Lookup(cfg.AgentName)
	if err != nil {
		log.Error("agent lookup", "err", err)
		os.Exit(1)
	}

	mb, err := mailbox.Dial(cfg, p.IMAPAddr)
	if err != nil {
		log.Error("mailbox dial", "err", err)
		os.Exit(1)
	}
	g, err := gate.New(cfg.AllowFrom)
	if err != nil {
		log.Error("gate build", "err", err)
		os.Exit(1)
	}
	if mb.IdleSupported() {
		log.Info("mailbox ready", "mode", "idle+poll")
	} else {
		log.Info("mailbox ready", "mode", "poll-only", "note", "server has no IMAP IDLE; using POLL_INTERVAL")
	}
	sess, err := session.Load(cfg.SessionStore)
	if err != nil {
		log.Error("session load", "err", err)
		os.Exit(1)
	}
	a := app.New(cfg, mb, g, sess,
		runner.New(&ag, cfg.AgentWorkdir, cfg.AgentPermMode, cfg.TaskTimeout),
		replier.New(cfg, p.SMTPAddr),
		log,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("watcher started", "email", cfg.Email, "provider", p.Name, "agent", ag.Name, "poll", cfg.PollInterval, "allow_from", cfg.AllowFrom)
	if err := a.Run(ctx); err != nil {
		log.Error("run", "err", err)
		os.Exit(1)
	}
	log.Info("watcher stopped")
}
```

- [ ] Step 4.5: Run full tree

```
go build ./... && go test ./... -count=1
```

Expected: PASS

- [ ] Step 4.6: Commit

```
git add cmd/perch internal/replier internal/mailbox go.mod go.sum
git commit -m "feat: wire provider + agent registries in main; setup.Ensure on first run

cmd/perch now resolves ProviderName/AgentName after config.Load,
passes derived endpoints to mailbox.Dial/replier.New, and calls
setup.Ensure to prompt for any missing fields. mailbox.Dial and
replier.New both gain an explicit endpoint parameter; no internal
behaviour change."
```

---

### Task 8: Update docs — `perch.yaml.example`, `perch.yaml`, README, README.zh-CN

**Files:**
- Modify: `perch.yaml.example` (full rewrite — new schema)
- Modify: `perch.yaml` (rename old key comments → new schema)
- Modify: `README.md` (config table)
- Modify: `README.zh-CN.md` (config table)

- [ ] Step 8.1: Rewrite `perch.yaml.example`

Replace the file with:

```yaml
# perch.yaml.example — example configuration for perch.
#
# Copy this file to `perch.yaml` (or `~/.config/perch/perch.yaml`) and edit.
# At startup perch loads the YAML file at this path, then layers env vars on
# top, then falls back to built-in defaults. Precedence:
#
#     env vars  >  this YAML file  >  built-in defaults
#
# Secrets (AGENT_AUTH_CODE) MUST be set via env, never committed here.
# See README.md / docs/DESIGN.md for the full design.
#
# First-run wizard: if any required field is missing AND stdin is a TTY,
# perch prompts for the missing fields interactively and writes the
# non-secret values to ~/.config/perch/perch.yaml (0600).

# --- Email provider --------------------------------------------------------
# Built-in names. Each one bakes in the right IMAP/SMTP endpoints and the
# per-provider transport quirks (IMAP ID requirement, IDLE support).
email_provider:
  name: 163           # 163 | 126 | qq
  # name: 126
  # name: qq

# --- AI agent --------------------------------------------------------------
# Built-in names. Each one bakes in the right argv adapter (session-id /
# resume / yolo, etc.).
ai_agent:
  name: claude        # claude | nanopi | pi
  workdir: .          # cwd for the spawned agent
  permission_mode: acceptEdits   # claude only; ignored by nanopi/pi
                       #   acceptEdits        — auto-allow file edits
                       #   bypassPermissions  — auto-allow ALL tools
                       #   plan               — read-only planning mode

# --- Whitelist -------------------------------------------------------------
# ONLY these senders can wake the agent. Empty list = deny everyone.
# Comma-separated when set via the ALLOW_FROM env var; YAML uses a real list.
# Each entry is either a literal address or a regex wrapped in `s"..."`:
#
#   - alice@163.com                   # literal (case-insensitive exact match)
#   - s".+@(foo|bar)\.example\.com"  # regex: any user on foo/bar .example.com
#   - s".*agent.*@qq\.com"           # regex: contains "agent" substring pattern
#   - s"(?i).+@trusted\.org"         # regex with case-insensitive flag
#
# Regexes are compiled at startup; a bad pattern fails the gate immediately
# (perch never starts in a fail-open state). The `s` prefix and matching
# trailing `"` are required — without them, the entry is treated as a
# literal address, NOT a regex.
#
# Use ALLOW_FROM env var for literals only (regexes belong in the YAML file).
allow_from:
  - alice@163.com
  - s".*agent.*@qq\.com"
  # - bob@example.com
  # - s".+@(foo|bar)\.example\.com"

# --- Loop timing -----------------------------------------------------------
poll_interval: 60s                       # POLL_INTERVAL (poll tick / IDLE keepalive)
                                         #   lower this if your server is poll-only
                                         #   (163/126: 10s feels snappier than 60s)
task_timeout: 30m                       # TASK_TIMEOUT (SIGTERM → 5s → SIGKILL)

# --- Email handling --------------------------------------------------------
max_prompt_bytes: 65536                 # MAX_PROMPT_BYTES (truncate huge bodies before
                                         #   the agent sees them — protects against
                                         #   absurdly large attachments / signatures)
max_attachment_bytes: 52428800          # MAX_ATTACHMENT_BYTES (per-attachment size cap,
                                         #   default 50 MB; oversized ones are dropped,
                                         #   the email still parses)

# --- Persistence -----------------------------------------------------------
# Thread-root → agent session UUID map (JSON). Survives restarts so a reply
# in the same thread resumes the same agent session.
# session_store: /var/lib/perch/sessions.json
# session_store: ./perch-sessions.json
# (default: $TMPDIR/perch-sessions.json)

# --- TLS -------------------------------------------------------------------
# DEV/TEST ONLY — accepts self-signed certs (e.g. a local GreenMail).
# NEVER enable against a real mailbox.
# tls_insecure_skip_verify: false
```

- [ ] Step 8.2: Rewrite `perch.yaml`

Replace the file with:

```yaml
# perch — default configuration.
#
# Loaded by `perch` at startup. The path can be overridden with:
#   --config <path>     (CLI flag)
#   PERCH_CONFIG <path> (env var)
# If neither is set, perch searches ./perch.yaml, then
# ~/.config/perch/perch.yaml, then falls back to these built-in defaults.
#
# Every field here can ALSO be overridden by an environment variable of the
# same name (see the env->field map below). Precedence: env > this file > built-in.
#
# Secrets (AGENT_AUTH_CODE) MUST be set via env, never committed here.

# --- Email provider --------------------------------------------------------
email_provider:
  name: 163              # 163 | 126 | qq (IMAP/SMTP endpoints derived)

# --- AI agent --------------------------------------------------------------
ai_agent:
  name: claude           # claude | nanopi | pi (binary + argv derived)
  workdir: .             # cwd for the spawned agent
  permission_mode: acceptEdits

# --- Whitelist -------------------------------------------------------------
# Empty list = deny everyone (safer default).
# Comma-separated when set via the ALLOW_FROM env var; YAML uses a real list.
allow_from:
  - zhangjin0602@126.com
  # - alice@163.com
  # - bob@example.com

# --- Loop timing -----------------------------------------------------------
poll_interval: 60s                    # POLL_INTERVAL (poll tick / IDLE keepalive)
task_timeout: 30m                    # TASK_TIMEOUT (SIGTERM → 5s → SIGKILL)

# --- Email handling --------------------------------------------------------
max_prompt_bytes: 65536              # MAX_PROMPT_BYTES (truncate huge bodies)
max_attachment_bytes: 52428800       # MAX_ATTACHMENT_BYTES (per-attachment cap, 50 MB)

# --- Persistence -----------------------------------------------------------
# Path is expanded at load: $TMPDIR/perch-sessions.json by default.
# session_store: /var/lib/perch/sessions.json

# --- TLS -------------------------------------------------------------------
# DEV/TEST ONLY — accepts self-signed certs (e.g. local GreenMail).
# tls_insecure_skip_verify: false
```

- [ ] Step 8.3: Update README.md config table

Find the configuration section in `README.md` (search for "imap_addr" or "configuration"):

Search the file for the old key names first:

```
grep -n 'imap_addr\|smtp_addr\|agent_bin\|claude_workdir\|claude_permission_mode' README.md
```

Replace the matched block(s) with a new config table that points to `email_provider.name` and `ai_agent.name` + `ai_agent.workdir` + `ai_agent.permission_mode`. Match the style already present in the README (table format with `| YAML key | env var | default | description |`). Concretely, replace any row containing the deprecated keys with:

```
| `email_provider.name`       | —                 | `163`            | `163` / `126` / `qq` — endpoints derived |
| `ai_agent.name`             | —                 | `claude`         | `claude` / `nanopi` / `pi` — binary derived |
| `ai_agent.workdir`          | —                 | `.`              | cwd for the spawned agent |
| `ai_agent.permission_mode`  | —                 | `acceptEdits`    | claude only; ignored by nanopi/pi |
```

(Apply with `Edit` against each matched row.)

- [ ] Step 8.4: Update README.zh-CN.md with the same change

```
grep -n 'imap_addr\|smtp_addr\|agent_bin\|claude_workdir\|claude_permission_mode' README.zh-CN.md
```

Replace the same rows with Chinese-localized equivalents pointing to `email_provider.name` and `ai_agent.name` / `ai_agent.workdir` / `ai_agent.permission_mode`. Match existing table format.

- [ ] Step 8.5: Verify

```
go build ./... && go test ./... -count=1
```

Expected: PASS. Also verify YAML files parse:

```
go run ./cmd/perch --config perch.yaml -version
# (or just: go vet ./...)
```

Then read both YAML files end-to-end to spot any stale comments referencing old keys.

- [ ] Step 8.6: Commit

```
git add perch.yaml perch.yaml.example README.md README.zh-CN.md
git commit -m "docs: migrate perch.yaml + README to email_provider/ai_agent schema

perch.yaml.example and perch.yaml now use the new built-in registry
keys; old imap_addr/smtp_addr/agent_bin/claude_workdir/claude_permission_mode
are gone. README and README.zh-CN config tables point at the new keys."
```

---

## Rollout checklist (final task — run after Tasks 1-8 land)

- [ ] `go build ./... && go test ./... -count=1` — full tree green.
- [ ] Confirm `golang.org/x/term` is the only new dep in `go.mod`.
- [ ] Grep one last time for residual references:

```
grep -rn 'cfg.IMAPAddr\|cfg.SMTPAddr\|cfg.ClaudeBin\|cfg.ClaudeWorkdir\|cfg.ClaudePermMode' internal cmd
grep -rn 'imap_addr\|smtp_addr\|agent_bin\|claude_workdir\|claude_permission_mode' internal cmd
```

  Expect zero hits outside deprecation-warning tests.
- [ ] Tag `v0.3.0` once the maintainer signs off (out of scope for this plan).