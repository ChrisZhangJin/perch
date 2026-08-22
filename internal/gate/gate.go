// Package gate enforces the sender whitelist and in-memory message dedup.
//
// Both are deterministic security boundaries — never delegate them to the LLM.
//
// Whitelist rules:
//
//   - Each entry is either a literal email address ("alice@163.com",
//     case-insensitive exact match), a regex written as `s"<pattern>"`, or
//     the literal `*` meaning "accept everyone".
//   - The `s` prefix and the surrounding double quotes are stripped; what
//     remains is compiled as a Go regexp. A bad regex is a fatal error at
//     gate construction time (fail-fast; never silently fail-open).
//   - Anchors are NOT auto-added: write `^foo$` if you want exact, leave them
//     off for "contains"-style matching.
//   - Case sensitivity is the regex's own business — for case-insensitive
//     matching, start the pattern with `(?i)`.
//   - An empty whitelist is fail-closed: every sender is rejected.
//     This is the safe default for env-only / headless setups that never
//     went through the interactive wizard.
//   - A single literal entry of `*` flips the gate to accept every non-empty
//     sender. This is the onboarding default the setup wizard writes;
//     operators who want fail-closed posture can edit the file down to `[]`.
//   - The whitelist is the trust anchor: ANY entry that matches an inbound
//     sender passes the gate. A bare `.*` regex is your footgun, not
//     perch's — prefer the `*` literal for "everyone".
package gate

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const regexMarker = `s"` // entries beginning with `s"` are regexes

// everyoneSentinel is the literal entry that flips the gate to "accept all".
// Lowercased before comparison so "*" and "*" match the same way.
const everyoneSentinel = "*"

// Gate enforces the sender whitelist and in-memory message-id dedup.
type Gate struct {
	literal  map[string]struct{} // lowercased, exact-match addresses
	regex    []*regexp.Regexp    // compiled regexes; matched against the raw addr
	allowAll bool                // true when the whitelist includes "*"
	mu       sync.Mutex
	seen     map[string]bool
}

// New compiles the whitelist. Each entry is parsed once; the regex subset
// is compiled eagerly so a bad pattern fails here (not at first match).
// A literal `*` entry sets the allow-all flag (it is also recorded in
// `literal` so the file round-trips through Load → Save unchanged).
func New(allow []string) (*Gate, error) {
	g := &Gate{
		literal: make(map[string]struct{}, len(allow)),
		seen:    make(map[string]bool),
	}
	for _, raw := range allow {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, regexMarker) && strings.HasSuffix(raw, `"`) && len(raw) > len(regexMarker)+1 {
			pat := raw[len(regexMarker) : len(raw)-1]
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, fmt.Errorf("gate: bad regex entry %q: %w", raw, err)
			}
			g.regex = append(g.regex, re)
			continue
		}
		lc := strings.ToLower(raw)
		g.literal[lc] = struct{}{}
		if lc == everyoneSentinel {
			g.allowAll = true
		}
	}
	return g, nil
}

// Allowed reports whether `from` is an authorized sender. When the gate
// is in allow-all mode (the whitelist contains `*`), any non-empty `from`
// passes. Otherwise regexes are matched against the raw (non-lowercased)
// From and literals are matched case-insensitively against the lowercased
// From. A non-empty match (any pattern, anchored or not) passes — perch's
// whitelist is a trust anchor, not a verifier.
func (g *Gate) Allowed(from string) bool {
	if from == "" {
		return false
	}
	if g.allowAll {
		return true
	}
	if _, ok := g.literal[strings.ToLower(from)]; ok {
		return true
	}
	for _, re := range g.regex {
		if re.MatchString(from) {
			return true
		}
	}
	return false
}

// FirstSight returns true the first time messageID is seen and records it.
func (g *Gate) FirstSight(messageID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[messageID] {
		return false
	}
	g.seen[messageID] = true
	return true
}
