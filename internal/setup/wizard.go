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
// interactive first run and persists it to the YAML file, but for
// headless (env-vars-only) setups an empty allow_from means "deny all"
// which is the safe default (perch logs WARN and ignores non-whitelisted
// senders), so we don't block startup over it.
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
	cfg.AgentName = prompt(in, out, "AI agent", cfg.AgentName, "claude / nanopi / pi")
	cfg.AgentWorkdir = prompt(in, out, "Agent workdir", cfg.AgentWorkdir, ".")
	cfg.AgentPermMode = prompt(in, out, "Agent permission mode (claude only)", cfg.AgentPermMode, "acceptEdits")
	cfg.LogLevel = prompt(in, out, "Log level", cfg.LogLevel, "debug / info / warn / error")
	allowRaw := prompt(in, out, "Allow senders (comma-separated, empty = deny all)", joinList(cfg.AllowFrom), "")
	if allowRaw != "" {
		cfg.AllowFrom = splitAndTrim(allowRaw)
	}
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
// byte caps, session store, TLS) are written with their built-in default
// and an inline comment so the operator can see every knob that exists.
// Format is hand-written (not yaml.Marshal) so comments can sit next to
// the values they describe.
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
		"  permission_mode: " + cfg.AgentPermMode + "   # claude only; nanopi/pi ignore\n\n" +
		"# --- Whitelist ---\n" +
		"# ONLY these senders can wake the agent. Empty list = deny everyone.\n" +
		"# Each entry is a literal address, or a regex prefixed with s\"...\"\n" +
		"# Use ALLOW_FROM env var for literals only (regexes belong in this file).\n" +
		"allow_from:\n" +
		formatAllowFrom(allowFrom) + "\n" +
		"# --- Account ---\n" +
		"email: " + cfg.Email + "   # AGENT_EMAIL env var overrides\n\n" +
		"# --- Loop timing ---\n" +
		"poll_interval: " + cfg.PollInterval.String() + "   # POLL_INTERVAL (poll tick / IDLE keepalive)\n" +
		"task_timeout: " + cfg.TaskTimeout.String() + "   # TASK_TIMEOUT (SIGTERM → 5s → SIGKILL)\n\n" +
		"# --- Logging ---\n" +
		"log_level: " + cfg.LogLevel + "   # LOG_LEVEL (debug / info / warn / error)\n\n" +
		"# --- Email handling ---\n" +
		"max_prompt_bytes: " + strconv.FormatInt(int64(cfg.MaxPromptBytes), 10) +
		"   # MAX_PROMPT_BYTES (truncate huge bodies before the agent sees them)\n" +
		"max_attachment_bytes: " + strconv.FormatInt(int64(cfg.MaxAttachmentBytes), 10) +
		"   # MAX_ATTACH_BYTES (per-attachment size cap; oversized ones are dropped)\n\n" +
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

// formatAllowFrom renders an AllowFrom slice as YAML list items. Empty
// slices produce an empty list body (operator can leave it empty = deny all).
func formatAllowFrom(items []string) string {
	if len(items) == 0 {
		return "  []\n"
	}
	var b strings.Builder
	for _, a := range items {
		b.WriteString("  - ")
		b.WriteString(a)
		b.WriteByte('\n')
	}
	return b.String()
}
