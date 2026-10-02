package log

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an io.Writer that appends to a file and rotates it once
// it grows past maxBytes: path -> path.1 -> path.2 … up to maxBackups, the
// oldest being dropped. maxBytes <= 0 disables rotation.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	f          *os.File
	size       int64
}

// OpenRotating opens (creating parent dirs as needed) the log file at path.
func OpenRotating(path string, maxSizeMB, maxBackups int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	r := &RotatingFile{path: path, maxBytes: int64(maxSizeMB) << 20, maxBackups: maxBackups}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// Keep logging to the current file rather than losing lines.
			fmt.Fprintf(os.Stderr, "perch: log rotate: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	if r.maxBackups <= 0 {
		os.Remove(r.path)
	} else {
		os.Remove(fmt.Sprintf("%s.%d", r.path, r.maxBackups))
		for i := r.maxBackups - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
		}
		os.Rename(r.path, r.path+".1")
	}
	return r.open()
}

// Close closes the underlying file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
