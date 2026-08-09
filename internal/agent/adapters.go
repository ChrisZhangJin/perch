package agent

// buildClaudeArgs preserves today's behaviour exactly: --session-id on first
// run, --resume on subsequent runs. PermMode is required for claude.
func buildClaudeArgs(a Args) []string {
	out := []string{"-p", a.Prompt, "--output-format", "text", "--permission-mode", a.PermMode}
	if a.IsNew {
		out = append(out, "--session-id", a.SessionID)
	} else {
		out = append(out, "--resume", a.SessionID)
	}
	return out
}

// buildNanopiArgs uses --session <sid> for both new and resume; IsNew is
// ignored. PermMode maps to nanopi's --approve so perch doesn't hang on
// the project-trust prompt: acceptEdits/bypassPermissions → --approve,
// any other value (or empty) → no flag (nanopi prompts or uses persisted
// trust).
func buildNanopiArgs(a Args) []string {
	args := []string{"-p", a.Prompt, "--output", "text", "--session", a.SessionID}
	switch a.PermMode {
	case "acceptEdits", "bypassPermissions":
		args = append(args, "--approve")
	}
	return args
}

// buildPiArgs distinguishes new (--session-id) vs resume (--session). PermMode
// is not part of pi's CLI; the --mode flag controls output format only.
func buildPiArgs(a Args) []string {
	if a.IsNew {
		return []string{"-p", a.Prompt, "--mode", "text", "--session-id", a.SessionID}
	}
	return []string{"-p", a.Prompt, "--mode", "text", "--session", a.SessionID}
}