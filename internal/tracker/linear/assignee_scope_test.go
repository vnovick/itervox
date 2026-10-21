package linear_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/tracker/store"
	"github.com/vnovick/itervox/internal/tracker/webhook"
)

// #182 — the combined scope (project AND assignee AND any-of labels), against
// a fake Linear GraphQL server that evaluates the filters it is sent over a
// small workspace, so a filter built wrong returns the wrong issues instead
// of a canned answer.

const (
	meID    = "11111111-1111-4111-8111-111111111111"
	otherID = "22222222-2222-4222-8222-222222222222"
	engID   = "33333333-3333-4333-8333-333333333333"
	opsID   = "44444444-4444-4444-8444-444444444444"
)

type fakeIssue struct {
	id, identifier, state, assignee, teamID, teamKey, project string
	labels                                                    []string
	updatedAt                                                 time.Time
}

type fakeUser struct{ id, name, email string }

type fakeLinear struct {
	t      *testing.T
	mu     sync.Mutex
	issues []*fakeIssue
	users  []fakeUser
	viewer string
	hidden bool // list reads return nothing (a cold store pull before the issues exist)
	ops    []string
}

func issueUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// newFakeLinear holds:
//
//	ENG-1 Todo        me      ENG  alpha       [agent]
//	OPS-2 In Progress me      OPS  beta        []
//	ENG-3 Backlog     me      ENG  (none)      [agent, bug]
//	ENG-4 Todo        other   ENG  alpha       [agent]
//	ENG-5 Todo        nobody  ENG  alpha       [bug]
func newFakeLinear(t *testing.T) (*fakeLinear, *httptest.Server) {
	t.Helper()
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	f := &fakeLinear{
		t:      t,
		viewer: meID,
		users: []fakeUser{
			{meID, "Itervox Bot", "bot@example.com"},
			{otherID, "Ada Lovelace", "ada@example.com"},
		},
		issues: []*fakeIssue{
			{issueUUID(1), "ENG-1", "Todo", meID, engID, "ENG", "alpha", []string{"agent"}, at},
			{issueUUID(2), "OPS-2", "In Progress", meID, opsID, "OPS", "beta", nil, at},
			{issueUUID(3), "ENG-3", "Backlog", meID, engID, "ENG", "", []string{"agent", "bug"}, at},
			{issueUUID(4), "ENG-4", "Todo", otherID, engID, "ENG", "alpha", []string{"agent"}, at},
			{issueUUID(5), "ENG-5", "Todo", "", engID, "ENG", "alpha", []string{"bug"}, at},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

var opNameRe = regexp.MustCompile(`^\s*(?:query|mutation)\s+(\w+)`)

func (f *fakeLinear) serve(w http.ResponseWriter, r *http.Request) {
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m := opNameRe.FindStringSubmatch(req.Query)
	if m == nil {
		f.t.Errorf("no operation name in %q", req.Query)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, m[1])
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.answer(m[1], req))
}

func (f *fakeLinear) user(match func(fakeUser) bool) map[string]any {
	for _, u := range f.users {
		if match(u) {
			return map[string]any{"id": u.id, "name": u.name, "email": u.email}
		}
	}
	return nil
}

func (f *fakeLinear) answer(op string, req gqlRequest) map[string]any {
	v := req.Variables
	switch op {
	case "ItervoxAssigneeViewer":
		return map[string]any{"data": map[string]any{"viewer": f.user(func(u fakeUser) bool { return u.id == f.viewer })}}
	case "ItervoxAssigneeByEmail":
		var nodes []any
		if u := f.user(func(u fakeUser) bool { return u.email == v["email"] }); u != nil {
			nodes = append(nodes, u)
		}
		return map[string]any{"data": map[string]any{"users": map[string]any{"nodes": nodes}}}
	case "ItervoxAssigneeById":
		if u := f.user(func(u fakeUser) bool { return u.id == v["id"] }); u != nil {
			return map[string]any{"data": map[string]any{"user": u}}
		}
		return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "Entity not found: User"}}}
	case "ItervoxLinearPollScoped", "ItervoxLinearChangesScoped", "ItervoxLinearPoll", "ItervoxLinearPollAll", "ItervoxLinearChanges", "ItervoxLinearChangesAll":
		nodes := []any{}
		for _, is := range f.issues {
			if f.hidden || !f.matches(is, req) {
				continue
			}
			nodes = append(nodes, is.node())
		}
		return map[string]any{"data": map[string]any{"issues": map[string]any{
			"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false},
		}}}
	case "ItervoxLinearIssuesInScope":
		ids, _ := v["ids"].([]any)
		var nodes []any
		for _, is := range f.issues {
			if slices.Contains(ids, any(is.id)) {
				nodes = append(nodes, is.node())
			}
		}
		return map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": nodes}}}
	}
	f.t.Errorf("unexpected operation %s", op)
	return map[string]any{"errors": []any{map[string]any{"message": "unexpected operation " + op}}}
}

// matches evaluates every filter fragment the query declares.
func (f *fakeLinear) matches(is *fakeIssue, req gqlRequest) bool {
	q, v := req.Query, req.Variables
	if strings.Contains(q, "project: {slugId: {eq: $projectSlug}}") && is.project != v["projectSlug"] {
		return false
	}
	if strings.Contains(q, "project: {null: true}") && is.project != "" {
		return false
	}
	if strings.Contains(q, "assignee: {id: {eq: $assigneeId}}") && is.assignee != v["assigneeId"] {
		return false
	}
	team, _ := v["team"].(string)
	if strings.Contains(q, "team: {key: {eq: $team}}") && is.teamKey != team {
		return false
	}
	if strings.Contains(q, "team: {id: {eq: $team}}") && is.teamID != team {
		return false
	}
	if strings.Contains(q, "labels: {some: {name: {in: $labels}}}") {
		want, _ := v["labels"].([]any)
		if !slices.ContainsFunc(is.labels, func(l string) bool { return slices.Contains(want, any(l)) }) {
			return false
		}
	}
	if names, ok := v["stateNames"].([]any); ok && !slices.Contains(names, any(is.state)) {
		return false
	}
	if since, ok := v["since"].(string); ok {
		s, err := time.Parse(time.RFC3339Nano, since)
		require.NoError(f.t, err)
		if is.updatedAt.Before(s) {
			return false
		}
	}
	return true
}

func (is *fakeIssue) node() map[string]any {
	labels := make([]any, 0, len(is.labels))
	for _, l := range is.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	n := map[string]any{
		"id": is.id, "identifier": is.identifier, "title": "T " + is.identifier,
		"state":     map[string]any{"name": is.state},
		"team":      map[string]any{"id": is.teamID, "key": is.teamKey},
		"labels":    map[string]any{"nodes": labels},
		"updatedAt": is.updatedAt.Format(time.RFC3339Nano),
		"createdAt": is.updatedAt.Add(-time.Hour).Format(time.RFC3339Nano),
		"assignee":  nil,
		"project":   nil,
	}
	if is.assignee != "" {
		n["assignee"] = map[string]any{"id": is.assignee}
	}
	if is.project != "" {
		n["project"] = map[string]any{"slugId": is.project}
	}
	return n
}

func (f *fakeLinear) opCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.ops {
		if o == op {
			n++
		}
	}
	return n
}

func (f *fakeLinear) listOps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.ops {
		if strings.HasPrefix(o, "ItervoxLinearPoll") || strings.HasPrefix(o, "ItervoxLinearChanges") {
			n++
		}
	}
	return n
}

func (f *fakeLinear) change(identifier string, mutate func(*fakeIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, is := range f.issues {
		if is.identifier == identifier {
			mutate(is)
			is.updatedAt = time.Now()
		}
	}
}

func (f *fakeLinear) setHidden(h bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidden = h
}

func idents(issues []domain.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Identifier)
	}
	slices.Sort(out)
	return out
}

var allStates = []string{"Todo", "In Progress", "Backlog", "Done"}

type scopeCase struct {
	name     string
	project  string
	assignee string
	team     string
	labels   []string
	filter   []string // runtime project filter (nil: project above)
	want     []string // every state
}

// scopeCases are the combinations every read path must agree on.
var scopeCases = []scopeCase{
	{name: "assignee only", assignee: "me", want: []string{"ENG-1", "ENG-3", "OPS-2"}},
	{name: "assignee in team", assignee: "me", team: "ENG", want: []string{"ENG-1", "ENG-3"}},
	{name: "assignee in team by id", assignee: "me", team: opsID, want: []string{"OPS-2"}},
	{name: "project and assignee", project: "alpha", assignee: "me", want: []string{"ENG-1"}},
	{name: "projects and assignee", filter: []string{"alpha", "__no_project__"}, assignee: "me", want: []string{"ENG-1", "ENG-3"}},
	{name: "persisted project list and assignee", project: "alpha, beta", assignee: "me", want: []string{"ENG-1", "OPS-2"}},
	{name: "assignee and labels", assignee: "me", labels: []string{"agent"}, want: []string{"ENG-1", "ENG-3"}},
	{name: "project and labels", project: "alpha", labels: []string{"agent"}, want: []string{"ENG-1", "ENG-4"}},
	{name: "labels any-of", labels: []string{"bug", "nope"}, want: []string{"ENG-3", "ENG-5"}},
	{name: "project, assignee and labels", project: "alpha", assignee: "ada@example.com", labels: []string{"agent"}, want: []string{"ENG-4"}},
	{name: "project only (unchanged path)", project: "alpha", want: []string{"ENG-1", "ENG-4", "ENG-5"}},
}

func (sc scopeCase) client(srv *httptest.Server) *linear.Client {
	c := linear.NewClient(linear.ClientConfig{
		APIKey: "k", Endpoint: srv.URL, ProjectSlug: sc.project, Assignee: sc.assignee, Team: sc.team, Labels: sc.labels,
		ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"},
	})
	if sc.filter != nil {
		c.SetProjectFilter(sc.filter)
	}
	return c
}

// TestScopeFullPoll (#182): the full poll (FetchIssuesByStates, behind the
// store's views and the dependency analyzer; FetchCandidateIssues for the
// active states) returns the intersection of project, assignee and labels.
func TestScopeFullPoll(t *testing.T) {
	f, srv := newFakeLinear(t)
	for _, tc := range scopeCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.client(srv).FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			assert.Equal(t, tc.want, idents(got))
		})
	}
	c := scopeCases[0].client(srv)
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "OPS-2"}, idents(got), "the active states only")
	before := f.opCount("ItervoxAssigneeViewer")
	_, err = c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, f.opCount("ItervoxAssigneeViewer"), "me resolves once and is cached")
}

// TestScopeIncrementalPull (#182): the incremental pull returns the same
// issue set as the full poll for every combination.
func TestScopeIncrementalPull(t *testing.T) {
	_, srv := newFakeLinear(t)
	since := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, tc := range scopeCases {
		t.Run(tc.name, func(t *testing.T) {
			changed, gone, err := tc.client(srv).FetchIssuesUpdatedSince(t.Context(), since)
			require.NoError(t, err)
			assert.Empty(t, gone)
			assert.Equal(t, tc.want, idents(changed))
		})
	}
}

// hookedStore wraps c in a store fed by a webhook handler.
func hookedStore(t *testing.T, c *linear.Client) (tracker.Tracker, *webhook.Handler) {
	t.Helper()
	wrapped, s, err := store.Wrap(c, store.Config{
		Path: filepath.Join(t.TempDir(), "tracker_store.json"), Scope: "scoped",
		ActiveStates: []string{"Todo", "In Progress"}, Views: [][]string{allStates},
		SyncInterval: time.Hour, NudgeDelay: time.Hour,
	})
	require.NoError(t, err)
	_, scoped := wrapped.(tracker.ScopeManager)
	require.True(t, scoped, "the store forwards the scope")
	h, err := webhook.New(webhook.Config{Tracker: "linear", Secret: "whsec", Store: s})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { h.Run(ctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })
	return wrapped, h
}

func deliverAll(t *testing.T, h http.Handler, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.Equal(t, http.StatusAccepted, signedLinearDelivery(t, h, id, time.Now()))
	}
}

// TestScopeWebhookRefresh (#182): webhook deliveries refresh the store
// through the by-ID scope check, with no list request, and leave it holding
// the same issue set the full poll returns, for every combination.
func TestScopeWebhookRefresh(t *testing.T) {
	f, srv := newFakeLinear(t)
	all := []string{issueUUID(1), issueUUID(2), issueUUID(3), issueUUID(4), issueUUID(5)}
	for _, tc := range scopeCases {
		t.Run(tc.name, func(t *testing.T) {
			f.setHidden(true) // the cold pull sees nothing; only deliveries place issues
			wrapped, h := hookedStore(t, tc.client(srv))
			got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			require.Empty(t, got)
			f.setHidden(false)
			lists := f.listOps()
			deliverAll(t, h, all...)
			var last []string
			require.Eventually(t, func() bool {
				got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
				last = idents(got)
				return err == nil && slices.Equal(last, tc.want)
			}, 5*time.Second, 5*time.Millisecond, "webhook refresh == full poll: want %v, last %v", tc.want, &last)
			assert.Equal(t, lists, f.listOps(), "refreshed by ID, no list request")
		})
	}
}

// TestScopeWebhookDropsIssuesThatLeave (#182): with project AND assignee AND
// labels, an issue is dropped from the store when any one part stops
// matching — reassigned, its label removed, or moved to another project
// while still assigned.
func TestScopeWebhookDropsIssuesThatLeave(t *testing.T) {
	for _, leave := range []struct {
		name   string
		mutate func(*fakeIssue)
	}{
		{"reassigned", func(is *fakeIssue) { is.assignee = otherID }},
		{"label removed", func(is *fakeIssue) { is.labels = nil }},
		{"project moved while assigned", func(is *fakeIssue) { is.project = "beta" }},
	} {
		t.Run(leave.name, func(t *testing.T) {
			f, srv := newFakeLinear(t)
			sc := scopeCase{project: "alpha", assignee: "me", labels: []string{"agent"}}
			wrapped, h := hookedStore(t, sc.client(srv))
			got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
			require.NoError(t, err)
			require.Equal(t, []string{"ENG-1"}, idents(got))
			f.change("ENG-1", leave.mutate)
			deliverAll(t, h, issueUUID(1))
			require.Eventually(t, func() bool {
				got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
				return err == nil && len(got) == 0
			}, 5*time.Second, 5*time.Millisecond, "ENG-1 left the scope")
		})
	}
}

func signedLinearDelivery(t *testing.T, h http.Handler, id string, at time.Time) int {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"action":"update","type":"Issue","data":{"id":%q},"webhookTimestamp":%d}`, id, at.UnixMilli()))
	mac := hmac.New(sha256.New, []byte("whsec"))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear", bytes.NewReader(body))
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestAssigneeResolvesEmailAndID (#182): an email resolves through
// users(filter: {email}), anything else as a user id; a user nobody can see
// is tracker.ErrAssigneeNotFound, and a read in that scope fails rather than
// widening to every issue.
func TestAssigneeResolvesEmailAndID(t *testing.T) {
	f, srv := newFakeLinear(t)

	u, err := (scopeCase{assignee: "ada@example.com"}).client(srv).ResolveAssignee(t.Context())
	require.NoError(t, err)
	assert.Equal(t, otherID, u.ID)
	u, err = (scopeCase{assignee: meID}).client(srv).ResolveAssignee(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Itervox Bot", u.Name)

	for _, spec := range []string{"nobody@example.com", "55555555-5555-4555-8555-555555555555"} {
		c := (scopeCase{assignee: spec}).client(srv)
		_, err := c.ResolveAssignee(t.Context())
		assert.True(t, errors.Is(err, tracker.ErrAssigneeNotFound), "%s: %v", spec, err)
		before := f.listOps()
		_, err = c.FetchCandidateIssues(t.Context())
		require.Error(t, err)
		assert.Equal(t, before, f.listOps(), "%s: no list request without a resolved user", spec)
	}
}

// TestScopeSetAndLabel (#182): SetScope replaces project, assignee and
// labels at runtime; SetProjectFilter changes only the project part; the
// key and the header label follow.
func TestScopeSetAndLabel(t *testing.T) {
	_, srv := newFakeLinear(t)
	c := (scopeCase{project: "alpha"}).client(srv)
	assert.Equal(t, "alpha", c.ScopeLabel())
	before := c.ScopeKey()

	c.SetScope(tracker.Scope{Projects: []string{"alpha"}, Assignee: "me", Labels: []string{"agent"}})
	assert.NotEqual(t, before, c.ScopeKey())
	assert.Equal(t, "alpha · assigned to me · label: agent", c.ScopeLabel())
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, idents(got))

	c.SetProjectFilter([]string{})
	assert.Equal(t, tracker.Scope{Projects: []string{}, Assignee: "me", Labels: []string{"agent"}}, c.Scope(), "the project part alone changed")
	assert.Equal(t, "all projects · assigned to me · label: agent", c.ScopeLabel())

	c.SetScope(tracker.Scope{Assignee: "ada@example.com", Labels: []string{"agent", "bug"}})
	_, err = c.ResolveAssignee(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "all projects · assigned to Ada Lovelace · labels: any of agent, bug", c.ScopeLabel())
}
