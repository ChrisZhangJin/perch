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
