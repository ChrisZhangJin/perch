package config

import (
	"testing"
	"time"
)

// TestHooksFromYAML pins the hooks block: on_email carries a path, timeout
// carries a duration, and an absent block leaves the defaults (hook off,
// 30s bound).
func TestHooksFromYAML(t *testing.T) {
	c := writeCfg(t, "hooks:\n  on_email: /home/agent/record.sh\n  timeout: 5s\n")
	if c.OnEmailHook != "/home/agent/record.sh" {
		t.Errorf("OnEmailHook = %q", c.OnEmailHook)
	}
	if c.HookTimeout != 5*time.Second {
		t.Errorf("HookTimeout = %s, want 5s", c.HookTimeout)
	}

	c = writeCfg(t, "log_level: warn\n")
	if c.OnEmailHook != "" {
		t.Errorf("absent hooks block gave OnEmailHook = %q, want empty (feature off)", c.OnEmailHook)
	}
	if c.HookTimeout != Defaults().HookTimeout {
		t.Errorf("absent hooks block gave HookTimeout = %s, want the default %s",
			c.HookTimeout, Defaults().HookTimeout)
	}
}

// TestHookTimeoutRejectsNonPositive: 0 or negative would mean "wait forever",
// which is the exact failure this bound exists to prevent — so it falls back
// to the default rather than being taken literally.
func TestHookTimeoutRejectsNonPositive(t *testing.T) {
	for _, body := range []string{
		"hooks:\n  on_email: /x.sh\n  timeout: 0s\n",
		"hooks:\n  on_email: /x.sh\n  timeout: -30s\n",
	} {
		if got := writeCfg(t, body).HookTimeout; got != Defaults().HookTimeout {
			t.Errorf("%q gave HookTimeout = %s, want the default %s", body, got, Defaults().HookTimeout)
		}
	}
}

// TestHooksEnvOverridesYAML pins the documented precedence (env > YAML).
func TestHooksEnvOverridesYAML(t *testing.T) {
	clearEnvOverrides(t)
	t.Setenv("ON_EMAIL_HOOK", "/env/hook.sh")
	t.Setenv("HOOK_TIMEOUT", "90s")

	c := Defaults()
	applyEnv(c)
	if c.OnEmailHook != "/env/hook.sh" {
		t.Errorf("OnEmailHook = %q, want the env value", c.OnEmailHook)
	}
	if c.HookTimeout != 90*time.Second {
		t.Errorf("HookTimeout = %s, want 90s", c.HookTimeout)
	}

	// A malformed or non-positive env duration keeps the current value rather
	// than silently unbounding the hook.
	for _, bad := range []string{"soon", "0", "-1s"} {
		c := Defaults()
		t.Setenv("HOOK_TIMEOUT", bad)
		applyEnv(c)
		if c.HookTimeout != Defaults().HookTimeout {
			t.Errorf("HOOK_TIMEOUT=%q gave %s, want the default %s",
				bad, c.HookTimeout, Defaults().HookTimeout)
		}
	}
}

func TestHookDefaults(t *testing.T) {
	d := Defaults()
	if d.OnEmailHook != "" {
		t.Errorf("default OnEmailHook = %q, want empty (opt-in)", d.OnEmailHook)
	}
	if d.HookTimeout != 30*time.Second {
		t.Errorf("default HookTimeout = %s, want 30s", d.HookTimeout)
	}
}
