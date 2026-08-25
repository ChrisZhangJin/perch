package mailtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	plog "github.com/ChrisZhangJin/perch/internal/log"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// Mailtest is an end-to-end perch harness with no network. Construct with
// New, push messages with Send / SendRaw, drive with RunOnce, observe with
// Replies / SeenUIDs. Not safe for concurrent use.
type Mailtest struct {
	mb  *FakeMailbox
	rep *FakeSender
	run app.TaskRunner
	app *app.App
	log *slog.Logger
	seq uint64 // monotonic for synthetic Message-IDs
}

// New builds a Mailtest wired to a real *app.App backed by the fakes. cfg is
// used as-is.
//
// Start cfg from config.Defaults() and override what the test needs:
//
//	cfg := config.Defaults()
//	cfg.MaxPromptBytes = 4096
//	cfg.AgentWorkdir = t.TempDir()
//
// A bare &config.Config{} is a trap. Several fields have non-zero defaults —
// AgentTaskOnly (emits the SAFETY PROTOCOL) is true, PromptContracts is
// "on_resume" — so a struct literal silently configures the opposite of
// production and the harness stops reproducing what perch actually does.
//
// allowFrom is forwarded to gate.New; pass ["*"] for allow-all or a
// literal/regex slice. run may be nil -> a ScriptedRunner that always returns
// ("the answer", "", nil).
func New(cfg *config.Config, allowFrom []string, run app.TaskRunner) (*Mailtest, error) {
	if cfg == nil {
		return nil, fmt.Errorf("mailtest: cfg must not be nil")
	}
	if cfg.AgentWorkdir == "" {
		return nil, fmt.Errorf("mailtest: cfg.AgentWorkdir must not be empty")
	}

	g, err := gate.New(allowFrom)
	if err != nil {
		return nil, fmt.Errorf("mailtest: gate: %w", err)
	}

	sessPath := filepath.Join(cfg.AgentWorkdir, "mailtest-sessions.json")
	sess, err := session.Load(sessPath)
	if err != nil {
		return nil, fmt.Errorf("mailtest: session: %w", err)
	}

	mb := &FakeMailbox{}
	rep := &FakeSender{}
	if run == nil {
		run = &ScriptedRunner{Outs: []string{"the answer"}}
	}

	// Quiet logger so test output stays clean; switch to Debug here if a test
	// needs to see the perch pipeline logs.
	log := slog.New(plog.New(os.Stderr, slog.LevelError))

	a := app.New(cfg, mb, g, sess, run, rep, log)
	return &Mailtest{mb: mb, rep: rep, run: run, app: a, log: log}, nil
}

// Send composes a minimal RFC822 message and queues it into the fake
// mailbox. UID is auto-assigned (monotonic, starts at 1). from must contain
// "@" and no whitespace; to must be non-empty. The minted Message-ID is
// <mtest-<sequence>@mailtest> so it's unique and obviously synthetic.
func (m *Mailtest) Send(from, to, subject, body string) (uint32, error) {
	if from == "" || !strings.Contains(from, "@") || strings.ContainsAny(from, " \t\r\n") {
		return 0, fmt.Errorf("mailtest: invalid from %q", from)
	}
	if to == "" {
		return 0, fmt.Errorf("mailtest: to must not be empty")
	}

	m.seq++
	msgID := fmt.Sprintf("<mtest-%d@mailtest>", m.seq)
	eml := composeRFC822(from, to, subject, msgID, body)

	uid := m.mb.Enqueue(0, eml)
	return uid, nil
}

// SendRaw queues pre-built RFC822 bytes under the given UID (must be > 0;
// UID 0 is reserved for "unassigned"). The harness does NOT enforce
// monotonic ordering across SendRaw calls -- callers that mix Send
// (auto-assigns) and SendRaw (caller-chosen) are responsible for choosing
// UIDs that don't collide with auto-assigned ones. Use this for attachments,
// unusual Content-Types, or to reuse canned .eml fixtures from tests.
func (m *Mailtest) SendRaw(uid uint32, raw []byte) error {
	if uid == 0 {
		return fmt.Errorf("mailtest: SendRaw uid must be > 0 (0 is reserved for unassigned)")
	}
	m.mb.Enqueue(uid, raw)
	return nil
}

// RunOnce calls app.ProcessUnseen once. ctx-cancel aware. Errors propagate
// as-is.
func (m *Mailtest) RunOnce(ctx context.Context) error {
	return m.app.ProcessUnseen(ctx)
}

// Replies returns every outbound reply recorded so far, in send-order.
// Snapshot -- subsequent RunOnce calls don't retroactively change the
// returned slice.
func (m *Mailtest) Replies() []Reply { return m.rep.Replies() }

// SeenUIDs returns every UID passed to MarkSeen, in append-order. Includes
// both whitelisted and rejected sends.
func (m *Mailtest) SeenUIDs() []uint32 {
	m.mb.mu.Lock()
	defer m.mb.mu.Unlock()
	out := make([]uint32, len(m.mb.seen))
	copy(out, m.mb.seen)
	return out
}

// App exposes the underlying *app.App for tests that need to flip config
// fields (e.g. cfg.LongTaskAck = true) after construction. Direct mutation
// of app state outside cfg is not supported.
func (m *Mailtest) App() *app.App { return m.app }

// composeRFC822 builds a minimal RFC822 message with CRLF line endings and
// the four headers the parser looks at (From / To / Subject / Message-ID)
// plus Content-Type. Date is included for realism.
func composeRFC822(from, to, subject, msgID, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Message-ID: %s\r\n", msgID)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}
