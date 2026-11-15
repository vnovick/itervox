package workspace

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyWorktreeAddErrorBothWordings (#193): both of git's wordings for
// "this branch is checked out elsewhere" become a typed BranchInUseError, so
// the orchestrator never spends retries on it; other errors pass through.
func TestClassifyWorktreeAddErrorBothWordings(t *testing.T) {
	root := "/ws/proj"
	for _, msg := range []string{
		"exit status 128: Preparing worktree (checking out 'user/a-1-x')\nfatal: 'user/a-1-x' is already used by worktree at '/ws/proj/worktrees/A-1'",
		"exit status 128: fatal: 'user/a-1-x' is already checked out at '/ws/proj/worktrees/A-1'",
	} {
		err := classifyWorktreeAddError(root, errors.New(msg))
		require.True(t, errors.Is(err, ErrBranchInUse), msg)
		var biu *BranchInUseError
		require.True(t, errors.As(fmt.Errorf("worktree: add: %w", err), &biu))
		assert.Equal(t, "user/a-1-x", biu.Branch)
		assert.Equal(t, "/ws/proj/worktrees/A-1", biu.Path)
		assert.Equal(t, "A-1", biu.Holder)
	}
	other := errors.New("exit status 128: fatal: invalid reference: nope")
	assert.Same(t, other, classifyWorktreeAddError(root, other))
	assert.False(t, errors.Is(classifyWorktreeAddError(root, other), ErrBranchInUse))
}

func TestParseWorktreeListPorcelain(t *testing.T) {
	out := "worktree /r/.bare\nbare\n\nworktree /r/worktrees/A-1\nHEAD abc\nbranch refs/heads/user/a-1\n\n" +
		"worktree /r/worktrees/B-1\nHEAD def\ndetached\nprunable gitdir file points to non-existent location\n"
	got := parseWorktreeList([]byte(out))
	assert.Equal(t, []worktreeEntry{
		{Path: "/r/.bare", Bare: true},
		{Path: "/r/worktrees/A-1", Branch: "user/a-1"},
		{Path: "/r/worktrees/B-1", Prunable: true},
	}, got)
	assert.Equal(t, "", holderIdentifier("/r", "/elsewhere/A-1"))
}
