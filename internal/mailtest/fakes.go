// Package mailtest provides in-process fakes for perch's IMAP mailbox and
// SMTP reply paths, plus a Mailtest harness that wires them into a real
// *app.App. Use it to drive perch's email pipeline end-to-end without IMAP,
// SMTP, Docker, or a network.
//
// The fakes are not safe for concurrent use; the underlying *app.App already
// serializes ProcessUnseen on a single goroutine, so single-threaded callers
// (the typical test) are fine.
package mailtest

import (
	"context"
	"sync"

	"github.com/ChrisZhangJin/perch/internal/mailbox"
)

// FakeMailbox satisfies mailbox.Mailbox. FetchUnseen drains the queue and
// returns every queued Raw in enqueue order; MarkSeen records the UID.
// Close is a no-op. Not safe for concurrent use.
type FakeMailbox struct {
	mu      sync.Mutex
	msgs    []mailbox.Raw
	seen    []uint32
	nextUID uint32
}

// Enqueue appends data to the mailbox queue. If uid is 0, the next sequential
// UID is assigned (starting at 1); otherwise the caller-chosen uid is used.
func (f *FakeMailbox) Enqueue(uid uint32, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if uid == 0 {
		uid = f.nextUID
		f.nextUID++
	} else if uid > f.nextUID {
		f.nextUID = uid
	}
	f.msgs = append(f.msgs, mailbox.Raw{UID: uid, Data: data})
}

func (f *FakeMailbox) FetchUnseen(ctx context.Context) ([]mailbox.Raw, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.msgs
	f.msgs = nil
	return out, nil
}

func (f *FakeMailbox) MarkSeen(ctx context.Context, uid uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, uid)
	return nil
}

func (f *FakeMailbox) Close() error { return nil }
