package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentBuildArgs_ClaudeNew(t *testing.T) {
	a, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "11111111-1111-4111-8111-111111111111",
		IsNew: true, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--session-id", "11111111-1111-4111-8111-111111111111"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_ClaudeResume(t *testing.T) {
	a, _ := Lookup("claude")
	got := a.BuildArgs(Args{
		Prompt: "do X", SessionID: "22222222-2222-4222-8222-222222222222",
		IsNew: false, Workdir: "/tmp/w", PermMode: "acceptEdits",
	})
	want := []string{"-p", "do X", "--output-format", "text", "--permission-mode", "acceptEdits", "--resume", "22222222-2222-4222-8222-222222222222"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claude resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiAcceptEdits(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "acceptEdits"})
	want := []string{"-p", "p", "--output", "text", "--approve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi acceptEdits = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiBypassPermissions(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "bypassPermissions"})
	want := []string{"-p", "p", "--output", "text", "--approve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi bypassPermissions = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiResume(t *testing.T) {
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--output", "text", "--session", "sid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nanopi resume = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_NanopiNewOmitsSession(t *testing.T) {
	// Regression: passing --session <new-uuid> to nanopi fails with
	// "first line must be a session header" because the flag is resume-only.
	// On IsNew=true we must omit the flag and let nanopi create the file.
	a, _ := Lookup("nanopi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid", IsNew: true, Workdir: "/w", PermMode: "acceptEdits"})
	for _, a := range got {
		if a == "--session" {
			t.Errorf("nanopi IsNew=true must not pass --session, got argv: %v", got)
		}
	}
}

func TestAgentBuildArgs_PiNew(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-new", IsNew: true, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session-id", "sid-new"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi new = %v, want %v", got, want)
	}
}

func TestAgentBuildArgs_PiResume(t *testing.T) {
	a, _ := Lookup("pi")
	got := a.BuildArgs(Args{Prompt: "p", SessionID: "sid-old", IsNew: false, Workdir: "/w"})
	want := []string{"-p", "p", "--mode", "text", "--session", "sid-old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pi resume = %v, want %v", got, want)
	}
}

func TestAgentLookupUnknown(t *testing.T) {
	_, err := Lookup("not-an-agent")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should list valid names, got: %v", err)
	}
}