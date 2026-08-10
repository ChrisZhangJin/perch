// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if a live PID is already recorded.
package daemon

import (
	"errors"
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
