package agent

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// helpProbeTimeout bounds the --help probe. A coding agent's --help is local
// and instant; anything slower than this is a binary that wants to do network
// or auth work at startup, and perch must not block mail on it.
const helpProbeTimeout = 5 * time.Second

// SupportsAppendSystemPrompt reports whether the agent binary advertises
// --append-system-prompt, by running `<binary> --help` and looking for the
// flag in its output.
//
// Asking the binary beats a static table in the registry, because the answer
// changes underneath perch: as of 2026-08-25 claude and pi have the flag and
// nanopi does not, but nanopi is actively adding it. A hardcoded `nanopi:
// false` would keep perch ignoring the flag after nanopi ships it, and the
// operator would have to discover that the fix is a perch release. Probing
// means the feature lights up on its own the moment the agent grows it.
//
// Both stdout and stderr are searched: --help output goes to either depending
// on the CLI framework. Any failure — binary missing, --help unsupported,
// probe timeout — reports false, which is the safe direction: perch runs
// without the flag instead of dying on an unknown one at every email.
func SupportsAppendSystemPrompt(binary string) bool {
	return helpMentions(binary, appendSystemPromptFlag)
}

// helpMentions runs `<binary> --help` and reports whether flag appears in the
// combined output.
func helpMentions(binary, flag string) bool {
	if binary == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), helpProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--help")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// A non-zero exit is not disqualifying: some CLIs print help and exit 1.
	// The output is what matters.
	_ = cmd.Run()
	return strings.Contains(out.String(), flag)
}
