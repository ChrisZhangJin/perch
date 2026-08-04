package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
)

type Runner struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Runner { return &Runner{cfg: cfg} }

// BuildPrompt frames an email as a task prompt for Claude.
func BuildPrompt(from, subject, body string) string {
	return fmt.Sprintf(
		"You received a task via email and must act on it, then produce a reply that will be emailed back to the sender.\n\nFrom: %s\nSubject: %s\n\n%s",
		from, subject, body,
	)
}

// Run spawns `claude -p` for one task. A brand-new thread creates a session via
// --session-id; a continuing thread resumes it via --resume. On ctx timeout the
// process gets SIGTERM, then SIGKILL after a 5s grace (via WaitDelay).
func (r *Runner) Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, error) {
	args := []string{"-p", prompt, "--output-format", "text", "--permission-mode", r.cfg.ClaudePermMode}
	if isNew {
		args = append(args, "--session-id", sessionID)
	} else {
		args = append(args, "--resume", sessionID)
	}

	ctx, cancel := context.WithTimeout(ctx, r.cfg.TaskTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.cfg.ClaudeBin, args...)
	cmd.Dir = r.cfg.ClaudeWorkdir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude failed: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
