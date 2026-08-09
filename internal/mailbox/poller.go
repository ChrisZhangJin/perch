package mailbox

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ChrisZhangJin/perch/internal/config"
)

// Poller is a short-connection IMAP transport. Each FetchUnseen/MarkSeen
// dials fresh, runs the command, logs out, closes. Used when the provider
// has no IDLE (163/126) or when the operator prefers the simplicity of
// per-fetch connections.
type Poller struct {
	cfg  *config.Config
	addr string
}

func NewPoller(cfg *config.Config, addr string) *Poller {
	return &Poller{cfg: cfg, addr: addr}
}

// dial is the IMAP login + SELECT INBOX helper shared by every short call.
func (p *Poller) dial(ctx context.Context) (*imapclient.Client, error) {
	c, err := imapclient.DialTLS(p.addr, &imapclient.Options{
		TLSConfig: p.tlsConfig(),
	})
	if err != nil {
		return nil, err
	}
	// IMAP ID before login (163 requires it; harmless elsewhere).
	_, _ = c.ID(&imap.IDData{Name: "perch", Version: "0.1"}).Wait()
	if err := c.Login(p.cfg.Email, p.cfg.AuthCode).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login: %w", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("select: %w", err)
	}
	return c, nil
}

func (p *Poller) tlsConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: p.cfg.TLSInsecure} //nolint:gosec
}

func (p *Poller) shutdown(c *imapclient.Client) {
	done := make(chan struct{})
	go func() { _ = c.Logout().Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	_ = c.Close()
}

func (p *Poller) FetchUnseen(ctx context.Context) ([]Raw, error) {
	c, err := p.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer p.shutdown(c)
	data, err := c.UIDSearch(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		return nil, err
	}
	out := make([]Raw, 0, len(msgs))
	for _, m := range msgs {
		for _, bs := range m.BodySection {
			out = append(out, Raw{UID: uint32(m.UID), Data: bs.Bytes})
			break
		}
	}
	return out, nil
}

func (p *Poller) MarkSeen(ctx context.Context, uid uint32) error {
	c, err := p.dial(ctx)
	if err != nil {
		return err
	}
	defer p.shutdown(c)
	return c.Store(imap.UIDSetNum(imap.UID(uid)), &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagSeen},
	}, nil).Close()
}

// Close is intentionally a no-op: Poller holds no persistent connection
// (each FetchUnseen/MarkSeen dials fresh in dial), so there is nothing to
// close. Required to satisfy the Mailbox interface.
func (p *Poller) Close() error { return nil }
