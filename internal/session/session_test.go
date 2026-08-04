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
