package mailtest

import (
	"context"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/mailbox"
)

func TestFakeMailboxDrainsOnFetch(t *testing.T) {
	f := &FakeMailbox{nextUID: 1}
	f.Enqueue(0, []byte("a"))
	f.Enqueue(0, []byte("b"))

	got, err := f.FetchUnseen(context.Background())
	if err != nil {
		t.Fatalf("FetchUnseen: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("first FetchUnseen: got %d, want 2", len(got))
	}
	if string(got[0].Data) != "a" || string(got[1].Data) != "b" {
		t.Errorf("first FetchUnseen: data order/loss wrong: %q %q", got[0].Data, got[1].Data)
	}
	if got[0].UID != 1 || got[1].UID != 2 {
		t.Errorf("first FetchUnseen: UIDs = (%d,%d), want (1,2) auto-assigned", got[0].UID, got[1].UID)
	}

	got2, err := f.FetchUnseen(context.Background())
	if err != nil {
		t.Fatalf("second FetchUnseen: %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("second FetchUnseen should drain: got %d, want 0", len(got2))
	}
}

func TestFakeMailboxMarkSeenAndClose(t *testing.T) {
	f := &FakeMailbox{nextUID: 10}
	f.Enqueue(0, []byte("x"))

	got, _ := f.FetchUnseen(context.Background())
	var msg mailbox.Raw
	if len(got) > 0 {
		msg = got[0]
	}
	if err := f.MarkSeen(context.Background(), msg.UID); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if len(f.seen) != 1 || f.seen[0] != 10 {
		t.Errorf("seen = %v, want [10]", f.seen)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close should be a no-op, got %v", err)
	}
}

func TestFakeMailboxAutoUIDStartsAtOne(t *testing.T) {
	// No pre-initialized nextUID: the first auto-assigned UID must be 1, not 0,
	// because SendRaw reserves UID 0 for "unassigned" and Mailtest.Send relies
	// on Send-allocated UIDs being > 0.
	f := &FakeMailbox{}
	f.Enqueue(0, []byte("a"))
	got, _ := f.FetchUnseen(context.Background())
	if got[0].UID != 1 {
		t.Errorf("first auto UID = %d, want 1", got[0].UID)
	}
	f.Enqueue(0, []byte("b"))
	got, _ = f.FetchUnseen(context.Background())
	if got[0].UID != 2 {
		t.Errorf("second auto UID = %d, want 2", got[0].UID)
	}
}

func TestFakeMailboxCallerUIDAdvancesNextUID(t *testing.T) {
	// Caller-chosen UID via SendRaw must advance nextUID so subsequent
	// auto-assignments don't collide.
	f := &FakeMailbox{nextUID: 5}
	f.Enqueue(42, []byte("a"))
	f.Enqueue(0, []byte("b"))
	msgs, _ := f.FetchUnseen(context.Background())
	if len(msgs) != 2 || msgs[0].UID != 42 || msgs[1].UID != 43 {
		t.Errorf("after Enqueue(42), Enqueue(0): got UIDs (%d,%d), want (42,43)",
			msgs[0].UID, msgs[1].UID)
	}
}
