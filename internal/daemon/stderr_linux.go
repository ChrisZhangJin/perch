//go:build linux

package daemon

import (
	"os"
	"syscall"
)

// RedirectStderr duplicates the file descriptor of the file at path onto
// os.Stderr's fd, so anything written to stderr afterwards lands in that
// file. Used by the daemon child when the parent was invoked with
// --daemon-stderr <path>: the child's stdio is /dev/null (see Daemonize),
// so without this redirect any fatal error during config/logger setup
// disappears silently.
//
// syscall.Dup3(oldfd, newfd, 0) is used instead of Dup2 because
// syscall.Dup2 is not defined on linux/arm64 (the arm64 kernel exposes
// only dup3). Dup3 with flags=0 is semantically equivalent to Dup2 and
// is available on every unix arch Go supports.
//
// A nil error means the redirect took effect. On open failure the file
// is left absent and stderr is unchanged; on Dup3 failure the file is
// closed and stderr is unchanged.
func RedirectStderr(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := syscall.Dup3(int(f.Fd()), int(os.Stderr.Fd()), 0); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
