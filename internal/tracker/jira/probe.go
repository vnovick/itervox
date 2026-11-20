package jira

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// ProbeProjectAccess is a read-only credential and scope check for `itervox
// doctor --deploy`. It reads /rest/api/3/myself to prove the credentials,
// then /rest/api/3/project/<ProjectSlug> to prove the account can see the
// project. It returns the account's display name, or its account id when the
// name is empty. An empty ProjectSlug is refused before any request.
func (c *Client) ProbeProjectAccess(ctx context.Context) (account string, err error) {
	if c.cfg.ProjectSlug == "" {
		return "", errors.New("jira_probe: tracker.project_slug is empty")
	}
	var me struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, "GET", "/rest/api/3/myself", nil, &me); err != nil {
		return "", fmt.Errorf("jira_probe: %w", err)
	}
	if err := c.do(ctx, "GET", "/rest/api/3/project/"+url.PathEscape(c.cfg.ProjectSlug), nil, nil); err != nil {
		return "", fmt.Errorf("jira_probe: project %s: %w", c.cfg.ProjectSlug, err)
	}
	if me.DisplayName != "" {
		return me.DisplayName, nil
	}
	return me.AccountID, nil
}
