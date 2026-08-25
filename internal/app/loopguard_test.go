package app

import (
	"testing"
	"time"
)

// fixedRate returns a replyRate whose clock the test drives.
func fixedRate(t *testing.T, window time.Duration) (*replyRate, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := newReplyRate(window)
	r.now = func() time.Time { return now }
	return r, func(d time.Duration) { now = now.Add(d) }
}

func TestReplyRateAllowsUpToCap(t *testing.T) {
	r, _ := fixedRate(t, time.Hour)
	for i := 1; i <= 3; i++ {
		ok, n := r.Allow("<t@x>", 3)
		if !ok {
			t.Fatalf("reply %d was blocked under a cap of 3", i)
		}
		if n != i {
			t.Errorf("reply %d reported count %d", i, n)
		}
	}
	if ok, n := r.Allow("<t@x>", 3); ok {
		t.Errorf("the 4th reply must be blocked, got ok=true n=%d", n)
	}
}

// TestReplyRateWindowSlides is what keeps the cap from breaking a long but
// legitimate conversation: the count is per rolling window, not per thread
// lifetime.
func TestReplyRateWindowSlides(t *testing.T) {
	r, advance := fixedRate(t, time.Hour)
	for i := 0; i < 3; i++ {
		r.Allow("<t@x>", 3)
	}
	if ok, _ := r.Allow("<t@x>", 3); ok {
		t.Fatal("expected the cap to be reached")
	}
	advance(61 * time.Minute)
	if ok, n := r.Allow("<t@x>", 3); !ok {
		t.Errorf("after the window passed the thread should be allowed again (n=%d)", n)
	}
}

func TestReplyRateIsPerThread(t *testing.T) {
	r, _ := fixedRate(t, time.Hour)
	for i := 0; i < 2; i++ {
		r.Allow("<a@x>", 2)
	}
	if ok, _ := r.Allow("<a@x>", 2); ok {
		t.Fatal("thread a should be capped")
	}
	if ok, _ := r.Allow("<b@x>", 2); !ok {
		t.Error("thread b must not be affected by thread a's cap")
	}
}

func TestReplyRateZeroMeansUnlimited(t *testing.T) {
	r, _ := fixedRate(t, time.Hour)
	for i := 0; i < 100; i++ {
		if ok, _ := r.Allow("<t@x>", 0); !ok {
			t.Fatalf("cap 0 must not block (blocked at %d)", i)
		}
	}
}

// TestReplyRatePrunesIdleThreads guards against unbounded growth: a
// long-running perch must not keep one map entry per thread it has ever seen.
func TestReplyRatePrunesIdleThreads(t *testing.T) {
	r, advance := fixedRate(t, time.Hour)
	for _, id := range []string{"<a@x>", "<b@x>", "<c@x>"} {
		r.Allow(id, 10)
	}
	if len(r.sent) != 3 {
		t.Fatalf("expected 3 tracked threads, got %d", len(r.sent))
	}
	advance(61 * time.Minute)
	r.prune()
	if len(r.sent) != 0 {
		t.Errorf("idle threads should be pruned, %d left", len(r.sent))
	}
}

// TestReplyRateAllowPrunesInline: Allow must also drop expired timestamps, or
// a thread that stays just under the cap forever would grow its slice without
// bound between prune calls.
func TestReplyRateAllowPrunesInline(t *testing.T) {
	r, advance := fixedRate(t, time.Hour)
	for i := 0; i < 20; i++ {
		r.Allow("<t@x>", 100)
		advance(10 * time.Minute)
	}
	if got := len(r.sent["<t@x>"]); got > 7 {
		t.Errorf("expected only entries inside the 1h window, got %d", got)
	}
}
