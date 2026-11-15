package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker/github"
)

// #198 — tracker.filter on GitHub, against a fake that EVALUATES the issue
// search queries it is sent (label:, -label:, assignee:, milestone:,
// is:open/closed), so a query compiled wrong returns the wrong issues.

type filterIssue struct {
	number    int
	open      bool
	labels    []string
	assignees []string
	milestone string
}

type ghFake struct {
	t       *testing.T
	mu      sync.Mutex
	issues  []filterIssue
	queries []string
}

// newGHFake holds (todo / in-progress are the active state labels):
//
//	#1 open   [todo agent]             bot
//	#2 open   [todo ai]                ada   milestone v1
//	#3 open   [todo agent blocked]     bot
//	#4 open   [in-progress agent ai]   ada
//	#5 open   [todo]                   bot
//	#6 open   [agent]                  bot   (no state label)
//	#7 closed [agent]                  bot
func newGHFake(t *testing.T) (*ghFake, *httptest.Server) {
	f := &ghFake{t: t, issues: []filterIssue{
		{1, true, []string{"todo", "agent"}, []string{"bot"}, ""},
		{2, true, []string{"todo", "ai"}, []string{"ada"}, "v1"},
		{3, true, []string{"todo", "agent", "blocked"}, []string{"bot"}, ""},
		{4, true, []string{"in-progress", "agent", "ai"}, []string{"ada"}, ""},
		{5, true, []string{"todo"}, []string{"bot"}, ""},
		{6, true, []string{"agent"}, []string{"bot"}, ""},
		{7, false, []string{"agent"}, []string{"bot"}, ""},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (is filterIssue) raw() map[string]any {
	labels := []any{}
	for _, l := range is.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	assignees := []any{}
	for _, a := range is.assignees {
		assignees = append(assignees, map[string]any{"login": a})
	}
	state := "closed"
	if is.open {
		state = "open"
	}
	r := map[string]any{
		"number": is.number, "title": "T" + strconv.Itoa(is.number), "state": state, "labels": labels, "assignees": assignees,
		"repository_url": "https://api.github.com/repos/o/r", "updated_at": "2026-10-01T10:00:00Z",
	}
	if is.milestone != "" {
		r["milestone"] = map[string]any{"title": is.milestone}
	}
	return r
}

// searchTokens splits a search query, keeping "quoted values" whole.
func searchTokens(q string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ' ' && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (f *ghFake) matches(is filterIssue, q string) bool {
	has := func(l string) bool {
		return slices.ContainsFunc(is.labels, func(x string) bool { return strings.EqualFold(x, l) })
	}
	for _, tok := range searchTokens(q) {
		k, v, _ := strings.Cut(tok, ":")
		ok := true
		switch k {
		case "repo":
			ok = v == "o/r"
		case "is":
			ok = v == "issue" || (v == "open") == is.open
		case "label":
			ok = has(v)
		case "-label":
			ok = !has(v)
		case "assignee":
			if v == "@me" {
				v = "bot"
			}
			ok = slices.Contains(is.assignees, v)
		case "milestone":
			ok = strings.EqualFold(is.milestone, v)
		default:
			f.t.Errorf("fake GitHub search: unsupported qualifier %q", tok)
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f *ghFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var out any
	switch {
	case r.URL.Path == "/search/issues":
		q := r.URL.Query().Get("q")
		f.queries = append(f.queries, q)
		items := []any{}
		for _, is := range f.issues {
			if f.matches(is, q) {
				items = append(items, is.raw())
			}
		}
		out = map[string]any{"total_count": len(items), "items": items}
	case r.URL.Path == "/user":
		out = map[string]any{"login": "bot"}
	case r.URL.Path == "/repos/o/r/issues":
		items := []any{}
		for _, is := range f.issues {
			items = append(items, is.raw())
		}
		out = items
	case strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues/"))
		for _, is := range f.issues {
			if is.number == n {
				out = is.raw()
			}
		}
		if out == nil {
			w.WriteHeader(http.StatusNotFound)
			out = map[string]any{}
		}
	default:
		f.t.Errorf("fake GitHub: unexpected %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		out = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func ghClient(srv *httptest.Server, f issuefilter.Filter) *github.Client {
	return github.NewClient(github.ClientConfig{
		APIKey: "k", ProjectSlug: "o/r", Endpoint: srv.URL, Filter: f,
		ActiveStates: []string{"todo", "in-progress"}, TerminalStates: []string{"closed"},
	})
}

func ghIdents(issues []domain.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Identifier)
	}
	slices.Sort(out)
	return out
}

// TestGitHubFilterGroupsUnionDedupe (#198): each group is one search (one
// per label when labels_any names several), the results are unioned and
// de-duplicated, state labels keep deciding the state, and the change
// listing and the by-ID read agree with the searches.
func TestGitHubFilterGroupsUnionDedupe(t *testing.T) {
	f, srv := newGHFake(t)
	filter := issuefilter.Filter{
		{LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked"}},
		{Assignee: "me", LabelsAll: []string{"todo"}},
	}
	c := ghClient(srv, filter)
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	want := []string{"#1", "#2", "#3", "#4", "#5"}
	assert.Equal(t, want, ghIdents(got), "#4 matches two searches and is listed once; #6 has no state label")
	assert.Len(t, got, len(want), "no duplicates")
	assert.Equal(t, []string{
		`repo:o/r is:issue -label:"blocked" label:"agent" is:open`,
		`repo:o/r is:issue -label:"blocked" label:"ai" is:open`,
		`repo:o/r is:issue label:"todo" assignee:@me is:open`,
	}, f.queries, "one search per group, labels_any split per label")

	// The change listing and the by-ID read evaluate the same filter.
	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, append(slices.Clone(want), "#7"), ghIdents(changed), "closed #7 is a tracked terminal state")
	assert.Equal(t, []string{"6"}, gone, "#6 has no state label")
	byID, err := c.FetchIssuesInScope(t.Context(), []string{"1", "2", "3", "4", "5", "6", "7"})
	require.NoError(t, err)
	assert.Equal(t, []string{"#1", "#2", "#3", "#4", "#5", "#6", "#7"}, ghIdents(byID),
		"by ID every issue the filter matches is in scope, whatever its state (as without a filter)")
	// Terminal reads search the closed issues the filter matches.
	closed, err := c.FetchIssuesByStates(t.Context(), []string{"closed"})
	require.NoError(t, err)
	assert.Equal(t, []string{"#7"}, ghIdents(closed))
	c.SetFilter(issuefilter.Filter{{LabelsNone: []string{"agent"}}})
	byID, err = c.FetchIssuesInScope(t.Context(), []string{"1", "2", "5"})
	require.NoError(t, err)
	assert.Equal(t, []string{"#2", "#5"}, ghIdents(byID), "an issue leaving the filter leaves the scope")

}

// TestGitHubFilterNoneAndStates (#198): labels_none excludes in the search
// and in the change listing; a states condition narrows the active states.
func TestGitHubFilterNoneAndStates(t *testing.T) {
	_, srv := newGHFake(t)
	c := ghClient(srv, issuefilter.Filter{{LabelsAny: []string{"agent"}, LabelsNone: []string{"blocked"}, States: []string{"in-progress"}}})
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"#4"}, ghIdents(got))
	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []string{"#4", "#7"}, ghIdents(changed), "closed #7 is not an active state, so states does not narrow it")
	slices.Sort(gone)
	assert.Equal(t, []string{"1", "2", "3", "5", "6"}, gone, "#3 is blocked, #1/#5 are todo, #2 lacks agent")
	assert.Equal(t, "agent · ¬blocked · state in-progress", c.ScopeLabel())

	m, known := c.MatchesFilter(domain.Issue{State: "in-progress", Labels: []string{"agent", "blocked"}})
	assert.True(t, known)
	assert.False(t, m)

	p, err := c.PreviewFilter(t.Context(), issuefilter.Filter{{Milestone: "V1"}}, 5)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Count)
	assert.Equal(t, []string{"#2"}, p.Identifiers)
}
