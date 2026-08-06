package app

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/message"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// Mailbox is the mail transport the app drives. Implementations must NOT be
// called concurrently: the app alternates FetchUnseen and WaitForActivity on
// one goroutine.
type Mailbox interface {
	FetchUnseen(ctx context.Context) ([]mailbox.Raw, error)
	MarkSeen(ctx context.Context, uid uint32) error
	WaitForActivity(ctx context.Context, timeout time.Duration) error
	Close() error
}

type TaskRunner interface {
	Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, error)
}

type ReplySender interface {
	Reply(to, subject, inReplyTo string, references []string, body string) error
}

type App struct {
	cfg  *config.Config
	mb   Mailbox
	gate *gate.Gate
	sess *session.Registry
	run  TaskRunner
	rep  ReplySender
	log  *slog.Logger
	mu   sync.Mutex // guards ProcessUnseen (defensive; app drives it serially)
}

func New(cfg *config.Config, mb Mailbox, g *gate.Gate, sess *session.Registry, run TaskRunner, rep ReplySender, log *slog.Logger) *App {
	return &App{cfg: cfg, mb: mb, gate: g, sess: sess, run: run, rep: rep, log: log}
}

// ProcessUnseen fetches and handles every currently-unseen message:
// parse -> dedup -> whitelist -> session -> run claude -> reply -> mark seen.
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
		m, err := message.Parse(bytes.NewReader(raw.Data), raw.UID, a.cfg.MaxPromptBytes)
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
		a.log.Info("processing", "from", m.From, "subject", m.Subject, "date", ts.Format(time.RFC3339))
		if !a.gate.FirstSight(m.MessageID) {
			a.log.Debug("dedup skipped", "from", m.From, "message_id", m.MessageID)
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
		prompt := runner.BuildPrompt(m.From, m.Subject, m.Body)
		out, err := a.run.Run(ctx, prompt, sid, isNew)
		if err != nil {
			a.log.Error("claude run failed", "from", m.From, "err", err)
			_ = a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID),
				"Sorry, the task failed to complete: "+err.Error())
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		if err := a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID), out); err != nil {
			a.log.Error("reply failed", "from", m.From, "err", err)
			continue // leave unseen so a later poll retries the reply
		}
		_ = a.mb.MarkSeen(ctx, m.UID)
		a.log.Info("task done", "from", m.From, "subject", m.Subject, "session", sid, "resumed", !isNew, "message_id", m.MessageID)
	}
	return nil
}

// Run alternates processing and IDLE-with-timeout on a single connection:
// IDLE returns early on new mail (near-real-time), or after PollInterval as a
// safety poll. No concurrent use of the mailbox connection.
func (a *App) Run(ctx context.Context) error {
	for {
		if err := a.ProcessUnseen(ctx); err != nil {
			a.log.Error("process failed", "err", err)
			if !sleep(ctx, 5*time.Second) {
				return a.shutdown()
			}
			continue
		}
		if ctx.Err() != nil {
			return a.shutdown()
		}
		if err := a.mb.WaitForActivity(ctx, a.cfg.PollInterval); err != nil {
			if ctx.Err() != nil {
				return a.shutdown()
			}
			a.log.Warn("idle wait failed; falling back to poll", "err", err)
			if !sleep(ctx, a.cfg.PollInterval) {
				return a.shutdown()
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
