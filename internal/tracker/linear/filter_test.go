package linear_test

import (
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// #198 — tracker.filter on Linear, against the fake server that evaluates
// the IssueFilter it is sent (filter_fake_test.go).

// newFilterFake holds:
//
//	F-1 Todo        me     ENG alpha [agent backend]       P1 cycle c1 (active)  M1
//	F-2 Todo        other  ENG alpha [agent]               P2
//	F-3 Todo        me     OPS beta  [agent blocked]       P3
//	F-4 Todo        nobody ENG -     [urgent]              P1
//	F-5 In Progress other  OPS beta  [ai needs-human]      P4 cycle c2
//	F-6 Todo        other  ENG alpha []                    P2                    M2
//	F-7 Backlog     me     ENG alpha [agent backend bug]   P1
//	F-8 Done        me     ENG alpha [agent]               P3
func newFilterFake(t *testing.T) (*fakeLinear, *httptest.Server) {
	f, srv := newFakeLinear(t)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	mk := func(n int, state, who, teamID, teamKey, project string, labels []string, prio int) *fakeIssue {
		return &fakeIssue{id: issueUUID(100 + n), identifier: "F-" + string(rune('0'+n)), state: state, assignee: who,
			teamID: teamID, teamKey: teamKey, project: project, labels: labels, updatedAt: at, priority: prio}
	}
	f.issues = []*fakeIssue{
		mk(1, "Todo", meID, engID, "ENG", "alpha", []string{"agent", "backend"}, 1),
		mk(2, "Todo", otherID, engID, "ENG", "alpha", []string{"agent"}, 2),
		mk(3, "Todo", meID, opsID, "OPS", "beta", []string{"agent", "blocked"}, 3),
		mk(4, "Todo", "", engID, "ENG", "", []string{"urgent"}, 1),
		mk(5, "In Progress", otherID, opsID, "OPS", "beta", []string{"ai", "needs-human"}, 4),
		mk(6, "Todo", otherID, engID, "ENG", "alpha", nil, 2),
		mk(7, "Backlog", meID, engID, "ENG", "alpha", []string{"agent", "backend", "bug"}, 1),
		mk(8, "Done", meID, engID, "ENG", "alpha", []string{"agent"}, 3),
	}
	f.issues[0].cycle, f.issues[0].cycleActive, f.issues[0].milestone = "c1", true, "M1"
	f.issues[4].cycle = "c2"
	f.issues[5].milestone = "M2"
	return f, srv
}

type filterCase struct {
	name   string
	filter issuefilter.Filter
	want   []string // every state
}

var filterCases = []filterCase{
	{"AND within a group", issuefilter.Filter{{Projects: []string{"alpha"}, LabelsAny: []string{"agent"}}},
		[]string{"F-1", "F-2", "F-7", "F-8"}},
	{"OR between groups", issuefilter.Filter{{Projects: []string{"beta"}}, {LabelsAny: []string{"urgent"}}},
		[]string{"F-3", "F-4", "F-5"}},
	{"any-of and none-of", issuefilter.Filter{{LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked", "needs-human"}}},
		[]string{"F-1", "F-2", "F-7", "F-8"}},
	{"none-of keeps unlabelled issues", issuefilter.Filter{{Projects: []string{"alpha"}, LabelsNone: []string{"bug"}}},
		[]string{"F-1", "F-2", "F-6", "F-8"}},
	{"all-of", issuefilter.Filter{{LabelsAll: []string{"agent", "backend"}}}, []string{"F-1", "F-7"}},
	{"assignee and priority", issuefilter.Filter{{Assignee: "me", Priority: []int{1}}}, []string{"F-1", "F-7"}},
	{"team by key", issuefilter.Filter{{Teams: []string{"OPS"}}}, []string{"F-3", "F-5"}},
	{"team by id", issuefilter.Filter{{Teams: []string{opsID}}}, []string{"F-3", "F-5"}},
	{"current cycle", issuefilter.Filter{{Cycle: "current"}}, []string{"F-1"}},
	{"cycle by id", issuefilter.Filter{{Cycle: "c2"}}, []string{"F-5"}},
	{"milestone", issuefilter.Filter{{Milestone: "m2"}}, []string{"F-6"}},
	{"states narrow the active states", issuefilter.Filter{{Projects: []string{"beta"}, States: []string{"Todo"}}}, []string{"F-3"}},
	{"no project", issuefilter.Filter{{Projects: []string{issuefilter.NoProject}}}, []string{"F-4"}},
	{"three groups", issuefilter.Filter{
		{Projects: []string{"alpha"}, LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked", "bug"}},
		{Assignee: "me", LabelsAll: []string{"agent", "blocked"}},
		{Priority: []int{4}},
	}, []string{"F-1", "F-2", "F-3", "F-5", "F-8"}},
}

func filterClient(srv *httptest.Server, f issuefilter.Filter) *linear.Client {
	return linear.NewClient(linear.ClientConfig{
		APIKey: "k", Endpoint: srv.URL, Filter: f,
		ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"},
	})
}

// TestLinearFilterCompilesAndOrNot (#198): AND inside a group, OR between
// groups and none-of compile to an IssueFilter that selects exactly the
// matching issues.
func TestLinearFilterCompilesAndOrNot(t *testing.T) {
	_, srv := newFilterFake(t)
	for _, tc := range filterCases[:4] {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterClient(srv, tc.filter).FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			assert.Equal(t, tc.want, idents(got))
		})
	}
	// The candidate poll is the same filter over the active states only.
	got, err := filterClient(srv, filterCases[1].filter).FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"F-3", "F-4", "F-5"}, idents(got))
}

// TestLinearLabelsAllMeansEveryListedLabel (#198): labels_all keeps an
// issue carrying every listed label, extra labels or not, and drops one
// missing any of them or carrying none — Linear's `every` ("all of the
// issue's labels are in the set") would get both wrong.
func TestLinearLabelsAllMeansEveryListedLabel(t *testing.T) {
	_, srv := newFilterFake(t)
	c := filterClient(srv, issuefilter.Filter{{LabelsAll: []string{"agent", "backend"}}})
	got, err := c.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Equal(t, []string{"F-1", "F-7"}, idents(got), "F-7 has an extra label; F-2 lacks backend; F-6 has none")
	match, known := c.MatchesFilter(domain.Issue{Labels: []string{"agent"}})
	assert.True(t, known)
	assert.False(t, match, "the in-memory matcher agrees")
}

// TestFilterSameSetOnPollPullWebhook (#198): for every condition, the full
// poll, the incremental pull, the by-ID scope check and the webhook refresh
// of the store return the same issue set.
func TestFilterSameSetOnPollPullWebhook(t *testing.T) {
	f, srv := newFilterFake(t)
	since := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var all []string
	for _, is := range f.issues {
		all = append(all, is.id)
	}
	for _, tc := range filterCases {
		t.Run(tc.name, func(t *testing.T) {
			poll, err := filterClient(srv, tc.filter).FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			assert.Equal(t, tc.want, idents(poll), "full poll")

			changed, gone, err := filterClient(srv, tc.filter).FetchIssuesUpdatedSince(t.Context(), since)
			require.NoError(t, err)
			assert.Empty(t, gone)
			assert.Equal(t, tc.want, idents(changed), "incremental pull")

			byID, err := filterClient(srv, tc.filter).FetchIssuesInScope(t.Context(), all)
			require.NoError(t, err)
			assert.Equal(t, tc.want, idents(byID), "by-ID scope check")

			f.setHidden(true) // the cold pull sees nothing; only deliveries place issues
			wrapped, h := hookedStore(t, filterClient(srv, tc.filter))
			_, err = wrapped.FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			f.setHidden(false)
			deliverAll(t, h, all...)
			var last []string
			require.Eventually(t, func() bool {
				got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
				last = idents(got)
				return err == nil && slices.Equal(last, tc.want)
			}, 5*time.Second, 5*time.Millisecond, "webhook refresh: want %v, last %v", tc.want, &last)
		})
	}
}

// TestFilterScopeKeyAndLabel (#198): the scope key covers the whole filter
// (a change rebaselines), the label summarises it, and a runtime filter
// replaces the configured one.
func TestFilterScopeKeyAndLabel(t *testing.T) {
	_, srv := newFilterFake(t)
	c := filterClient(srv, issuefilter.Filter{
		{Projects: []string{"ENG"}, LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked"}},
		{Assignee: "me", LabelsAll: []string{"urgent"}},
	})
	assert.True(t, c.FilterMode())
	assert.Equal(t, "ENG · agent|ai · ¬blocked  OR  me · urgent", c.ScopeLabel())
	before := c.ScopeKey()
	c.SetFilter(issuefilter.Filter{{Projects: []string{"ENG"}, LabelsAny: []string{"agent", "ai"}}})
	assert.NotEqual(t, before, c.ScopeKey(), "dropping a condition changes the key")
	c.SetFilter(nil)
	assert.False(t, c.FilterMode(), "an empty filter reads every issue")
	got, err := c.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Len(t, got, 8)

	// A legacy scope is one implicit group with the same meaning.
	legacy := (scopeCase{project: "alpha", assignee: "me", labels: []string{"agent"}}).client(srv)
	assert.False(t, legacy.FilterMode())
	assert.Equal(t, issuefilter.Filter{{Projects: []string{"alpha"}, Assignee: "me", LabelsAny: []string{"agent"}}}, legacy.Filter())
}

// TestFilterPreviewAndTeams (#198): the preview counts the active-state
// issues a proposed filter reads without changing the scope.
func TestFilterPreviewAndTeams(t *testing.T) {
	f, srv := newFilterFake(t)
	f.teams = []map[string]any{{"id": opsID, "key": "OPS", "name": "Ops"}, {"id": engID, "key": "ENG", "name": "Eng"}}
	c := filterClient(srv, nil)
	p, err := c.PreviewFilter(t.Context(), issuefilter.Filter{{Projects: []string{"beta"}}, {LabelsAny: []string{"urgent"}}}, 2)
	require.NoError(t, err)
	assert.Equal(t, tracker.FilterPreview{Count: 3, Identifiers: []string{"F-3", "F-4"}}, p)
	assert.False(t, c.FilterMode(), "the preview does not change the scope")
	teams, err := c.FetchTeams(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []tracker.Team{{ID: engID, Key: "ENG", Name: "Eng"}, {ID: opsID, Key: "OPS", Name: "Ops"}}, teams)
}
