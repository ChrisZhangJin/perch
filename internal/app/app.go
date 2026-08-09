package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/message"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// Mailbox is the mail transport the app drives. It exposes only commands;
// wake/sleep semantics live in Trigger implementations, which the app fans
// in on a single goroutine and dispatches to ProcessUnseen.
type Mailbox interface {
	FetchUnseen(ctx context.Context) ([]mailbox.Raw, error)
	MarkSeen(ctx context.Context, uid uint32) error
	Close() error
}

type TaskRunner interface {
	Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, error)
}

// ReplySender mails the agent's answer back. attachments lists files staged
// in the reply/ dir by the agent; when non-empty the message is multipart.
type ReplySender interface {
	Reply(to, subject, inReplyTo string, references []string, body string, attachments []string) error
}

type App struct {
	cfg      *config.Config
	mb       Mailbox
	gate     *gate.Gate
	sess     *session.Registry
	run      TaskRunner
	rep      ReplySender
	log      *slog.Logger
	triggers []mailbox.Trigger
	mu       sync.Mutex // guards ProcessUnseen (defensive; app drives it serially)
}

func New(cfg *config.Config, mb Mailbox, g *gate.Gate, sess *session.Registry, run TaskRunner, rep ReplySender, log *slog.Logger, triggers ...mailbox.Trigger) *App {
	if len(triggers) == 0 {
		triggers = []mailbox.Trigger{mailbox.TimerTrigger{Interval: cfg.PollInterval}}
	}
	return &App{cfg: cfg, mb: mb, gate: g, sess: sess, run: run, rep: rep, log: log, triggers: triggers}
}

// ProcessUnseen fetches and handles every currently-unseen message:
// parse -> dedup -> whitelist -> session -> run claude -> reply -> mark seen.
// Inbound attachments are saved under <workdir>/attachments/<msg-id>/ and the
// agent is pointed at them; files the agent writes into <workdir>/reply/ are
// attached to the reply email.
func (a *App) ProcessUnseen(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	raws, err := a.mb.FetchUnseen(ctx)
	if err != nil {
		return err
	}
	if len(raws) > 0 {
		a.log.Info("received", "count", len(raws))
	}
	for _, raw := range raws {
		m, err := message.Parse(bytes.NewReader(raw.Data), raw.UID, a.cfg.MaxPromptBytes, a.cfg.MaxAttachmentBytes)
		if err != nil {
			a.log.Error("parse failed", "uid", raw.UID, "err", err)
			_ = a.mb.MarkSeen(ctx, raw.UID)
			continue
		}
		// Log every parsed message with the sender-visible timestamp (Date
		// header) so the operator can correlate a perch event with when the
		// human actually sent it. If Date is missing/unparseable we fall back
		// to the fetch time.
		ts := m.Date
		if ts.IsZero() {
			ts = time.Now()
		}
		a.log.Info("processing", "from", m.From, "subject", m.Subject, "attachments", len(m.Attachments), "date", ts.Format(time.RFC3339))
		if !a.gate.FirstSight(m.MessageID) {
			a.log.Info("dedup skipped (same thread, later reply will handle)",
				"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
			continue // duplicate delivery within this run
		}
		if !a.gate.Allowed(m.From) {
			a.log.Warn("rejected sender", "from", m.From, "subject", m.Subject, "message_id", m.MessageID)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		sid, isNew, err := a.sess.Resolve(m.ThreadRoot())
		if err != nil {
			a.log.Error("session resolve failed", "err", err)
			continue
		}

		// Inbound attachments: persist so the agent can read them with its
		// file tools. Failure is non-fatal — the task still runs on the body.
		saved, err := saveAttachments(a.cfg.AgentWorkdir, m)
		if err != nil {
			a.log.Warn("attachment save failed", "from", m.From, "err", err)
		}

		// Outbound staging dir: the agent writes files here to send back.
		rpDir := replyDir(a.cfg.AgentWorkdir)
		if err := os.MkdirAll(rpDir, 0o755); err != nil {
			a.log.Warn("reply dir create failed", "err", err)
		}

		prompt := runner.BuildPrompt(m.From, m.Subject, m.Body, saved, rpDir)
		out, err := a.run.Run(ctx, prompt, sid, isNew)
		if err != nil {
			a.log.Error("agent run failed", "from", m.From, "err", err)
			_ = a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID),
				"Sorry, the task failed to complete: "+err.Error(), nil)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		files, err := collectReplyFiles(rpDir)
		if err != nil {
			a.log.Warn("reply file collect failed", "err", err)
		}
		if err := a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID), out, files); err != nil {
			a.log.Error("reply failed", "from", m.From, "err", err)
			a.notifyReplyFailure(m, out, files, err)
			continue // leave unseen so a later poll retries the reply
		}
		if len(files) > 0 {
			_ = cleanReplyDir(rpDir)
		}
		_ = a.mb.MarkSeen(ctx, m.UID)
		a.log.Info("task done", "from", m.From, "subject", m.Subject, "session", sid, "resumed", !isNew, "files", len(files), "message_id", m.MessageID)
	}
	return nil
}

// FailureNotifier is an optional extension of ReplySender. If implemented,
// perch sends a "Perch failed:" notification to the original sender after
// the reply has exhausted its retries, so the human can see the failure
// instead of waiting forever for a reply that will never come.
type FailureNotifier interface {
	NotifyFailure(to, subject, inReplyTo string, references []string, attempts int, lastErr error, msgSize int) error
}

// notifyReplyFailure sends the optional failure notification if the reply
// sender implements it. Failure-to-notify is itself logged but never blocks
// the loop — we've already exhausted retries on the real reply.
func (a *App) notifyReplyFailure(m *message.Message, body string, attachments []string, lastErr error) {
	n, ok := a.rep.(FailureNotifier)
	if !ok {
		return
	}
	// Estimate reply size for the report (best-effort; matches what was attempted).
	msgSize := len(body)
	for _, p := range attachments {
		if info, err := os.Stat(p); err == nil {
			msgSize += int(info.Size())
		}
	}
	if err := n.NotifyFailure(m.From, m.Subject, m.MessageID,
		appendRef(m.References, m.MessageID), 3, lastErr, msgSize); err != nil {
		a.log.Error("failure notification send failed", "from", m.From, "err", err)
	}
}

// Run drives a main loop that fans in N trigger goroutines (one per
// trigger) and calls ProcessUnseen on every coalesced wake. Each trigger
// blocks in Wait until either its condition fires or the context cancels;
// it then signals a shared size-1 wake channel (drain semantics: extra
// wakes while a ProcessUnseen is in flight are coalesced into one). On a
// trigger error we log and back off.
func (a *App) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)
	for _, t := range a.triggers {
		go func(t mailbox.Trigger) {
			for {
				if err := t.Wait(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					a.log.Warn("trigger wait failed", "err", err)
					if !sleep(ctx, a.cfg.PollInterval) { // backoff, ctx-aware
						return
					}
					continue
				}
				select {
				case wake <- struct{}{}:
				default:
				}
				if ctx.Err() != nil {
					return
				}
			}
		}(t)
	}

	for {
		select {
		case <-ctx.Done():
			return a.shutdown()
		case <-wake:
			if err := a.ProcessUnseen(ctx); err != nil {
				a.log.Error("process failed", "err", err)
				if !sleep(ctx, 5*time.Second) {
					return a.shutdown()
				}
			}
		}
	}
}

// shutdown closes the mailbox on expected (ctx-cancelled) exit. A close error
// during teardown is cosmetic (e.g. IMAP connection desync on LOGOUT), so it is
// logged at debug and not propagated as a fatal run error.
func (a *App) shutdown() error {
	a.log.Info("shutting down")
	if err := a.mb.Close(); err != nil {
		a.log.Debug("mailbox close", "err", err)
	}
	return nil
}

// sleep waits d or until ctx is done. Returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func appendRef(refs []string, id string) []string {
	out := append([]string{}, refs...)
	if id != "" {
		out = append(out, id)
	}
	return out
}

// replyDir is the single staging directory an agent writes files into to have
// them attached to its reply email.
func replyDir(workdir string) string {
	return filepath.Join(workdir, "reply")
}

// saveAttachments writes every inbound attachment to
// <workdir>/attachments/<sanitized-message-id>/<sanitized-name> and returns
// the absolute paths. The FROM whitelist is perch's trust boundary, but
// sanitizing both the filename (done at parse) and the directory id keeps an
// attacker-controlled header from escaping the attachments tree.
func saveAttachments(workdir string, m *message.Message) ([]string, error) {
	if len(m.Attachments) == 0 {
		return nil, nil
	}
	dir := filepath.Join(workdir, "attachments", sanitizeDirName(m.MessageID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(m.Attachments))
	for _, att := range m.Attachments {
		p := filepath.Join(dir, att.Name)
		if err := os.WriteFile(p, att.Data, 0o600); err != nil {
			return paths, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// sanitizeDirName turns an arbitrary Message-ID into a safe directory name.
func sanitizeDirName(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "<>"))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "msg"
	}
	return out
}

// collectReplyFiles returns the paths of every file staged in the reply dir
// (top-level only; subdirectories are ignored to avoid surprises).
func collectReplyFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files, nil
}

// cleanReplyDir removes everything under the reply dir after a successful
// send, so the next task starts empty.
func cleanReplyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
