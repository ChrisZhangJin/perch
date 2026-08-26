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
	"strconv"
	"strings"

	"github.com/ChrisZhangJin/perch/internal/agent"
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
// ~/.perch/perch.yaml with mode 0600, and updates cfg in place.
func Ensure(cfg *config.Config, stdin io.Reader, stdout io.Writer, stderr io.Writer, pw PasswordFn) error {
	missing := missingFields(cfg)
	if len(missing) == 0 {
		return nil
	}
	// Non-interactive path: cannot prompt; emit hint and error.
	if !isTTYFn(stdin) {
		fmt.Fprintf(stderr, "missing required fields: %s\n", strings.Join(missing, ", "))
		fmt.Fprintln(stderr, "hint: run interactively (./perch) or pre-fill via env vars:")
		fmt.Fprintln(stderr, "  AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=xxxxxxxx ./perch")
		fmt.Fprintln(stderr, "  or edit ~/.perch/perch.yaml")
		return ErrMissingFields
	}
	return runWizard(cfg, stdin, stdout, pw)
}

// MissingFields returns the cfg field names that still need to be populated.
// Exported so callers (e.g. --daemon) can refuse to start before any wizard
// work runs, without going through Ensure and its TTY-driven hint output.
func MissingFields(cfg *config.Config) []string {
	return missingFields(cfg)
}

// missingFields returns the cfg field names that still need to be populated.
// AllowFrom is intentionally NOT here — the wizard prompts for it on
// interactive first run and persists a `["*"]` accept-all entry to the
// YAML file as the onboarding default. For headless (env-vars-only) setups
// an empty allow_from means "deny all" (fail-closed, the safe default
// for non-wizard deployments); perch logs WARN and ignores non-whitelisted
// senders. Either way we don't block startup over it.
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
	if cfg.LogLevel == "" {
		m = append(m, "log_level")
	}
	return m
}

// isTTYFn is the TTY check used by Ensure. Defaults to isInteractive (the
// production check); tests override it to drive the wizard without a PTY.
var isTTYFn = isInteractive

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
// it does not echo. Non-secret fields are persisted to ~/.perch/perch.yaml.
func runWizard(cfg *config.Config, in io.Reader, out io.Writer, pw PasswordFn) error {
	fmt.Fprintln(out, "Welcome to perch.")
	fmt.Fprintln(out, "Press Enter to accept the default shown in [brackets]; type to override.")
	fmt.Fprintln(out)

	cfg.ProviderName = prompt(in, out, "Email provider", cfg.ProviderName, "163 / 126 / qq")
	cfg.AgentName = promptAgent(in, out, cfg.AgentName)
	cfg.AgentWorkdir = prompt(in, out, "Agent workdir", cfg.AgentWorkdir, ".")
	cfg.AgentPermMode = prompt(in, out, "Agent permission mode (claude only)", cfg.AgentPermMode, "acceptEdits")
	cfg.LogLevel = prompt(in, out, "Log level", cfg.LogLevel, "debug / info / warn / error")
	// allow_from default: when the operator is running the wizard for the
	// first time the cfg has no entries yet, so show `*` (the accept-all
	// sentinel gate.New recognizes) as the bracket default. When re-running
	// the wizard with an existing cfg, surface the current value so an
	// accidental Enter doesn't silently open the gate.
	allowDef := "*"
	if len(cfg.AllowFrom) > 0 {
		allowDef = joinList(cfg.AllowFrom)
	}
	allowRaw := prompt(in, out, "Allow senders (comma-separated; default = accept all)", allowDef, "")
	cfg.AllowFrom = splitAndTrim(allowRaw)
	cfg.Email = prompt(in, out, "Agent email (env var AGENT_EMAIL overrides; otherwise saved here)", cfg.Email, "")

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

// promptAgent is prompt() with a binary-on-PATH check after each pick.
// The wizard refuses to accept an agent name whose binary isn't found —
// perch dies with a confusing error otherwise when the runner tries to
// exec a missing command. On EOF (non-TTY) we keep the default and let
// the daemon preflight catch it later, matching how other fields work.
func promptAgent(in io.Reader, out io.Writer, def string) string {
	for {
		got := prompt(in, out, "AI agent", def, "claude / nanopi / pi")
		// Empty here means EOF or scan error inside prompt() — keep default.
		if got == "" {
			return def
		}
		if _, err := agent.BinaryPath(got); err != nil {
			fmt.Fprintf(out, "  ! %v\n", err)
			fmt.Fprintln(out, "    pick another, or Ctrl-D to abort.")
			def = got // last attempted value, so re-prompt doesn't reset hint
			continue
		}
		return got
	}
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

// persist writes the non-secret fields of cfg to ~/.perch/perch.yaml
// with mode 0600. AuthCode is NEVER written — only AGENT_AUTH_CODE env var.
// Email IS written so unattended restarts don't have to re-enter it.
//
// The file is fully populated: fields the wizard prompted for carry the
// operator's answer; fields the wizard did not prompt for (loop timing,
// byte caps, session store, TLS, task_only, long_task_ack) are written with
// their built-in default and an inline comment so the operator can see every
// knob that exists. TestPersistWritesEveryYAMLKey enforces that "every knob"
// claim against config.YAMLKeys().
// Format is hand-written (not yaml.Marshal) so comments can sit next to
// the values they describe.
//
// INVARIANT: cfg must originate from config.Load (i.e. be seeded by
// config.Defaults), not a bare &config.Config{}. The defaults block below
// can only restore zero-valued fields whose default is also the zero value;
// AgentTaskOnly defaults to TRUE, so a bare struct would persist
// `task_only: false` and silently disable the guardrail on the next start.
// main.go satisfies this — it calls config.Load before setup.Ensure.
func persist(cfg *config.Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".perch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "perch.yaml")

	allowFrom := cfg.AllowFrom
	if len(allowFrom) == 0 {
		allowFrom = []string{} // explicit empty list, not null
	}

	// Apply built-in defaults for any field the wizard didn't prompt for
	// (loop timing, byte caps, session store, TLS). The wizard writes a
	// fully populated file so the operator can see every knob that exists.
	// Defaults are pulled from config.Defaults() so a future change to
	// the default in one place flows to both places.
	def := config.Defaults()
	if cfg.PollInterval == 0 {
		cfg.PollInterval = def.PollInterval
	}
	if cfg.TaskTimeout == 0 {
		cfg.TaskTimeout = def.TaskTimeout
	}
	if cfg.MaxPromptBytes == 0 {
		cfg.MaxPromptBytes = def.MaxPromptBytes
	}
	if cfg.MaxAttachmentBytes == 0 {
		cfg.MaxAttachmentBytes = def.MaxAttachmentBytes
	}
	if cfg.SessionStore == "" {
		cfg.SessionStore = def.SessionStore
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = def.LogLevel
	}
	if cfg.PromptContracts == "" {
		cfg.PromptContracts = def.PromptContracts
	}
	if cfg.StripQuoted == "" {
		cfg.StripQuoted = def.StripQuoted
	}
	// MaxRepliesPerHour has a non-zero default, so a zero here is ambiguous:
	// it could be "unset" or a deliberate "no cap". Treat it as unset, which
	// is the safe reading — the alternative silently removes the backstop.
	if cfg.MaxRepliesPerHour == 0 {
		cfg.MaxRepliesPerHour = def.MaxRepliesPerHour
	}
	// A non-positive hook timeout would mean "wait forever" for a hook that
	// hangs, so a zero here is treated as unset, like the reply cap above.
	if cfg.HookTimeout <= 0 {
		cfg.HookTimeout = def.HookTimeout
	}

	body := "# perch configuration (written by setup wizard).\n" +
		"# Secrets live in env vars, not here: AGENT_AUTH_CODE.\n" +
		"# AGENT_EMAIL is read from this file when not set in env.\n" +
		"# Edit any field below; perch re-reads this file on every start.\n\n" +
		"# --- Email provider ---\n" +
		"# 163 / 126 → Poller (short-conn, poll-only — server has no IDLE)\n" +
		"# qq        → IMAPMailbox + IDLETrigger (long-conn, idle+poll)\n" +
		"email_provider:\n" +
		"  name: " + cfg.ProviderName + "\n\n" +
		"# --- AI agent ---\n" +
		"ai_agent:\n" +
		"  name: " + cfg.AgentName + "\n" +
		"  workdir: " + cfg.AgentWorkdir + "\n" +
		"  permission_mode: " + cfg.AgentPermMode + "   # claude only; nanopi/pi ignore\n" +
		"  # task_only (DEFAULT true): inject a SAFETY PROTOCOL section into the\n" +
		"  # agent prompt. Read-only inspection and anything inside workdir stay\n" +
		"  # allowed; destructive actions outside workdir (rm, sudo, curl to\n" +
		"  # external hosts, package install) are refused UNLESS destruction IS\n" +
		"  # the stated task. A light guardrail against side-requests smuggled\n" +
		"  # into an email body. Set false to disable.\n" +
		"  task_only: " + strconv.FormatBool(cfg.AgentTaskOnly) + "\n\n" +
		"# --- Prompt ---\n" +
		"# contracts: when to send the format-contract sections (reply framing,\n" +
		"# GREETING PROTOCOL, ATTACHMENT PROTOCOL) — roughly 1.8 KB per email.\n" +
		"#   on_resume — DEFAULT. Only when opening a new agent session; a\n" +
		"#               resumed session already has them in its replayed\n" +
		"#               history, and repeating them compounds because every\n" +
		"#               turn is persisted and re-read on later resumes.\n" +
		"#   always    — repeat on every email; use if long threads drift\n" +
		"#               out of reply format.\n" +
		"# SAFETY and GROUNDING are never removed by this, only compacted on a\n" +
		"# resumed turn: cwd and the full refusal list stay, the rationale and\n" +
		"# the worked example go.\n" +
		"prompt:\n" +
		"  contracts: " + cfg.PromptContracts + "\n" +
		"  # strip_quoted: drop the quoted history a client appends on Reply\n" +
		"  # (「原始邮件」blocks, \"在 ... 写道：\", \"> \" lines, header blocks).\n" +
		"  #   never     — DEFAULT. Leave the body as received.\n" +
		"  #   on_resume — strip only when resuming a session, which already\n" +
		"  #               has those turns. A cold session may need the quote:\n" +
		"  #               a forwarded thread is sometimes the whole task.\n" +
		"  #   always    — strip unconditionally.\n" +
		"  # Ambiguous lines are left alone; a quote-only body is never stripped.\n" +
		"  strip_quoted: " + cfg.StripQuoted + "\n\n" +
		"# --- Loop protection ---\n" +
		"# Two mail robots answering each other never stop on their own. Each\n" +
		"# round costs an agent invocation on both sides.\n" +
		"#\n" +
		"# skip_automated: ignore inbound mail that labels itself machine\n" +
		"# generated (RFC 3834 Auto-Submitted, Precedence: bulk/list/junk,\n" +
		"# List-* headers). Set false only if something you WANT answered sets\n" +
		"# them — a monitoring job mailing in a task legitimately does.\n" +
		"#\n" +
		"# max_replies_per_hour: per-thread cap, the backstop for a robot that\n" +
		"# labels nothing at all. 0 disables it. A human does not round-trip\n" +
		"# one thread ten times an hour; a loop does it in minutes.\n" +
		"#\n" +
		"# Mail from perch's own address is always ignored, with no knob.\n" +
		"loop_guard:\n" +
		"  skip_automated: " + strconv.FormatBool(cfg.SkipAutomated) + "\n" +
		"  max_replies_per_hour: " + strconv.FormatInt(int64(cfg.MaxRepliesPerHour), 10) + "\n\n" +
		"# --- Whitelist ---\n" +
		"# ONLY these senders can wake the agent. Empty list = deny everyone\n" +
		"# (fail-closed). A literal `*` entry accepts everyone (the wizard\n" +
		"# writes this as the onboarding default — tighten before going live).\n" +
		"# Each other entry is a literal address, or a regex prefixed with s\"...\"\n" +
		"# Use ALLOW_FROM env var for literals only (regexes belong in this file).\n" +
		"allow_from:\n" +
		formatAllowFrom(allowFrom) + "\n" +
		"# --- Account ---\n" +
		"email: " + cfg.Email + "   # AGENT_EMAIL env var overrides\n\n" +
		"# --- Loop timing ---\n" +
		"poll_interval: " + cfg.PollInterval.String() + "   # POLL_INTERVAL (poll tick / IDLE keepalive)\n" +
		"task_timeout: " + cfg.TaskTimeout.String() + "   # TASK_TIMEOUT (SIGTERM → 5s → SIGKILL)\n\n" +
		"# --- Long-task interim ack ---\n" +
		"# When true, perch runs a lightweight classifier before the real task.\n" +
		"# If the classifier says \"long\", perch sends an interim ack email so\n" +
		"# you know the request landed while the real reply is still cooking.\n" +
		"# Off by default — enabling it costs one extra agent run per inbound.\n" +
		"long_task_ack: " + strconv.FormatBool(cfg.LongTaskAck) + "   # LONG_TASK_ACK\n\n" +
		"# --- Hooks ---\n" +
		"# on_email: a script perch runs for every inbound email it ACCEPTS,\n" +
		"# just before the agent runs. Called with five positional arguments:\n" +
		"#   $1 email_id (Message-Id, \"\" when the mail has none)\n" +
		"#   $2 subject\n" +
		"#   $3 body (as received, truncated at 64 KiB)\n" +
		"#   $4 sender\n" +
		"#   $5 new_thread | reply_thread\n" +
		"# Observer only: a missing script, a non-zero exit or a timeout is\n" +
		"# logged and the email is handled as usual. Runs with cwd = workdir.\n" +
		"# Empty (the default) disables it. Env: ON_EMAIL_HOOK / HOOK_TIMEOUT\n" +
		"hooks:\n" +
		formatOnEmailHook(cfg.OnEmailHook) +
		"  timeout: " + cfg.HookTimeout.String() + "\n\n" +
		"# --- Logging ---\n" +
		"log_level: " + cfg.LogLevel + "   # LOG_LEVEL (debug / info / warn / error)\n\n" +
		"# --- Email handling ---\n" +
		"max_prompt_bytes: " + strconv.FormatInt(int64(cfg.MaxPromptBytes), 10) +
		"   # MAX_PROMPT_BYTES (truncate huge bodies before the agent sees them)\n" +
		"max_attachment_bytes: " + strconv.FormatInt(int64(cfg.MaxAttachmentBytes), 10) +
		"   # MAX_ATTACHMENT_BYTES (per-attachment size cap; oversized ones are dropped)\n\n" +
		"# --- Persistence ---\n" +
		"# thread-root → agent session map (JSON). Survives restarts.\n" +
		"# (default = $TMPDIR/perch-sessions.json)\n" +
		"session_store: " + cfg.SessionStore +
		"   # SESSION_STORE\n\n" +
		"# --- TLS ---\n" +
		"# DEV/TEST ONLY — accepts self-signed certs (e.g. a local GreenMail).\n" +
		"# NEVER enable against a real mailbox.\n" +
		"tls_insecure_skip_verify: " + strconv.FormatBool(cfg.TLSInsecure) +
		"   # TLS_INSECURE_SKIP_VERIFY\n"

	return os.WriteFile(path, []byte(body), 0o600)
}

// formatAppendSystemPrompt renders the ai_agent.append_system_prompt line.
// Unset writes the key commented out with an example path, so the operator
// sees the knob and a filled-in form. A set value is written as a YAML block
// scalar when it is multi-line (which literal role definitions always are) —
// quoting or escaping that onto one line would be unreadable and easy to
// corrupt by hand.
func formatAppendSystemPrompt(v string) string {
	if strings.TrimSpace(v) == "" {
		return "  # append_system_prompt: /path/to/helpdesk.md\n"
	}
	if !strings.ContainsAny(v, "\n\r") {
		return "  append_system_prompt: " + v + "\n"
	}
	var b strings.Builder
	b.WriteString("  append_system_prompt: |\n")
	for _, line := range strings.Split(strings.TrimRight(v, "\n"), "\n") {
		b.WriteString("    " + strings.TrimRight(line, "\r") + "\n")
	}
	return b.String()
}

// formatOnEmailHook renders the hooks.on_email line. With no hook configured
// it writes the key commented out with an example path, rather than an empty
// value: the operator sees the knob and what a filled-in one looks like, and
// there is no blank string to wonder about. The completeness test matches the
// key either way.
func formatOnEmailHook(path string) string {
	if strings.TrimSpace(path) == "" {
		return "  # on_email: /home/agent/record.sh\n"
	}
	return "  on_email: " + path + "\n"
}

// formatAllowFrom renders an AllowFrom slice as YAML list items. Empty
// slices produce an empty list body (operator can leave it empty = deny
// all, fail-closed for non-wizard setups). The wizard never writes `[]`
// itself — it writes `["*"]` so onboarding defaults to accept-all.
//
// Entries that look like YAML scalars with special meaning (currently
// just the bare `*`, which is the YAML alias indicator and trips the
// strict parser) are quoted on the way out; gate.New strips the quotes
// (it never sees them — yaml.v3 hands the decoded string back as "*").
// Literal addresses and `s"..."` regexes never need quoting.
func formatAllowFrom(items []string) string {
	if len(items) == 0 {
		return "  []\n"
	}
	var b strings.Builder
	for _, a := range items {
		b.WriteString("  - ")
		if a == "*" {
			b.WriteString(`"*"`)
		} else {
			b.WriteString(a)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
