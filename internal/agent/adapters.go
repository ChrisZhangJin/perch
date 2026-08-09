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

// buildNanopiArgs omits --session on first touch and uses --session <sid>
// on resume. nanopi's --session flag is RESUME-ONLY — passing a non-existent
// id yields "first line must be a session header" (the file isn't there
// yet). On IsNew=true we let nanopi mint its own UUIDv7; after the run
// completes, runner.DiscoverSessionID() reads ~/.nanopi/sessions/active to
// recover the new id, which app.ProcessUnseen writes back into the perch
// registry so subsequent emails in the same thread resume correctly.
// PermMode maps to nanopi's --approve so perch doesn't hang on the
// project-trust prompt: acceptEdits/bypassPermissions → --approve; any
// other value (or empty) → no flag (nanopi prompts or uses persisted trust).
func buildNanopiArgs(a Args) []string {
	args := []string{"-p", a.Prompt, "--output", "text"}
	if !a.IsNew {
		args = append(args, "--session", a.SessionID)
	}
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