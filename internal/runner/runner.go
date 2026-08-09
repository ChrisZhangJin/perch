package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	log         *slog.Logger
}

// New wires a Runner around an already-resolved agent. workdir becomes cmd.Dir
// for every spawned process; permMode is forwarded to the agent's argv
// adapter (claude only). log is used to DEBUG-log every spawned command
// (binary + argv + workdir) and exit status, so an operator with log_level=debug
// can reproduce what perch sent.
func New(ag *agent.Agent, workdir, permMode string, taskTimeout time.Duration, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{ag: ag, workdir: workdir, permMode: permMode, taskTimeout: taskTimeout, log: log}
}

// discoverNanopiSessionID reads ~/.nanopi/sessions/active (or $NANOPI_HOME/sessions/active),
// finds the line for our workdir, reads the referenced session file, and
// returns the UUID written in its header line.
func discoverNanopiSessionID(workdir string) string {
	dir := nanopiSessionsDir()
	if dir == "" {
		return ""
	}
	active := filepath.Join(dir, "active")
	data, err := os.ReadFile(active)
	if err != nil {
		return ""
	}
	wanted := workdir
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "\t")
		if !ok || k != wanted {
			continue
		}
		return readSessionHeaderID(v)
	}
	return ""
}

// nanopiSessionsDir mirrors nanopi's sessions_dir() helper: NANOPI_HOME env
// var if set, else $HOME/.nanopi/sessions.
func nanopiSessionsDir() string {
	if p := os.Getenv("NANOPI_HOME"); p != "" {
		return filepath.Join(p, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nanopi", "sessions")
}

// readSessionHeaderID parses the first non-empty JSONL line of the session
// file at path and returns the "id" field. nanopi's session header has
// shape {"type":"session","version":2,"id":"<uuid>",...} — we only need id.
func readSessionHeaderID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var hdr struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &hdr); err != nil {
			return ""
		}
		return hdr.ID
	}
	return ""
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
//
// Returns (reply, nativeID, err). nativeID is the agent's actual session
// id for this run when discoverable (nanopi on IsNew=true, where perch
// invented the UUID but nanopi minted its own). For all other agents and
// paths nativeID is "" — callers should keep their perch-invented UUID
// in that case.
func (r *Runner) Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, string, error) {
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

	// Log the exact command we're about to spawn. Prompt length instead of
	// contents — prompts are user email bodies and can be long; the argv
	// has the metadata an operator actually needs to reproduce.
	r.log.Debug("spawn agent",
		"agent", r.ag.Name,
		"binary", r.ag.Binary,
		"argv", args,
		"workdir", r.workdir,
		"is_new", isNew,
		"prompt_bytes", len(prompt),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	// Exit info — useful when an agent hangs or returns garbage. We log the
	// raw error and stderr snippet at DEBUG; at INFO we only emit on failure
	// (the WARN in the caller picks that up).
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		r.log.Debug("agent exit",
			"agent", r.ag.Name,
			"err", err,
			"exit_code", exitCode,
			"duration", dur,
			"stdout_bytes", stdout.Len(),
			"stderr", strings.TrimSpace(stderr.String()),
		)
		return "", "", fmt.Errorf("%s failed: %w; stderr: %s", r.ag.Name, err, strings.TrimSpace(stderr.String()))
	}
	r.log.Debug("agent exit",
		"agent", r.ag.Name,
		"exit_code", exitCode,
		"duration", dur,
		"stdout_bytes", stdout.Len(),
	)
	reply := strings.TrimSpace(stdout.String())

	// Discover the agent's native session id (only nanopi, only after a
	// successful IsNew=true run where we omitted --session). Returns ""
	// otherwise — callers treat empty as "no update needed".
	var nativeID string
	if isNew && r.ag.Name == "nanopi" {
		nativeID = discoverNanopiSessionID(r.workdir)
		if nativeID != "" {
			r.log.Debug("adopt nanopi session id", "perch_uuid", sessionID, "nanopi_uuid", nativeID)
		}
	}
	return reply, nativeID, nil
}
