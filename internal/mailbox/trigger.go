package mailbox

import (
	"context"
	"time"
)

// Trigger wakes the app loop to fetch mail. Any trigger returning a
// non-nil error causes the loop to log and sleep; a clean return means
// "go fetch now".
type Trigger interface {
	Wait(ctx context.Context) error
}

// TimerTrigger wakes the loop every Interval. Used in poll-only mode.
type TimerTrigger struct{ Interval time.Duration }

func (t TimerTrigger) Wait(ctx context.Context) error {
	timer := time.NewTimer(t.Interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IDLETrigger wakes the loop when the IMAP server reports new mail via
// IDLE, or after Timeout as a safety poll. mb.WaitForActivity already
// implements this exact semantics.
type IDLETrigger struct {
	MB      *IMAPMailbox
	Timeout time.Duration
}

func (t IDLETrigger) Wait(ctx context.Context) error {
	return t.MB.WaitForActivity(ctx, t.Timeout)
}
