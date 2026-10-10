package agent

import (
	"slices"
	"testing"
)

func TestBuildPiArgsEmptySessionOmitsFlag(t *testing.T) {
	got := buildPiArgs(Args{Prompt: "p", IsNew: true})
	if slices.Contains(got, "--session-id") || slices.Contains(got, "--session") {
		t.Fatalf("empty SessionID must not pass a session flag: %v", got)
	}
}
