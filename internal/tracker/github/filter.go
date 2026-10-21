package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// tracker.filter on GitHub (#198). Each group compiles to one issue search
// (`repo:o/r is:issue is:open label:"a" -label:"b" assignee:x
// milestone:"m"`) and the results are unioned and de-duplicated by issue
// number. GitHub search has no OR between labels inside one query, so a
// group with several labels_any runs one search per label (unioned like the
// groups). States stay labels: the searches read every open issue the group
// matches (and the closed ones when "closed" is asked for), and the state is
// derived from the labels exactly as the unfiltered listing does; the
// group's states condition narrows the result. The change listing (the
// incremental pull) and the by-ID read (the webhook refresh) evaluate the
// same filter in memory (issuefilter.Match): an issue that left it is
// reported gone.
//
// Projects, teams, priority and cycle have no GitHub search equivalent and
// are refused by config validation (FilterConditions) rather than ignored.

var _ tracker.FilterManager = (*Client)(nil)

// FilterConditions implements tracker.FilterManager.
func (c *Client) FilterConditions() []string { return issuefilter.SupportedBy("github") }

// Filter implements tracker.FilterManager: the runtime filter where set,
// else tracker.filter.
func (c *Client) Filter() issuefilter.Filter {
	c.filterMu.RLock()
	defer c.filterMu.RUnlock()
	if c.filterOverride != nil {
		return slices.Clone(*c.filterOverride)
	}
	return slices.Clone(c.cfg.Filter)
}

// FilterMode implements tracker.FilterManager.
func (c *Client) FilterMode() bool { return len(c.Filter()) > 0 }

// SetFilter implements tracker.FilterManager.
func (c *Client) SetFilter(f issuefilter.Filter) {
	f = f.Normalize()
	if f == nil {
		f = issuefilter.Filter{}
	}
	c.filterMu.Lock()
	c.filterOverride = &f
	c.filterMu.Unlock()
}

// ResolveFilter implements tracker.FilterManager: "me" is the token's
// login (GET /user, cached); any other assignee is a login as written.
func (c *Client) ResolveFilter(ctx context.Context, f issuefilter.Filter) error {
	for _, spec := range f.Assignees() {
		if !strings.EqualFold(spec, "me") {
			continue
		}
		if _, ok := c.resolvedLogin(spec); ok {
			continue
		}
		raw, _, err := c.get(ctx, c.cfg.Endpoint+"/user")
		if err != nil {
			return fmt.Errorf("github: resolve assignee me: %w", err)
		}
		obj, _ := raw.(map[string]any)
		login, _ := obj["login"].(string)
		if login == "" {
			return fmt.Errorf("github: resolve assignee me: %w", tracker.ErrAssigneeNotFound)
		}
		c.filterMu.Lock()
		c.me = login
		c.filterMu.Unlock()
	}
	return nil
}

func (c *Client) resolvedLogin(spec string) (string, bool) {
	if !strings.EqualFold(spec, "me") {
		return spec, true
	}
	c.filterMu.RLock()
	defer c.filterMu.RUnlock()
	return c.me, c.me != ""
}

func (c *Client) matchEnv() issuefilter.Env {
	return issuefilter.Env{Assignee: c.resolvedLogin, ActiveStates: c.activeStates(), WorkingState: c.cfg.WorkingState}
}

// MatchesFilter implements tracker.FilterManager.
func (c *Client) MatchesFilter(issue domain.Issue) (match, known bool) {
	return c.Filter().Match(issue, c.matchEnv())
}

// groupQueries compiles one group to its search queries (without the
// is:open / is:closed qualifier): one, or one per label when labels_any
// names several.
func (c *Client) groupQueries(g issuefilter.Group) []string {
	base := []string{"repo:" + c.owner + "/" + c.repo, "is:issue"}
	for _, l := range g.LabelsAll {
		base = append(base, "label:"+searchQuote(l))
	}
	for _, l := range g.LabelsNone {
		base = append(base, "-label:"+searchQuote(l))
	}
	if g.Assignee != "" {
		who := g.Assignee
		if strings.EqualFold(who, "me") {
			who = "@me"
		}
		base = append(base, "assignee:"+who)
	}
	if g.Milestone != "" {
		base = append(base, "milestone:"+searchQuote(g.Milestone))
	}
	if len(g.LabelsAny) == 0 {
		return []string{strings.Join(base, " ")}
	}
	out := make([]string, 0, len(g.LabelsAny))
	for _, l := range g.LabelsAny {
		out = append(out, strings.Join(append(slices.Clone(base), "label:"+searchQuote(l)), " "))
	}
	return out
}

func searchQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, "") + `"` }

// fetchFiltered reads the issues in states that f matches: every group's
// searches, unioned and de-duplicated.
func (c *Client) fetchFiltered(ctx context.Context, f issuefilter.Filter, states []string) ([]domain.Issue, error) {
	if len(states) == 0 {
		return []domain.Issue{}, nil
	}
	var labelStates []string
	closed := false
	for _, s := range states {
		if strings.EqualFold(s, "closed") {
			closed = true
		} else {
			labelStates = append(labelStates, s)
		}
	}
	env := c.matchEnv()
	seen := map[string]struct{}{}
	var all []domain.Issue
	for _, g := range f {
		for _, q := range c.groupQueries(g) {
			var qualifiers []string
			if len(labelStates) > 0 {
				qualifiers = append(qualifiers, "is:open")
			}
			if closed {
				qualifiers = append(qualifiers, "is:closed")
			}
			for _, qual := range qualifiers {
				issues, err := c.searchIssues(ctx, q+" "+qual, labelStates, qual == "is:closed")
				if err != nil {
					return nil, err
				}
				for _, is := range issues {
					if _, dup := seen[is.ID]; dup || len(g.StatesAllowed([]string{is.State}, env)) == 0 {
						continue
					}
					seen[is.ID] = struct{}{}
					all = append(all, is)
				}
			}
		}
	}
	return c.populateBlockerStates(ctx, all), nil
}

// searchIssues pages through one issue search. An open issue is kept when it
// carries one of labelStates (its state is derived as the listing derives
// it); a closed one when closed is asked for.
func (c *Client) searchIssues(ctx context.Context, query string, labelStates []string, closed bool) ([]domain.Issue, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(100))
	next := fmt.Sprintf("%s/search/issues?%s", c.cfg.Endpoint, q.Encode())
	var out []domain.Issue
	for next != "" {
		body, link, err := c.get(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("github_search: %w", err)
		}
		obj, ok := body.(map[string]any)
		if !ok {
			return nil, errors.New("github_unknown_payload: expected a search result")
		}
		items, _ := obj["items"].([]any)
		for _, item := range items {
			raw, ok := item.(map[string]any)
			if !ok || isPullRequest(raw) {
				continue
			}
			var derived string
			if closed {
				derived = deriveState(raw, c.activeStates(), c.terminalStates())
			} else if firstLabelIn(extractLabels(raw), labelStates) != "" {
				derived = deriveState(raw, c.activeStates(), c.terminalStates())
				if derived == "" {
					derived = firstLabelIn(extractLabels(raw), labelStates)
				}
			}
			if derived == "" {
				continue
			}
			if is := normalizeIssue(raw, derived); is != nil {
				out = append(out, *is)
			}
		}
		n, err := ParseNextLink(link)
		if err != nil {
			break // no rel="next": the last page
		}
		next = n
	}
	return out, nil
}

// PreviewFilter implements tracker.FilterManager.
func (c *Client) PreviewFilter(ctx context.Context, f issuefilter.Filter, limit int) (tracker.FilterPreview, error) {
	f = f.Normalize()
	if err := c.ResolveFilter(ctx, f); err != nil {
		return tracker.FilterPreview{}, err
	}
	var issues []domain.Issue
	var err error
	if len(f) == 0 {
		issues, err = c.fetchUnfiltered(ctx)
	} else {
		issues, err = c.fetchFiltered(ctx, f, c.activeStates())
	}
	if err != nil {
		return tracker.FilterPreview{}, fmt.Errorf("github_filter_preview: %w", err)
	}
	p := tracker.FilterPreview{Count: len(issues), Identifiers: []string{}}
	for _, is := range issues[:min(len(issues), max(limit, 0))] {
		p.Identifiers = append(p.Identifiers, is.Identifier)
	}
	return p, nil
}

// FetchTeams implements tracker.FilterManager: GitHub has no teams
// condition.
func (c *Client) FetchTeams(context.Context) ([]tracker.Team, error) { return []tracker.Team{}, nil }

// FetchLabels implements tracker.FilterManager: the repository's labels.
func (c *Client) FetchLabels(ctx context.Context) ([]string, error) {
	labels, err := c.ListLabels(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(labels)
	return labels, nil
}

// FetchUsers implements tracker.FilterManager: the repository's assignable
// users, by login.
func (c *Client) FetchUsers(ctx context.Context) ([]tracker.AssigneeUser, error) {
	next := fmt.Sprintf("%s/repos/%s/%s/assignees?per_page=100", c.cfg.Endpoint, c.owner, c.repo)
	var out []tracker.AssigneeUser
	for next != "" {
		raw, link, err := c.get(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("github_fetch_users: %w", err)
		}
		items, _ := raw.([]any)
		for _, item := range items {
			obj, _ := item.(map[string]any)
			if login, _ := obj["login"].(string); login != "" {
				out = append(out, tracker.AssigneeUser{ID: login, Name: login})
			}
		}
		n, err := ParseNextLink(link)
		if err != nil {
			break
		}
		next = n
	}
	slices.SortFunc(out, func(a, b tracker.AssigneeUser) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// ScopeKey implements tracker.FilterManager: the repository plus the
// filter (and the login "me" resolved to).
func (c *Client) ScopeKey() string {
	f := c.Filter()
	key := "repo:" + strings.ToLower(c.owner+"/"+c.repo)
	if len(f) == 0 {
		return key
	}
	key += "|filter:" + f.Key()
	if slices.ContainsFunc(f.Assignees(), func(s string) bool { return strings.EqualFold(s, "me") }) {
		if login, ok := c.resolvedLogin("me"); ok {
			key += "|me=" + login
		}
	}
	return key
}

// ScopeLabel implements tracker.FilterManager: the filter's summary, ""
// without one.
func (c *Client) ScopeLabel() string { return c.Filter().Label() }
