package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// CommitPathOnly stages relPath in the repository at wsPath and commits ONLY
// that pathspec with message, skipping hooks.
//
// Scoped add + pathspec-scoped commit — it never sweeps unrelated changes,
// even pre-staged ones. It returns staged=false (and a nil error) when
// `git add` fails, which is the normal outcome in a non-git workspace; callers
// treat that as "nothing to record". A commit failure other than "nothing to
// commit" is returned with staged=true.
func CommitPathOnly(ctx context.Context, wsPath, relPath, message string) (staged bool, err error) {
	addCmd := gitexec.Command(ctx, wsPath, "add", relPath)
	if addCmd.Run() != nil {
		return false, nil
	}
	commitCmd := gitexec.Command(ctx, wsPath, "commit", "-m", message, "--no-verify", "--", relPath)
	if out, cerr := commitCmd.CombinedOutput(); cerr != nil && !strings.Contains(string(out), "nothing to commit") {
		return true, fmt.Errorf("workspace: commit %s: %w: %s", relPath, cerr, strings.TrimSpace(string(out)))
	}
	return true, nil
}
