//go:build windows

package agent

import (
	"errors"
	"io"
	"os"
	"testing"
)

// TestPromptFileDeletedOnCloseWindows: Windows cannot unlink an open file,
// so promptFile's temp file is created delete-on-close. It reads back the
// prompt from offset 0 and is gone once the last handle closes.
func TestPromptFileDeletedOnCloseWindows(t *testing.T) {
	f, err := promptFile("hello prompt")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != "hello prompt" {
		t.Fatalf("read = %q, %v; want the prompt", got, err)
	}
	closePromptFile(f)
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt file %s still exists after close (stat: %v)", name, err)
	}
}
