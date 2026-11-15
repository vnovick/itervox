//go:build windows

package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/procgroup"
)

// TestRunHookWithoutBashFailsWindows: on a Windows host where
// procgroup.Shell finds no bash, a hook fails at once with a message that
// names what is missing, instead of an opaque "file not found".
func TestRunHookWithoutBashFailsWindows(t *testing.T) {
	empty := t.TempDir()
	t.Setenv(procgroup.ShellEnv, "")
	t.Setenv("ProgramFiles", empty)
	t.Setenv("PATH", empty)

	err := RunHook(context.Background(), "echo hi", t.TempDir(), 5000)
	if !errors.Is(err, ErrNoHookShell) {
		t.Fatalf("RunHook = %v, want ErrNoHookShell", err)
	}
	for _, want := range []string{"hook_failed", "Git for Windows", "ITERVOX_SHELL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
