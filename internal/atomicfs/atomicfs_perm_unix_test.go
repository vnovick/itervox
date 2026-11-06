//go:build unix

package atomicfs

import (
	"os"
	"path/filepath"
	"testing"
)

// Unix permission bits; Windows has none (os.Chmod only toggles read-only).

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
