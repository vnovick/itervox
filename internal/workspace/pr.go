package workspace

import (
	"context"
	"encoding/json"
	"errors"
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

// SetPRBase points the pull request at prURL to base (#73) and reports
// whether it changed anything. It reads the current base first so an already
// correct PR is left alone (no edit, no notification), then runs
// `gh pr edit <url> --base <base>`. GitHub refuses a base branch that does
// not exist on the remote; that error is returned for the caller to log.
func SetPRBase(ctx context.Context, prURL, base string) (bool, error) {
	if prURL == "" || base == "" {
		return false, nil
	}
	view := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "baseRefName", "--jq", ".baseRefName")
	view.Env = gitexec.Environ()
	out, err := view.Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	if strings.TrimSpace(string(out)) == base {
		return false, nil
	}
	edit := exec.CommandContext(ctx, "gh", "pr", "edit", prURL, "--base", base)
	edit.Env = gitexec.Environ()
	if out, err := edit.CombinedOutput(); err != nil {
		return false, fmt.Errorf("gh pr edit %s --base %s: %w: %s", prURL, base, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// PRFooterMarker marks the Itervox footer in a pull request body (#81); a body
// that carries it is never edited again.
const PRFooterMarker = "<!-- itervox:shipped -->"

// PRFooterText is the footer EnsurePRFooter appends.
const PRFooterText = "Shipped with [Itervox](https://github.com/vnovick/itervox)"

// EnsurePRFooter appends the Itervox footer to the body of the pull request
// at prURL unless the body already carries PRFooterMarker or PRFooterText,
// and reports
// whether it edited the PR. The body is read and written with `gh pr view`
// and `gh pr edit --body-file -`, so arbitrary body text round-trips.
func EnsurePRFooter(ctx context.Context, prURL string) (bool, error) {
	if prURL == "" {
		return false, nil
	}
	view := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "body", "--jq", ".body")
	view.Env = gitexec.Environ()
	out, err := view.Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	body := strings.TrimRight(string(out), "\n")
	// The marker is the reliable signal; the visible text also counts, so a
	// body rewritten from a rendered view (HTML comments dropped) does not
	// get a second footer.
	if strings.Contains(body, PRFooterMarker) || strings.Contains(body, PRFooterText) {
		return false, nil
	}
	if body != "" {
		body += "\n\n"
	}
	body += "---\n" + PRFooterMarker + "\n" + PRFooterText + "\n"
	edit := exec.CommandContext(ctx, "gh", "pr", "edit", prURL, "--body-file", "-")
	edit.Env = gitexec.Environ()
	edit.Stdin = strings.NewReader(body)
	if out, err := edit.CombinedOutput(); err != nil {
		return false, fmt.Errorf("gh pr edit %s --body-file -: %w: %s", prURL, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// PRChecks summarises the CI checks on a pull request (#80).
type PRChecks struct {
	Total, Passed, Pending, Failed int
}

// Green reports whether every check passed (skipped checks count as
// passed) and there is at least one.
func (c PRChecks) Green() bool { return c.Total > 0 && c.Pending == 0 && c.Failed == 0 }

// ReadPRChecks reads the checks on prURL with `gh pr checks --json`. gh
// exits non-zero while checks are pending or failing, so the JSON is parsed
// whenever there is any; an error means it could not be read at all (no
// checks reported, gh missing, not authenticated).
func ReadPRChecks(ctx context.Context, prURL string) (PRChecks, error) {
	var sum PRChecks
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks", prURL, "--json", "name,bucket")
	cmd.Env = gitexec.Environ()
	out, err := cmd.Output()
	var rows []struct {
		Bucket string `json:"bucket"`
	}
	if jsonErr := json.Unmarshal(out, &rows); jsonErr != nil || len(rows) == 0 {
		if err == nil {
			err = errors.New("no checks reported")
		}
		return sum, fmt.Errorf("gh pr checks %s: %w", prURL, err)
	}
	for _, r := range rows {
		sum.Total++
		switch r.Bucket {
		case "pass", "skipping":
			sum.Passed++
		case "pending":
			sum.Pending++
		default: // fail, cancel
			sum.Failed++
		}
	}
	return sum, nil
}
