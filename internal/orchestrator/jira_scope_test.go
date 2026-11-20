package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker/jira"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// Dispatch against the Jira tracker scope, over the Jira adapter and the
// tracker store.
//
// fakeJiraSite is a fake Jira Cloud site that answers the adapter's searches
// by the project, assignee, label, status and update-time clauses of the
// JQL, as Jira does. In particular a search for `project = "PROJ"` does not
// list an issue that was moved to another project: Jira gives it the other
// project's key and keeps its numeric id. Reads by id (the issue GET, the
// bulk fetch) find the issue wherever it is.
type fakeJiraSite struct {
	mu     sync.Mutex
	issues []*fakeJiraSiteIssue
	clock  int64
}

type fakeJiraSiteIssue struct {
	id, key, status, assignee string
	labels                    []string
	updated                   int64
}

var (
	siteProjectRe  = regexp.MustCompile(`project = "([^"]*)"`)
	siteStatusesRe = regexp.MustCompile(`status in \(([^)]*)\)`)
	siteLabelsRe   = regexp.MustCompile(`labels in \(([^)]*)\)`)
	siteAssigneeRe = regexp.MustCompile(`assignee = "([^"]*)"`)
	siteUpdatedRe  = regexp.MustCompile(`updated >= (-?\d+)`)
	siteQuotedRe   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	siteIssuePath  = regexp.MustCompile(`^/rest/api/3/issue/([0-9A-Z-]+)(/comment)?$`)
)

func siteQuotedLower(s string) []string {
	var out []string
	for _, m := range siteQuotedRe.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

func newFakeJiraSite(t *testing.T, issues ...*fakeJiraSiteIssue) (*fakeJiraSite, *httptest.Server) {
	t.Helper()
	f := &fakeJiraSite{issues: issues, clock: time.Now().Add(-time.Hour).UnixMilli()}
	for _, is := range issues {
		is.updated = f.clock
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

// edit changes an issue as a person would in Jira and bumps its update time.
func (f *fakeJiraSite) edit(id string, change func(*fakeJiraSiteIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock += 1000
	for _, is := range f.issues {
		if is.id == id {
			change(is)
			is.updated = f.clock
		}
	}
}

func (f *fakeJiraSite) render(is *fakeJiraSiteIssue) map[string]any {
	labels := []any{}
	for _, l := range is.labels {
		labels = append(labels, l)
	}
	fields := map[string]any{
		"summary": "Summary " + is.key,
		"status":  map[string]any{"name": is.status},
		"labels":  labels,
		"updated": time.UnixMilli(is.updated).UTC().Format("2006-01-02T15:04:05.000-0700"),
	}
	if is.assignee != "" {
		fields["assignee"] = map[string]any{"accountId": is.assignee}
	}
	return map[string]any{"id": is.id, "key": is.key, "fields": fields}
}

func (f *fakeJiraSite) matches(is *fakeJiraSiteIssue, jql string) bool {
	if m := siteProjectRe.FindStringSubmatch(jql); m != nil && !strings.HasPrefix(is.key, m[1]+"-") {
		return false
	}
	if m := siteStatusesRe.FindStringSubmatch(jql); m != nil && !slices.Contains(siteQuotedLower(m[1]), strings.ToLower(is.status)) {
		return false
	}
	if m := siteLabelsRe.FindStringSubmatch(jql); m != nil {
		want := siteQuotedLower(m[1])
		if !slices.ContainsFunc(is.labels, func(l string) bool { return slices.Contains(want, strings.ToLower(l)) }) {
			return false
		}
	}
	if m := siteAssigneeRe.FindStringSubmatch(jql); m != nil && is.assignee != m[1] {
		return false
	}
	if m := siteUpdatedRe.FindStringSubmatch(jql); m != nil {
		since, _ := strconv.ParseInt(m[1], 10, 64)
		if is.updated < since {
			return false
		}
	}
	return true
}

func (f *fakeJiraSite) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/rest/api/3/search/jql":
		var body struct {
			JQL string `json:"jql"`
		}
		_ = json.Unmarshal(raw, &body)
		out := []any{}
		for _, is := range f.issues {
			if f.matches(is, body.JQL) {
				out = append(out, f.render(is))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": out, "isLast": true})
		return
	case "/rest/api/3/issue/bulkfetch":
		var body struct {
			IDs []string `json:"issueIdsOrKeys"`
		}
		_ = json.Unmarshal(raw, &body)
		out := []any{}
		for _, is := range f.issues {
			if slices.Contains(body.IDs, is.id) {
				out = append(out, f.render(is))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": out})
		return
	case "/rest/api/3/comment/list":
		_ = json.NewEncoder(w).Encode(map[string]any{"values": []any{}})
		return
	}
	if m := siteIssuePath.FindStringSubmatch(r.URL.Path); m != nil {
		if m[2] != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []any{}, "total": 0})
			return
		}
		for _, is := range f.issues {
			if is.id == m[1] || is.key == m[1] {
				_ = json.NewEncoder(w).Encode(f.render(is))
				return
			}
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

// TestClaimCheckRefusesAJiraIssueThatLeftTheScope: an issue that left the
// Jira tracker scope after the store's last full pull gets no agent, and an
// issue still in the scope is dispatched as before.
//
// The dispatch claim check (#116) reads the issue by id, unscoped. With the
// store on (the default for Jira), the stale candidate list offers:
//
//   - an issue moved to another Jira project: Jira keeps its numeric id and
//     gives it the other project's key. The incremental pull searches
//     `project = "PROJ"` and never lists it again, so the store keeps it as
//     a candidate until the next full pull (default one hour). The claim
//     read by id finds it under the new key, in a status of the same name
//     ("To Do" is in both default workflows).
//   - an issue reassigned to another person (or with its label removed)
//     since the last pull: the claim read shows the new assignee.
//
// The claim check asks the adapter's tracker.ScopeMatcher about the read,
// as the retry path (fireRetries, #198) does, and refuses both.
func TestClaimCheckRefusesAJiraIssueThatLeftTheScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assignee string
		labels   []string
		leave    func(*fakeJiraSiteIssue)
		// pullAfter runs the store's incremental pulls after the change, so
		// the store has seen every update Jira's search shows.
		pullAfter bool
	}{
		{"moved to another project", "", nil, func(is *fakeJiraSiteIssue) { is.key = "OTHER-9" }, true},
		{"reassigned", "557058:me", nil, func(is *fakeJiraSiteIssue) { is.assignee = "557058:ada" }, false},
		{"label removed", "", []string{"agent"}, func(is *fakeJiraSiteIssue) { is.labels = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := &config.Config{}
			cfg.Tracker.Kind = "jira"
			cfg.Tracker.ActiveStates = []string{"To Do", "In Progress"}
			cfg.Tracker.TerminalStates = []string{"Done"}
			cfg.Agent.MaxConcurrentAgents = 3
			cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
			cfg.Agent.MaxTurns = 1

			site, srv := newFakeJiraSite(t,
				&fakeJiraSiteIssue{id: "10001", key: "PROJ-1", status: "To Do", assignee: "557058:me", labels: []string{"agent"}},
				// PROJ-2 stays in the scope: it is dispatched as before.
				&fakeJiraSiteIssue{id: "10002", key: "PROJ-2", status: "To Do", assignee: "557058:me", labels: []string{"agent"}},
			)
			up := jira.NewClient(jira.ClientConfig{
				Endpoint: srv.URL, APIKey: "me@example.com:tok", ProjectSlug: "PROJ",
				ActiveStates: cfg.Tracker.ActiveStates, Assignee: tc.assignee, Labels: tc.labels,
			})
			interval := time.Hour
			if tc.pullAfter {
				interval = 10 * time.Millisecond
			}
			tr, st, err := store.Wrap(up, store.Config{
				Path: filepath.Join(t.TempDir(), "tracker_store.json"), Scope: "jira|PROJ",
				ActiveStates: cfg.Tracker.ActiveStates, SyncInterval: interval,
				FullSyncInterval: time.Hour, ListingsLagWrites: true,
			})
			require.NoError(t, err)
			_, err = tr.FetchCandidateIssues(ctx) // the store's full pull
			require.NoError(t, err)

			site.edit("10001", tc.leave)
			if tc.pullAfter {
				changedAt := time.Now()
				go st.Run(ctx)
				require.Eventually(t, func() bool {
					s := st.SyncStatus()
					return s.SyncedAt.After(changedAt) && s.LastErr == nil
				}, 5*time.Second, 5*time.Millisecond, "an incremental pull ran after the change")
			}

			// The by-id read the claim check makes, and the adapter's own
			// verdict on it: out of the scope, and known. Read from the
			// adapter, not through the store, which would drop the issue on
			// this read and leave the claim check nothing to refuse.
			fresh, err := up.FetchIssueStatesByIDs(ctx, []string{"10001"})
			require.NoError(t, err)
			require.Len(t, fresh, 1)
			in, known := up.MatchesFilter(fresh[0])
			require.True(t, known)
			require.False(t, in, "precondition: the adapter says %s is outside the scope", fresh[0].Identifier)

			rr := &parkingRunner{}
			o := New(cfg, tr, rr, nil)
			t.Cleanup(func() {
				cancel()
				o.cancelAndCleanupWorker("PROJ-1")
				o.cancelAndCleanupWorker("PROJ-2")
				o.cancelAndCleanupWorker(fresh[0].Identifier)
				o.workersWg.Wait(5 * time.Second)
			})
			state := o.onTick(ctx, NewState(cfg))
			assert.NotContains(t, state.Running, "10001",
				"an agent was started on %s, which the tracker scope (project PROJ, assignee %q, labels %v) no longer covers",
				fresh[0].Identifier, tc.assignee, tc.labels)
			if assert.Contains(t, state.Running, "10002", "an issue still in the scope is dispatched") {
				assert.Equal(t, "PROJ-2", state.Running["10002"].Issue.Identifier)
			}
		})
	}
}
