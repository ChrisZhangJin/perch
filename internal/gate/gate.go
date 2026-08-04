package gate

import (
	"strings"
	"sync"
)

// Gate enforces the sender whitelist and in-memory message dedup.
// Both are deterministic security boundaries — never delegate them to the LLM.
type Gate struct {
	allow map[string]bool
	mu    sync.Mutex
	seen  map[string]bool
}

func New(allow []string) *Gate {
	m := make(map[string]bool, len(allow))
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			m[a] = true
		}
	}
	return &Gate{allow: m, seen: make(map[string]bool)}
}

// Allowed reports whether from is an authorized sender (exact, case-insensitive).
func (g *Gate) Allowed(from string) bool {
	from = strings.ToLower(strings.TrimSpace(from))
	if from == "" {
		return false
	}
	return g.allow[from]
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
