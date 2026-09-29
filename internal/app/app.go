package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/hook"
	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/message"
	"github.com/ChrisZhangJin/perch/internal/replier"
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
	// Run returns (reply, nativeID, err). nativeID is non-empty only when
	// the agent minted its own session id (e.g. nanopi on IsNew=true with
	// --session omitted) and the caller should adopt that id for future
	// resumes; empty means "keep your current perch-invented UUID".
	Run(ctx context.Context, prompt, sessionID string, isNew bool) (string, string, error)
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
	rate     *replyRate   // per-thread reply cap; see loopguard.go
	hook     *hook.Runner // optional on-email script; nil when unconfigured
	jev      *jev.Client  // optional duration classifier; nil unless configured
	mu       sync.Mutex   // guards ProcessUnseen (defensive; app drives it serially)
	// replyFailures counts consecutive SMTP failures per Message-ID, so the
	// "leave it unseen and retry next poll" path is bounded instead of
	// infinite. Cleared on success and on giving up. Only touched from
	// ProcessUnseen, which holds mu for its whole body.
	replyFailures map[string]int
	// lastReplyAttempts is the attempt count of the most recent Reply, fed by
	// the hook installed in New. The failure notice used to hardcode "3
	// attempt(s)", which on 2026-09-29 told the sender (and the operator
	// reading the mail) that perch had tried three times when the single 535
	// had ended it after one. Same locking note as replyFailures.
	lastReplyAttempts int
}

// maxReplyAttempts bounds how many polls may retry one message's reply before
// perch gives up, notifies the sender and marks the mail seen.
const maxReplyAttempts = 3

// New wires an App. The optional on-email hook is built here from cfg rather
// than passed in, so every construction path — production, --testmode, the
// mailtest harness — gets identical hook behaviour with no call-site change.
// hook.New returns nil when cfg.OnEmailHook is empty and every hook method is
// nil-safe, so "no hook configured" needs no branch downstream.
func New(cfg *config.Config, mb Mailbox, g *gate.Gate, sess *session.Registry, run TaskRunner, rep ReplySender, log *slog.Logger, triggers ...mailbox.Trigger) *App {
	if len(triggers) == 0 {
		triggers = []mailbox.Trigger{mailbox.TimerTrigger{Interval: cfg.PollInterval}}
	}
	h := hook.New(cfg.OnEmailHook, cfg.AgentWorkdir, cfg.HookTimeout, log)
	if h != nil && log != nil {
		log.Info("on-email hook enabled", "script", h.Path(), "timeout", h.Timeout())
	}
	// Duration classifier. Built here from cfg for the same reason the hook
	// is: every construction path gets identical behaviour with no
	// call-site change. jev.New returns nil without a key, and a nil client
	// means the agent probe — so "not configured" needs no branch beyond
	// the one in classifyTask.
	var jc *jev.Client
	if cfg.Classifier == config.ClassifierJev || cfg.EndDetect {
		if jc = jev.New(cfg.JevAPIKey, cfg.JevAPIURL, cfg.JevModel, cfg.JevTimeout, log); jc != nil && log != nil {
			log.Info("jev enabled", "model", jc.Model(), "timeout", cfg.JevTimeout,
				"classifier", cfg.Classifier == config.ClassifierJev, "end_detect", cfg.EndDetect)
		} else if log != nil {
			log.Warn("jev is enabled but TYPESAFE_API_KEY is unset; " +
				"the duration probe falls back to the agent and end-detection is inert")
		}
	}
	a := &App{cfg: cfg, mb: mb, gate: g, sess: sess, run: run, rep: rep, log: log,
		triggers: triggers, rate: newReplyRate(time.Hour), hook: h, jev: jc,
		replyFailures: make(map[string]int)}
	// Record how many SMTP attempts each Reply actually made, so the failure
	// notice reports the truth. Optional, like FailureNotifier: senders that
	// don't retry (testmode's InjectSender) simply don't implement it.
	if ah, ok := rep.(AttemptHooker); ok {
		ah.SetHook(func(attempt int, _ error, _ int) { a.lastReplyAttempts = attempt })
	}
	return a
}

// SessForTest returns the session registry wired into this App. Test-only;
// production code does not need this accessor because it works through the
// ProcessUnseen loop.
func (a *App) SessForTest() *session.Registry { return a.sess }

// Cfg exposes the config so tests can flip fields (e.g. cfg.LongTaskAck)
// after construction.
func (a *App) Cfg() *config.Config { return a.cfg }

// SetMailboxForTest swaps the inbound mailbox. Test-only; production code
// never calls this because the mailbox is set at New time. Used by the
// --testmode HTTP server (cmd/perch) to inject messages into the same App
// without going through IMAP.
func (a *App) SetMailboxForTest(mb Mailbox) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mb = mb
}

// SetReplySenderForTest swaps the outbound reply sender. Test-only; used
// by --testmode to capture replies before they hit SMTP.
func (a *App) SetReplySenderForTest(rep ReplySender) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rep = rep
}

// ReplySenderForTest returns the current outbound reply sender. Test-only;
// used by --testmode to wrap the real sender with InjectSender so replies
// are captured in flight while still hitting SMTP.
func (a *App) ReplySenderForTest() ReplySender {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rep
}

// MailboxForTest returns the current inbound mailbox. Test-only; used by
// --testmode to recover the QueuedMailbox wired by buildApp.
func (a *App) MailboxForTest() Mailbox {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mb
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
	a.rate.prune()
	for _, raw := range raws {
		m, err := message.Parse(bytes.NewReader(raw.Data), raw.UID, a.cfg.MaxAttachmentBytes)
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
		// --- loop protection -------------------------------------------
		// All three checks sit after the whitelist and before any agent work,
		// so a loop costs a log line rather than an agent invocation.

		// Self-addressed mail. Never legitimate, and not configurable: a
		// bounce, a Cc back to ourselves, or a whitelist wide enough to match
		// our own domain would otherwise have perch answering itself.
		if a.cfg.Email != "" && strings.EqualFold(m.From, a.cfg.Email) {
			a.log.Warn("skipping mail from our own address (loop guard)",
				"from", m.From, "subject", m.Subject)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		if a.cfg.SkipAutomated {
			if automated, why := m.IsAutomated(); automated {
				a.log.Warn("skipping machine-generated mail (loop guard)",
					"from", m.From, "subject", m.Subject, "reason", why)
				_ = a.mb.MarkSeen(ctx, m.UID)
				continue
			}
		}
		// Cc-only mail. Being copied is how a human says "for your
		// awareness" — the request is someone else's, and answering it is
		// both noise and a way to end up doing another agent's work.
		//
		// Checked here, before rate.Allow and before the on-email hook,
		// because "ignored" has to mean ignored: a mail we never accepted
		// must not consume one of the thread's hourly reply slots, and the
		// hook's contract is that it sees mail perch ACCEPTS.
		//
		// Absent from both lists is NOT cc-only: Bcc, a mailing list, and
		// most forwarding setups all deliver with perch's address in
		// neither header, and those are ordinary mail addressed to us. Only
		// an explicit "you are in Cc, someone else is in To" is ignored,
		// which also means a missing/unparseable To header fails open.
		role := recipientRole(m, a.cfg.Email)
		// One line per email naming which of the four shapes it was. The
		// addresses themselves stay at DEBUG: the counts are what a reader
		// scanning for "why was that ignored?" needs, and echoing every
		// recipient of every email is how a log stops being read at all.
		a.log.Info("recipients decision", "role", role,
			"to_count", len(m.To), "cc_count", len(m.Cc),
			"from", m.From, "subject", m.Subject, "message_id", m.MessageID)
		a.log.Debug("recipients", "role", role, "to", strings.Join(m.To, ","),
			"cc", strings.Join(m.Cc, ","), "self", a.cfg.Email,
			"message_id", m.MessageID)
		if role == roleCcOnly {
			a.log.Info("cc only; ignoring", "from", m.From, "subject", m.Subject,
				"message_id", m.MessageID)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		if ok, n := a.rate.Allow(m.ThreadRoot(), a.cfg.MaxRepliesPerHour); !ok {
			a.log.Warn("thread reply cap reached; not replying (loop guard)",
				"from", m.From, "subject", m.Subject, "thread", m.ThreadRoot(),
				"replies_last_hour", n, "cap", a.cfg.MaxRepliesPerHour)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		// ---------------------------------------------------------------

		sid, isNew, err := a.sess.Resolve(m.ThreadRoot())
		if err != nil {
			a.log.Error("session resolve failed", "err", err)
			continue
		}

		// On-email hook (opt-in, cfg.OnEmailHook). Fired here — past the
		// whitelist and the loop guards, past session resolution, before any
		// agent work — for three reasons: the script only ever sees mail from
		// trusted senders; isNew is what tells it new_thread vs reply_thread;
		// and m.Body is still the body as received, before the quote-strip and
		// truncation done for the prompt below.
		//
		// The hook is an observer. A missing script, a non-zero exit or a
		// timeout is logged and the email proceeds exactly as it would have
		// otherwise — an operator's recording script must never be able to
		// stop perch answering mail.
		if err := a.hook.Fire(ctx, hook.Event{
			EmailID:   m.MessageID,
			Subject:   m.Subject,
			Body:      m.Body,
			Sender:    m.From,
			NewThread: isNew,
		}); err != nil {
			a.log.Warn("on-email hook failed; continuing",
				"from", m.From, "subject", m.Subject, "err", err)
		}

		// End-of-conversation check (opt-in, cfg.EndDetect). Placed here —
		// after the whitelist and the loop guards, after the hook has
		// recorded the mail, before attachments are staged and before any
		// agent work — so a "thanks, got it" costs one Jev call instead of
		// an agent run, while an operator's recording hook still sees every
		// email it saw before.
		//
		// The session is deliberately left untouched: a later message in
		// this thread must still resume where the agent left off. Note the
		// reply-rate slot was already consumed by rate.Allow above; not
		// refunding it is the conservative reading and keeps the loop guard
		// the single authority on its own counters.
		if a.conversationEnded(ctx, m, isNew) {
			_ = a.mb.MarkSeen(ctx, m.UID)
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

		// Duration probe: when cfg.LongTaskAck is on, ask whether this task
		// will run long. If long, send an interim ack email so the human
		// isn't left wondering while the real run works. Probe failures are
		// logged and swallowed — a bad probe must never block the real reply.
		//
		// Off by default. See config.LongTaskAck and classifyTask.
		if a.cfg.LongTaskAck {
			if runtime, etaMin, ok := a.classifyTask(ctx, m); ok && runtime == "long" {
				ack := BuildLongAckBody(m.FromName,
					runner.AgentDisplayName(a.cfg.Email), etaMin,
					looksChinese(failureLanguageSample(m.Subject, m.Body)))
				if err := a.rep.Reply(m.From, m.Subject, m.MessageID,
					appendRef(m.References, m.MessageID), ack, nil); err != nil {
					a.log.Warn("long-task ack send failed; continuing to run task",
						"from", m.From, "err", err)
				}
			}
		}

		originalBody := m.Body
		// Body transformations, in this order. Stripping must come before the
		// size cap: applied the other way round, a long thread's quoted
		// history consumes MaxPromptBytes and the sender's actual request is
		// what gets truncated away.
		//
		// Both are keyed on `cold`, not on isNew, so the cold-retry path
		// below reruns them with the right answer: a fresh session has no
		// history, so the quote may be the only context it gets.
		applyBody := func(cold bool) {
			body := m.Body
			if a.cfg.StripQuotedFor(cold) {
				stripped, removed := message.StripQuoted(body)
				if removed > 0 {
					a.log.Debug("stripped quoted history",
						"from", m.From, "removed_bytes", removed, "kept_bytes", len(stripped))
					body = stripped
				}
			}
			m.Body = message.TruncateUTF8(body, a.cfg.MaxPromptBytes)
		}
		applyBody(isNew)

		// buildPrompt is a closure so the cold-retry path below can rebuild
		// with contracts forced on. Passing cold=true means "this run opens
		// a fresh agent session", which is what decides whether the format
		// contracts must be included.
		buildPrompt := func(cold bool) string {
			return runner.BuildPrompt(m.From, m.FromName, m.Subject, m.Body, saved, rpDir,
				a.cfg.Email, a.cfg.AgentWorkdir, runner.PromptOpts{
					TaskOnly:  a.cfg.AgentTaskOnly,
					Contracts: a.cfg.ContractsFor(cold),
					To:        m.To,
					Cc:        m.Cc,
				})
		}

		// sidUnconfirmed tracks whether sid names a session an agent has
		// actually taken a turn in. Resolve persists a minted id BEFORE the
		// agent runs, so on first sight the id is a promise, not a fact; the
		// stale id on the session-lost path below is worse than unconfirmed,
		// it is known-dead. Either way, a run that then fails must not leave
		// that id in the registry — see Registry.Forget.
		sidUnconfirmed := isNew

		out, nativeID, err := a.run.Run(ctx, buildPrompt(isNew), sid, isNew)
		// A resume can fail because the stored session id no longer maps to
		// a live agent session. The thread is still serviceable from a cold
		// session, but the prompt we just sent was built for a warm one and
		// may omit the format contracts — so rebuild before retrying, or the
		// agent starts fresh never having seen the ATTACHMENT PROTOCOL and
		// silently drops file replies.
		if errors.Is(err, runner.ErrSessionLost) && !isNew {
			a.log.Warn("session lost; retrying with a cold session and full contracts",
				"from", m.From, "stale_sid", sid)
			sidUnconfirmed = true
			// Rebuild the body too: a cold session has no history, so quoted
			// text stripped for the resume has to come back.
			m.Body = originalBody
			applyBody(true)
			out, nativeID, err = a.run.Run(ctx, buildPrompt(true), "", true)
		}
		if err != nil {
			// Drop an unconfirmed session id: nothing ever ran in it, so
			// keeping it costs the NEXT email in this thread a doomed resume
			// (spawn → "session lost" → cold retry) before it recovers.
			if sidUnconfirmed {
				if ferr := a.sess.Forget(m.ThreadRoot()); ferr != nil {
					a.log.Warn("session forget failed", "from", m.From, "err", ferr)
				}
			}
			// The operator gets the real error here, in the log. The sender
			// gets an out-of-office notice: the raw error is meaningless to
			// them and leaks binary names, paths and stderr. See failure.go.
			a.log.Error("agent run failed", "from", m.From, "err", err)
			zh := looksChinese(failureLanguageSample(m.Subject, originalBody))
			_ = a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID),
				BuildAgentFailureBody(m.FromName, runner.AgentDisplayName(a.cfg.Email), zh), nil)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		// Adopt the agent's native session id when the runner hands one back.
		// Today this only happens for nanopi on IsNew=true (where we omitted
		// --session and let nanopi mint its own UUIDv7); future agents that
		// pick their own id will plug in the same way.
		if nativeID != "" && nativeID != sid {
			if err := a.sess.Replace(m.ThreadRoot(), nativeID); err != nil {
				a.log.Warn("session replace failed", "from", m.From, "err", err)
			} else {
				a.log.Debug("adopted native session id",
					"from", m.From, "old", sid, "new", nativeID)
			}
			// Track the adopted id locally even if Replace failed: the agent
			// IS on nativeID now, so that is what a retry must resume and
			// what "task done" should report. A failed Replace only means the
			// registry still holds the old id, which the next email recovers
			// from via the ErrSessionLost path above.
			sid = nativeID
		}
		// Greeting protocol: the agent must open its reply with a salutation
		// line (Hi/Hello + name, or Hi there for name-less senders).
		// Anything written before the greeting — audit reports,
		// classification narratives, tool-call notes — is silently dropped
		// here and never reaches the human. This is enforced as a hard
		// contract on both sides: the prompt in BuildPrompt tells the
		// agent that pre-greeting content will be discarded (so it
		// doesn't waste tokens on it), and this scanner enforces it on
		// receipt.
		greeted, gerr := ExtractBodyAfterGreeting(out, m.FromName)
		if gerr != nil {
			a.log.Warn("agent stdout missing greeting; sending as-is",
				"from", m.From, "subject", m.Subject, "preview", preview(out))
		}
		// Degenerate-reply guard: a run whose body is only the mandatory
		// greeting (or nothing) terminated before doing the task — observed
		// 2026-08-12 when a weak model opened a resumed thread with "Hi
		// Chris," as a tool-call-free message and ended its turn on the spot.
		// Never email a bare greeting: retry once (a second spawn usually
		// does the work), then give up with an explicit failure notice
		// rather than shipping an empty reply.
		if IsDegenerateReply(greeted) {
			a.log.Warn("agent produced a greeting-only reply; retrying once",
				"from", m.From, "subject", m.Subject, "preview", preview(out))
			retrySid := sid // already the adopted id when the agent minted one
			// cold=false: retrySid names a session the agent just took a turn
			// in, so the contracts are already in its history.
			if out2, nativeID2, rerr := a.run.Run(ctx, buildPrompt(false), retrySid, false); rerr != nil {
				a.log.Error("agent retry failed", "from", m.From, "err", rerr)
			} else {
				out = out2
				if nativeID2 != "" && nativeID2 != retrySid {
					if err := a.sess.Replace(m.ThreadRoot(), nativeID2); err != nil {
						a.log.Warn("session replace failed", "from", m.From, "err", err)
					}
				}
				greeted, gerr = ExtractBodyAfterGreeting(out, m.FromName)
				if gerr != nil {
					a.log.Warn("agent stdout missing greeting after retry; sending as-is",
						"from", m.From, "subject", m.Subject, "preview", preview(out))
				}
			}
			if IsDegenerateReply(greeted) {
				a.log.Error("agent produced a greeting-only reply twice; not sending bare greeting",
					"from", m.From, "subject", m.Subject)
				zh := looksChinese(failureLanguageSample(m.Subject, originalBody))
				_ = a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID),
					BuildNoReplyBody(m.FromName, runner.AgentDisplayName(a.cfg.Email), zh), nil)
				_ = a.mb.MarkSeen(ctx, m.UID)
				continue
			}
		}
		files, err := collectReplyFiles(rpDir)
		if err != nil {
			a.log.Warn("reply file collect failed", "err", err)
		}
		if err := a.rep.Reply(m.From, m.Subject, m.MessageID, appendRef(m.References, m.MessageID), greeted, files); err != nil {
			// The answer exists; only the handoff to SMTP failed. Retry it on
			// a later poll — but that only works if we also drop the dedup
			// record, or FirstSight bounces the message on the next fetch and
			// the mail sits unseen in INBOX forever (2026-09-29, a 535 from
			// 163). Bounded by maxReplyAttempts so a genuinely dead SMTP path
			// does not re-run the agent on every poll for the rest of time.
			n := a.replyFailures[m.MessageID] + 1
			a.replyFailures[m.MessageID] = n
			if n < maxReplyAttempts {
				a.gate.ForgetSight(m.MessageID)
				a.log.Warn("reply send failed; leaving unseen to retry on the next poll",
					"to", m.From, "subject", m.Subject, "failures", n,
					"max", maxReplyAttempts, "err", err)
				continue
			}
			// Out of retries: tell the sender, and mark seen so the mail stops
			// accumulating unseen in INBOX.
			a.logSendFailure("reply failed", m, err)
			a.notifyReplyFailure(m, out, files, err)
			delete(a.replyFailures, m.MessageID)
			_ = a.mb.MarkSeen(ctx, m.UID)
			continue
		}
		delete(a.replyFailures, m.MessageID) // sent; don't let the map grow
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

// AttemptHooker is an optional extension of ReplySender: a sender that
// retries internally can report how many attempts each Reply took, which is
// what the failure notice quotes. Satisfied by *replier.Replier; senders that
// never retry (testmode's InjectSender) leave it unimplemented and the notice
// falls back to the configured maximum.
type AttemptHooker interface {
	SetHook(fn func(attempt int, err error, msgSize int))
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
	// The real attempt count, recorded by the hook installed in New. Zero
	// means the sender doesn't report attempts at all, so quote the cap.
	attempts := a.lastReplyAttempts
	if attempts < 1 {
		attempts = maxReplyAttempts
	}
	if err := n.NotifyFailure(m.From, m.Subject, m.MessageID,
		appendRef(m.References, m.MessageID), attempts, lastErr, msgSize); err != nil {
		a.logSendFailure("failure notification send failed", m, err)
	}
}

// logSendFailure logs an outbound SMTP failure. On an auth rejection the
// message's sender/recipient are irrelevant — the credential is what the
// server refused — so it logs the auth account (cfg.Email, the SMTP username)
// and omits from/to. Any other failure logs the recipient so an operator can
// see which reply didn't go out. errors.Is matches replier.ErrAuth through the
// wrap chain.
func (a *App) logSendFailure(msg string, m *message.Message, err error) {
	if errors.Is(err, replier.ErrAuth) {
		a.log.Error(msg, "account", a.cfg.Email, "err", err)
		return
	}
	a.log.Error(msg, "to", m.From, "err", err)
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
// replyDir returns the absolute path of the outbound staging directory.
//
// Absolute matters twice. The path goes into the ATTACHMENT PROTOCOL, and a
// bare "reply" (what filepath.Join gives for a workdir of ".") reads as
// ambiguous to a model that cannot know what "." resolves to — the SAFETY
// section already resolves workdir for the same reason. It also decouples
// perch's own cwd from the agent's: perch creates and scans this directory
// while the agent writes into cmd.Dir, and with a relative path those only
// agree as long as perch was launched from the workdir.
func replyDir(workdir string) string {
	dir := filepath.Join(workdir, "reply")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
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

// collectReplyFiles returns the paths of every file staged in the reply dir.
// Top-level files are collected as-is; each top-level subdirectory is packed
// into a sibling <name>.tar.gz so a single email can carry a folder tree.
// The tarballs are written into dir itself, so cleanReplyDir sweeps them.
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
			tarPath, err := packDirToTarGz(filepath.Join(dir, e.Name()), dir)
			if err != nil {
				return nil, err
			}
			files = append(files, tarPath)
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files, nil
}

// packDirToTarGz writes a gzipped tar of srcDir into outDir as
// <base(srcDir)>.tar.gz and returns the tarball path. Entry names inside
// the archive are relative to srcDir's parent so extraction reproduces the
// original folder name at the root.
func packDirToTarGz(srcDir, outDir string) (string, error) {
	base := filepath.Base(srcDir)
	outPath := filepath.Join(outDir, base+".tar.gz")
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	parent := filepath.Dir(srcDir)
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(parent, path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, src)
		src.Close()
		return copyErr
	})
	if err != nil {
		return "", err
	}
	return outPath, nil
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

// Recipient roles, as reported on the "recipients decision" log line. Named
// constants rather than bare strings because the log is the only place the
// cc-only rule is visible — an operator whose mail went unanswered searches
// for the role, so the four values have to be stable.
const (
	roleSoleTo      = "sole_to"        // the only address in To: ordinary mail
	roleToWithOther = "to_with_others" // in To alongside others: process, but scoped
	roleCcOnly      = "cc_only"        // copied, not addressed: ignored entirely
	roleBccOrList   = "bcc_or_list"    // in neither header: Bcc, list, forward
)

// recipientRole classifies where the agent's own address appears in an
// email's To/Cc. selfAddr is cfg.Email; an empty one (or an unparseable To)
// can only yield bcc_or_list, which is the fail-open direction — perch that
// does not know its own address must not start ignoring mail.
func recipientRole(m *message.Message, selfAddr string) string {
	inTo := message.HasRecipient(m.To, selfAddr)
	switch {
	case inTo && len(m.To) == 1 && len(m.Cc) == 0:
		return roleSoleTo
	case inTo:
		return roleToWithOther
	case len(m.To) > 0 && message.HasRecipient(m.Cc, selfAddr):
		return roleCcOnly
	default:
		return roleBccOrList
	}
}
