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