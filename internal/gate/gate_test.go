package gate

import "testing"

func TestAllowedLiteral(t *testing.T) {
	g, err := New([]string{"alice@163.com", "bob@126.com"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from string
		want bool
	}{
		{"alice@163.com", true},
		{"ALICE@163.com", true}, // case-insensitive on literals
		{"bob@126.com", true},
		{"mallory@evil.com", false},
		{"", false},
		{"alice@163.com.evil.com", false}, // no substring containment on literals
	}
	for _, c := range cases {
		if got := g.Allowed(c.from); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.from, got, c.want)
		}
	}
}

func TestAllowedRegex(t *testing.T) {
	g, err := New([]string{
		"alice@163.com",                 // literal
		`s".+@(foo|bar)\.example\.com"`, // alternation
		`s".*agent.*@qq\.com"`,          // substring
		`s"(?i).+@trusted\.org"`,        // case-insensitive regex (note the s" prefix!)
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from string
		want bool
	}{
		{"alice@163.com", true},         // literal
		{"ALICE@163.com", true},         // literal, case-insensitive
		{"carol@foo.example.com", true}, // regex hit
		{"carol@bar.example.com", true}, // regex alternation
		{"dave@baz.example.com", false}, // regex miss (not in alternation)
		{"x_agent_x@qq.com", true},      // substring regex match
		{"AGENT@QQ.com", false},         // substring regex is case-sensitive by default
		{"Bob@Trusted.org", true},       // (?i) regex hit
	}
	for _, c := range cases {
		if got := g.Allowed(c.from); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.from, got, c.want)
		}
	}
}

func TestAllowedRegexRequiresMarker(t *testing.T) {
	// Without the `s` prefix and trailing `"`, an entry is a literal — NOT
	// a regex. This is the guard against accidentally letting a typo
	// (e.g. `(?i).+@x.com` without `s`) become a wildcard.
	g, err := New([]string{"(?i).+@trusted\\.org"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Allowed("Bob@Trusted.org") {
		t.Error("entry without s\" marker must be treated as a literal address, not a regex")
	}
}

func TestNewBadRegexFails(t *testing.T) {
	_, err := New([]string{`s"[unclosed"`})
	if err == nil {
		t.Fatal("expected compile error on bad regex")
	}
}

func TestNewIgnoresEmpty(t *testing.T) {
	g, err := New([]string{"", "  ", "alice@163.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Allowed("alice@163.com") {
		t.Error("alice should still be allowed")
	}
}

func TestFirstSight(t *testing.T) {
	g, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
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

// TestAllowedAcceptAllWildlet pins the literal "*" accept-everyone
// sentinel. This is the onboarding default the wizard writes when the
// operator hits Enter on the allow_from prompt.
func TestAllowedAcceptAllWildlet(t *testing.T) {
	g, err := New([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{
		"alice@163.com",
		"bob@126.com",
		"anyone@anywhere.com",
		"x@y",
	} {
		if !g.Allowed(from) {
			t.Errorf("Allowed(%q) with [*] = false, want true", from)
		}
	}
	// Empty from is still rejected — `*` accepts everyone with a known
	// address, not the empty/missing case.
	if g.Allowed("") {
		t.Error("Allowed(\"\") with [*] = true, want false (empty from is rejected even in allow-all mode)")
	}
}

// TestAllowedAcceptAllMixedWithLiteral pins that `*` mixed with concrete
// literals still means "everyone allowed". Keeping the concrete entries
// in the list means an operator who edits a wizard-written file doesn't
// have to strip the literals just to re-enable allow-all.
func TestAllowedAcceptAllMixedWithLiteral(t *testing.T) {
	g, err := New([]string{"*", "bob@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"bob@example.com", "carol@elsewhere.org", ""} {
		got := g.Allowed(from)
		want := from != "" // every non-empty passes
		if got != want {
			t.Errorf("Allowed(%q) with [*, bob@…] = %v, want %v", from, got, want)
		}
	}
}

// TestAllowedEmptyStillDenies is the regression guard for the fail-closed
// property. Empty / nil whitelist must still reject every non-empty
// sender — this is the safe default for env-only / headless setups that
// never went through the wizard.
func TestAllowedEmptyStillDenies(t *testing.T) {
	for _, in := range [][]string{nil, {}, {"", "   "}} {
		g, err := New(in)
		if err != nil {
			t.Fatalf("New(%v): %v", in, err)
		}
		for _, from := range []string{"alice@163.com", "anyone@anywhere.com"} {
			if g.Allowed(from) {
				t.Errorf("New(%v).Allowed(%q) = true, want false (empty whitelist must deny)", in, from)
			}
		}
	}
}
