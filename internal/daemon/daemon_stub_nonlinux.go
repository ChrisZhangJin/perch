//go:build !linux

// Daemon stubs for non-Linux builds. --daemon is a Linux-specific feature
// (setsid, /proc/self/exe, SIGKILL-by-pid, dup3 stderr redirect); on
// darwin/windows we still want cmd/perch to compile so the rest of the
// binary (mail watcher in the foreground) ships everywhere, but every
// daemon entry point returns an "unsupported" error.
package daemon

import (
	"errors"
	"log/slog"
)

var (
	ErrAlreadyRunning = errors.New("daemon already running")
	ErrStalePidfile   = errors.New("stale pidfile present")
	ErrReexecFailed   = errors.New("daemonize re-exec failed")
	ErrUnsupported    = errors.New("--daemon not supported on this platform")
)

// Daemonize is a no-op stub on non-Linux. Callers get ErrUnsupported.
func Daemonize(argv0 string, argv []string, pidfilePath string, extraEnv []string, log *slog.Logger) (int, error) {
	return 0, ErrUnsupported
}

// RedirectStderr is a no-op stub on non-Linux. --daemon-stderr has no
// meaning off the daemon code path, but keeping the symbol exported lets
// cmd/perch/main.go call it unconditionally.
func RedirectStderr(path string) error {
	return ErrUnsupported
}
