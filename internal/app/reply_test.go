package app

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestCollectReplyFilesMissingDir(t *testing.T) {
	files, err := collectReplyFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if files != nil {
		t.Fatalf("expected nil files for missing dir, got %v", files)
	}
}

func TestCollectReplyFilesTopLevelOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "one")
	writeFile(t, filepath.Join(dir, "b.txt"), "two")

	files, err := collectReplyFiles(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := basenames(files)
	sort.Strings(got)
	want := []string{"a.txt", "b.txt"}
	if !equal(got, want) {
		t.Fatalf("top-level files: got %v want %v", got, want)
	}
}

func TestCollectReplyFilesPacksSubdir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.md"), "top-level")
	sub := filepath.Join(dir, "work-folder")
	if err := os.MkdirAll(filepath.Join(sub, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "a.txt"), "alpha")
	writeFile(t, filepath.Join(sub, "nested", "b.txt"), "beta")

	files, err := collectReplyFiles(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := basenames(files)
	sort.Strings(got)
	want := []string{"note.md", "work-folder.tar.gz"}
	if !equal(got, want) {
		t.Fatalf("collected: got %v want %v", got, want)
	}

	// Verify the tarball's entries reproduce the folder structure.
	var tarPath string
	for _, p := range files {
		if filepath.Base(p) == "work-folder.tar.gz" {
			tarPath = p
			break
		}
	}
	entries := tarEntries(t, tarPath)
	sort.Strings(entries)
	wantEntries := []string{
		"work-folder/",
		"work-folder/a.txt",
		"work-folder/nested/",
		"work-folder/nested/b.txt",
	}
	if !equal(entries, wantEntries) {
		t.Fatalf("tar entries: got %v want %v", entries, wantEntries)
	}
}

func TestCollectReplyFilesEmptySubdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := collectReplyFiles(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "empty.tar.gz" {
		t.Fatalf("empty subdir should still pack to a tarball, got %v", files)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func basenames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open tar: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}
