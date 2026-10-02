package session

import (
	"path/filepath"
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestResolveNewThenStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id1, isNew, err := r.Resolve("<root@x>")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Error("first resolve should be new")
	}
	if !uuidRE.MatchString(id1) {
		t.Errorf("id not a uuid v4: %q", id1)
	}
	id2, isNew2, _ := r.Resolve("<root@x>")
	if isNew2 {
		t.Error("second resolve should not be new")
	}
	if id2 != id1 {
		t.Errorf("id changed: %q vs %q", id1, id2)
	}
}

func TestResolvePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	r1, _ := Load(path)
	id1, _, _ := r1.Resolve("<root@x>")

	r2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id2, isNew, _ := r2.Resolve("<root@x>")
	if isNew {
		t.Error("reloaded registry should already know the thread")
	}
	if id2 != id1 {
		t.Errorf("persisted id mismatch: %q vs %q", id1, id2)
	}
}

func TestReplaceUpdatesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	r, _ := Load(path)
	oldID, _, _ := r.Resolve("<root@x>")

	newID := "019fd2a7-812a-73c0-9052-c07bee77dabf"
	if err := r.Replace("<root@x>", newID); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// In-memory view reflects the swap.
	got, isNew, _ := r.Resolve("<root@x>")
	if isNew {
		t.Error("Resolve should report !isNew after Replace")
	}
	if got != newID {
		t.Errorf("Resolve after Replace = %q, want %q", got, newID)
	}

	// Reload from disk to confirm persistence.
	r2, _ := Load(path)
	got2, _, _ := r2.Resolve("<root@x>")
	if got2 != newID {
		t.Errorf("reloaded id = %q, want %q (Replace didn't persist)", got2, newID)
	}
	if got2 == oldID {
		t.Errorf("Replace had no effect: still %q", oldID)
	}
}

func TestReplaceUnknownThreadFails(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "sessions.json"))
	if err := r.Replace("<not-seen>", "new-id"); err == nil {
		t.Error("expected error on unknown thread root")
	}
}

func TestReplaceEmptyIDFails(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "sessions.json"))
	_, _, _ = r.Resolve("<root@x>")
	if err := r.Replace("<root@x>", ""); err == nil {
		t.Error("expected error on empty newID")
	}
}

func TestReplaceSameIDNoOp(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "sessions.json"))
	id, _, _ := r.Resolve("<root@x>")
	if err := r.Replace("<root@x>", id); err != nil {
		t.Errorf("Replace with same id should be a no-op, got %v", err)
	}
}

// TestForgetDropsUnconfirmedID pins the cleanup path. Resolve persists an id
// before the agent has run, so a first run that dies leaves an id naming a
// session that was never created. Forget makes the next email in the thread
// first-sight again instead of resuming a phantom.
func TestForgetDropsUnconfirmedID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id, isNew, err := r.Resolve("<t1@x>")
	if err != nil || !isNew || id == "" {
		t.Fatalf("Resolve: id=%q isNew=%v err=%v", id, isNew, err)
	}
	if err := r.Forget("<t1@x>"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	// Same thread must now look brand new, with a different id.
	id2, isNew2, err := r.Resolve("<t1@x>")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew2 {
		t.Error("after Forget the thread must be first sight again")
	}
	if id2 == id {
		t.Errorf("Forget then Resolve returned the same id %q", id2)
	}
	// And the removal must have hit disk, not just the map.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := reloaded.Resolve("<t1@x>"); got != id2 {
		t.Errorf("reloaded registry id = %q, want the post-Forget id %q", got, id2)
	}
}

// TestForgetUnknownThreadIsNoOp: the caller is cleaning up after a failure,
// not asserting that the key was there.
func TestForgetUnknownThreadIsNoOp(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Forget("<never-seen@x>"); err != nil {
		t.Errorf("Forget on an unknown thread: %v, want nil", err)
	}
}

func TestKnownMatchesAnyThreadID(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Resolve("<root@x>"); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Known([]string{"<rewritten@x>", "<root@x>"}); !ok || got != "<root@x>" {
		t.Errorf("Known = %q, %v; want <root@x>, true", got, ok)
	}
	if _, ok := r.Known([]string{"<other@x>"}); ok {
		t.Error("Known matched an unregistered id")
	}
}
