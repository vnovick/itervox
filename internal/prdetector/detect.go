package prdetector

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
)

// ReviewComment is a single PR review comment.
type ReviewComment struct {
	Body   string
	Author string
}

// maxFullDiffLines is the maximum number of diff lines to include in the full
// diff section of the prompt. Larger diffs are omitted to keep the prompt within
// context-window limits.
const maxFullDiffLines = 150

// PRContext holds the context of an open pull request for prompt injection.
type PRContext struct {
	URL            string
	Branch         string
	Description    string
	ReviewComments []ReviewComment
	DiffStat       string // always: git diff --stat <base>
	FullDiff       string // non-empty only if total diff < maxFullDiffLines lines
}

// detectDefaultBranch resolves the remote HEAD branch for wsPath using the
// local ref cache (no network call). Returns e.g. "origin/main". Returns ""
// when the ref cannot be resolved (e.g. origin/HEAD was never fetched).
func detectDefaultBranch(ctx context.Context, wsPath string) string {
	cmd := gitexec.Command(ctx, wsPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Output is "refs/remotes/origin/main\n"; strip prefix to get "origin/main".
	ref := strings.TrimSpace(string(out))
	ref = strings.TrimPrefix(ref, "refs/remotes/")
	return ref
}

var prURLRegex = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/pull/\d+\b`)

// ParsePRURLs returns all unique GitHub PR URLs found in text, in order of
// first appearance.
func ParsePRURLs(text string) []string {
	matches := prURLRegex.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// prViewJSON is the JSON shape returned by `gh pr view --json state,headRefName,body,isDraft`.
// Note: `gh pr view --json state` returns "OPEN" for both open and draft PRs;
// draft status is exposed as a separate isDraft boolean field.
type prViewJSON struct {
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	Body        string `json:"body"`
	IsDraft     bool   `json:"isDraft"`
}

// CheckPR calls `gh pr view <url> --json state,headRefName,body,isDraft`.
// Returns nil, nil if the PR is merged, closed, or gh fails for any reason.
// Returns a PRContext with URL, Branch, and Description set if the PR is OPEN
// (including draft PRs, which also report state "OPEN" with isDraft=true).
func CheckPR(ctx context.Context, url string) (*PRContext, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", url,
		"--json", "state,headRefName,body,isDraft")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil //nolint:nilerr // gh failure is non-fatal — fall through to normal flow
	}
	var v prViewJSON
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, nil //nolint:nilerr
	}
	if v.State != "OPEN" {
		return nil, nil
	}
	return &PRContext{
		URL:         url,
		Branch:      v.HeadRefName,
		Description: v.Body,
	}, nil
}

// prReviewsJSON is the shape of one entry from `gh pr view --json reviews`.
type prReviewsJSON struct {
	Reviews []struct {
		Body   string `json:"body"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	} `json:"reviews"`
}

// FetchPRContext enriches a PRContext (already confirmed OPEN) with review
// comments and a diff. Failures are non-fatal — the PRContext is returned
// partially filled if gh or git commands fail.
//
// baseBranch is the remote branch used as the diff base (e.g. "origin/main").
// When empty, FetchPRContext auto-detects via `git symbolic-ref
// refs/remotes/origin/HEAD`, falling back to "origin/main" if detection fails.
//
// Note: `--json reviews` returns only formal review-level comments (submitted
// via "Submit review"). Inline thread comments (line-level comments) and
// general PR comments (issue-level comments) are NOT included. Callers should
// therefore expect ReviewComments to be empty unless a reviewer explicitly
// submitted a formal review with a non-empty top-level body.
func FetchPRContext(ctx context.Context, pr *PRContext, wsPath, baseBranch string) {
	// Fetch review comments.
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", pr.URL,
		"--json", "reviews")
	if out, err := cmd.Output(); err == nil {
		var v prReviewsJSON
		if err := json.Unmarshal(out, &v); err != nil {
			slog.Debug("prdetector: failed to parse gh pr view reviews JSON", "url", pr.URL, "error", err)
		} else {
			for _, r := range v.Reviews {
				if strings.TrimSpace(r.Body) == "" {
					continue
				}
				pr.ReviewComments = append(pr.ReviewComments, ReviewComment{
					Body:   r.Body,
					Author: r.Author.Login,
				})
			}
		}
	}

	// Fetch diff stat (always).
	if wsPath != "" {
		base := baseBranch
		if base == "" {
			base = detectDefaultBranch(ctx, wsPath)
		}
		if base == "" {
			base = "origin/main"
		}

		statCmd := gitexec.Command(ctx, wsPath, "diff", "--stat", base)
		if out, err := statCmd.Output(); err == nil {
			pr.DiffStat = strings.TrimSpace(string(out))
		}

		// Fetch full diff only if small.
		diffCmd := gitexec.Command(ctx, wsPath, "diff", base)
		if out, err := diffCmd.Output(); err == nil {
			lines := strings.Count(string(out), "\n")
			if lines <= maxFullDiffLines {
				pr.FullDiff = strings.TrimSpace(string(out))
			}
		}
	}
}

// prEntry tracks a PR URL and its last-seen position in the scan order.
// Package-level so sortEntriesByPos can reference it by name.
type prEntry struct {
	url string
	pos int
}

// collectPRURLs extracts all PR URLs from an issue's description and comments.
// Returns unique URLs in "most-recently-sourced" order: URLs from later comments
// appear later in the slice. If a URL appears in multiple places, the latest
// occurrence wins (last-write-wins dedup, not first-occurrence dedup).
func collectPRURLs(issue domain.Issue) []string {
	seen := make(map[string]*prEntry)
	pos := 0

	record := func(urls []string) {
		for _, u := range urls {
			if e, ok := seen[u]; ok {
				e.pos = pos // update to latest position
			} else {
				seen[u] = &prEntry{url: u, pos: pos}
			}
			pos++
		}
	}

	if issue.Description != nil {
		record(ParsePRURLs(*issue.Description))
	}
	// Sort comments oldest-to-newest so later comments overwrite earlier ones.
	comments := make([]domain.Comment, len(issue.Comments))
	copy(comments, issue.Comments)
	sortCommentsByTime(comments)
	for _, c := range comments {
		record(ParsePRURLs(c.Body))
	}

	// Build output sorted by last-seen pos ascending (oldest-to-newest source).
	entries := make([]*prEntry, 0, len(seen))
	for _, e := range seen {
		entries = append(entries, e)
	}
	sortEntriesByPos(entries)
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.url
	}
	return out
}

func sortCommentsByTime(comments []domain.Comment) {
	slices.SortStableFunc(comments, func(a, b domain.Comment) int {
		if a.CreatedAt == nil && b.CreatedAt == nil {
			return 0
		}
		if a.CreatedAt == nil {
			return 1 // nil sorts last
		}
		if b.CreatedAt == nil {
			return -1
		}
		return a.CreatedAt.Compare(*b.CreatedAt)
	})
}

func sortEntriesByPos(entries []*prEntry) {
	slices.SortStableFunc(entries, func(a, b *prEntry) int {
		return cmp.Compare(a.pos, b.pos)
	})
}

// Detect looks for an open PR associated with the issue.
// It parses PR URLs from the issue description and all comments, then checks
// each URL (most-recently-sourced first) with CheckPR.
// Returns the first open/draft PRContext found, or nil, nil if none.
// FetchPRContext is NOT called here — the caller is responsible for enriching
// the PRContext with diffs after setting up the workspace.
func Detect(ctx context.Context, issue domain.Issue) (*PRContext, error) {
	urls := collectPRURLs(issue)
	// Iterate in reverse so the most-recently-sourced URL is checked first.
	for i := len(urls) - 1; i >= 0; i-- {
		pr, err := CheckPR(ctx, urls[i])
		if err != nil {
			slog.Debug("prdetector: CheckPR error (best-effort)", "url", urls[i], "error", err)
			return nil, nil //nolint:nilerr // best-effort
		}
		if pr != nil {
			return pr, nil
		}
	}
	return nil, nil
}

// FormatPRContext renders a PRContext as a plain-text block suitable for
// appending to the agent prompt. Returns "" if pr is nil.
func FormatPRContext(pr *PRContext) string {
	if pr == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Open PR Context\n")
	b.WriteString("PR: ")
	b.WriteString(pr.URL)
	b.WriteString(" | Branch: ")
	b.WriteString(pr.Branch)
	b.WriteString("\n")
	if pr.Description != "" {
		b.WriteString("\n### PR Description\n")
		b.WriteString(pr.Description)
		b.WriteString("\n")
	}
	if len(pr.ReviewComments) > 0 {
		b.WriteString("\n### Review Comments\n")
		for _, rc := range pr.ReviewComments {
			b.WriteString("- ")
			b.WriteString(rc.Author)
			b.WriteString(": ")
			b.WriteString(rc.Body)
			b.WriteString("\n")
		}
	}
	if pr.DiffStat != "" {
		b.WriteString("\n### Changes\n```\n")
		b.WriteString(pr.DiffStat)
		b.WriteString("\n```\n")
	}
	if pr.FullDiff != "" {
		b.WriteString("\n### Diff\n```diff\n")
		b.WriteString(pr.FullDiff)
		b.WriteString("\n```\n")
	}
	return b.String()
}
