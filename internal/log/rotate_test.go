package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileRotatesAndCapsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "perch.log")
	r, err := OpenRotating(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := []byte(strings.Repeat("x", 600<<10)) // 600 KB: two writes exceed 1 MB
	for i := 0; i < 5; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("%s.3 should not exist (max_backups=2)", path)
	}
	if st, _ := os.Stat(path); st.Size() > 1<<20 {
		t.Errorf("active file %d bytes exceeds 1 MB", st.Size())
	}
}
