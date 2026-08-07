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
