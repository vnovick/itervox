package store_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// The Jira adapter through the store (#113, #114), over a fake Jira site.

// fakeJiraIssue is one issue the fake site holds; updated is epoch ms.
type fakeJiraIssue struct {
	id, key, status, summary, branch string
	labels                           []string
	updated                          int64
}

// fakeJira is a Jira site that answers the adapter's searches by the JQL's
// status, label and update-time clauses, returns the itervox.branch
// property only when a request asks for it, and serves the detail read.
type fakeJira struct {
	mu       sync.Mutex
	issues   []*fakeJiraIssue
	clock    int64
	searches atomic.Int64
	jqls     []string
	// lagging names an issue the next search does not show yet: Jira's
	// search index has not caught up with it.
	lagging string
}

var (
	jqlStatuses = regexp.MustCompile(`status in \(([^)]*)\)`)
	jqlLabels   = regexp.MustCompile(`labels in \(([^)]*)\)`)
	jqlUpdated  = regexp.MustCompile(`updated >= (-?\d+)`)
	jqlQuoted   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	issuePathRe = regexp.MustCompile(`^/rest/api/3/issue/([0-9A-Z-]+)(/comment)?$`)
)

func quotedList(s string) []string {
	var out []string
	for _, m := range jqlQuoted.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

func newFakeJira(t *testing.T, issues ...*fakeJiraIssue) (*fakeJira, *httptest.Server) {
	t.Helper()
	f := &fakeJira{issues: issues, clock: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).UnixMilli()}
	for _, is := range issues {
		is.updated = f.clock
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

// edit changes an issue as a person would in Jira: it bumps its update time.
func (f *fakeJira) edit(key string, change func(*fakeJiraIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock += 1000
	for _, is := range f.issues {
		if is.key == key {
			change(is)
			is.updated = f.clock
		}
	}
}

func (f *fakeJira) render(is *fakeJiraIssue, withBranch bool) map[string]any {
	labels := make([]any, 0, len(is.labels))
	for _, l := range is.labels {
		labels = append(labels, l)
	}
	out := map[string]any{"id": is.id, "key": is.key, "fields": map[string]any{
		"summary": is.summary,
		"status":  map[string]any{"name": is.status},
		"labels":  labels,
		"updated": time.UnixMilli(is.updated).UTC().Format("2006-01-02T15:04:05.000-0700"),
	}}
	if withBranch && is.branch != "" {
		out["properties"] = map[string]any{"itervox.branch": map[string]any{"branch": is.branch}}
	}
	return out
}

func (f *fakeJira) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/rest/api/3/search/jql" {
		f.searches.Add(1)
		var body struct {
			JQL        string   `json:"jql"`
			Properties []string `json:"properties"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.jqls = append(f.jqls, body.JQL)
		withBranch := slices.Contains(body.Properties, "itervox.branch")
		var statuses, labels []string
		if m := jqlStatuses.FindStringSubmatch(body.JQL); m != nil {
			statuses = quotedList(m[1])
		}
		if m := jqlLabels.FindStringSubmatch(body.JQL); m != nil {
			labels = quotedList(m[1])
		}
		since := int64(-1 << 62)
		if m := jqlUpdated.FindStringSubmatch(body.JQL); m != nil {
			since, _ = strconv.ParseInt(m[1], 10, 64)
		}
		out := []any{}
		hidden := f.lagging
		f.lagging = ""
		for _, is := range f.issues {
			if is.key == hidden {
				continue
			}
			if statuses != nil && !slices.Contains(statuses, strings.ToLower(is.status)) {
				continue
			}
			if labels != nil && !slices.ContainsFunc(is.labels, func(l string) bool { return slices.Contains(labels, strings.ToLower(l)) }) {
				continue
			}
			if is.updated < since {
				continue
			}
			out = append(out, f.render(is, withBranch))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": out, "isLast": true})
		return
	}
	if m := issuePathRe.FindStringSubmatch(r.URL.Path); m != nil {
		if m[2] != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []any{}, "total": 0})
			return
		}
		for _, is := range f.issues {
			if is.id == m[1] || is.key == m[1] {
				_ = json.NewEncoder(w).Encode(f.render(is, r.URL.Query().Get("properties") == "itervox.branch"))
				return
			}
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *fakeJira) lastJQL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jqls[len(f.jqls)-1]
}

func jiraClient(srv *httptest.Server, labels ...string) *jira.Client {
	return jira.NewClient(jira.ClientConfig{
		Endpoint: srv.URL, APIKey: "me@example.com:tok", ProjectSlug: "PROJ",
		ActiveStates: []string{"To Do", "In Progress"}, Labels: labels,
	})
}

func jiraStoreConfig(t *testing.T) store.Config {
	t.Helper()
	cfg := storeConfig(t, t.TempDir())
	cfg.Scope = "jira||PROJ|in progress,to do"
	cfg.ActiveStates = []string{"To Do", "In Progress"}
	cfg.Views = [][]string{{"To Do", "In Progress", "Done"}}
	return cfg
}

// TestStoreJiraAdapter: the Jira client works through the store, and after
// the cold pull (the candidates and one search per view) answers from it
// without a request.
func TestStoreJiraAdapter(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"},
		&fakeJiraIssue{id: "2", key: "PROJ-2", status: "Backlog"},
		&fakeJiraIssue{id: "3", key: "PROJ-3", status: "Done"},
	)
	cfg := jiraStoreConfig(t)
	cold, after, cands, board := readsAfterColdPull(t, jiraClient(srv), &f.searches, cfg)
	assert.Equal(t, int64(2), cold, "the candidates and the one view")
	assert.Zero(t, after, "no Jira request after the cold pull")
	assert.Equal(t, []string{"PROJ-1"}, cands)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-3"}, board)
}

// TestStoreJiraIncrementalPull: between full pulls the store asks Jira only
// for what changed, in one search by update time with no status clause; it
// moves an issue whose status changed, drops one that left the label scope,
// and keeps the branch a detail read stored when the issue is listed again.
func TestStoreJiraIncrementalPull(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do", labels: []string{"agent"}},
		&fakeJiraIssue{id: "2", key: "PROJ-2", status: "In Progress", labels: []string{"agent"}, branch: "itervox/PROJ-2"},
		&fakeJiraIssue{id: "3", key: "PROJ-3", status: "Done", labels: []string{"agent"}},
		&fakeJiraIssue{id: "4", key: "PROJ-4", status: "To Do"},
	)
	tr, st, err := store.Wrap(jiraClient(srv, "agent"), jiraStoreConfig(t))
	require.NoError(t, err)
	ctx := t.Context()
	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(cands))
	detail, err := tr.FetchIssueDetail(ctx, "2")
	require.NoError(t, err)
	require.NotNil(t, detail.BranchName)
	full := f.searches.Load()

	f.edit("PROJ-1", func(is *fakeJiraIssue) { is.status = "Done" })
	f.edit("PROJ-2", func(is *fakeJiraIssue) { is.summary = "Renamed in Jira" })
	f.edit("PROJ-3", func(is *fakeJiraIssue) { is.labels = nil })
	require.NoError(t, st.RequestSyncNowForTest(ctx))

	assert.Equal(t, full+1, f.searches.Load(), "one search")
	jql := f.lastJQL()
	assert.Regexp(t, `^project = "PROJ" AND updated >= \d+ ORDER BY updated DESC$`, jql)
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"PROJ-2"}, identifiers(cands), "PROJ-1 moved to Done")
	all, err := tr.FetchIssuesByStates(ctx, []string{"To Do", "In Progress", "Done"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(all), "PROJ-3 left the label scope")
	assert.Equal(t, full+1, f.searches.Load(), "the reads are answered from the store")
	for _, is := range all {
		if is.ID != "2" {
			continue
		}
		assert.Equal(t, "Renamed in Jira", is.Title)
		if assert.NotNil(t, is.BranchName, "the listing again keeps the branch") {
			assert.Equal(t, "itervox/PROJ-2", *is.BranchName)
		}
	}
}

// TestStoreKeepsJirasScopeMatcher: the Jira adapter's in-memory scope check
// (tracker.ScopeMatcher), which the orchestrator runs on a retry's by-ID
// read (#198), answers through the store as it does on the adapter; the
// rate-limit snapshot stays nil.
func TestStoreKeepsJirasScopeMatcher(t *testing.T) {
	_, srv := newFakeJira(t)
	up := jiraClient(srv, "agent")
	tr, _, err := store.Wrap(up, jiraStoreConfig(t))
	require.NoError(t, err)

	m, ok := tr.(tracker.ScopeMatcher)
	require.True(t, ok, "the store forwards Jira's scope check")
	in := domain.Issue{ID: "1", Identifier: "PROJ-1", Labels: []string{"Agent"}}
	out := domain.Issue{ID: "2", Identifier: "PROJ-2"}
	moved := domain.Issue{ID: "3", Identifier: "OTHER-3", Labels: []string{"agent"}}
	for _, is := range []domain.Issue{in, out, moved} {
		wantIn, wantKnown := up.MatchesFilter(is)
		gotIn, gotKnown := m.MatchesFilter(is)
		assert.Equal(t, wantIn, gotIn, is.Identifier)
		assert.Equal(t, wantKnown, gotKnown, is.Identifier)
	}
	match, _ := m.MatchesFilter(out)
	assert.False(t, match)
	rl, ok := tr.(tracker.RateLimiter)
	require.True(t, ok)
	assert.Nil(t, rl.RateLimitSnapshot())
	_, ok = tr.(tracker.FilterManager)
	assert.False(t, ok, "Jira has no filter to manage")
}

// TestStoreActiveViewIssuesAreCandidates: Jira's search index lags, so a
// full pull's first search (the candidates) can miss an issue that was just
// created or moved into an active state, while its next search (a view)
// lists it. With Config.ActiveViewIssuesAreCandidates (set for Jira) the
// issue is a candidate after that pull, and a view issue in a status that
// is not active is not; without it (Linear and GitHub, as before) the
// missed issue stays out of the candidates through the incremental pulls,
// until the next full pull.
func TestStoreActiveViewIssuesAreCandidates(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off, as before", true: "on"}[on], func(t *testing.T) {
			f, srv := newFakeJira(t,
				&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"},
				&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do"},
				&fakeJiraIssue{id: "3", key: "PROJ-3", status: "Done"},
			)
			f.lagging = "PROJ-2" // the first search, the candidates, misses it
			cfg := jiraStoreConfig(t)
			cfg.ActiveViewIssuesAreCandidates = on
			tr, st, err := store.Wrap(jiraClient(srv), cfg)
			require.NoError(t, err)
			ctx := t.Context()
			want := []string{"PROJ-1"}
			if on {
				want = []string{"PROJ-1", "PROJ-2"}
			}

			cands, err := tr.FetchCandidateIssues(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, want, identifiers(cands), "after the full pull")
			board, err := tr.FetchIssuesByStates(ctx, cfg.Views[0])
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2", "PROJ-3"}, identifiers(board), "the view lists it either way")
			require.NoError(t, st.RequestSyncNowForTest(ctx))
			cands, err = tr.FetchCandidateIssues(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, want, identifiers(cands), "after an incremental pull")
		})
	}
}

// TestStoreActiveViewPromotionKeepsTheScope: the promotion cannot add an
// issue outside the tracker scope. Jira's views are read with the same
// scope as the candidates (every search of the pull carries the label
// clause; the assignee clause too, see TestFetchIssuesByStatesIsScoped in
// the jira package), so an active issue without the label is never listed
// by a view, and so never promoted, while an in-scope issue the candidate
// search missed is.
func TestStoreActiveViewPromotionKeepsTheScope(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do", labels: []string{"agent"}},
		&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do", labels: []string{"agent"}},
		&fakeJiraIssue{id: "3", key: "PROJ-3", status: "To Do"},
	)
	f.lagging = "PROJ-2"
	cfg := jiraStoreConfig(t)
	cfg.ActiveViewIssuesAreCandidates = true
	tr, _, err := store.Wrap(jiraClient(srv, "agent"), cfg)
	require.NoError(t, err)
	ctx := t.Context()

	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(cands), "PROJ-3 has no label")
	board, err := tr.FetchIssuesByStates(ctx, cfg.Views[0])
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(board))
	f.mu.Lock()
	jqls := slices.Clone(f.jqls)
	f.mu.Unlock()
	require.Len(t, jqls, 2, "the candidates and the one view")
	for _, jql := range jqls {
		assert.Contains(t, jql, `labels in ("agent")`, "every search of the pull is scoped")
	}
}
