package gate

import "testing"

func TestAllowed(t *testing.T) {
	g := New([]string{"alice@163.com", "bob@126.com"})
	if !g.Allowed("alice@163.com") {
		t.Error("alice should be allowed")
	}
	if !g.Allowed("ALICE@163.com") {
		t.Error("uppercase alice should be allowed")
	}
	if g.Allowed("mallory@evil.com") {
		t.Error("mallory should be denied")
	}
	if g.Allowed("") {
		t.Error("empty from should be denied")
	}
}

func TestFirstSight(t *testing.T) {
	g := New(nil)
	if !g.FirstSight("<m1@x>") {
		t.Error("first time should be true")
	}
	if g.FirstSight("<m1@x>") {
		t.Error("second time should be false")
	}
	if !g.FirstSight("<m2@x>") {
		t.Error("new id should be true")
	}
}
