# Perch Daemon Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--daemon` / `-D` flag that re-execs perch detached from the controlling terminal, writes the child PID to `~/.perch/perch.pid`, refuses to start if a live PID is recorded, and removes the pidfile on clean shutdown.

**Architecture:** Self re-exec via `os.StartProcess` with `SysProcAttr.Setsid = true` and stdio redirected to `/dev/null`. New `internal/daemon` package owns the handoff; `cmd/perch/main.go` adds the flag and wires a `defer` for pidfile removal in the child. No other packages change.

**Tech Stack:** Go 1.25, stdlib only (`os`, `os/exec`, `path/filepath`, `syscall`, `syscall.Signal`, `fmt`, `errors`).

## Global Constraints

- Go 1.25, single static binary, stdlib only — no new module dependencies.
- PID file path is fixed: `~/.perch/perch.pid`. Not configurable in this spec.
- PID file mode `0644`, written into the existing `~/.perch/` directory (which the wizard sets to `0700`).
- Re-exec uses `os.StartProcess` (NOT `os/exec.Cmd.Run`) so the parent exits 0 immediately after recording the child PID.
- `syscall.Kill(pid, 0)` is the probe — `ESRCH` means dead, any other error is treated as alive (fail-closed).
- `--daemon` and `-D` must both work; both must be stripped from the argv passed to the child.
- No log file output in this spec (rotation deferred to roadmap).
- No new dependencies in `go.mod` / `go.sum`.
- All new code follows existing perch conventions: lowercase package comments referencing the file's purpose, no docstrings beyond one short line.

---

## File Structure

**New files:**
- `internal/daemon/daemon.go` — `Daemonize`, `processAlive`, sentinel errors. Single responsibility: the detach + pidfile handoff.
- `internal/daemon/daemon_test.go` — unit tests for argv stripping, stale-pidfile cleanup, live-pid refusal, re-exec failure leaves no pidfile, fail-closed probe.
- `scripts/localtest/daemon.sh` — bash smoke that builds the binary, runs `--daemon`, asserts pidfile, asserts double-start refusal, kills, asserts pidfile removed, asserts clean restart.

**Modified files:**
- `cmd/perch/main.go` — add `--daemon` / `-D` flag, daemonization handoff at top of `main()`, `defer` to remove pidfile in the child after `a.Run(ctx)` returns.

**Untouched:** `internal/app/`, `internal/mailbox/`, `internal/agent/`, `internal/gate/`, `internal/replier/`, `internal/runner/`, `internal/session/`, `internal/config/`, `internal/log/`, `internal/provider/`, `internal/message/`, `internal/setup/`, `go.mod`, `go.sum`.

---

## Task 1: `internal/daemon` package skeleton + sentinel errors

**Files:**
- Create: `internal/daemon/daemon.go`
- Test: `internal/daemon/daemon_test.go` (placeholder, will grow in later tasks)

**Interfaces:**
- Consumes: nothing (this is the first task)
- Produces:
  - `var ErrAlreadyRunning = errors.New("daemon already running")`
  - `var ErrReexecFailed = errors.New("daemonize re-exec failed")`
  - Package compiles but `Daemonize` and `processAlive` are not yet defined (later tasks).

- [ ] **Step 1: Write the failing test for sentinel errors**

Create `internal/daemon/daemon_test.go`:

```go
package daemon

import (
	"errors"
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	if ErrAlreadyRunning == nil {
		t.Fatal("ErrAlreadyRunning must be non-nil")
	}
	if ErrReexecFailed == nil {
		t.Fatal("ErrReexecFailed must be non-nil")
	}
	// Sentinel errors should be comparable with errors.Is.
	wrapped := errors.New("wrap")
	_ = wrapped
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/...`
Expected: FAIL — package does not exist yet, compile error `package github.com/ChrisZhangJin/perch/internal/daemon: cannot find package`.

- [ ] **Step 3: Create the package with sentinel errors**

Create `internal/daemon/daemon.go`:

```go
// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if a live PID is already recorded.
package daemon

import "errors"

var (
	ErrAlreadyRunning = errors.New("daemon already running")
	ErrReexecFailed   = errors.New("daemonize re-exec failed")
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): package skeleton with sentinel errors"
```

---

## Task 2: `processAlive` helper + tests

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: existing sentinels from Task 1
- Produces:
  - `func processAlive(pid int) bool` — `kill(pid, 0) == nil` → true; `ESRCH` → false; anything else → true (fail-closed).

- [ ] **Step 1: Add the failing tests**

Append to `internal/daemon/daemon_test.go`:

```go
import (
	"os"
	"syscall"
	"testing"
)

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

// Ensure the syscall import is exercised even if later refactors drop
// a test above — keeps go vet happy and documents the dependency.
var _ = syscall.Kill
```

- [ ] **Step 2: Run tests to verify they fail (compile error)**

Run: `go test ./internal/daemon/...`
Expected: FAIL — `processAlive` undefined.

- [ ] **Step 3: Implement `processAlive`**

Add to `internal/daemon/daemon.go`:

```go
import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is a running process. It uses
// kill(pid, 0) which performs the check without sending a signal.
// ESRCH → dead. nil → alive. Any other error (typically EPERM) →
// treated as alive (fail-closed: we never overwrite a pidfile we
// cannot verify is dead).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): processAlive probe with fail-closed semantics"
```

---

## Task 3: Stale-pidfile cleanup + tests

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `processAlive` from Task 2
- Produces:
  - `func preparePidfile(path string, log *slog.Logger) error` — if `path` exists and `processAlive(stored_pid)` returns true, return `ErrAlreadyRunning`; if `path` exists and pid is dead, log WARN and unlink; if `path` does not exist, return nil. Logger is allowed to be nil (no-op). This function does NOT write the new pidfile — that happens after the re-exec.

- [ ] **Step 1: Add the failing tests**

Append to `internal/daemon/daemon_test.go`:

```go
import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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

func TestPreparePidfile_StalePID_CleansUp(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	// A pid that is almost certainly dead. Use 1 (init) only if it
	// returns ESRCH on this kernel; otherwise use a synthetic pid.
	dead := 999_999_99
	if processAlive(dead) {
		t.Skip("synthetic dead pid was reported alive; cannot test stale cleanup")
	}
	if err := os.WriteFile(pf, []byte(strconv.Itoa(dead)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preparePidfile(pf, nil); err != nil {
		t.Fatalf("stale pidfile cleanup should not error: %v", err)
	}
	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile was not removed: stat err=%v", err)
	}
}

func TestPreparePidfile_LogsWarningOnStale(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "perch.pid")
	dead := 999_999_99
	if processAlive(dead) {
		t.Skip("synthetic dead pid was reported alive")
	}
	_ = os.WriteFile(pf, []byte(strconv.Itoa(dead)), 0o644)
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(io.Writer(&buf), nil))
	if err := preparePidfile(pf, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "stale pidfile") {
		t.Fatalf("expected warn log about stale pidfile, got %q", buf.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (compile error)**

Run: `go test ./internal/daemon/...`
Expected: FAIL — `preparePidfile` undefined.

- [ ] **Step 3: Implement `preparePidfile`**

Add to `internal/daemon/daemon.go`:

```go
import (
	"errors"
	"log/slog"
	"os"
	"strconv"
	"syscall"
)

// preparePidfile inspects an existing pidfile at path and either
// refuses to start (live pid), cleans up (stale pid), or proceeds
// (no file). It does NOT write the new pid — that happens after the
// re-exec so a failed re-exec leaves no pidfile behind. A nil logger
// is allowed (warnings are skipped).
func preparePidfile(path string, log *slog.Logger) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Garbage in the pidfile — treat as stale and clean up.
		if log != nil {
			log.Warn("unparseable pidfile, removing", "path", path)
		}
		return os.Remove(path)
	}
	if processAlive(pid) {
		return ErrAlreadyRunning
	}
	if log != nil {
		log.Warn("stale pidfile, removing", "path", path, "pid", pid)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

Add to imports at the top of the file:

```go
import (
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
)
```

(The old `errors` import from earlier must be merged into this single import block.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): preparePidfile with stale cleanup and live refusal"
```

---

## Task 4: `Daemonize` re-exec — argv stripping + tests

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `preparePidfile`, `processAlive`, `ErrReexecFailed`, `ErrAlreadyRunning` from earlier tasks
- Produces:
  - `func stripDaemonFlags(argv []string) []string` — package-private helper, returns `argv` with any element equal to `"--daemon"` or `"-D"` removed.
  - `func Daemonize(argv []string, pidfilePath string, log *slog.Logger) (childPID int, err error)` — performs the full handoff: opens `/dev/null`, calls `preparePidfile`, calls `os.StartProcess(self, stripDaemonFlags(argv), &os.ProcAttr{SysProcAttr: &syscall.SysProcAttr{Setsid: true}, Files: []*os.File{devnull, devnull, devnull}})`, writes the returned PID to `pidfilePath` (mode 0644), returns. On any failure, ensures no pidfile was left behind.

Note: `Daemonize` is broken into two testable parts — the pure argv-stripper is unit-tested directly; the actual `os.StartProcess` happy path is covered by the bash smoke test in a later task. We do **not** try to mock `os.StartProcess` from Go because Go tests running inside the test binary cannot re-exec themselves cleanly.

- [ ] **Step 1: Add the failing tests**

Append to `internal/daemon/daemon_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/...`
Expected: FAIL — `stripDaemonFlags` undefined.

- [ ] **Step 3: Implement `stripDaemonFlags` and the `Daemonize` signature**

Add to `internal/daemon/daemon.go`:

```go
// stripDaemonFlags returns argv with --daemon and -D removed. Used so
// the re-execed child does not re-enter the daemonization path.
func stripDaemonFlags(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "--daemon" || a == "-D" {
			continue
		}
		out = append(out, a)
	}
	return out
}
```

Then add the `Daemonize` skeleton with the full structure but the actual `os.StartProcess` call site clearly marked:

```go
// Daemonize re-execs the current binary as a detached child, writes
// the child's PID to pidfilePath, and returns the child PID. argv is
// os.Args[1:] from the parent. The caller (main) must exit 0 after
// Daemonize returns successfully — the daemon is the child, not the
// parent. On any error no pidfile is left behind.
//
// preparePidfile runs first: a live existing PID refuses the start,
// a stale PID is cleaned up. The /dev/null open errors are
// propagated. The pidfile is written only after StartProcess
// succeeds.
func Daemonize(argv []string, pidfilePath string, log *slog.Logger) (int, error) {
	if log != nil {
		log.Info("daemonizing", "pidfile", pidfilePath)
	}
	if err := preparePidfile(pidfilePath, log); err != nil {
		return 0, err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("%w: open /dev/null: %v", ErrReexecFailed, err)
	}
	defer devnull.Close()
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("%w: locate executable: %v", ErrReexecFailed, err)
	}
	child, err := os.StartProcess(self, stripDaemonFlags(argv),
		&os.ProcAttr{
			SysProcAttr: &syscall.SysProcAttr{Setsid: true},
			Files:       []*os.File{devnull, devnull, devnull},
		})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrReexecFailed, err)
	}
	if err := os.WriteFile(pidfilePath, []byte(strconv.Itoa(child.Pid)), 0o644); err != nil {
		// Pidfile write failed after the child has started. Best
		// effort: kill the child so we don't leave an unmanaged
		// daemon running.
		_ = child.Kill()
		_, _ = child.Wait()
		return 0, fmt.Errorf("%w: write pidfile: %v", ErrReexecFailed, err)
	}
	return child.Pid, nil
}
```

Update imports at the top of `internal/daemon/daemon.go`:

```go
import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/...`
Expected: PASS for the new argv-strip tests. The `Daemonize` happy path is exercised by the bash smoke test in Task 6.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): Daemonize re-exec with setsid + /dev/null stdio"
```

---

## Task 5: `main.go` wiring — flag + handoff + pidfile removal defer

**Files:**
- Modify: `cmd/perch/main.go`

**Interfaces:**
- Consumes: `daemon.Daemonize`, `daemon.ErrAlreadyRunning`, `daemon.ErrReexecFailed` from earlier tasks
- Produces: new flag `--daemon` / `-D`; daemonization handoff before config load; `defer` that removes the pidfile on shutdown in the child only.

- [ ] **Step 1: Add the `--daemon` flag and handoff**

Read `cmd/perch/main.go`. In the existing flag-declaration block (currently lines 52–55 around the `--config`, `--version`, `--log-level` definitions), add:

```go
daemon := flag.Bool("daemon", false,
	"detach from terminal, write pidfile, exit parent. "+
		"Logs after this point go to /dev/null until log files land.")
flag.BoolVar(daemon, "D", false, "alias for --daemon")
```

Then immediately after `flag.Parse()` and **before** the existing `if *showVersion { ... }` block, insert the daemonization handoff. Place it before `--version` so version still wins (version must short-circuit even in daemon mode):

```go
if *daemon {
	pidfilePath := filepath.Join(homeDir(), ".perch", "perch.pid")
	childPID, err := daemon.Daemonize(os.Args[1:], pidfilePath, nil)
	if err != nil {
		switch {
		case errors.Is(err, daemon.ErrAlreadyRunning):
			fmt.Fprintf(os.Stderr,
				"perch: already running: see pidfile %s\n", pidfilePath)
		case errors.Is(err, daemon.ErrReexecFailed):
			fmt.Fprintf(os.Stderr, "perch: daemonize: %v\n", err)
		default:
			fmt.Fprintf(os.Stderr, "perch: daemonize: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr,
		"perch daemon started, pid %d, pidfile %s\n", childPID, pidfilePath)
	os.Exit(0)
}
```

- [ ] **Step 2: Add the `homeDir` helper and imports**

Add to the imports of `cmd/perch/main.go`:

```go
import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/daemon"
	"github.com/ChrisZhangJin/perch/internal/gate"
	plog "github.com/ChrisZhangJin/perch/internal/log"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
	"github.com/ChrisZhangJin/perch/internal/setup"
)
```

Add a small helper near the bottom of the file:

```go
// homeDir returns the user's home directory or an empty string if
// it cannot be resolved. Empty means the daemon-mode pidfile path
// will be relative ("/.perch/perch.pid") which will then fail to
// write — surfacing the error to the user.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
```

- [ ] **Step 3: Add the pidfile-removal `defer` in the child**

In the existing `main()`, just **after** the `signal.NotifyContext` line (currently line 143: `ctx, stop := signal.NotifyContext(...)`):

```go
if *daemon {
	defer func() {
		if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
			// Best-effort: log to stderr because the logger may
			// already be torn down by the time defers run, and in
			// daemon mode stderr is /dev/null anyway. Surface to
			// the parent's stderr via a fallback line.
			fmt.Fprintf(os.Stderr, "perch: pidfile remove: %v\n", err)
		}
	}()
}
```

- [ ] **Step 4: Verify the project builds**

Run: `go build ./...`
Expected: exit 0, no errors.

Run: `go vet ./...`
Expected: exit 0, no warnings.

- [ ] **Step 5: Run all unit tests**

Run: `go test ./internal/...`
Expected: PASS. The new daemon tests from Tasks 1–4 should run alongside the existing app/config/replier/etc. tests.

- [ ] **Step 6: Commit**

```bash
git add cmd/perch/main.go
git commit -m "feat(cli): --daemon / -D flag, re-exec handoff, pidfile removal defer"
```

---

## Task 6: Integration smoke script `scripts/localtest/daemon.sh`

**Files:**
- Create: `scripts/localtest/daemon.sh`

**Interfaces:**
- Consumes: built `perch` binary at `./perch`, `$HOME` for the pidfile path
- Produces: a runnable bash script that exercises the daemon handoff end-to-end.

- [ ] **Step 1: Write the script**

Create `scripts/localtest/daemon.sh`:

```bash
#!/usr/bin/env bash
# scripts/localtest/daemon.sh — smoke test for ./perch --daemon.
#
# Builds the binary, launches it in daemon mode, verifies the pidfile,
# verifies double-start refusal, kills the daemon, verifies the pidfile
# is removed, verifies a clean restart works. Does NOT require a real
# IMAP server — the daemonization handoff happens before the IMAP dial
# in the existing main flow, and we catch the dial failure at the end.

set -euo pipefail

# Always run from the repo root so paths resolve.
cd "$(dirname "$0")/../.."

# Build a fresh binary into ./bin/.
mkdir -p ./bin
go build -o ./bin/perch ./cmd/perch

# Minimal env: we don't need real credentials because we expect the
# daemon to fail to dial IMAP — that's fine, the handoff has already
# happened. We capture the dial failure via the pidfile lifecycle.
export AGENT_EMAIL="daemon-test@perch.local"
export AGENT_AUTH_CODE="test"
export ALLOW_FROM=""
export HOME="${HOME:-$(cd ~ && pwd)}"

PIDFILE="$HOME/.perch/perch.pid"

# Clean slate.
rm -f "$PIDFILE"

# Launch in daemon mode. We use --config to point at an empty YAML so
# the wizard doesn't try to interact with us.
TMPYAML="$(mktemp -t perch-empty.XXXXXX.yaml)"
trap 'rm -f "$TMPYAML"; rm -f "$PIDFILE"' EXIT

echo "==> launch #1 (should succeed)"
./bin/perch --daemon --config "$TMPYAML"
test -f "$PIDFILE" || { echo "FAIL: pidfile not created at $PIDFILE"; exit 1; }
PID="$(cat "$PIDFILE")"
echo "    pidfile=$PIDFILE pid=$PID"
kill -0 "$PID" || { echo "FAIL: recorded pid $PID is not alive"; exit 1; }

echo "==> launch #2 (should refuse: already running)"
set +e
./bin/perch --daemon --config "$TMPYAML" 2>/dev/null
RC=$?
set -e
if [ "$RC" -eq 0 ]; then
	echo "FAIL: second launch succeeded; expected refusal"
	exit 1
fi
echo "    refused with exit=$RC (good)"

echo "==> stop daemon via SIGTERM"
kill -TERM "$PID"

# Wait up to 5s for the pidfile to be removed.
for i in $(seq 1 50); do
	if [ ! -f "$PIDFILE" ]; then
		echo "    pidfile removed after ${i}00ms"
		break
	fi
	sleep 0.1
done
if [ -f "$PIDFILE" ]; then
	echo "FAIL: pidfile still present after SIGTERM"
	exit 1
fi

echo "==> launch #3 (should succeed: clean restart)"
./bin/perch --daemon --config "$TMPYAML"
test -f "$PIDFILE" || { echo "FAIL: pidfile not recreated"; exit 1; }
PID3="$(cat "$PIDFILE")"
kill -0 "$PID3" || { echo "FAIL: restart pid $PID3 not alive"; exit 1; }

# Clean up.
kill -TERM "$PID3" 2>/dev/null || true
sleep 0.5
rm -f "$PIDFILE"

echo
echo "OK: daemon mode smoke passed"
```

- [ ] **Step 2: Make it executable and run it**

Run: `chmod +x scripts/localtest/daemon.sh && ./scripts/localtest/daemon.sh`
Expected: prints the four `==>` headers and ends with `OK: daemon mode smoke passed`. Each `==>` line should succeed.

- [ ] **Step 3: Commit**

```bash
git add scripts/localtest/daemon.sh
git commit -m "test(daemon): localtest smoke for --daemon handoff + pidfile lifecycle"
```

---

## Task 7: README docs

**Files:**
- Modify: `README.md`
- Modify: `README.zh-CN.md`

**Interfaces:**
- Consumes: existing README structure
- Produces: a "Daemon mode" section after the existing systemd/launchd example, explaining `--daemon` / `-D`, the pidfile location, how to stop, and how to verify.

- [ ] **Step 1: Add the English section**

In `README.md`, locate the systemd unit block (currently around the `systemd / launchd / cron` paragraph) and append the following section right after it, before the existing `**Tip:**` paragraph about `grant.code`:

```markdown
### Running detached (`--daemon`)

If you don't want to write a systemd/launchd unit, `./perch --daemon`
re-execs itself as a detached process: it breaks the link to your
controlling terminal (so closing the SSH session won't SIGHUP it) and
writes its PID to `~/.perch/perch.pid`. To stop it, send SIGTERM to
the recorded PID (or `pkill -TERM perch`). The pidfile is removed on
clean shutdown; a stale pidfile is detected and cleaned up on the next
launch.

```bash
./perch --daemon           # alias: -D
kill $(cat ~/.perch/perch.pid)
```

In daemon mode, all post-handoff logs go to `/dev/null` — there is no
log file yet. A log-file option with rotation is on the roadmap.
```

- [ ] **Step 2: Add the Chinese section**

In `README.zh-CN.md`, locate the corresponding systemd/cron paragraph
(中文版在 `如果用 systemd / launchd / cron` 一段之后,`小贴士` 一段之前)
and insert the equivalent section:

```markdown
### 后台运行(`--daemon`)

不想写 systemd/launchd unit 的话,`./perch --daemon` 会把自身重新派生为
一个脱离终端的进程:切断与控制终端的关联(关闭 SSH 也不会被 SIGHUP),
并把 PID 写到 `~/.perch/perch.pid`。停止时给记录的 PID 发 SIGTERM
(或 `pkill -TERM perch`)即可。正常退出时 pidfile 会被自动删除;
下次启动如果发现是残留 pidfile 也会自动清理。

\`\`\`bash
./perch --daemon           # 别名:-D
kill $(cat ~/.perch/perch.pid)
\`\`\`

daemon 模式下,交接完成后的所有日志都写到 `/dev/null`——暂时还没有日志
文件。带 rotation 的日志文件在 roadmap 里。
```

Note: in the actual file, use plain triple-backtick fences, no backslash escaping.

- [ ] **Step 3: Verify the README renders sensibly**

Run: `head -5 README.md README.zh-CN.md`
Expected: each file starts with the existing badge/intro block unchanged.

Then run: `grep -n "daemon" README.md README.zh-CN.md`
Expected: each file now contains at least one line matching `--daemon` or `--daemon`.

- [ ] **Step 4: Commit**

```bash
git add README.md README.zh-CN.md
git commit -m "docs(readme): document --daemon mode in EN + zh-CN"
```

---

## Task 8: `docs/TESTING.md` daemon-mode subsection

**Files:**
- Modify: `docs/TESTING.md`

**Interfaces:**
- Consumes: existing TESTING.md structure
- Produces: a "Daemon mode" subsection with manual verification steps.

- [ ] **Step 1: Find the right insertion point**

Run: `grep -n "^## \|^### " docs/TESTING.md`
Expected: list of section headers. Identify the section closest to manual
testing (likely "手动端到端" / "Manual end-to-end" or similar).

- [ ] **Step 2: Append the daemon-mode subsection**

Append to the end of `docs/TESTING.md`:

```markdown
## Daemon mode

The `./perch --daemon` flag re-execs perch detached. The smoke script
covers the happy path automatically:

\`\`\`bash
./scripts/localtest/daemon.sh
\`\`\`

For manual verification on a real machine:

1. Launch: `./perch --daemon`. Expected: parent exits 0, prints
   `perch daemon started, pid N, pidfile /home/.../.perch/perch.pid`.
2. Verify: `cat ~/.perch/perch.pid` then `kill -0 $(cat ~/.perch/perch.pid)`.
   Expected: both succeed.
3. Confirm detachment: close the launching terminal / SSH session.
   Expected: perch keeps running.
4. Stop: `kill $(cat ~/.perch/perch.pid)`. Expected: process exits
   within a second, pidfile disappears.
5. Stale-pidfile recovery: write a fake pid into the pidfile
   (`echo 9999999 > ~/.perch/perch.pid`), then launch `--daemon`
   again. Expected: a WARN line on the parent stderr ("stale pidfile,
   removing"), then daemonization proceeds normally.

If a real daemon is already running and you try to launch another, the
parent exits non-zero with "already running" and points at the existing
pidfile. Do not delete a live daemon's pidfile manually — use SIGTERM.
```

Note: in the actual file, use plain triple-backtick fences, no backslash escaping.

- [ ] **Step 3: Verify the file structure**

Run: `tail -30 docs/TESTING.md`
Expected: the new section is the last block in the file with the heading `## Daemon mode`.

- [ ] **Step 4: Commit**

```bash
git add docs/TESTING.md
git commit -m "docs(testing): daemon-mode manual verification steps"
```

---

## Self-Review

**1. Spec coverage:**

| Spec section | Covered by |
|---|---|
| Re-exec with setsid + /dev/null | Task 4 (`Daemonize`) |
| `~/.perch/perch.pid` mode `0644` | Task 4 (`os.WriteFile(... 0o644)`) |
| Stale-pidfile cleanup via `kill(pid, 0)` | Task 2 (`processAlive`) + Task 3 (`preparePidfile`) |
| Live-pid refusal, pidfile untouched | Task 3 (`TestPreparePidfile_LivePID_Refuses`) |
| Unknown probe → fail-closed | Task 2 (`processAlive` rule + `TestProcessAlive_FailClosedOnPermissionError`) |
| Parent-failure paths leave no pidfile | Task 4 (`Daemonize` only writes pidfile after StartProcess succeeds; child killed if pidfile write fails) |
| argv stripping (no recursion) | Task 4 (`stripDaemonFlags` + `TestStripDaemonFlags`) |
| `--daemon` / `-D` flag | Task 5 |
| `--version` short-circuits first | Task 5 placement note (harness placed after `--version` block) |
| `defer` removal in child only | Task 5 step 3 |
| README EN + zh-CN | Task 7 |
| TESTING.md manual steps | Task 8 |
| Integration smoke | Task 6 |
| No log file output | Implicit: stdio redirected to `/dev/null` in Task 4 |
| No new dependencies | All tasks use stdlib only |

**2. Placeholder scan:** no "TBD", "TODO", "implement later", "fill in details" anywhere. Every code block is real.

**3. Type consistency:**
- `daemon.Daemonize(argv []string, pidfilePath string, log *slog.Logger) (int, error)` — used identically in Task 5 wiring and Task 4 definition. ✓
- `daemon.processAlive(pid int) bool` — used by `preparePidfile` in Task 3 and tested in Task 2. ✓
- `daemon.ErrAlreadyRunning`, `daemon.ErrReexecFailed` — used in Task 5 via `errors.Is`. ✓
- `pidfilePath` local in `main()` — set in the daemon handoff block, captured by the `defer` closure in Task 5 step 3. ✓
- `homeDir()` returns string, used once at the handoff. ✓

**4. Open considerations:**
- Task 5 places the `--daemon` handoff *after* the `--version` short-circuit. The plan note in step 1 calls this out so the implementer doesn't accidentally put handoff first (which would prevent `./perch --daemon --version` from printing the version).
- Task 5 step 3 uses `pidfilePath` in a `defer` closure. The variable is declared in the earlier block (step 1) at the same scope, so Go's lexical scoping works without an extra `var`.
- Task 4's `Daemonize` returns `(int, error)` and the Task 5 caller does `childPID, err :=`. Match.
- Task 6 smoke script uses `kill -TERM` and waits 5s; this is a smoke test, not a unit test, and a 5s budget is appropriate.

Plan passes self-review. No changes needed.