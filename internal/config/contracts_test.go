package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContractsForModes pins the decision table the prompt builder relies on.
func TestContractsForModes(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		isNew bool
		want  bool
		why   string
	}{
		{ContractsOnResume, true, true, "cold session must always get the contracts"},
		{ContractsOnResume, false, false, "resumed session already replayed them"},
		{ContractsAlways, true, true, "always mode repeats them"},
		{ContractsAlways, false, true, "always mode repeats them on resume too"},
		// An unset value must fail SAFE (contracts on), not silently strip
		// them: a zero-valued Config appears in tests and ad-hoc callers.
		{"", true, true, "unset mode must not strip contracts"},
		{"", false, true, "unset mode must not strip contracts"},
	} {
		c := &Config{PromptContracts: tc.mode}
		if got := c.ContractsFor(tc.isNew); got != tc.want {
			t.Errorf("PromptContracts=%q isNew=%v -> %v, want %v (%s)",
				tc.mode, tc.isNew, got, tc.want, tc.why)
		}
	}
}

func TestPromptContractsDefault(t *testing.T) {
	if got := Defaults().PromptContracts; got != ContractsOnResume {
		t.Errorf("default PromptContracts = %q, want %q", got, ContractsOnResume)
	}
}

// writeCfg writes a YAML file and loads it with env overrides cleared.
func writeCfg(t *testing.T, body string) *Config {
	t.Helper()
	clearEnvOverrides(t)
	p := filepath.Join(t.TempDir(), "perch.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestPromptContractsFromYAML(t *testing.T) {
	if got := writeCfg(t, "prompt:\n  contracts: always\n").PromptContracts; got != ContractsAlways {
		t.Errorf("got %q, want always", got)
	}
	if got := writeCfg(t, "prompt:\n  contracts: on_resume\n").PromptContracts; got != ContractsOnResume {
		t.Errorf("got %q, want on_resume", got)
	}
	// Absent key keeps the default rather than blanking the field.
	if got := writeCfg(t, "log_level: warn\n").PromptContracts; got != ContractsOnResume {
		t.Errorf("absent key gave %q, want the default on_resume", got)
	}
}

// TestPromptContractsRejectsUnknownValue pins the fail-safe: a typo must not
// take perch down, and must not land on the mode that omits contracts by
// accident. It falls back to on_resume, which still sends them cold.
func TestPromptContractsRejectsUnknownValue(t *testing.T) {
	if got := writeCfg(t, "prompt:\n  contracts: sometimes\n").PromptContracts; got != ContractsOnResume {
		t.Errorf("unknown value gave %q, want fallback to on_resume", got)
	}
}
