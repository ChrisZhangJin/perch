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
//
// workdir is resolved to an absolute path before being stored. nanopi
// records cwd as std::env::current_dir() in ~/.nanopi/sessions/active —
// that's the resolved absolute path, not the value Go was given — so
// discoverNanopiSessionID must match against the same form. Without this
// resolution, a config-supplied "." stores as "." while nanopi stores
// "/root/workspace/perch" and the discover lookup never matches, leaving
// perch stuck on its perch-minted UUID on every resume.
func New(ag *agent.Agent, workdir, permMode string, taskTimeout time.Duration, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if abs, err := filepath.Abs(workdir); err == nil {
		workdir = abs
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

// agentDisplayName derives a human-readable name from the agent's email
// for the sign-off template. For "agent_phillip@163.com" it returns
// "phillip"; for "agent_tommy@163.com" it returns "tommy". Names are
// raw lowercase (no capitalization). Falls back to the original local-
// part when the address doesn't start with the agent_ prefix, and to
// "there" on empty input or on "" after the agent_ prefix strip.
func agentDisplayName(email string) string {
	at := strings.LastIndex(email, "@")
	local := email
	if at >= 0 {
		local = email[:at]
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return "there"
	}
	// agent_ prefix is the perch convention; strip it but only if there's
	// something meaningful left. agent_@... (empty after strip) keeps the
	// original local-part so the sign-off doesn't read like "Best,\n".
	if rest, ok := strings.CutPrefix(local, "agent_"); ok && rest != "" {
		return rest
	}
	return local
}

// BuildPrompt frames an email as a task prompt for the agent, listing any
// inbound attachments already saved to disk and the outbound reply/ staging
// directory the agent should write files into.
//
// The framing is deliberate: this is an EMAIL reply, not a CLI session.
// The agent's stdout becomes the email body verbatim, so the model must
// (a) skip the internal-monologue preamble that CLI agents default to
// ("Let me first check..."), (b) not narrate tool calls, (c) not write
// internal-reasoning summaries ("I'll start by...", "Now I have..."),
// (d) keep the final answer short and direct, and (e) write a polite
// email reply with a greeting and sign-off — agents defaulting to a
// terse CLI tone come across as rude to a human recipient. fromName is
// the sender's display name from the From header ("Chris"); when non-
// empty it's used in the salutation ("Hi Chris,"). The sign-off derives
// the agent's own name from agentEmail via agentDisplayName so the
// reply email ends with "Best,\n<local-part>" matching the agent's
// mailbox. Files the agent writes into replyDir are attached to the
// reply automatically; the body should be a one-line caption, not a
// transcript of the work.
func BuildPrompt(from, fromName, subject, body string, attachments []string, replyDir, agentEmail string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You received a task via email from %s (subject: %q). Your entire stdout will be sent back to them as the body of the reply email — there is no follow-up turn to read intermediate output.\n\n", from, subject)
	name := agentDisplayName(agentEmail)
	greeting := "Hi"
	if fromName != "" {
		greeting = "Hi " + fromName + ","
	}
	b.WriteString("Reply rules:\n")
	b.WriteString("- Be concise. Lead with the answer or result, not a plan.\n")
	b.WriteString("- Do NOT narrate tool calls. Do NOT write phrases like \"Let me check...\", \"I see that...\", \"First I'll...\". The user does not see your reasoning; they only see your final text.\n")
	b.WriteString("- Do NOT write internal-reasoning summaries either. Phrases like \"I'll start by...\", \"Now I have...\", \"I need to check...\", \"Both files exist but...\" must stay in your head and NEVER appear on stdout. The screenshot regression we are guarding against looked exactly like this — the agent's reasoning leaked into the email body and the human reader never wanted to see it.\n")
	b.WriteString("- ZERO-thought rule: your stdout is the OUTPUT of the work, not the WORK ITSELF. If the answer is a short result, emit the result. If the answer requires walking through steps, write the polished steps — not your discoveries along the way.\n")
	b.WriteString("- Do NOT preface with \"I will respond to your email\" or similar meta-commentary.\n")
	b.WriteString("- If you must inspect files / run commands, do so silently and only emit the conclusion.\n")
	b.WriteString("- If the task produces a file the user wants back, write it to the reply dir and your body should be a one-line caption (\"Here's the file you asked for.\"). Do not paste the file contents in the body.\n")
	fmt.Fprintf(&b, "- This is a real human on the other end. Open with a polite salutation (e.g. %q), close with a sign-off (e.g. \"Best,\\n%s\"). The body is the email itself, not a chat transcript.\n\n", greeting, name)
	b.WriteString("Task:\n")
	b.WriteString(body)
	if len(attachments) > 0 {
		fmt.Fprintf(&b, "\n\nAttachments (%d) saved on disk under:\n%s\nRead them with your file tools if the task requires it.", len(attachments), strings.Join(attachments, "\n"))
	}
	if replyDir != "" {
		fmt.Fprintf(&b, "\n\nTo return files to the sender, write them into %s — they will be attached to your reply email automatically. Do not echo file contents in the body.", replyDir)
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
	reply, nativeID, err := r.runOnce(ctx, prompt, sessionID, isNew)
	if err != nil && !isNew && r.ag.Name == "nanopi" && isSessionLostErr(err) {
		// The perch-minted UUID we stored in the registry no longer maps to
		// a real nanopi session file (the file was deleted, or the active
		// pointer drifted to a different cwd, or this thread predates the
		// workdir-resolution fix). Fall back to a fresh session so the
		// thread keeps working; discoverNanopiSessionID will hand back the
		// new UUID and the caller will Replace the registry entry.
		r.log.Warn("nanopi session lost, starting fresh",
			"perch_uuid", sessionID, "workdir", r.workdir)
		return r.runOnce(ctx, prompt, "", true)
	}
	return reply, nativeID, err
}

// runOnce performs a single spawn→exit→discover cycle. Split out so Run
// can retry on a lost-session error without duplicating the spawn wiring.
func (r *Runner) runOnce(ctx context.Context, prompt, sessionID string, isNew bool) (string, string, error) {
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

	// Log the agent's reply verbatim so an operator with log_level=debug
	// can see exactly what would have been emailed back — without this
	// the DEBUG traces only show argv + sizes, and an operator who wants
	// to know "what did the agent actually decide" has to fish through
	// their mailbox. Replies can include email bodies from real senders,
	// so we treat the value as sensitive content and emit it under a
	// dedicated key the operator can filter / suppress if needed.
	if reply != "" {
		r.log.Debug("agent reply", "reply", reply)
	}

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
	return cleanAgentOutput(reply, r.ag.Name), nativeID, nil
}

// isSessionLostErr matches nanopi's "first line must be a session header"
// error, which it emits when --session points to a UUID whose .jsonl file
// no longer exists. Triggering a fresh-session fallback is safe because
// nanopi will mint a new UUID and perch will adopt it via the normal
// discover→Replace path.
func isSessionLostErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "first line must be a session header")
}

// cleanAgentOutput strips agent-side rendering noise from captured stdout
// so the value returned to the email is the assistant's actual reply, not
// a transcript of the run. nanopi's StdoutRenderer streams the reply with
// ANSI color codes and bracket-style tool-call / tool-result markers
// directly to stdout; the clean text is also buffered internally and
// returned via finalize(), but we don't have access to that from outside
// the process — so we reconstruct it by:
//
//   1. Dropping every line that matches nanopi's per-event markers
//      (`[tool_call: ...]`, `[bash → ... Took ...]`, etc.). These are
//      control lines, never part of the reply.
//   2. Stripping any remaining ANSI escape sequences (color codes).
//   3. Trimming surrounding whitespace.
//
// Other agents (claude, pi) print plain text and pass through unchanged.
func cleanAgentOutput(s, agentName string) string {
	if agentName != "nanopi" {
		return strings.TrimSpace(s)
	}
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		stripped := stripANSI(line)
		t := strings.TrimSpace(stripped)
		if t == "" {
			out.WriteByte('\n')
			continue
		}
		// Drop nanopi's control-line markers. These are written by
		// StdoutRenderer for tool_call, tool_result, error, compaction.
		if isNanopiControlLine(t) {
			continue
		}
		out.WriteString(stripped)
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}

// isNanopiControlLine reports whether a line is one of nanopi's
// per-event rendering markers (after ANSI strip). Matches the formats
// in nanopi/src/render/stdout.rs:
//   [tool_call: <name> <id>]
//   [<name> → <n> bytes  Took <time>]
//   [<name> ✗ <n> bytes  Took <time>]
//   [error: <msg>]
//   [compacting context (<reason>)…]
//   [compacted <n> messages via <kind>]
func isNanopiControlLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "[tool_call:"):
		return true
	case strings.HasPrefix(line, "[error:"):
		return true
	case strings.HasPrefix(line, "[compacting context"):
		return true
	case strings.HasPrefix(line, "[compacted "):
		return true
	}
	// tool_result lines: [<name> → N bytes  Took ...] or [<name> ✗ N bytes  Took ...]
	if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		inner := line[1 : len(line)-1]
		// Has "→" or "✗" between first word and "bytes"
		if strings.Contains(inner, " bytes") {
			return true
		}
	}
	return false
}

// stripANSI removes CSI escape sequences: ESC [ ... <final byte 0x40-0x7e>.
// Covers the colors and styles nanopi uses (SGR codes like \x1b[1;32m).
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we hit a final byte (0x40-0x7e).
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
