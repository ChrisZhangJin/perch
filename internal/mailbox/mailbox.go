package mailbox

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ChrisZhangJin/perch/internal/config"
)

// Raw is a fetched, unparsed message.
type Raw struct {
	UID  uint32
	Data []byte // full RFC822 message bytes
}

// IMAPMailbox is a single-connection IMAP client. It is NOT safe to call
// FetchUnseen/MarkSeen concurrently with WaitForActivity — the app drives them
// in a strict alternation on one goroutine.
type IMAPMailbox struct {
	c   *imapclient.Client
	cfg *config.Config

	idleSupported bool

	mu         sync.Mutex
	onActivity func()
}

// IdleSupported reports whether the server advertised the IMAP IDLE capability.
// Some providers (e.g. 163) do not, in which case perch runs in poll-only mode.
func (m *IMAPMailbox) IdleSupported() bool { return m.idleSupported }

// Dial connects with implicit TLS to imapAddr (resolved by the provider
// registry in cmd/perch/main.go), sends the IMAP ID command (163 requires it),
// logs in with the authorization code, and selects INBOX.
func Dial(cfg *config.Config, imapAddr string) (*IMAPMailbox, error) {
	m := &IMAPMailbox{cfg: cfg}
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{InsecureSkipVerify: cfg.TLSInsecure}, //nolint:gosec // dev/test-only, gated by TLS_INSECURE_SKIP_VERIFY
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					m.signal()
				}
			},
		},
	}
	c, err := imapclient.DialTLS(imapAddr, opts)
	if err != nil {
		return nil, err
	}
	m.c = c

	// 163 rejects sessions ("Unsafe Login") unless the client sends ID first.
	if _, err := c.ID(&imap.IDData{Name: "perch", Version: "0.1"}).Wait(); err != nil {
		// Non-fatal: some servers do not require/allow ID. Continue to login.
		_ = err
	}
	if err := c.Login(cfg.Email, cfg.AuthCode).Wait(); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		c.Close()
		return nil, err
	}

	// Detect IDLE support. 163 does not support IDLE and desyncs the connection
	// if we send it, so we must know up front and stay in poll-only mode there.
	caps := c.Caps()
	if len(caps) == 0 {
		if fetched, err := c.Capability().Wait(); err == nil {
			caps = fetched
		}
	}
	m.idleSupported = caps.Has(imap.CapIdle)

	return m, nil
}

func (m *IMAPMailbox) signal() {
	m.mu.Lock()
	f := m.onActivity
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

// FetchUnseen returns all messages in INBOX without the \Seen flag.
func (m *IMAPMailbox) FetchUnseen(ctx context.Context) ([]Raw, error) {
	data, err := m.c.UIDSearch(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	set := imap.UIDSetNum(uids...)
	opts := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	msgs, err := m.c.Fetch(set, opts).Collect()
	if err != nil {
		return nil, err
	}
	out := make([]Raw, 0, len(msgs))
	for _, msg := range msgs {
		for _, bs := range msg.BodySection {
			out = append(out, Raw{UID: uint32(msg.UID), Data: bs.Bytes})
			break // first (whole-body) section only
		}
	}
	return out, nil
}

// MarkSeen adds the \Seen flag to a message by UID.
func (m *IMAPMailbox) MarkSeen(ctx context.Context, uid uint32) error {
	set := imap.UIDSetNum(imap.UID(uid))
	store := &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagSeen},
	}
	return m.c.Store(set, store, nil).Close()
}

// WaitForActivity runs IDLE and returns when the server reports new mail, when
// timeout elapses (safety poll), or when ctx is cancelled — whichever first.
func (m *IMAPMailbox) WaitForActivity(ctx context.Context, timeout time.Duration) error {
	// Poll-only mode (server has no IDLE, e.g. 163): just wait out the interval.
	// Never send IDLE here — on 163 it errors and corrupts the connection.
	if !m.idleSupported {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		return nil
	}

	got := make(chan struct{}, 1)
	m.mu.Lock()
	m.onActivity = func() {
		select {
		case got <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.onActivity = nil
		m.mu.Unlock()
	}()

	idle, err := m.c.Idle()
	if err != nil {
		return err
	}
	defer idle.Close()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-got:
	case <-timer.C:
	}
	return nil
}

func (m *IMAPMailbox) Close() error {
	// Best-effort graceful LOGOUT (bounded), then close the connection. Errors
	// here are teardown noise (the server may already be gone) — the caller
	// treats an expected-shutdown close as non-fatal.
	done := make(chan struct{})
	go func() { _ = m.c.Logout().Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return m.c.Close()
}
