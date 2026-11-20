package jira

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// The read scope is the project AND the assignee AND any of the labels, the
// same meaning as tracker.assignee and tracker.labels have for Linear. Every
// list read (the candidate poll and FetchIssuesByStates) compiles it to JQL
// in statesJQL; MatchesFilter evaluates the same scope in memory on an issue
// read by id, which is never narrowed. An email is not an assignee: Jira
// Cloud can hide a user's email address (profile visibility), so an email
// cannot be matched to an account reliably.

// assigneeMe is the assignee spec for the token's own account.
const assigneeMe = "me"

func isMe(spec string) bool { return strings.EqualFold(spec, assigneeMe) }

// resolveMe makes sure the token's account id is known, for an assignee of
// "me": the first list read calls it, so MatchesFilter can answer for every
// issue a poll has offered. Once read, the id is kept for the life of the
// client (a WORKFLOW.md reload builds a new client).
func (c *Client) resolveMe(ctx context.Context) error {
	if _, ok := c.cachedAccount(assigneeMe); ok {
		return nil
	}
	_, err := c.ResolveAssignee(ctx)
	return err
}

// ResolveAssignee returns the account the configured assignee names: for
// "me" the token's account (GET /rest/api/3/myself, whose id the client
// keeps), otherwise the account with that id (GET /rest/api/3/user). An id
// that no account has gives an error wrapping tracker.ErrAssigneeNotFound.
// A client without an assignee is refused before any request.
func (c *Client) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	spec := c.cfg.Assignee
	if spec == "" {
		return tracker.AssigneeUser{}, errors.New("jira_resolve_assignee: no assignee configured")
	}
	var u struct {
		AccountID    string `json:"accountId"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	}
	path := "/rest/api/3/user?" + url.Values{"accountId": {spec}}.Encode()
	if isMe(spec) {
		path = "/rest/api/3/myself"
	}
	err := c.do(ctx, "GET", path, nil, &u)
	var nf *tracker.NotFoundError
	switch {
	case errors.As(err, &nf) || (err == nil && u.AccountID == ""):
		return tracker.AssigneeUser{}, fmt.Errorf("jira_resolve_assignee: %w", tracker.ErrAssigneeNotFound)
	case err != nil:
		return tracker.AssigneeUser{}, fmt.Errorf("jira_resolve_assignee: %w", err)
	}
	if isMe(spec) {
		c.meMu.Lock()
		c.me = u.AccountID
		c.meMu.Unlock()
	}
	return tracker.AssigneeUser{ID: u.AccountID, Name: u.DisplayName, Email: u.EmailAddress}, nil
}

// MatchesFilter implements tracker.ScopeMatcher: whether an issue read by id
// is still inside the scope the list reads use — its key is in the project
// (a moved issue gets another project's key), it is assigned to the
// configured account, and it carries one of the labels (any case, as JQL
// compares labels). It makes no request. known is false only for an
// assignee of "me" before any list read has read the token's account.
func (c *Client) MatchesFilter(issue domain.Issue) (match, known bool) {
	if !strings.HasPrefix(issue.Identifier, c.cfg.ProjectSlug+"-") {
		return false, true
	}
	scope := issuefilter.FromLegacy(nil, c.cfg.Assignee, "", c.cfg.Labels)
	return scope.Match(issue, issuefilter.Env{Assignee: c.cachedAccount})
}

// cachedAccount resolves an assignee spec to the account id an issue's
// Assignees carry, from the cache only: "me" is known once resolveMe has
// succeeded; an account id is itself.
func (c *Client) cachedAccount(spec string) (string, bool) {
	if !isMe(spec) {
		return spec, true
	}
	c.meMu.Lock()
	defer c.meMu.Unlock()
	return c.me, c.me != ""
}
