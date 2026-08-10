// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if a live PID is already recorded.
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
)

var (
	ErrAlreadyRunning = errors.New("daemon already running")
	ErrReexecFailed   = errors.New("daemonize re-exec failed")
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
// preparePidfile runs first: a live existing PID refuses the start,
// a stale PID is cleaned up. The /dev/null open errors are
// propagated. The pidfile is written only after StartProcess
// succeeds.
func Daemonize(argv0 string, argv []string, pidfilePath string, log *slog.Logger) (int, error) {
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
	child, err := os.StartProcess(self, stripDaemonFlags(argv),
		&os.ProcAttr{
			Env: append(os.Environ(),
				"PERCH_DAEMON_CHILD=1",
				"PERCH_DAEMON_PIDFILE="+pidfilePath,
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
	return child.Pid, nil
}
