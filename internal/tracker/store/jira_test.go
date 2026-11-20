package store_test

import (
	"context"
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
// project, status, label and update-time clauses, returns the itervox.branch
// property only when a request asks for it, and serves the detail read and
// the bulk fetch. A search for `project = "PROJ"` does not list an issue
// moved to another project: Jira gives it the other project's key and keeps
// its numeric id; reads by id find it wherever it is.
type fakeJira struct {
	mu        sync.Mutex
	issues    []*fakeJiraIssue
	clock     int64
	searches  atomic.Int64
	bulkReads atomic.Int64
	jqls      []string
	// lagging names an issue the next search does not show yet: Jira's
	// search index has not caught up with it.
	lagging string
	// staleSearch, when > 0, is the ordinal of the search answered from
	// staleCopy (see snapshot) instead of the live issues: Jira's search is
	// not read-after-write consistent, so two searches of one pull can see
	// different versions of the same issue. Reads by id never lag.
	staleSearch int64
	staleCopy   []fakeJiraIssue
	// failBulk makes the bulk fetch answer 400.
	failBulk bool
}

var (
	jqlProject  = regexp.MustCompile(`project = "([^"]*)"`)
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

// snapshot returns a copy of the issues as they are now, for staleCopy.
func (f *fakeJira) snapshot() []fakeJiraIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeJiraIssue, 0, len(f.issues))
	for _, is := range f.issues {
		c := *is
		c.labels = slices.Clone(is.labels)
		out = append(out, c)
	}
	return out
}

// lagSearch makes the n-th search answer from stale, an earlier snapshot.
func (f *fakeJira) lagSearch(n int64, stale []fakeJiraIssue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staleSearch, f.staleCopy = n, stale
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
	if r.URL.Path == "/rest/api/3/issue/bulkfetch" {
		f.bulkReads.Add(1)
		if f.failBulk {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"errorMessages": []string{"bulk fetch refused"}})
			return
		}
		var body struct {
			IDs        []string `json:"issueIdsOrKeys"`
			Properties []string `json:"properties"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		out := []any{}
		for _, is := range f.issues {
			if slices.Contains(body.IDs, is.id) {
				out = append(out, f.render(is, slices.Contains(body.Properties, "itervox.branch")))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": out})
		return
	}
	if r.URL.Path == "/rest/api/3/search/jql" {
		n := f.searches.Add(1)
		var body struct {
			JQL        string   `json:"jql"`
			Properties []string `json:"properties"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.jqls = append(f.jqls, body.JQL)
		withBranch := slices.Contains(body.Properties, "itervox.branch")
		project := ""
		if m := jqlProject.FindStringSubmatch(body.JQL); m != nil {
			project = m[1] + "-"
		}
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
		source := f.issues
		if n == f.staleSearch {
			source = nil
			for i := range f.staleCopy {
				source = append(source, &f.staleCopy[i])
			}
		}
		out := []any{}
		hidden := f.lagging
		f.lagging = ""
		for _, is := range source {
			if is.key == hidden || !strings.HasPrefix(is.key, project) {
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

// jiraStoreConfig is the store as withTrackerStore builds it for Jira, with
// one board view.
func jiraStoreConfig(t *testing.T) store.Config {
	t.Helper()
	cfg := storeConfig(t, t.TempDir())
	cfg.Scope = "jira||PROJ|in progress,to do"
	cfg.ActiveStates = []string{"To Do", "In Progress"}
	cfg.Views = [][]string{{"To Do", "In Progress", "Done"}}
	cfg.ListingsLagWrites = true
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
// (tracker.ScopeMatcher), which the orchestrator runs on the by-ID read of a
// retry and of a dispatch claim check (#198), answers through the store as
// it does on the adapter; the rate-limit snapshot stays nil.
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

// TestStoreCountsAConfirmedActiveViewIssueAsACandidate: Jira's search index
// lags, so a full pull's first search (the candidates) can miss an issue that
// was just created or moved into an active state, while its next search (a
// view) lists it. With Config.ListingsLagWrites (set for Jira) the store
// reads that issue by id, in one bulk read, and it is a candidate after that
// pull because the read shows it active and in the scope; a view issue in a
// status that is not active is not read. Without it (Linear and GitHub, as
// before) nothing is read by id and the missed issue stays out of the
// candidates through the incremental pulls, until the next full pull.
func TestStoreCountsAConfirmedActiveViewIssueAsACandidate(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off, as before", true: "on"}[on], func(t *testing.T) {
			f, srv := newFakeJira(t,
				&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"},
				&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do"},
				&fakeJiraIssue{id: "3", key: "PROJ-3", status: "Done"},
			)
			f.lagging = "PROJ-2" // the first search, the candidates, misses it
			cfg := jiraStoreConfig(t)
			cfg.ListingsLagWrites = on
			tr, st, err := store.Wrap(jiraClient(srv), cfg)
			require.NoError(t, err)
			ctx := t.Context()
			want, reads := []string{"PROJ-1"}, int64(0)
			if on {
				want, reads = []string{"PROJ-1", "PROJ-2"}, 1
			}

			cands, err := tr.FetchCandidateIssues(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, want, identifiers(cands), "after the full pull")
			assert.Equal(t, reads, f.bulkReads.Load(), "reads by id in the full pull")
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

// TestStoreActiveViewPromotionKeepsTheScope: every search of a full pull
// carries the scope's label clause (the assignee clause too, see
// TestFetchIssuesByStatesIsScoped in the jira package), so a view never lists
// an active issue without the label and the promotion never reads it, while
// an in-scope issue the candidate search missed is read by id and promoted.
// A view search that lags and still lists an issue that has left the scope
// is the case of TestStorePromotionLeavesOutAnIssueThatLeftTheLabelScope:
// the read by id leaves it out.
func TestStoreActiveViewPromotionKeepsTheScope(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do", labels: []string{"agent"}},
		&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do", labels: []string{"agent"}},
		&fakeJiraIssue{id: "3", key: "PROJ-3", status: "To Do"},
	)
	f.lagging = "PROJ-2"
	cfg := jiraStoreConfig(t)
	tr, _, err := store.Wrap(jiraClient(srv, "agent"), cfg)
	require.NoError(t, err)
	ctx := t.Context()

	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(cands), "PROJ-3 has no label")
	assert.Equal(t, int64(1), f.bulkReads.Load(), "PROJ-2 was read by id")
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

// outOfScope returns the identifiers of the candidates the store's own
// tracker.ScopeMatcher says are outside the scope.
func outOfScope(t *testing.T, tr tracker.Tracker, cands []domain.Issue) []string {
	t.Helper()
	m, ok := tr.(tracker.ScopeMatcher)
	require.True(t, ok)
	var out []string
	for _, is := range cands {
		if in, known := m.MatchesFilter(is); known && !in {
			out = append(out, is.Identifier)
		}
	}
	return out
}

// TestStoreDropsAJiraIssueMovedToAnotherProject: an issue moved to another
// Jira project after a full pull leaves the store once a read by id shows its
// new key. Jira keeps the issue's id and gives it the other project's key.
// FetchIssuesUpdatedSince searches `project = "PROJ"`, which never lists the
// moved issue again, so no incremental pull can drop it. The claim check's
// read by id (FetchIssueDetail) shows it under the other project's key, which
// the store's own MatchesFilter (Jira's ScopeMatcher, forwarded by
// matchingStore) puts outside the scope; with Config.ListingsLagWrites that
// read removes it, instead of storing it as a candidate until the next full
// pull (tracker.store.full_sync_interval_ms, default one hour).
func TestStoreDropsAJiraIssueMovedToAnotherProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(ctx context.Context, tr tracker.Tracker) (domain.Issue, error)
	}{
		{"detail read (dispatch)", func(ctx context.Context, tr tracker.Tracker) (domain.Issue, error) {
			is, err := tr.FetchIssueDetail(ctx, "10001")
			if is == nil {
				return domain.Issue{}, err
			}
			return *is, err
		}},
		{"state read (retry, reviewer, automation)", func(ctx context.Context, tr tracker.Tracker) (domain.Issue, error) {
			got, err := tr.FetchIssueStatesByIDs(ctx, []string{"10001"})
			if len(got) != 1 {
				return domain.Issue{}, err
			}
			return got[0], err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeJira(t,
				&fakeJiraIssue{id: "10001", key: "PROJ-1", status: "To Do"},
				&fakeJiraIssue{id: "10002", key: "PROJ-2", status: "To Do"},
			)
			cfg := jiraStoreConfig(t)
			tr, st, err := store.Wrap(jiraClient(srv), cfg)
			require.NoError(t, err)
			ctx := t.Context()
			cands, err := tr.FetchCandidateIssues(ctx)
			require.NoError(t, err)
			require.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(cands))

			f.edit("PROJ-1", func(is *fakeJiraIssue) { is.key = "OTHER-9" }) // moved; same id, same status name
			require.NoError(t, st.RequestSyncNowForTest(ctx))                // incremental
			read, err := tc.read(ctx, tr)                                    // the claim read
			require.NoError(t, err)
			require.Equal(t, "OTHER-9", read.Identifier)

			cands, err = tr.FetchCandidateIssues(ctx)
			require.NoError(t, err)
			assert.Empty(t, outOfScope(t, tr, cands),
				"the store offers candidates that its own MatchesFilter puts outside project PROJ (candidates: %v)", identifiers(cands))
			assert.Equal(t, []string{"PROJ-2"}, identifiers(cands))
			board, err := tr.FetchIssuesByStates(ctx, cfg.Views[0])
			require.NoError(t, err)
			assert.Equal(t, []string{"PROJ-2"}, identifiers(board), "the board view drops it too")
		})
	}
}

// TestStorePromotionLeavesOutAnIssueThatLeftTheLabelScope: Jira's search lag
// works in both directions. When the candidate search (the first request of
// a full pull) already sees a person's edit and a view search (a later
// request) still returns the older copy, the view lists an issue the
// candidate search rightly left out. Here a person removed the scope label
// of PROJ-2 just before the pull: the candidate search does not list it, and
// a lagging view search lists the old copy with the label. With
// Config.ListingsLagWrites the store reads PROJ-2 by id before it promotes
// it; the read shows it outside the scope, so it is not a candidate and
// leaves the store. With the option off (Linear and GitHub) nothing is
// promoted.
func TestStorePromotionLeavesOutAnIssueThatLeftTheLabelScope(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "option off", true: "option on (Jira)"}[on], func(t *testing.T) {
			f, srv := newFakeJira(t,
				&fakeJiraIssue{id: "10001", key: "PROJ-1", status: "To Do", labels: []string{"agent"}},
				&fakeJiraIssue{id: "10002", key: "PROJ-2", status: "To Do", labels: []string{"agent"}},
			)
			before := f.snapshot()
			f.edit("PROJ-2", func(is *fakeJiraIssue) { is.labels = nil }) // a person removes the label
			f.lagSearch(2, before)                                        // the view search (2nd request) lags

			cfg := jiraStoreConfig(t)
			cfg.ListingsLagWrites = on
			tr, _, err := store.Wrap(jiraClient(srv, "agent"), cfg)
			require.NoError(t, err)
			cands, err := tr.FetchCandidateIssues(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []string{"PROJ-1"}, identifiers(cands),
				"PROJ-2 lost its scope label before the pull and the candidate search left it out")
			if on {
				assert.Equal(t, int64(1), f.bulkReads.Load(), "one read by id, of PROJ-2")
				board, err := tr.FetchIssuesByStates(t.Context(), cfg.Views[0])
				require.NoError(t, err)
				assert.Equal(t, []string{"PROJ-1"}, identifiers(board), "the read by id took it out of the store")
			}
		})
	}
}

// TestStorePromotionChecksTheStateOnTheReadByID: a person closed PROJ-2 just
// before a full pull; the candidate search does not list it, and a lagging
// view search lists the old copy in an active state. The read by id shows it
// closed, so it is not a candidate, and the board shows the read's state.
func TestStorePromotionChecksTheStateOnTheReadByID(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "10001", key: "PROJ-1", status: "To Do"},
		&fakeJiraIssue{id: "10002", key: "PROJ-2", status: "To Do"},
	)
	before := f.snapshot()
	f.edit("PROJ-2", func(is *fakeJiraIssue) { is.status = "Done" })
	f.lagSearch(2, before) // the view search lags
	cfg := jiraStoreConfig(t)
	tr, _, err := store.Wrap(jiraClient(srv), cfg)
	require.NoError(t, err)
	ctx := t.Context()

	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"PROJ-1"}, identifiers(cands))
	assert.Equal(t, int64(1), f.bulkReads.Load())
	board, err := tr.FetchIssuesByStates(ctx, cfg.Views[0])
	require.NoError(t, err)
	states := map[string]string{}
	for _, is := range board {
		states[is.Identifier] = is.State
	}
	assert.Equal(t, map[string]string{"PROJ-1": "To Do", "PROJ-2": "Done"}, states)
}

// TestStorePromotionReadFailureWaitsForTheNextPull: when the read by id of a
// full pull fails, the issue only a view listed is not a candidate, the pull
// still succeeds, and the next pull is a full one, so the issue does not wait
// for tracker.store.full_sync_interval_ms.
func TestStorePromotionReadFailureWaitsForTheNextPull(t *testing.T) {
	f, srv := newFakeJira(t,
		&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"},
		&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do"},
	)
	f.lagging = "PROJ-2" // the candidate search misses it, the view lists it
	f.failBulk = true
	tr, st, err := store.Wrap(jiraClient(srv), jiraStoreConfig(t))
	require.NoError(t, err)
	ctx := t.Context()
	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"PROJ-1"}, identifiers(cands), "not confirmed, not a candidate")
	assert.Nil(t, st.SyncStatus().LastErr, "the pull succeeded")

	f.mu.Lock()
	f.failBulk = false
	f.mu.Unlock()
	searches := f.searches.Load()
	require.NoError(t, st.RequestSyncNowForTest(ctx))
	assert.Equal(t, searches+2, f.searches.Load(), "a full pull: the candidates and the view")
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, identifiers(cands))
}

// TestStoreJiraIncrementalPullKeepsTheNewerCopy: an incremental pull used to
// replace the stored copy of every listed issue whose listing differs
// (sameListing), without asking which copy is newer. On Linear and GitHub a
// listing is never older than a read by id made before the pull; on Jira it
// can be, because search lags writes and the Jira pull looks back five
// minutes (searchLag), so it lists recently changed issues again. Here a
// person closes PROJ-1, the claim check's read by id stores Done, and the
// next incremental pull is answered from a search index that has not caught
// up. With Config.ListingsLagWrites the older In Progress listing does not
// replace the Done copy, so the store does not offer a closed issue as a
// candidate again.
func TestStoreJiraIncrementalPullKeepsTheNewerCopy(t *testing.T) {
	f, srv := newFakeJira(t, &fakeJiraIssue{id: "10001", key: "PROJ-1", status: "In Progress"})
	tr, st, err := store.Wrap(jiraClient(srv), jiraStoreConfig(t))
	require.NoError(t, err)
	ctx := t.Context()
	cands, err := tr.FetchCandidateIssues(ctx) // full pull: searches 1 and 2
	require.NoError(t, err)
	require.Equal(t, []string{"PROJ-1"}, identifiers(cands))

	before := f.snapshot()
	f.edit("PROJ-1", func(is *fakeJiraIssue) { is.status = "Done" })
	detail, err := tr.FetchIssueDetail(ctx, "10001") // the claim read stores Done
	require.NoError(t, err)
	require.Equal(t, "Done", detail.State)
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Empty(t, cands, "precondition: the store knows PROJ-1 is Done")

	f.lagSearch(3, before) // the incremental search lags
	require.NoError(t, st.RequestSyncNowForTest(ctx))
	require.Equal(t, int64(3), f.searches.Load(), "precondition: the pull was the incremental search")
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Empty(t, cands, "an older listing (updated before the copy the store holds) replaced the stored Done copy")
}

// TestStoreWithoutListingLagIsUnchanged: Linear and GitHub leave
// Config.ListingsLagWrites off, and the store then does what it did before
// the option carried the rules above, request for request: a full pull reads
// nothing by id and promotes no issue only a view listed; a read by id that
// shows an issue outside the scope leaves it in the store (the orchestrator's
// dispatch claim check refuses it and makes the store forget it, see
// TestIssueThatLeftTheScopeLeavesTheStoreAtTheClaimCheck); and an incremental
// pull places every listing that differs from the stored copy, newer or not.
func TestStoreWithoutListingLagIsUnchanged(t *testing.T) {
	off := func(t *testing.T) store.Config {
		cfg := jiraStoreConfig(t)
		cfg.ListingsLagWrites = false
		return cfg
	}
	t.Run("full pull", func(t *testing.T) {
		f, srv := newFakeJira(t,
			&fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"},
			&fakeJiraIssue{id: "2", key: "PROJ-2", status: "To Do"},
		)
		f.lagging = "PROJ-2" // the candidate search misses it, the view lists it
		tr, _, err := store.Wrap(jiraClient(srv), off(t))
		require.NoError(t, err)
		cands, err := tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"PROJ-1"}, identifiers(cands), "not promoted")
		assert.Equal(t, int64(2), f.searches.Load(), "the candidates and the one view")
		assert.Zero(t, f.bulkReads.Load(), "nothing read by id")
	})
	t.Run("read by id outside the scope", func(t *testing.T) {
		f, srv := newFakeJira(t, &fakeJiraIssue{id: "1", key: "PROJ-1", status: "To Do"})
		tr, _, err := store.Wrap(jiraClient(srv), off(t))
		require.NoError(t, err)
		ctx := t.Context()
		_, err = tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		f.edit("PROJ-1", func(is *fakeJiraIssue) { is.key = "OTHER-9" })
		_, err = tr.FetchIssueDetail(ctx, "1")
		require.NoError(t, err)
		_, err = tr.FetchIssueStatesByIDs(ctx, []string{"1"})
		require.NoError(t, err)
		cands, err := tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"OTHER-9"}, identifiers(cands), "kept, with the read's copy")
	})
	t.Run("older listing", func(t *testing.T) {
		f, srv := newFakeJira(t, &fakeJiraIssue{id: "1", key: "PROJ-1", status: "In Progress"})
		tr, st, err := store.Wrap(jiraClient(srv), off(t))
		require.NoError(t, err)
		ctx := t.Context()
		_, err = tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		before := f.snapshot()
		f.edit("PROJ-1", func(is *fakeJiraIssue) { is.status = "Done" })
		_, err = tr.FetchIssueDetail(ctx, "1")
		require.NoError(t, err)
		f.lagSearch(3, before)
		require.NoError(t, st.RequestSyncNowForTest(ctx))
		cands, err := tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"PROJ-1"}, identifiers(cands), "the listing is placed")
	})
}
