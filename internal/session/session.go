package session

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Registry maps an email thread root to a stable Claude session UUID,
// persisted to disk so threads resume the same session across restarts.
type Registry struct {
	path string
	mu   sync.Mutex
	m    map[string]string // threadRoot -> uuid
}

func Load(path string) (*Registry, error) {
	r := &Registry{path: path, m: make(map[string]string)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &r.m); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Resolve returns the session UUID for a thread root, generating and
// persisting a new one on first sight. isNew is true only on first sight.
func (r *Registry) Resolve(threadRoot string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.m[threadRoot]; ok {
		return id, false, nil
	}
	id, err := newUUIDv4()
	if err != nil {
		return "", false, err
	}
	r.m[threadRoot] = id
	if err := r.save(); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// Replace overwrites the session UUID for an existing thread root and
// persists. Used when an agent auto-generates its own session id (e.g.
// nanopi on IsNew=true) and perch must adopt that id for subsequent
// resumes. threadRoot must already exist in the registry — this is not
// a fresh-insert path. A no-op + nil error when newID equals the current
// value (so callers don't have to special-case "already up to date").
func (r *Registry) Replace(threadRoot, newID string) error {
	if newID == "" {
		return fmt.Errorf("session: empty new id for thread %q", threadRoot)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.m[threadRoot]
	if !ok {
		return fmt.Errorf("session: unknown thread root %q", threadRoot)
	}
	if cur == newID {
		return nil
	}
	r.m[threadRoot] = newID
	return r.save()
}

// Forget drops the mapping for a thread root, so the next email in that
// thread is treated as first sight and starts a genuinely cold session.
// Absent keys are not an error — the caller is cleaning up, not asserting.
//
// This exists because Resolve mints and PERSISTS an id before the agent has
// run. When that first run dies outright — a missing binary, a crash, an
// exhausted quota — the id it minted names a session no agent ever created.
// Left in place, the next email in the thread resolves to it with isNew=false,
// spawns a resume against a session that isn't there, and only recovers via
// the ErrSessionLost cold-retry path: one wasted agent invocation and a pair
// of alarming WARNs per affected thread. Observed 2026-08-25.
//
// Only call this when the id has NOT been confirmed by a successful run.
// Forgetting a working session id would strand the thread's real history.
func (r *Registry) Forget(threadRoot string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[threadRoot]; !ok {
		return nil
	}
	delete(r.m, threadRoot)
	return r.save()
}

func (r *Registry) save() error {
	data, err := json.MarshalIndent(r.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
