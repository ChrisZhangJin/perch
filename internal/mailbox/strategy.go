package mailbox

import (
	"context"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/provider"
)

// Strategy is the runtime transport selection: a Mailbox (commands) plus
// one or more Triggers (wakers). Multiple Triggers can be registered; the
// app loop wakes on any of them.
type Strategy struct {
	Box      Mailbox
	Triggers []Trigger
}

// Mailbox is the narrowed command surface shared by every strategy.
// Triggers are intentionally NOT in this interface — wake-up is a
// separate concern from IMAP commands.
type Mailbox interface {
	FetchUnseen(ctx context.Context) ([]Raw, error)
	MarkSeen(ctx context.Context, uid uint32) error
	Close() error
}

// BuildStrategy chooses the strategy from provider capabilities. P mode
// for providers without IDLE; L mode for providers that advertise it.
// Future: S mode if PushWebhookURL is set.
func BuildStrategy(cfg *config.Config, p provider.Provider) (*Strategy, error) {
	if p.Caps.SupportsIDLE {
		mb, err := Dial(cfg, p.IMAPAddr)
		if err != nil {
			return nil, err
		}
		return &Strategy{
			Box: mb,
			Triggers: []Trigger{
				IDLETrigger{MB: mb, Timeout: cfg.PollInterval},
			},
		}, nil
	}
	return &Strategy{
		Box: NewPoller(cfg, p.IMAPAddr),
		Triggers: []Trigger{
			TimerTrigger{Interval: cfg.PollInterval},
		},
	}, nil
}
