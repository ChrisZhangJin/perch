package mailtest

import (
	"context"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/mailbox"
)

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

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

func TestFakeSenderRecordsInOrder(t *testing.T) {
	s := &FakeSender{}
	if err := s.Reply("alice@x", "Re: hi", "<r@x>", []string{"<r@x>"}, "first", []string{"/tmp/a.txt"}); err != nil {
		t.Fatalf("Reply 1: %v", err)
	}
	if err := s.Reply("bob@x", "Re: hello", "<r2@x>", nil, "second", nil); err != nil {
		t.Fatalf("Reply 2: %v", err)
	}

	rs := s.Replies()
	if len(rs) != 2 {
		t.Fatalf("Replies: got %d, want 2", len(rs))
	}
	if rs[0].To != "alice@x" || rs[0].Subject != "Re: hi" || rs[0].InReplyTo != "<r@x>" ||
		!sliceEq(rs[0].References, []string{"<r@x>"}) ||
		rs[0].Body != "first" || len(rs[0].Attachments) != 1 || rs[0].Attachments[0] != "/tmp/a.txt" {
		t.Errorf("Reply[0]: %+v", rs[0])
	}
	if rs[1].To != "bob@x" || rs[1].Subject != "Re: hello" || rs[1].InReplyTo != "<r2@x>" ||
		len(rs[1].References) != 0 ||
		rs[1].Body != "second" || len(rs[1].Attachments) != 0 {
		t.Errorf("Reply[1]: %+v", rs[1])
	}
}

func TestFakeSenderRepliesReturnsCopy(t *testing.T) {
	s := &FakeSender{}
	_ = s.Reply("a@x", "s", "<r@x>", nil, "b", nil)
	r1 := s.Replies()
	r1[0].Body = "MUTATED"
	r2 := s.Replies()
	if r2[0].Body != "b" {
		t.Errorf("Replies must return a copy; got mutated body %q", r2[0].Body)
	}
}

func TestFakeSenderRepliesDeepCopiesInnerSlices(t *testing.T) {
	s := &FakeSender{}
	_ = s.Reply("a@x", "s", "<r@x>", []string{"<r@x>"}, "b", []string{"/tmp/x"})
	r := s.Replies()
	// Mutate inner slices in the snapshot.
	r[0].References[0] = "MUTATED"
	r[0].References = append(r[0].References, "EXTRA")
	r[0].Attachments[0] = "/tmp/mutated"
	r[0].Attachments = append(r[0].Attachments, "/tmp/extra")
	// Get a fresh snapshot — inner slices must be isolated.
	r2 := s.Replies()
	if r2[0].References[0] != "<r@x>" || len(r2[0].References) != 1 {
		t.Errorf("References leaked: %v", r2[0].References)
	}
	if r2[0].Attachments[0] != "/tmp/x" || len(r2[0].Attachments) != 1 {
		t.Errorf("Attachments leaked: %v", r2[0].Attachments)
	}
}

func TestScriptedRunnerReturnsPerCall(t *testing.T) {
	r := &ScriptedRunner{
		Outs: []string{"alpha", "beta", "gamma"},
	}
	got1, n1, err := r.Run(context.Background(), "p1", "sid-1", true)
	if err != nil || got1 != "alpha" || n1 != "" {
		t.Errorf("call 1: got (%q,%q,%v), want (alpha,,nil)", got1, n1, err)
	}
	got2, _, _ := r.Run(context.Background(), "p2", "sid-2", false)
	if got2 != "beta" {
		t.Errorf("call 2: got %q, want beta", got2)
	}
	got3, _, _ := r.Run(context.Background(), "p3", "sid-3", true)
	if got3 != "gamma" {
		t.Errorf("call 3: got %q, want gamma", got3)
	}
	// Overflow reuses the last entry (defensive).
	got4, _, _ := r.Run(context.Background(), "p4", "sid-4", false)
	if got4 != "gamma" {
		t.Errorf("overflow call 4: got %q, want gamma", got4)
	}
	if !sliceEq(r.Prompts, []string{"p1", "p2", "p3", "p4"}) {
		t.Errorf("Prompts = %v, want [p1 p2 p3 p4]", r.Prompts)
	}
	if !sliceEq(r.SIDs, []string{"sid-1", "sid-2", "sid-3", "sid-4"}) {
		t.Errorf("SIDs = %v, want [sid-1 sid-2 sid-3 sid-4]", r.SIDs)
	}
	if !r.IsNews[0] || r.IsNews[1] || !r.IsNews[2] || r.IsNews[3] {
		t.Errorf("IsNews = %v, want [true false true false]", r.IsNews)
	}
}

func TestScriptedRunnerReturnsNativeWhenSet(t *testing.T) {
	r := &ScriptedRunner{
		Outs:   []string{"hello"},
		Native: "019fd2a7-812a-73c0-9052-c07bee77dabf",
	}
	_, native, err := r.Run(context.Background(), "p", "", true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if native != "019fd2a7-812a-73c0-9052-c07bee77dabf" {
		t.Errorf("native = %q, want the configured UUID", native)
	}
}
