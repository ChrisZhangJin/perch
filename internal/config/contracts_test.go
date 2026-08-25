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

// TestStripQuotedForModes pins the decision table app.ProcessUnseen relies on.
func TestStripQuotedForModes(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		isNew bool
		want  bool
		why   string
	}{
		{StripNever, true, false, "never means never"},
		{StripNever, false, false, "never means never"},
		{StripOnResume, false, true, "resumed session already has those turns"},
		{StripOnResume, true, false, "a cold session may need the quote as its only context"},
		{StripAlways, true, true, "always means always"},
		{StripAlways, false, true, "always means always"},
		// Unset must fail SAFE, i.e. do not delete anything.
		{"", true, false, "unset must not strip"},
		{"", false, false, "unset must not strip"},
	} {
		c := &Config{StripQuoted: tc.mode}
		if got := c.StripQuotedFor(tc.isNew); got != tc.want {
			t.Errorf("StripQuoted=%q isNew=%v -> %v, want %v (%s)",
				tc.mode, tc.isNew, got, tc.want, tc.why)
		}
	}
}

func TestStripQuotedDefaultIsNever(t *testing.T) {
	if got := Defaults().StripQuoted; got != StripNever {
		t.Errorf("default StripQuoted = %q, want %q — stripping must be opt-in", got, StripNever)
	}
}

func TestStripQuotedFromYAML(t *testing.T) {
	for in, want := range map[string]string{
		"never": StripNever, "on_resume": StripOnResume, "always": StripAlways,
	} {
		if got := writeCfg(t, "prompt:\n  strip_quoted: "+in+"\n").StripQuoted; got != want {
			t.Errorf("strip_quoted: %s -> %q, want %q", in, got, want)
		}
	}
	// A typo must not silently enable deletion.
	if got := writeCfg(t, "prompt:\n  strip_quoted: sometimes\n").StripQuoted; got != StripNever {
		t.Errorf("unknown value gave %q, want fallback to never", got)
	}
}
