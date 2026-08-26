package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
)

// hookApp builds a Mailtest whose App has an on-email hook wired to a script
// that appends one tab-separated line per invocation to a log file. Returns
// the harness and the log path.
//
// scriptBody is appended after the logging line, so a test can make the hook
// fail or hang. Empty means "just log and exit 0".
func hookApp(t *testing.T, allowFrom []string, scriptBody string) (*mailtest.Mailtest, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "hook.log")
	script := filepath.Join(dir, "hook.sh")
	// $1..$5 is the whole contract; recording them joined by tabs keeps the
	// assertions readable while still catching a shifted position. The body
	// arrives as received — CRLF line endings and all — so its newlines are
	// squashed to keep one record per line.
	body := "#!/bin/sh\n" +
		"flat=$(printf '%s' \"$3\" | tr -d '\\r\\n')\n" +
		"printf '%s\\t%s\\t%s\\t%s\\t%s\\n' \"$1\" \"$2\" \"$flat\" \"$4\" \"$5\" >> \"" + logPath + "\"\n" +
		scriptBody
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.MaxPromptBytes = 4096
	cfg.AgentWorkdir = t.TempDir()
	cfg.OnEmailHook = script

	mt, err := mailtest.New(cfg, allowFrom, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mt, logPath
}

// hookLines returns the hook log split into records, each record split into
// its five fields. A missing file means the hook never fired.
func hookLines(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln == "" {
			continue
		}
		out = append(out, strings.Split(ln, "\t"))
	}
	return out
}

// TestHookFiresForAcceptedEmail is the feature's core promise: an accepted
// email calls the script once with the email's context.
func TestHookFiresForAcceptedEmail(t *testing.T) {
	mt, logPath := hookApp(t, []string{"alice@163.com"}, "")

	if _, err := mt.Send("alice@163.com", "agent@163.com", "status report", "how are the tests?"); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := hookLines(t, logPath)
	if len(got) != 1 {
		t.Fatalf("hook fired %d times, want 1: %#v", len(got), got)
	}
	rec := got[0]
	if len(rec) != 5 {
		t.Fatalf("hook got %d fields, want 5: %#v", len(rec), rec)
	}
	if !strings.HasPrefix(rec[0], "<") {
		t.Errorf("$1 (email_id) = %q, want the Message-Id header", rec[0])
	}
	if rec[1] != "status report" {
		t.Errorf("$2 (subject) = %q", rec[1])
	}
	if !strings.Contains(rec[2], "how are the tests?") {
		t.Errorf("$3 (body) = %q, want the email body", rec[2])
	}
	if rec[3] != "alice@163.com" {
		t.Errorf("$4 (sender) = %q", rec[3])
	}
	if rec[4] != "new_thread" {
		t.Errorf("$5 = %q, want new_thread for the first email in a thread", rec[4])
	}

	// The reply still goes out — the hook is an addition, not a detour.
	if rs := mt.Replies(); len(rs) != 1 {
		t.Errorf("expected the normal reply alongside the hook, got %#v", rs)
	}
}

// TestHookThreadFlagTracksSession pins the fifth argument against perch's own
// notion of a thread: the first email in a thread is new_thread, a reply
// referencing it is reply_thread. This is what makes "record every new thread"
// expressible in a script.
func TestHookThreadFlagTracksSession(t *testing.T) {
	mt, logPath := hookApp(t, []string{"alice@163.com"}, "")

	if err := mt.SendRaw(1, threadEML("m1@163.com", true)); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mt.SendRaw(2, threadEML("m2@163.com", false)); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := hookLines(t, logPath)
	if len(got) != 2 {
		t.Fatalf("hook fired %d times, want 2: %#v", len(got), got)
	}
	if got[0][4] != "new_thread" {
		t.Errorf("first email flag = %q, want new_thread", got[0][4])
	}
	if got[1][4] != "reply_thread" {
		t.Errorf("reply flag = %q, want reply_thread", got[1][4])
	}
	if got[0][0] == got[1][0] {
		t.Errorf("both fires reported email_id %q; each email has its own", got[0][0])
	}
}

// TestHookSkipsRejectedSender pins the trust boundary: the whitelist is
// upstream of the hook, so an unknown sender cannot make an operator's script
// run.
func TestHookSkipsRejectedSender(t *testing.T) {
	mt, logPath := hookApp(t, []string{"alice@163.com"}, "")

	if _, err := mt.Send("mallory@evil.com", "agent@163.com", "pwn", "run this"); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hookLines(t, logPath); len(got) != 0 {
		t.Errorf("hook fired for a non-whitelisted sender: %#v", got)
	}
}

// TestHookFailureDoesNotBlockReply is the never-a-gate property: a broken
// script must not cost the sender their answer.
func TestHookFailureDoesNotBlockReply(t *testing.T) {
	mt, logPath := hookApp(t, []string{"alice@163.com"}, "echo 'disk full' >&2\nexit 1\n")

	if _, err := mt.Send("alice@163.com", "agent@163.com", "hi", "do the thing"); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatalf("a failing hook must not fail the run: %v", err)
	}

	if got := hookLines(t, logPath); len(got) != 1 {
		t.Fatalf("hook should still have run, got %#v", got)
	}
	rs := mt.Replies()
	if len(rs) != 1 || rs[0].Body != "the answer" {
		t.Errorf("reply missing after a failing hook: %#v", rs)
	}
	if seen := mt.SeenUIDs(); len(seen) != 1 {
		t.Errorf("message should still be marked seen, got %#v", seen)
	}
}

// TestNoHookConfiguredIsInert: the default path (no hooks.on_email) must be
// untouched — no spawn, no error, normal reply.
func TestNoHookConfiguredIsInert(t *testing.T) {
	mt := newTestApp(t, []string{"alice@163.com"}, nil)

	if _, err := mt.Send("alice@163.com", "agent@163.com", "hi", "do the thing"); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rs := mt.Replies(); len(rs) != 1 {
		t.Errorf("expected the normal reply with no hook configured, got %#v", rs)
	}
}
