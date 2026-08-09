// Package agent names the AI agents perch can spawn and ships each one's
// argv adapter so the runner does not hard-code Claude flags.
package agent

import "fmt"

// Args is everything the runner passes to an agent. Workdir is informational
// here (runner sets cmd.Dir). PermMode is required by claude; for nanopi
// it maps to --approve (skip the project-trust prompt); pi ignores it.
type Args struct {
	Prompt    string
	SessionID string
	IsNew     bool // true => create; false => resume
	Workdir   string
	PermMode  string
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