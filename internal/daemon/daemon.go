// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if a live PID is already recorded.
package daemon

import (
	"errors"
	"log/slog"
	"os"
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
