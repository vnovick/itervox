//go:build windows

package atomicfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteFileReplacesFileHeldOpenWindows: on Windows a rename over a file
// another handle has open fails with a sharing violation for as long as the
// handle lives. WriteFile retries until the reader lets go, so a reader
// that briefly holds the file (the dashboard, a watcher) cannot fail a
// write.
func TestWriteFileReplacesFileHeldOpenWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- WriteFile(path, []byte("v2"), 0o600) }()
	time.Sleep(100 * time.Millisecond)
	_ = reader.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WriteFile while a reader held the file: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFile still blocked 5s after the reader closed")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "v2" {
		t.Fatalf("content = %q, %v; want v2", got, err)
	}
}
