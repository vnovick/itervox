package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// GetCurrentBranch returns the name of the current git branch in wsPath,
// or "" if the workspace is in detached HEAD state, git is unavailable, or
// any other error occurs. Callers should treat "" as "unknown / not on a
// feature branch".
func GetCurrentBranch(ctx context.Context, wsPath string) string {
	if wsPath == "" {
		return ""
	}
	cmd := gitexec.Command(ctx, wsPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "" // detached HEAD — not a named branch
	}
	return branch
}

// CheckoutBranch attempts to check out the named branch in wsPath.
// It first does a targeted git fetch for that branch (best-effort, so that
// remote-only branches are available locally), then runs git checkout.
// Returns a wrapped error if checkout fails; callers should log and continue.
func CheckoutBranch(ctx context.Context, wsPath, branch string) error {
	if wsPath == "" || branch == "" {
		return nil
	}
	// Best-effort fetch — makes the branch available if it was pushed to origin.
	fetchCmd := gitexec.Command(ctx, wsPath, "fetch", "origin", branch)
	_ = fetchCmd.Run()

	checkoutCmd := gitexec.Command(ctx, wsPath, "checkout", branch)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FindOpenPRURL returns the URL of the open pull request for the current git
// branch in wsPath, or "" if no open PR is found (or on any error, including
// gh not installed or not a git repository).
func FindOpenPRURL(ctx context.Context, wsPath string) string {
	if wsPath == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "view",
		"--json", "url,state",
		"--jq", `select(.state=="OPEN").url`,
	)
	cmd.Dir = wsPath
	// gh resolves the repository and branch by running git itself.
	cmd.Env = gitexec.Environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
