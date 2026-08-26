package config

import (
	"strings"
	"testing"
)

// --- ai_agent.append_system_prompt -----------------------------------------// TestAppendSystemPromptFromYAML covers both shapes an operator writes: a
// path on one line, and a block scalar of literal text. perch keeps the value
// verbatim — resolving text-vs-path is runner.NewSystemPrompt's job, not the
// config layer's.
func TestAppendSystemPromptFromYAML(t *testing.T) {
	c := writeCfg(t, "ai_agent:\n  append_system_prompt: /root/perch/helpdesk.md\n")
	if c.AppendSystemPrompt != "/root/perch/helpdesk.md" {
		t.Errorf("path form: got %q", c.AppendSystemPrompt)
	}

	c = writeCfg(t, "ai_agent:\n  append_system_prompt: |\n    You are the support desk.\n    Tasks live in ./tasks/.\n")
	if !strings.Contains(c.AppendSystemPrompt, "You are the support desk.") ||
		!strings.Contains(c.AppendSystemPrompt, "Tasks live in ./tasks/.") {
		t.Errorf("block scalar form: got %q", c.AppendSystemPrompt)
	}

	// Absent, and whitespace-only, both mean off.
	if got := writeCfg(t, "log_level: warn\n").AppendSystemPrompt; got != "" {
		t.Errorf("absent key gave %q, want empty", got)
	}
	if got := writeCfg(t, "ai_agent:\n  append_system_prompt: \"   \"\n").AppendSystemPrompt; got != "" {
		t.Errorf("whitespace-only value gave %q, want empty (off)", got)
	}
}

func TestAppendSystemPromptEnvOverride(t *testing.T) {
	clearEnvOverrides(t)
	t.Setenv("APPEND_SYSTEM_PROMPT", "/etc/perch/role.md")
	c := Defaults()
	c.AppendSystemPrompt = "/from/yaml.md"
	applyEnv(c)
	if c.AppendSystemPrompt != "/etc/perch/role.md" {
		t.Errorf("env must win over YAML, got %q", c.AppendSystemPrompt)
	}
}

func TestAppendSystemPromptDefaultOff(t *testing.T) {
	if got := Defaults().AppendSystemPrompt; got != "" {
		t.Errorf("default = %q, want empty (opt-in)", got)
	}
}
