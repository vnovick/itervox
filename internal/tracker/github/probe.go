package github

import (
	"context"
	"fmt"
)

// ProbeRepoAccess is a read-only credential and scope check (CORE-063,
// `itervox doctor --deploy`): one GET /repos/{owner}/{repo} through the
// client's normal transport. It reports whether the token can see the repo
// and whether GitHub grants it push (write) permission, which label and state
// changes need.
func (c *Client) ProbeRepoAccess(ctx context.Context) (canPush bool, err error) {
	if c.owner == "" || c.repo == "" {
		return false, fmt.Errorf("github_probe: tracker.project_slug must be owner/repo")
	}
	raw, _, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/%s", c.cfg.Endpoint, c.owner, c.repo))
	if err != nil {
		return false, err
	}
	obj, _ := raw.(map[string]any)
	perms, _ := obj["permissions"].(map[string]any)
	push, _ := perms["push"].(bool)
	return push, nil
}
