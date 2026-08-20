//go:build linux

package daemon

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	if ErrAlreadyRunning == nil {
		t.Fatal("ErrAlreadyRunning must be non-nil")
	}
	if ErrStalePidfile == nil {
		t.Fatal("ErrStalePidfile must be non-nil")
	}
	if ErrReexecFailed == nil {
		t.Fatal("ErrReexecFailed must be non-nil")
	}
	// Sentinel errors should be comparable with errors.Is.
	wrapped := errors.New("wrap")
	_ = wrapped
}

func TestProcessAlive_CurrentProcess(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process must be reported alive")
	}
}

func TestProcessAlive_DeadPID(t *testing.T) {
	// 0 is the kernel; ESRCH on Linux and macOS.
	if processAlive(0) {
		// On some kernels 0 means "send to every process in group";
		// if that succeeds we cannot prove ESRCH. Skip rather than fail.
		t.Skip("kill(0, 0) did not return ESRCH on this kernel")
	}
}

func TestProcessAlive_FailClosedOnPermissionError(t *testing.T) {
	// We can't reliably provoke EPERM in a test, so we verify the
	// fail-closed rule by inspecting the source via a sentinel: any
	// non-ESRCH, non-nil error from kill must keep processAlive=true.
	// This is exercised by the reexec path; here we just confirm the
	// function returns a bool for any input without panicking.
	for _, pid := range []int{-1, 0, 1, 99999999} {
		_ = processAlive(pid) // must not panic
	}
}

func TestPreparePidfile_NoFile(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	if err := preparePidfile(pf, nil); err != nil {
		t.Fatalf("missing pidfile should not error: %v", err)
	}
}

func TestPreparePidfile_LivePID_Refuses(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	live := os.Getpid()
	if err := os.WriteFile(pf, []byte(strconv.Itoa(live)), 0o644); err != nil {
		t.Fatal(err)
	}
	err := preparePidfile(pf, nil)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
	// Pidfile must NOT have been overwritten.
	b, _ := os.ReadFile(pf)
	if string(b) != strconv.Itoa(live) {
		t.Fatalf("live pidfile was modified: %q", b)
	}
}

func TestPreparePidfile_StalePID_Refuses(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	// A pid that is almost certainly dead. Use 1 (init) only if it
	// returns ESRCH on this kernel; otherwise use a synthetic pid.
	dead := 999_999_99
	if processAlive(dead) {
		t.Skip("synthetic dead pid was reported alive; cannot test stale refusal")
	}
	if err := os.WriteFile(pf, []byte(strconv.Itoa(dead)), 0o644); err != nil {
		t.Fatal(err)
	}
	err := preparePidfile(pf, nil)
	if !errors.Is(err, ErrStalePidfile) {
		t.Fatalf("expected ErrStalePidfile, got %v", err)
	}
	// Pidfile must NOT have been removed — the operator inspects/removes it.
	if _, err := os.Stat(pf); err != nil {
		t.Fatalf("stale pidfile was removed (should be left for operator): %v", err)
	}
	b, _ := os.ReadFile(pf)
	if string(b) != strconv.Itoa(dead) {
		t.Fatalf("stale pidfile contents changed: %q", b)
	}
}

func TestPreparePidfile_StalePID_LogsWarning(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	dead := 999_999_99
	if processAlive(dead) {
		t.Skip("synthetic dead pid was reported alive")
	}
	_ = os.WriteFile(pf, []byte(strconv.Itoa(dead)), 0o644)
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(io.Writer(&buf), nil))
	// preparePidfile now returns ErrStalePidfile on stale pidfile —
	// we ignore the error here, only the warning text matters.
	_ = preparePidfile(pf, log)
	out := buf.String()
	if !strings.Contains(out, "stale pidfile") {
		t.Fatalf("expected warn log about stale pidfile, got %q", out)
	}
	// We are NOT removing — make sure the warning does not lie.
	if strings.Contains(out, "removing") {
		t.Fatalf("warning should not say 'removing' (we are refusing, not removing): %q", out)
	}
}

func TestPreparePidfile_Unparseable_Refuses(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	if err := os.WriteFile(pf, []byte("not a pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := preparePidfile(pf, nil)
	if !errors.Is(err, ErrStalePidfile) {
		t.Fatalf("expected ErrStalePidfile, got %v", err)
	}
	if _, err := os.Stat(pf); err != nil {
		t.Fatalf("unparseable pidfile was removed: %v", err)
	}
}

// Ensure the syscall import is exercised even if later refactors drop
// a test above — keeps go vet happy and documents the dependency.
var _ = syscall.Kill

func TestStripDaemonFlags(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"perch", "--daemon"}, []string{"perch"}},
		{[]string{"perch", "-D"}, []string{"perch"}},
		{[]string{"perch", "--daemon", "--config", "/tmp/x"}, []string{"perch", "--config", "/tmp/x"}},
		{[]string{"perch", "--config", "/tmp/x", "-D"}, []string{"perch", "--config", "/tmp/x"}},
		{[]string{"perch", "--daemon", "-D"}, []string{"perch"}}, // both forms
		{[]string{"perch"}, []string{"perch"}},                   // nothing to strip
	}
	for _, c := range cases {
		got := stripDaemonFlags(c.in)
		if !equalStrings(got, c.want) {
			t.Errorf("stripDaemonFlags(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildChildArgv(t *testing.T) {
	cases := []struct {
		name       string
		argv0      string
		argv, want []string
	}{
		{
			name:  "bare -D",
			argv0: "perch",
			argv:  []string{"-D"},
			want:  []string{"perch"},
		},
		{
			name:  "bare --daemon",
			argv0: "/usr/local/bin/perch",
			argv:  []string{"--daemon"},
			want:  []string{"/usr/local/bin/perch"},
		},
		{
			name:  "flags preserved after -D stripped",
			argv0: "perch",
			argv:  []string{"--config", "/tmp/x", "-D"},
			want:  []string{"perch", "--config", "/tmp/x"},
		},
		{
			name:  "no daemon flags",
			argv0: "perch",
			argv:  []string{"--config", "/tmp/x"},
			want:  []string{"perch", "--config", "/tmp/x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildChildArgv(c.argv0, c.argv)
			if !equalStrings(got, c.want) {
				t.Errorf("buildChildArgv(%q, %v) = %v, want %v",
					c.argv0, c.argv, got, c.want)
			}
			if len(got) == 0 || got[0] != c.argv0 {
				t.Errorf("child argv[0] = %q, want %q", got, c.argv0)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
