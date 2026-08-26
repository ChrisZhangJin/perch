// Package hook runs an operator-supplied script for each inbound email perch
// accepts. It exists so a side effect ("record every new thread", "ping my
// phone", "bump a counter") can be attached to arriving mail without patching
// perch itself.
//
// The contract is positional argv, deliberately dumb so a five-line shell
// script is enough:
//
//	<script> <email_id> <subject> <body> <sender> <new_thread|reply_thread>
//
// email_id is the provider-assigned Message-Id header. Mail without one gets
// an empty string rather than a missing argument, so $4 is always the sender.
//
// The hook is an observer, never a gate: a missing script, a non-zero exit, or
// a script that hangs past the timeout is logged and mail handling continues.
// Perch's whitelist is upstream of every fire, so the script only ever sees
// mail from senders the operator already trusts.
package hook

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisZhangJin/perch/internal/message"
)

// Argument caps. Linux allows at most MAX_ARG_STRLEN (128 KiB) per single
// argument and E2BIG is an all-or-nothing failure, so an unbounded email body
// would mean the hook silently stops firing on exactly the long threads an
// operator most wants recorded. Truncation is rune-safe (message.TruncateUTF8)
// so a script never receives a half-decoded character.
//
// The body cap sits at the default max_prompt_bytes, i.e. the most the agent
// itself would ever see of one email.
const (
	MaxBodyBytes    = 65536
	MaxSubjectBytes = 4096
)

// Thread-state argv values. Spelled out rather than true/false so a shell
// script reads as `[ "$5" = new_thread ]` and needs no comment.
const (
	FlagNewThread   = "new_thread"
	FlagReplyThread = "reply_thread"
)

// Event is the inbound email reduced to what the hook contract carries.
type Event struct {
	EmailID   string // Message-Id header verbatim ("<abc@163.com>"); "" when absent
	Subject   string
	Body      string
	Sender    string // lowercased addr-spec, e.g. "alice@163.com"
	NewThread bool   // true when perch had no session for this thread yet
}

// Args renders the event as the positional argument vector. Exported so tests
// and callers can see exactly what a script will receive.
func (e Event) Args() []string {
	flag := FlagReplyThread
	if e.NewThread {
		flag = FlagNewThread
	}
	return []string{
		e.EmailID,
		message.TruncateUTF8(e.Subject, MaxSubjectBytes),
		message.TruncateUTF8(e.Body, MaxBodyBytes),
		e.Sender,
		flag,
	}
}

// Runner executes one configured hook script. A nil *Runner means "no hook
// configured" and every method is a no-op, so callers never branch.
type Runner struct {
	path    string
	workdir string
	timeout time.Duration
	log     *slog.Logger
}

// DefaultTimeout bounds a single hook run when the caller passes 0.
const DefaultTimeout = 30 * time.Second

// New resolves path and returns a Runner, or nil when path is empty (the
// feature is off).
//
// Resolution: a bare name is looked up on PATH; anything else (and anything
// PATH did not resolve) is made absolute, so the script perch runs never
// depends on perch's cwd at fire time. A path that does not exist, or exists
// without an executable bit,
// gets one WARN here and is kept anyway — perch must not refuse to start over
// an operator's typo, and the script may well be created before the first
// email arrives. Each failed fire logs on its own.
func New(path, workdir string, timeout time.Duration, log *slog.Logger) *Runner {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if found, err := exec.LookPath(path); err == nil && strings.ContainsRune(found, filepath.Separator) {
		path = found
	}
	// Absolutise whatever we ended up with, including a bare name that PATH
	// didn't resolve. os/exec refuses to run a bare relative name (ErrDot
	// since Go 1.19), so leaving it alone would only ever fail; treating it as
	// relative to perch's cwd matches how the rest of perch reads paths.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// Same treatment runner.New gives the agent workdir: resolve it once so
	// the child's cwd is stable no matter where perch was launched from.
	if workdir != "" {
		if abs, err := filepath.Abs(workdir); err == nil {
			workdir = abs
		}
	}
	r := &Runner{path: path, workdir: workdir, timeout: timeout, log: log}
	r.warnIfUnusable()
	return r
}

// Path returns the resolved script path. Exported for startup logging.
func (r *Runner) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Timeout returns the effective per-run timeout, which is not always what the
// caller passed: a zero or negative value falls back to DefaultTimeout, and
// startup logging should report what will actually be enforced.
func (r *Runner) Timeout() time.Duration {
	if r == nil {
		return 0
	}
	return r.timeout
}

// warnIfUnusable emits a single startup WARN when the script is missing, is a
// directory, or carries no executable bit for anyone.
func (r *Runner) warnIfUnusable() {
	info, err := os.Stat(r.path)
	if err != nil {
		r.log.Warn("on-email hook script not found; hook will error on every email",
			"script", r.path, "err", err)
		return
	}
	if info.IsDir() {
		r.log.Warn("on-email hook script is a directory", "script", r.path)
		return
	}
	if info.Mode().Perm()&0o111 == 0 {
		r.log.Warn("on-email hook script is not executable; chmod +x it",
			"script", r.path, "mode", info.Mode().Perm().String())
	}
}

// Fire runs the script for one email and waits for it, bounded by the
// configured timeout. It returns the exec error so the caller can log it; the
// caller must NOT treat that error as a reason to stop handling the email.
//
// A nil receiver returns nil, which is how "no hook configured" is expressed.
func (r *Runner) Fire(ctx context.Context, ev Event) error {
	if r == nil {
		return nil
	}
	args := ev.Args()

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.path, args...)
	cmd.Dir = r.workdir
	// SIGTERM first, SIGKILL after a grace period — a hook that ignores TERM
	// must not hold the poll loop open. Mirrors runner.runOnce.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	r.log.Debug("spawn on-email hook",
		"script", r.path,
		"workdir", r.workdir,
		"email_id", ev.EmailID,
		"sender", ev.Sender,
		"thread", args[4],
		"body_bytes", len(args[2]),
	)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return fmt.Errorf("hook %s: %w (exit %d, %s); stderr: %s",
			filepath.Base(r.path), err, exitCode, dur.Round(time.Millisecond),
			tail(strings.TrimSpace(stderr.String()), 2048))
	}
	r.log.Debug("on-email hook done",
		"script", r.path, "exit_code", exitCode, "duration", dur,
		"stdout", tail(strings.TrimSpace(stdout.String()), 2048))
	return nil
}

// tail returns the last max bytes of s, prefixed with an ellipsis when it had
// to cut. Hook output is operator-controlled and can be arbitrarily long; the
// end of it is where the error usually is.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !isRuneStart(s[cut]) {
		cut++
	}
	return "…" + s[cut:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
