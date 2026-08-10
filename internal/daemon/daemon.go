// Package daemon implements perch's --daemon mode: re-exec the binary
// detached from the controlling terminal, write the child PID to a
// pidfile, and refuse to start if a live PID is already recorded.
package daemon

import "errors"

var (
	ErrAlreadyRunning = errors.New("daemon already running")
	ErrReexecFailed   = errors.New("daemonize re-exec failed")
)