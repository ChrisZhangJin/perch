//go:build linux

// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if any pidfile is already recorded
// (live or stale). Stale pidfiles must be removed by the operator —
// auto-removal would mask a dying daemon that the user needs to
// investigate.
//
// This file is Linux-only. Daemon semantics (setsid, /proc/self/exe,
// SIGKILL by pid) are POSIX-with-Linux-specifics; the daemon path is
// not offered on darwin/windows. See daemon_stub.go for the fallback
// stubs on non-Linux platforms.
package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	ErrAlreadyRunning = errors.New("daemon already running")
	ErrStalePidfile   = errors.New("stale pidfile present")
	ErrReexecFailed   = errors.New("daemonize re-exec failed")
)

// verifyGrace is how long Daemonize waits after writing the pidfile
// before checking the child is still alive. The child needs a moment
// to read config and set up logging; a sub-second failure window is
// plausible. 300 ms catches immediate crash without slowing normal
// startups noticeably. Not configurable — keep the daemon handoff
// hermetic.
const verifyGrace = 300 * time.Millisecond

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

// preparePidfile inspects an existing pidfile at path and either
// refuses to start (live pid) or refuses to start (stale pid or
// unparseable content). It does NOT remove the existing pidfile and
// does NOT write the new pid — that happens after the re-exec so a
// failed re-exec leaves no pidfile behind. The defensive refusal
// (vs. silent cleanup of stale pidfiles) means an operator with a
// dying daemon must inspect and remove the file manually; auto-
// cleanup would mask the symptom. A nil logger is allowed (warnings
// are skipped).
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
		// Garbage in the pidfile — refuse; do not auto-remove.
		if log != nil {
			log.Warn("unparseable pidfile; refusing to start", "path", path)
		}
		return ErrStalePidfile
	}
	if processAlive(pid) {
		return ErrAlreadyRunning
	}
	if log != nil {
		log.Warn("stale pidfile present; refusing to start",
			"path", path, "pid", pid)
	}
	return ErrStalePidfile
}

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

// buildChildArgv assembles the argv passed to os.StartProcess for the
// daemon child: argv0 prepended so /proc/<pid>/cmdline shows the
// program name (else `ps` reports a blank command), followed by the
// parent's argv with daemon flags stripped.
func buildChildArgv(argv0 string, argv []string) []string {
	return append([]string{argv0}, stripDaemonFlags(argv)...)
}

// Daemonize re-execs the current binary as a detached child, writes
// the child's PID to pidfilePath, and returns the child PID. argv0
// is os.Args[0] from the parent (the program name as invoked);
// argv is os.Args[1:]. The caller (main) must exit 0 after Daemonize
// returns successfully — the daemon is the child, not the parent.
// On any error no pidfile is left behind.
//
// The child process is marked with two environment variables so the
// re-execed code path can detect it's the daemon and know where the
// pidfile is:
//   - PERCH_DAEMON_CHILD=1 signals "you are the daemon child, not the parent"
//   - PERCH_DAEMON_PIDFILE=<path> provides the pidfile path (the child
//     has no --daemon flag and no local pidfilePath variable)
//
// main.go reads both on startup. The PERCH_DAEMON_PIDFILE env var is
// critical because the child skips the daemon handoff branch (which
// sets pidfilePath locally), so without it the child would call
// os.Remove("") on shutdown, silently failing to clean up the pidfile.
//
// preparePidfile runs first: an existing pidfile (live or stale)
// refuses the start. The /dev/null open errors are propagated. The
// pidfile is written only after StartProcess succeeds, and after
// that a short grace window re-checks the child is still alive so
// a crash during its first config/logger setup leaves the parent
// with a meaningful error instead of a stale pidfile.
func Daemonize(argv0 string, argv []string, pidfilePath string, extraEnv []string, log *slog.Logger) (int, error) {
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
		// Fallback: os.Executable uses readlink(/proc/self/exe), which
		// returns ENOENT in some environments (kernel without procfs,
		// chroots with masked /proc, or unlinked-binary situations).
		// exec.LookPath on argv0 is the standard fallback and works
		// whenever the binary is reachable through PATH or as a path
		// relative to the current working directory.
		if argv0 != "" {
			if lp, lpErr := exec.LookPath(argv0); lpErr == nil {
				self = lp
				err = nil
			}
		}
		if err != nil {
			return 0, fmt.Errorf("%w: locate executable: %v", ErrReexecFailed, err)
		}
	}
	child, err := os.StartProcess(self, buildChildArgv(argv0, argv),
		&os.ProcAttr{
			Env: append(os.Environ(),
				append([]string{
					"PERCH_DAEMON_CHILD=1",
					"PERCH_DAEMON_PIDFILE=" + pidfilePath,
				}, extraEnv...)...,
			),
			Sys:   &syscall.SysProcAttr{Setsid: true},
			Files: []*os.File{devnull, devnull, devnull},
		})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrReexecFailed, err)
	}
	// Ensure the parent directory exists. The wizard creates ~/.perch
	// with mode 0700 when it persists the YAML, but a user invoking
	// --daemon on a fresh install (no YAML yet) has no ~/.perch/.
	// 0o755 is fine here: the only file in there is the pidfile.
	if err := os.MkdirAll(filepath.Dir(pidfilePath), 0o755); err != nil {
		_ = child.Kill()
		_, _ = child.Wait()
		return 0, fmt.Errorf("%w: mkdir for pidfile: %v", ErrReexecFailed, err)
	}
	if err := os.WriteFile(pidfilePath, []byte(strconv.Itoa(child.Pid)), 0o644); err != nil {
		// Pidfile write failed after the child has started. Best
		// effort: kill the child so we don't leave an unmanaged
		// daemon running.
		_ = child.Kill()
		_, _ = child.Wait()
		return 0, fmt.Errorf("%w: write pidfile: %v", ErrReexecFailed, err)
	}
	// Verify the child actually stayed up long enough to take over.
	// The child reads config and sets up logging before servicing
	// mail, so a sub-second failure window is plausible; verifyGrace
	// is enough to catch immediate crash without slowing normal
	// startups noticeably. If we don't see it alive after the
	// grace, the pidfile points at a dead process — clean up so
	// the operator's next --daemon attempt sees a clean slate.
	time.Sleep(verifyGrace)
	if !processAlive(child.Pid) {
		_ = os.Remove(pidfilePath)
		return 0, fmt.Errorf("%w: child process %d died after start",
			ErrReexecFailed, child.Pid)
	}
	return child.Pid, nil
}
