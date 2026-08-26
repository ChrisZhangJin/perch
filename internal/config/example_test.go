package config

import (
	"os"
	"strings"
	"testing"
)

// examplePath is the shipped sample config, relative to this package dir.
const examplePath = "../../perch.yaml.example"

// envOverrides lists every env var Load consults for a YAML-backed field.
// Tests clear these so they read the file under test, not the ambient shell.
var envOverrides = []string{
	"AGENT_EMAIL", "ALLOW_FROM", "POLL_INTERVAL", "TASK_TIMEOUT",
	"MAX_PROMPT_BYTES", "MAX_ATTACHMENT_BYTES", "SESSION_STORE",
	"TLS_INSECURE_SKIP_VERIFY", "LOG_LEVEL", "LONG_TASK_ACK", "PERCH_CONFIG",
	"ON_EMAIL_HOOK", "HOOK_TIMEOUT", "APPEND_SYSTEM_PROMPT",
}

func clearEnvOverrides(t *testing.T) {
	t.Helper()
	for _, k := range envOverrides {
		t.Setenv(k, "")
	}
}

// TestExampleDocumentsEveryYAMLKey pins perch.yaml.example's promise that
// every key perch understands appears in the file — active for the ones you
// normally set, commented for optional ones showing their default. An
// operator should never have to read Go source to learn a key name.
//
// Regression: `email` and `log_level` were absent entirely, and task_only /
// long_task_ack were added to config without landing here.
func TestExampleDocumentsEveryYAMLKey(t *testing.T) {
	body, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read %s: %v", examplePath, err)
	}
	lines := strings.Split(string(body), "\n")

	for _, key := range YAMLKeys() {
		// Nested keys sit indented under their parent block, so match the
		// leaf; the parent has its own entry in YAMLKeys.
		leaf := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			leaf = key[i+1:]
		}
		found := false
		for _, ln := range lines {
			s := strings.TrimSpace(ln)
			s = strings.TrimPrefix(s, "# ") // commented-out form
			s = strings.TrimPrefix(s, "#")
			if strings.HasPrefix(s, leaf+":") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("perch.yaml.example never mentions %q — document it there "+
				"(active, or commented with its default)", key)
		}
	}
}

// TestExampleParsesAsShipped guards the sample against being syntactically
// valid YAML but semantically rejected by Load — a stray tab, an unquoted
// `*` in allow_from, a duration the parser won't take. Copying this file to
// perch.yaml is the documented first step, so it has to actually work.
func TestExampleParsesAsShipped(t *testing.T) {
	clearEnvOverrides(t)

	c, err := Load(examplePath)
	if err != nil {
		t.Fatalf("perch.yaml.example does not load: %v", err)
	}

	// Spot-check that values came from the file rather than silently falling
	// back to defaults, which would make the parse check vacuous.
	if c.ProviderName != "163" {
		t.Errorf("ProviderName = %q, want 163 (from the example file)", c.ProviderName)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info (from the example file)", c.LogLevel)
	}
	if c.Email == "" {
		t.Error("Email is empty — the example's `email:` key did not reach Config")
	}
	if len(c.AllowFrom) == 0 {
		t.Error("AllowFrom is empty — the example's allow_from list did not parse")
	}
	// task_only is commented out in the example, so the built-in default
	// (true) must survive. A regression to `false` here means the guardrail
	// silently switched off for anyone copying the sample.
	if !c.AgentTaskOnly {
		t.Error("AgentTaskOnly = false; commented-out task_only must leave the default true")
	}
}

// TestExampleEnvVarNamesAreReal catches the doc bug where a comment
// advertises an env var Load never reads (e.g. MAX_ATTACH_BYTES vs the real
// MAX_ATTACHMENT_BYTES). Operators trust these comments.
func TestExampleEnvVarNamesAreReal(t *testing.T) {
	body, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read %s: %v", examplePath, err)
	}
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	real := string(src)

	// Env-var-shaped tokens the example calls out in comments. Only check
	// ones that look like perch knobs (contain an underscore) to avoid
	// matching prose like NEVER or SIGTERM.
	for _, tok := range tokensWithUnderscore(string(body)) {
		if !strings.Contains(real, `os.Getenv("`+tok+`")`) {
			t.Errorf("perch.yaml.example advertises env var %q, but config.go never reads it", tok)
		}
	}
}

// tokensWithUnderscore pulls SCREAMING_SNAKE_CASE words out of text. Splitting
// on anything that isn't [A-Z_] means lowercase yaml keys (max_prompt_bytes)
// shed their letters and leave bare "_" fragments behind, so a token only
// counts when it starts and ends with a capital and has an underscore in
// between -- the shape of every env var Load actually reads.
func tokensWithUnderscore(s string) []string {
	isUpper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z') && r != '_'
	}) {
		inner := strings.Trim(f, "_")
		if !strings.Contains(inner, "_") || len(f) < 3 || seen[f] {
			continue
		}
		if !isUpper(f[0]) || !isUpper(f[len(f)-1]) {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}
