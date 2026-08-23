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
