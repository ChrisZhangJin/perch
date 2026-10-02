package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestRemovedPromptBlockWarns pins that the retired prompt.contracts /
// prompt.strip_quoted keys still load, with a WARN telling the operator
// they no longer do anything.
func TestRemovedPromptBlockWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	writeCfg(t, "prompt:\n  contracts: always\n  strip_quoted: never\n")
	if !strings.Contains(buf.String(), "key=prompt") {
		t.Fatalf("expected a deprecated-key WARN for prompt, got %q", buf.String())
	}
}
