package mailbox

import (
	"context"
	"testing"
)

func TestPollerCloseIsNoop(t *testing.T) {
	p := &Poller{}
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestPollerImplementsMailbox(t *testing.T) {
	// Compile-time check: Poller satisfies Mailbox.
	var _ Mailbox = (*Poller)(nil)
	var _ Mailbox = (*IMAPMailbox)(nil)
	_ = context.Background
}