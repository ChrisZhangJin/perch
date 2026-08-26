// Package agent names the AI agents perch can spawn and ships each one's
// argv adapter so the runner does not hard-code Claude flags.
package agent

import (
	"errors"
	"fmt"
	"os/exec"
)

// Args is everything the runner passes to an agent. Workdir is informational
// here (runner sets cmd.Dir). PermMode is required by claude; for nanopi
// it maps to --approve (skip the project-trust prompt); pi ignores it.
type Args struct {
	Prompt    string
	SessionID string
	IsNew     bool // true => create; false => resume
	Workdir   string
	PermMode  string
	// AppendSystemPrompt is extra text layered onto the agent's own system
	// prompt (--append-system-prompt), used to give the agent a standing role
	// — "you are the support desk, task definitions live in ./tasks" — that
	// outlives any single email. Empty means "don't pass the flag".
	//
	// Always TEXT, never a path: claude takes text only, so perch resolves a
	// configured file itself and hands every agent the same thing.
	AppendSystemPrompt string
}

// Agent is a built-in CLI runner.
type Agent struct {
	Name      string
	Binary    string
	BuildArgs func(Args) []string
}

// agents is the built-in table. Order is stable; Lookup's error preserves it.
var agents = map[string]Agent{
	"claude": {Name: "claude", Binary: "claude", BuildArgs: buildClaudeArgs},
	"nanopi": {Name: "nanopi", Binary: "nanopi", BuildArgs: buildNanopiArgs},
	"pi":     {Name: "pi", Binary: "pi", BuildArgs: buildPiArgs},
}

// Lookup resolves an agent name to its built-in adapter. Unknown names return
// an error listing the valid choices.
func Lookup(name string) (Agent, error) {
	if a, ok := agents[name]; ok {
		return a, nil
	}
	return Agent{}, fmt.Errorf("unknown AI agent %q (valid: claude, nanopi, pi)", name)
}

// ErrBinaryNotFound is returned by BinaryPath when the agent name is known
// but its binary is not on PATH. Wrapped with the agent name and binary
// path so callers can present a useful hint.
var ErrBinaryNotFound = errors.New("agent binary not found on PATH")

// BinaryPath resolves name → agent binary path. Returns ErrBinaryNotFound
// (wrapped) if the binary is not on PATH, or a plain Lookup error if the
// name itself is unknown. Used by the wizard and the --daemon preflight
// to fail fast with a clear message before any agent work runs.
func BinaryPath(name string) (string, error) {
	a, err := Lookup(name)
	if err != nil {
		return "", err
	}
	path, err := exec.LookPath(a.Binary)
	if err != nil {
		return "", fmt.Errorf("%w: %s (install %s or fix PATH)", ErrBinaryNotFound, a.Binary, a.Binary)
	}
	return path, nil
}