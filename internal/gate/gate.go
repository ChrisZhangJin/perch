// Package gate enforces the sender whitelist and in-memory message dedup.
//
// Both are deterministic security boundaries — never delegate them to the LLM.
//
// Whitelist rules:
//
//   - Each entry is either a literal email address ("alice@163.com",
//     case-insensitive exact match) or a regex written as `s"<pattern>"`.
//   - The `s` prefix and the surrounding double quotes are stripped; what
//     remains is compiled as a Go regexp. A bad regex is a fatal error at
//     gate construction time (fail-fast; never silently fail-open).
//   - Anchors are NOT auto-added: write `^foo$` if you want exact, leave them
//     off for "contains"-style matching.
//   - Case sensitivity is the regex's own business — for case-insensitive
//     matching, start the pattern with `(?i)`.
//   - The whitelist is the trust anchor: ANY entry that matches an inbound
//     sender passes the gate. A bare `.*` is your footgun, not perch's.
package gate

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const regexMarker = `s"` // entries beginning with `s"` are regexes

// Gate enforces the sender whitelist and in-memory message-id dedup.
type Gate struct {
	literal map[string]struct{} // lowercased, exact-match addresses
	regex   []*regexp.Regexp    // compiled regexes; matched against the raw addr
	mu      sync.Mutex
	seen    map[string]bool
}

// New compiles the whitelist. Each entry is parsed once; the regex subset
// is compiled eagerly so a bad pattern fails here (not at first match).
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
		g.literal[strings.ToLower(raw)] = struct{}{}
	}
	return g, nil
}

// Allowed reports whether `from` is an authorized sender. Regexes are
// matched against the raw (non-lowercased) From; literals are matched
// case-insensitively against the lowercased From. A non-empty match (any
// pattern, anchored or not) passes — perch's whitelist is a trust anchor,
// not a verifier.
func (g *Gate) Allowed(from string) bool {
	if from == "" {
		return false
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