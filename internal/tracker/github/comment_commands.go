package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

// Reads and writes for `/itervox` comment commands (#84).

// RepoComment is one issue comment from the repository-wide comment list.
type RepoComment struct {
	ID          string
	IssueNumber string // the issue the comment is on, e.g. "42"
	Body        string
	Login       string // the author's GitHub login
	UserType    string // "User" or "Bot"
	CreatedAt   time.Time
	// OnPullRequest is true for a pull request's conversation comment: the
	// repository comment list includes them, since every PR is an issue.
	OnPullRequest bool
}

// ListRepoCommentsSince returns the repository's issue comments created or
// updated at or after since, oldest first, following pagination. One request
// per page covers every issue, unlike per-issue comment reads.
func (c *Client) ListRepoCommentsSince(ctx context.Context, since time.Time) ([]RepoComment, error) {
	q := url.Values{}
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("sort", "created")
	q.Set("direction", "asc")
	q.Set("per_page", "100")
	next := fmt.Sprintf("%s/repos/%s/%s/issues/comments?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())
	var out []RepoComment
	for next != "" {
		body, link, err := c.get(ctx, next)
		if err != nil {
			return nil, err
		}
		items, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("github_unknown_payload: expected array response")
		}
		for _, item := range items {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rc := RepoComment{}
			if id, ok := tracker.ToIntVal(raw["id"]); ok {
				rc.ID = strconv.Itoa(id)
			}
			rc.Body, _ = raw["body"].(string)
			if issueURL, _ := raw["issue_url"].(string); issueURL != "" {
				rc.IssueNumber = issueURL[strings.LastIndex(issueURL, "/")+1:]
			}
			if user, ok := raw["user"].(map[string]any); ok {
				rc.Login, _ = user["login"].(string)
				rc.UserType, _ = user["type"].(string)
			}
			if htmlURL, _ := raw["html_url"].(string); strings.Contains(htmlURL, "/pull/") {
				rc.OnPullRequest = true
			}
			if t := tracker.ParseTime(raw["created_at"]); t != nil {
				rc.CreatedAt = *t
			}
			if rc.ID != "" && rc.IssueNumber != "" {
				out = append(out, rc)
			}
		}
		next = ""
		if m := linkNextRe.FindStringSubmatch(link); m != nil {
			next = m[1]
		}
	}
	return out, nil
}

// CollaboratorPermission returns login's permission on the repository:
// "admin", "maintain", "write", "triage", "read" or "none" (not a
// collaborator).
func (c *Client) CollaboratorPermission(ctx context.Context, login string) (string, error) {
	body, _, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/%s/collaborators/%s/permission",
		c.cfg.Endpoint, c.owner, c.repo, url.PathEscape(login)))
	if err != nil {
		var nf *tracker.NotFoundError
		if errors.As(err, &nf) {
			return "none", nil
		}
		return "", err
	}
	obj, _ := body.(map[string]any)
	// role_name carries maintain/triage; permission folds them into
	// write/read for older API versions.
	if role, _ := obj["role_name"].(string); role != "" {
		return role, nil
	}
	perm, _ := obj["permission"].(string)
	if perm == "" {
		return "none", nil
	}
	return perm, nil
}

// AuthenticatedLogin returns the login of the token's own account.
func (c *Client) AuthenticatedLogin(ctx context.Context) (string, error) {
	body, _, err := c.get(ctx, c.cfg.Endpoint+"/user")
	if err != nil {
		return "", err
	}
	obj, _ := body.(map[string]any)
	login, _ := obj["login"].(string)
	if login == "" {
		return "", errors.New("github_user: no login in response")
	}
	return login, nil
}

// AddCommentReaction adds a reaction ("+1", "eyes", "confused", …) to an
// issue comment. GitHub answers 200 when the reaction already exists, so it
// is idempotent.
func (c *Client) AddCommentReaction(ctx context.Context, commentID, content string) error {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%s/reactions", c.cfg.Endpoint, c.owner, c.repo, url.PathEscape(commentID))
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return fmt.Errorf("github_add_reaction: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github_add_reaction: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github", tracker.GitHubRateLimitClassifier)
	if err != nil {
		return fmt.Errorf("github_add_reaction: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github_add_reaction: status %d", resp.StatusCode)
	}
	return nil
}
