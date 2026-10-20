package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/tracker"
)

// ListLabels returns the names of every label on the repository, following
// Link-header pagination (#75, `itervox doctor`). The GitHub tracker maps
// workflow states to labels, so a configured state whose label does not exist
// silently matches nothing.
func (c *Client) ListLabels(ctx context.Context) ([]string, error) {
	if c.owner == "" || c.repo == "" {
		return nil, fmt.Errorf("github_list_labels: tracker.project_slug must be owner/repo")
	}
	q := url.Values{}
	q.Set("per_page", "100")
	next := fmt.Sprintf("%s/repos/%s/%s/labels?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())
	var names []string
	for next != "" {
		raw, link, err := c.get(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("github_list_labels: %w", err)
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("github_list_labels: expected array response")
		}
		for _, item := range items {
			obj, _ := item.(map[string]any)
			if name, _ := obj["name"].(string); name != "" {
				names = append(names, name)
			}
		}
		// No Link header parses as ("", nil); one without rel="next" as
		// ErrMissingPageLink. Either way this was the last page.
		n, err := ParseNextLink(link)
		if err != nil {
			break
		}
		next = n
	}
	return names, nil
}

// CreateLabel creates a repository label. color is a 6-digit hex string
// without the leading '#'.
func (c *Client) CreateLabel(ctx context.Context, name, color string) error {
	if c.owner == "" || c.repo == "" {
		return fmt.Errorf("github_create_label: tracker.project_slug must be owner/repo")
	}
	payload, err := json.Marshal(map[string]string{"name": name, "color": strings.TrimPrefix(color, "#")})
	if err != nil {
		return fmt.Errorf("github_create_label: marshal: %w", err)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/labels", c.cfg.Endpoint, c.owner, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github_create_label: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github", tracker.GitHubRateLimitClassifier)
	if err != nil {
		return fmt.Errorf("github_create_label: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github_create_label %q: status %d", name, resp.StatusCode)
	}
	return nil
}
