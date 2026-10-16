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

// #182 — the assignee scope, against a fake Linear GraphQL server that
// evaluates the filters it is sent over a small workspace, so a filter that
// is built wrong returns the wrong issues instead of a canned answer.

const (
	meID    = "11111111-1111-4111-8111-111111111111"
	otherID = "22222222-2222-4222-8222-222222222222"
	engID   = "33333333-3333-4333-8333-333333333333"
	opsID   = "44444444-4444-4444-8444-444444444444"
)

type fakeIssue struct {
	id, identifier, state, assignee, teamID, teamKey, project string
	updatedAt                                                 time.Time
}

type fakeUser struct{ id, name, email string }

type fakeLinear struct {
	t      *testing.T
	mu     sync.Mutex
	issues []*fakeIssue
	users  []fakeUser
	viewer string
	ops    []string // operation names, in order
	reqs   []gqlRequest
}

func issueUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// newFakeLinear holds, for the key's own user (me):
//
//	ENG-1 Todo (ENG, project alpha), OPS-2 In Progress (OPS, project beta),
//	ENG-3 Backlog (ENG, no project);
//
// for someone else ENG-4 Todo, and ENG-5 Todo unassigned.
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
			{issueUUID(1), "ENG-1", "Todo", meID, engID, "ENG", "alpha", at},
			{issueUUID(2), "OPS-2", "In Progress", meID, opsID, "OPS", "beta", at},
			{issueUUID(3), "ENG-3", "Backlog", meID, engID, "ENG", "", at},
			{issueUUID(4), "ENG-4", "Todo", otherID, engID, "ENG", "alpha", at},
			{issueUUID(5), "ENG-5", "Todo", "", engID, "ENG", "alpha", at},
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
	f.reqs = append(f.reqs, req)
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
	case "ItervoxLinearPollAssignee", "ItervoxLinearChangesAssignee":
		var nodes []any
		for _, is := range f.issues {
			if !f.matchesAssigneeFilter(is, req) {
				continue
			}
			if names, ok := v["stateNames"].([]any); ok && !slices.Contains(names, any(is.state)) {
				continue
			}
			if since, ok := v["since"].(string); ok {
				s, err := time.Parse(time.RFC3339Nano, since)
				require.NoError(f.t, err)
				if is.updatedAt.Before(s) {
					continue
				}
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

// matchesAssigneeFilter evaluates the assignee and team filter the query
// declares, failing the test if the query has no assignee filter at all.
func (f *fakeLinear) matchesAssigneeFilter(is *fakeIssue, req gqlRequest) bool {
	if !strings.Contains(req.Query, "assignee: {id: {eq: $assigneeId}}") {
		f.t.Errorf("assignee-scoped read without an assignee filter: %s", req.Query)
		return false
	}
	if is.assignee != req.Variables["assigneeId"] {
		return false
	}
	switch team, _ := req.Variables["team"].(string); {
	case strings.Contains(req.Query, "team: {key: {eq: $team}}"):
		return is.teamKey == team
	case strings.Contains(req.Query, "team: {id: {eq: $team}}"):
		return is.teamID == team
	}
	return true
}

func (is *fakeIssue) node() map[string]any {
	n := map[string]any{
		"id": is.id, "identifier": is.identifier, "title": "T " + is.identifier,
		"state":     map[string]any{"name": is.state},
		"team":      map[string]any{"id": is.teamID, "key": is.teamKey},
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

func (f *fakeLinear) listOps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, o := range f.ops {
		if strings.HasPrefix(o, "ItervoxLinearPoll") || strings.HasPrefix(o, "ItervoxLinearChanges") {
			out = append(out, o)
		}
	}
	return out
}

func (f *fakeLinear) reassign(identifier, assignee string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, is := range f.issues {
		if is.identifier == identifier {
			is.assignee, is.updatedAt = assignee, at
		}
	}
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

func assigneeClient(srv *httptest.Server, assignee, team string) *linear.Client {
	return linear.NewClient(linear.ClientConfig{
		APIKey: "k", Endpoint: srv.URL, Assignee: assignee, Team: team,
		ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"},
	})
}

// TestAssigneeScopeFullPoll (#182): the full poll (FetchCandidateIssues, and
// FetchIssuesByStates behind the dependency analyzer and the store's views)
// reads exactly the issues assigned to the scope's user — from every team
// and project, or one team when narrowed — resolving "me" through the
// viewer once.
func TestAssigneeScopeFullPoll(t *testing.T) {
	f, srv := newFakeLinear(t)
	c := assigneeClient(srv, "me", "")

	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "OPS-2"}, idents(got), "active issues assigned to me, across teams")
	got, err = c.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "ENG-3", "OPS-2"}, idents(got), "every issue assigned to me")
	assert.Equal(t, 1, f.opCount("ItervoxAssigneeViewer"), "me resolves once and is cached")
	u, ok := c.ResolvedAssignee()
	require.True(t, ok)
	assert.Equal(t, meID, u.ID)
	assert.Equal(t, "assigned to Itervox Bot", c.ScopeLabel())

	team := assigneeClient(srv, "me", "ENG")
	got, err = team.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "ENG-3"}, idents(got), "team narrows by key")
	byID := assigneeClient(srv, "me", opsID)
	got, err = byID.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Equal(t, []string{"OPS-2"}, idents(got), "team narrows by id")
}

// TestAssigneeScopeIncrementalPull (#182): the incremental pull returns the
// same issue set as the full poll for the same scope.
func TestAssigneeScopeIncrementalPull(t *testing.T) {
	_, srv := newFakeLinear(t)
	since := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, team := range []string{"", "ENG"} {
		c := assigneeClient(srv, "me", team)
		full, err := c.FetchIssuesByStates(t.Context(), allStates)
		require.NoError(t, err)
		changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), since)
		require.NoError(t, err)
		assert.Empty(t, gone)
		assert.Equal(t, idents(full), idents(changed), "team %q: incremental == full", team)
		assert.NotEmpty(t, changed)
	}
}

// TestAssigneeScopeWebhookRefresh (#182): a webhook delivery refreshes the
// store through the scope check by ID, with no list request, and leaves the
// store holding the same issue set the full poll returns — an issue assigned
// to me is placed, one reassigned away is dropped.
func TestAssigneeScopeWebhookRefresh(t *testing.T) {
	f, srv := newFakeLinear(t)
	c := assigneeClient(srv, "me", "")
	wrapped, s, err := store.Wrap(c, store.Config{
		Path: filepath.Join(t.TempDir(), "tracker_store.json"), Scope: "assignee",
		ActiveStates: []string{"Todo", "In Progress"}, Views: [][]string{allStates},
		SyncInterval: time.Hour, NudgeDelay: time.Hour,
	})
	require.NoError(t, err)
	_, scoped := wrapped.(tracker.AssigneeScoper)
	assert.True(t, scoped, "the store forwards the assignee scope")
	h, err := webhook.New(webhook.Config{Tracker: "linear", Secret: "whsec", Store: s})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { h.Run(ctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })

	got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	require.Equal(t, []string{"ENG-1", "ENG-3", "OPS-2"}, idents(got), "the cold pull")
	lists := len(f.listOps())

	// ENG-4 is handed to me, ENG-1 is taken back; deliveries name both,
	// plus an unrelated unassigned issue.
	now := time.Now()
	f.reassign("ENG-4", meID, now)
	f.reassign("ENG-1", otherID, now)
	for _, id := range []string{issueUUID(4), issueUUID(1), issueUUID(5)} {
		require.Equal(t, http.StatusAccepted, signedLinearDelivery(t, h, id, now))
	}
	want := []string{"ENG-3", "ENG-4", "OPS-2"}
	require.Eventually(t, func() bool {
		got, err := wrapped.FetchIssuesByStates(t.Context(), allStates)
		return err == nil && slices.Equal(idents(got), want)
	}, 5*time.Second, 5*time.Millisecond, "the deliveries place ENG-4 and drop ENG-1")
	assert.Len(t, f.listOps(), lists, "refreshed by ID, no list request")

	full, err := assigneeClient(srv, "me", "").FetchIssuesByStates(t.Context(), allStates)
	require.NoError(t, err)
	assert.Equal(t, want, idents(full), "webhook refresh == full poll")
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

	u, err := assigneeClient(srv, "ada@example.com", "").ResolveAssignee(t.Context())
	require.NoError(t, err)
	assert.Equal(t, otherID, u.ID)
	got, err := assigneeClient(srv, "ada@example.com", "").FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-4"}, idents(got))

	u, err = assigneeClient(srv, meID, "").ResolveAssignee(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Itervox Bot", u.Name)

	for _, spec := range []string{"nobody@example.com", "55555555-5555-4555-8555-555555555555"} {
		c := assigneeClient(srv, spec, "")
		_, err := c.ResolveAssignee(t.Context())
		assert.True(t, errors.Is(err, tracker.ErrAssigneeNotFound), "%s: %v", spec, err)
		before := len(f.listOps())
		_, err = c.FetchCandidateIssues(t.Context())
		require.Error(t, err)
		assert.Len(t, f.listOps(), before, "%s: no list request without a resolved user", spec)
	}
}

// TestAssigneeScopeFilterSwitch (#182): the runtime filter's assignee entry
// switches a project-configured client to the assignee scope ("me"), alone,
// and the scope key changes with it.
func TestAssigneeScopeFilterSwitch(t *testing.T) {
	_, srv := newFakeLinear(t)
	c := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL, ProjectSlug: "alpha", ActiveStates: []string{"Todo"}})
	assert.Equal(t, "project alpha", c.ScopeLabel())
	before := c.ScopeKey()
	c.SetProjectFilter([]string{"alpha", tracker.AssigneeScopeFilter})
	assert.Equal(t, []string{tracker.AssigneeScopeFilter}, c.GetProjectFilter(), "never combined with projects")
	assert.Equal(t, "me", c.AssigneeSpec())
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, idents(got))
	assert.NotEqual(t, before, c.ScopeKey())
	assert.Equal(t, "assigned to Itervox Bot", c.ScopeLabel())
}
