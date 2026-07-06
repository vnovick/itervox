package atomicfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFile_HappyPathOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")

	if err := WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, []byte("v2"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("got %q, want v2", got)
	}
}

func TestWriteFile_PreservesPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	if err := WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatalf("write 0600: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("after 0600 write: got %o, want 0600", got)
	}

	if err := WriteFile(path, []byte("b"), 0o644); err != nil {
		t.Fatalf("write 0644: %v", err)
	}
	st, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Fatalf("after 0644 write: got %o, want 0644", got)
	}
}

func TestWriteFile_NoLeftoverTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")

	if err := WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".WORKFLOW.md.tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
	}
}

func TestWriteFile_NoPartialOnTempFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ENOSPC simulation via permission isn't reliable on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	original := []byte("original")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Make the directory read-only so CreateTemp fails. The original file
	// content must remain untouched.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := WriteFile(path, []byte("new"), 0o600)
	if err == nil {
		t.Fatalf("expected error on read-only dir, got nil")
	}

	// Restore perms so we can read the file.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("original was modified: got %q, want %q", got, original)
	}

	// And no .tmp- siblings left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file after failure: %s", e.Name())
		}
	}
}
