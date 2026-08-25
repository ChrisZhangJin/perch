package app

import (
	"sync"
	"time"
)

// replyRate counts replies per email thread over a rolling window, as the
// last line of defence against a mail loop.
//
// The header checks in message.IsAutomated only work against a counterparty
// that labels its own mail. The loop that actually happened in production was
// between two agents that label nothing: each answered the other politely,
// every round costing an agent invocation on both sides, with nothing in the
// pipeline able to notice. This counter notices.
//
// In memory on purpose. It bounds a loop within one perch run, which is what
// matters — a restart is manual intervention, and by then the operator is
// already looking. Persisting it would mean writing to disk on every reply to
// defend against a case that only arises if nobody is watching at all.
type replyRate struct {
	mu     sync.Mutex
	window time.Duration
	sent   map[string][]time.Time // thread root -> reply timestamps
	now    func() time.Time       // injectable for tests
}

func newReplyRate(window time.Duration) *replyRate {
	return &replyRate{
		window: window,
		sent:   make(map[string][]time.Time),
		now:    time.Now,
	}
}

// Allow reports whether another reply may be sent on this thread, and records
// it when so. max <= 0 disables the cap.
//
// Recording happens here rather than after a successful send: a reply that
// fails at SMTP still consumed an agent run, which is the resource a loop
// burns. Counting attempts is what bounds the loop.
func (r *replyRate) Allow(threadRoot string, max int) (bool, int) {
	if max <= 0 {
		return true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	cutoff := now.Add(-r.window)

	kept := r.sent[threadRoot][:0]
	for _, t := range r.sent[threadRoot] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		// Drop the key rather than leaving an empty slice behind, so a
		// long-lived perch does not accumulate one entry per thread it has
		// ever seen.
		delete(r.sent, threadRoot)
	} else {
		r.sent[threadRoot] = kept
	}

	if len(kept) >= max {
		return false, len(kept)
	}
	r.sent[threadRoot] = append(kept, now)
	return true, len(kept) + 1
}

// prune drops threads with no activity inside the window. Called on the
// fetch path so an idle perch releases memory for threads that went quiet.
func (r *replyRate) prune() {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-r.window)
	for root, ts := range r.sent {
		live := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				live = append(live, t)
			}
		}
		if len(live) == 0 {
			delete(r.sent, root)
		} else {
			r.sent[root] = live
		}
	}
}
