package hook

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// dumpScript writes an executable script that records each argument it got on
// its own line in outPath, and returns the script's path. One line per
// argument (not a space-joined line) so a test can tell an empty argument
// apart from a missing one.
func dumpScript(t *testing.T, outPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.sh")
	body := "#!/bin/sh\n" +
		": > \"" + outPath + "\"\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + outPath + "\"; done\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// readArgs returns the lines the dump script recorded. A trailing newline
// produces a final empty field, which is dropped.
func readArgs(t *testing.T, outPath string) []string {
	t.Helper()
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("hook did not write %s: %v", outPath, err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TestFirePassesTheArgvContract pins the documented contract:
// <email_id> <subject> <body> <sender> <new_thread|reply_thread>, in that
// order. Scripts index these positionally, so the order is the API.
func TestFirePassesTheArgvContract(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args.txt")
	r := New(dumpScript(t, out), t.TempDir(), time.Minute, quietLogger())

	err := r.Fire(context.Background(), Event{
		EmailID:   "<abc@163.com>",
		Subject:   "status report",
		Body:      "how are the tests doing?",
		Sender:    "alice@163.com",
		NewThread: true,
	})
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}

	want := []string{"<abc@163.com>", "status report", "how are the tests doing?",
		"alice@163.com", FlagNewThread}
	got := readArgs(t, out)
	if len(got) != len(want) {
		t.Fatalf("hook got %d args %#v, want %d %#v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg $%d = %q, want %q", i+1, got[i], want[i])
		}
	}
}

// TestFireThreadFlag pins the fifth argument's two spellings. A script
// distinguishing "record only new threads" reads this and nothing else.
func TestFireThreadFlag(t *testing.T) {
	for _, tc := range []struct {
		newThread bool
		want      string
	}{
		{true, "new_thread"},
		{false, "reply_thread"},
	} {
		out := filepath.Join(t.TempDir(), "args.txt")
		r := New(dumpScript(t, out), t.TempDir(), time.Minute, quietLogger())
		if err := r.Fire(context.Background(), Event{Sender: "a@b.com", NewThread: tc.newThread}); err != nil {
			t.Fatalf("Fire: %v", err)
		}
		got := readArgs(t, out)
		if len(got) != 5 {
			t.Fatalf("got %d args %#v, want 5", len(got), got)
		}
		if got[4] != tc.want {
			t.Errorf("NewThread=%v -> $5 = %q, want %q", tc.newThread, got[4], tc.want)
		}
	}
}

// TestFireKeepsPositionsWhenEmailIDMissing is the reason email_id is passed
// even when the mail has no Message-Id: dropping it would silently shift the
// sender from $4 to $3 and every script would start recording the body.
func TestFireKeepsPositionsWhenEmailIDMissing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args.txt")
	r := New(dumpScript(t, out), t.TempDir(), time.Minute, quietLogger())

	if err := r.Fire(context.Background(), Event{
		Subject: "no id here",
		Body:    "body",
		Sender:  "alice@163.com",
	}); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	got := readArgs(t, out)
	if len(got) != 5 {
		t.Fatalf("got %d args %#v, want 5 (empty email_id must still occupy $1)", len(got), got)
	}
	if got[0] != "" {
		t.Errorf("$1 = %q, want empty", got[0])
	}
	if got[3] != "alice@163.com" {
		t.Errorf("$4 = %q, want the sender — positions shifted", got[3])
	}
}

// TestFireTruncatesOversizedBody guards against E2BIG. Linux rejects the whole
// exec when one argument exceeds MAX_ARG_STRLEN (128 KiB), so an uncapped body
// would mean the hook stops firing on precisely the long emails an operator
// wants recorded.
func TestFireTruncatesOversizedBody(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args.txt")
	r := New(dumpScript(t, out), t.TempDir(), time.Minute, quietLogger())

	// Multi-byte runes so the cut lands mid-rune unless it's rune-aware.
	huge := strings.Repeat("汉", MaxBodyBytes) // 3 bytes each => 3x over the cap
	if err := r.Fire(context.Background(), Event{Sender: "a@b.com", Body: huge}); err != nil {
		t.Fatalf("Fire with an oversized body: %v", err)
	}
	got := readArgs(t, out)
	if len(got) != 5 {
		t.Fatalf("got %d args %#v, want 5", len(got), got)
	}
	if len(got[2]) > MaxBodyBytes {
		t.Errorf("body arg is %d bytes, want <= %d", len(got[2]), MaxBodyBytes)
	}
	if len(got[2]) == 0 {
		t.Error("body arg is empty; truncation must keep the head of the body")
	}
	if !utf8.ValidString(got[2]) {
		t.Error("truncated body is not valid UTF-8 — the cut landed mid-rune")
	}
}

// TestNewDisabledWhenPathEmpty pins how "no hook configured" is expressed:
// a nil *Runner whose methods are no-ops, so no caller needs a branch.
func TestNewDisabledWhenPathEmpty(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if r := New(path, ".", time.Minute, quietLogger()); r != nil {
			t.Fatalf("New(%q) = %v, want nil", path, r)
		}
	}
	var nilRunner *Runner
	if err := nilRunner.Fire(context.Background(), Event{Sender: "a@b.com"}); err != nil {
		t.Errorf("Fire on a nil Runner: %v, want nil", err)
	}
	if nilRunner.Path() != "" || nilRunner.Timeout() != 0 {
		t.Error("nil Runner accessors must be zero-valued")
	}
}

// TestFireTimesOut pins the bound: a hook that never returns must not hold
// the mail loop open.
func TestFireTimesOut(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "sleep.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(script, dir, 200*time.Millisecond, quietLogger())

	start := time.Now()
	err := r.Fire(context.Background(), Event{Sender: "a@b.com"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Fire on a hanging script returned nil, want a timeout error")
	}
	if elapsed > 10*time.Second {
		t.Errorf("Fire took %s; the timeout did not kill the script", elapsed)
	}
}

// TestFireNonZeroExitReports checks the caller gets an error it can log —
// with the script's stderr, which is where an operator's bug shows up.
func TestFireNonZeroExitReports(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fail.sh")
	body := "#!/bin/sh\necho 'ledger is read-only' >&2\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(script, dir, time.Minute, quietLogger())

	err := r.Fire(context.Background(), Event{Sender: "a@b.com"})
	if err == nil {
		t.Fatal("Fire on exit 3 returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "ledger is read-only") {
		t.Errorf("error does not carry the script's stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("error does not carry the exit code: %v", err)
	}
}

// TestFireMissingScriptErrors: a typo'd path must produce a loggable error,
// not a panic and not silence.
func TestFireMissingScriptErrors(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "nope.sh"), t.TempDir(), time.Minute, quietLogger())
	if r == nil {
		t.Fatal("New returned nil for a non-empty path")
	}
	if err := r.Fire(context.Background(), Event{Sender: "a@b.com"}); err == nil {
		t.Error("Fire on a missing script returned nil, want an error")
	}
}

// TestNewResolvesRelativePathAgainstCwd: the script perch runs must not
// depend on perch's cwd at fire time, so the path is absolutised once at
// construction.
func TestNewResolvesRelativePathAgainstCwd(t *testing.T) {
	r := New("record.sh", ".", time.Minute, quietLogger())
	if !filepath.IsAbs(r.Path()) {
		t.Errorf("Path() = %q, want an absolute path", r.Path())
	}
}

// TestNewDefaultsTimeout: a zero timeout would mean "wait forever", which is
// the one thing this bound exists to prevent.
func TestNewDefaultsTimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		r := New("/bin/true", ".", d, quietLogger())
		if r.Timeout() != DefaultTimeout {
			t.Errorf("New(timeout=%s).Timeout() = %s, want %s", d, r.Timeout(), DefaultTimeout)
		}
	}
}

// TestFireRunsInWorkdir pins cwd: a script writing a relative path should land
// in the agent workdir, which is the directory an operator thinks in.
func TestFireRunsInWorkdir(t *testing.T) {
	wd := t.TempDir()
	dir := t.TempDir()
	script := filepath.Join(dir, "pwd.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\npwd > cwd.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(script, wd, time.Minute, quietLogger())
	if err := r.Fire(context.Background(), Event{Sender: "a@b.com"}); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wd, "cwd.txt"))
	if err != nil {
		t.Fatalf("script did not write into the workdir: %v", err)
	}
	// macOS hands out /var symlinks for TempDir, so compare resolved paths.
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	wantDir, _ := filepath.EvalSymlinks(wd)
	if gotDir != wantDir {
		t.Errorf("hook cwd = %q, want %q", gotDir, wantDir)
	}
}
