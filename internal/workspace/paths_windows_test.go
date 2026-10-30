//go:build windows

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspacePathWindows: workspace paths are joined with Windows
// separators under a drive-letter root, the identifier sanitized as on Unix.
func TestWorkspacePathWindows(t *testing.T) {
	if got, want := WorkspacePath(`C:\itervox\ws`, "ENG 1/foo"), `C:\itervox\ws\ENG_1_foo`; got != want {
		t.Fatalf("WorkspacePath = %q, want %q", got, want)
	}
	if got, want := BarePath(`C:\itervox\ws`), `C:\itervox\ws\.bare`; got != want {
		t.Fatalf("BarePath = %q, want %q", got, want)
	}
}

// TestWorktreePathsFromGitWindows: git for Windows reports paths with
// forward slashes (`gitdir: C:/…` in a linked worktree's .git file, and in
// `git worktree list --porcelain`). BranchCheckedOutAt and holderIdentifier
// must read them as the same paths the daemon builds with backslashes.
func TestWorktreePathsFromGitWindows(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktrees", "ENG-1")
	gitDir := filepath.Join(root, ".bare", "worktrees", "ENG-1")
	for _, d := range []string{wt, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/eng-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	slashed := strings.ReplaceAll(gitDir, `\`, "/")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+slashed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !BranchCheckedOutAt(wt, "eng-1") {
		t.Fatal("BranchCheckedOutAt did not follow a forward-slash gitdir")
	}
	if got := holderIdentifier(root, strings.ReplaceAll(wt, `\`, "/")); got != "ENG-1" {
		t.Fatalf("holderIdentifier(forward-slash path) = %q, want ENG-1", got)
	}
}
