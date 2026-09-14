package linear_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// ---------------------------------------------------------------------------
// Workflow-state resolution cache
//
// UpdateIssueState used to cost two requests every time: resolve the state
// name to a UUID, then apply it. These tests pin the request COUNT, not just
// the outcome — the whole point of the cache is the request it does not send,
// and an assertion on the returned error alone cannot see that.
// ---------------------------------------------------------------------------

// stateOpServer serves the two operations UpdateIssueState issues, counts each,
// and records the stateId carried by every mutation.
type stateOpServer struct {
	*httptest.Server

	mu        sync.Mutex
	resolves  int
	updates   int
	mutatedTo []string
}

func (s *stateOpServer) counts() (resolves, updates int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolves, s.updates
}

// stateIDs returns the stateId of each mutation, in order. This is what
// catches a cache that returns a stale or wrong-name UUID — a count-only
// assertion would pass while every transition moved the issue to one state.
func (s *stateOpServer) stateIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mutatedTo...)
}

// newStateOpServer builds the server. resolve and update receive the 1-based
// index of the call, so a test can change Linear's answer between calls.
func newStateOpServer(
	t *testing.T,
	resolve func(n int) map[string]any,
	update func(n int) map[string]any,
) *stateOpServer {
	t.Helper()
	s := &stateOpServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		var resp map[string]any
		matched := true
		s.mu.Lock()
		switch {
		case strings.Contains(string(raw), "ItervoxResolveStateId"):
			s.resolves++
			resp = resolve(s.resolves)
		case strings.Contains(string(raw), "ItervoxUpdateIssueState"):
			s.updates++
			s.mutatedTo = append(s.mutatedTo, stateIDOf(raw))
			resp = update(s.updates)
		default:
			matched = false
		}
		s.mu.Unlock()

		if !matched {
			t.Errorf("unexpected request body: %s", raw)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// A matched handler returning nil simulates a transport-level failure
		// (HTTP 500), as distinct from a well-formed success:false response.
		if resp == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.Close)
	return s
}

// stateIDOf pulls variables.stateId out of a GraphQL request body.
func stateIDOf(raw []byte) string {
	var req struct {
		Variables struct {
			StateID string `json:"stateId"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return ""
	}
	return req.Variables.StateID
}

// resolveBody renders the issue -> team -> states response shape.
func resolveBody(teamID string, states map[string]string) map[string]any {
	nodes := make([]any, 0, len(states))
	for name, id := range states {
		nodes = append(nodes, map[string]any{"id": id, "name": name})
	}
	return map[string]any{"data": map[string]any{
		"issue": map[string]any{"team": map[string]any{
			"id":     teamID,
			"states": map[string]any{"nodes": nodes},
		}},
	}}
}

func updateBody(success bool) map[string]any {
	return map[string]any{"data": map[string]any{
		"issueUpdate": map[string]any{"success": success},
	}}
}

func stateCacheClient(t *testing.T, srv *stateOpServer) *linear.Client {
	t.Helper()
	return linear.NewClient(linear.ClientConfig{APIKey: "test-key", Endpoint: srv.URL})
}

// A repeated transition on the same issue must not re-resolve. The second
// assertion matters as much as the first: caching must not swallow mutations.
func TestUpdateIssueStateResolvesOnceForRepeatedTransitions(t *testing.T) {
	srv := newStateOpServer(t,
		func(int) map[string]any {
			return resolveBody("team-1", map[string]string{
				"In Progress": "s-prog",
				"Done":        "s-done",
			})
		},
		func(int) map[string]any { return updateBody(true) },
	)
	client := stateCacheClient(t, srv)
	ctx := context.Background()

	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "In Progress"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "In Progress"))

	resolves, updates := srv.counts()
	assert.Equal(t, 1, resolves, "the team state map should be resolved once and reused")
	assert.Equal(t, 3, updates, "every transition must still issue its own mutation")

	// A second state name is served from the same cached map — the old
	// name-filtered query could only ever answer for one name.
	assert.Equal(t, []string{"s-prog", "s-done", "s-prog"}, srv.stateIDs())
}

// The cache is keyed by issue, so a first transition on a new issue still
// costs a resolve even when its team is already known. Pinned deliberately:
// this is the limit of the optimisation, not an accident.
func TestUpdateIssueStateResolvesPerIssueEvenWithinOneTeam(t *testing.T) {
	srv := newStateOpServer(t,
		func(int) map[string]any {
			return resolveBody("team-1", map[string]string{"Done": "s-done"})
		},
		func(int) map[string]any { return updateBody(true) },
	)
	client := stateCacheClient(t, srv)
	ctx := context.Background()

	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-2", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-2", "Done"))

	resolves, updates := srv.counts()
	assert.Equal(t, 2, resolves, "one resolve per first-touched issue")
	assert.Equal(t, 3, updates)
}

// A state added or renamed in Linear after the map was cached must be picked
// up rather than failing "state not found" for the life of the process.
func TestUpdateIssueStateRefetchesWhenStateNameNotCached(t *testing.T) {
	srv := newStateOpServer(t,
		func(n int) map[string]any {
			states := map[string]string{"Done": "s-done"}
			if n > 1 {
				states["In Review"] = "s-review" // added in Linear after the first resolve
			}
			return resolveBody("team-1", states)
		},
		func(int) map[string]any { return updateBody(true) },
	)
	client := stateCacheClient(t, srv)
	ctx := context.Background()

	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "In Review"))

	resolves, _ := srv.counts()
	assert.Equal(t, 2, resolves, "an uncached state name must trigger a refetch")
	assert.Equal(t, []string{"s-done", "s-review"}, srv.stateIDs())
}

// A state name Linear genuinely does not have must still error — the refetch
// path must not turn a real misconfiguration into an infinite retry.
func TestUpdateIssueStateStillErrorsOnUnknownState(t *testing.T) {
	srv := newStateOpServer(t,
		func(int) map[string]any {
			return resolveBody("team-1", map[string]string{"Done": "s-done"})
		},
		func(int) map[string]any { return updateBody(true) },
	)
	client := stateCacheClient(t, srv)

	err := client.UpdateIssueState(context.Background(), "issue-1", "Nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `state "Nonexistent" not found`)

	_, updates := srv.counts()
	assert.Zero(t, updates, "an unresolvable state must not reach the mutation")
}

// If a cached UUID is rejected, the cache must be dropped so the retry path
// re-resolves. Without this a state deleted and recreated in Linear would make
// the daemon replay a dead UUID on every retry of the same transition.
func TestUpdateIssueStateInvalidatesCacheAfterFailedMutation(t *testing.T) {
	srv := newStateOpServer(t,
		func(n int) map[string]any {
			id := "s-done-v1"
			if n > 1 {
				id = "s-done-v2" // recreated in Linear under a new UUID
			}
			return resolveBody("team-1", map[string]string{"Done": id})
		},
		// Only the second mutation fails: 1 warms the cache, 2 rejects the
		// cached UUID, 3 must succeed against a freshly resolved one.
		func(n int) map[string]any { return updateBody(n != 2) },
	)
	client := stateCacheClient(t, srv)
	ctx := context.Background()

	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.Error(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))

	resolves, updates := srv.counts()
	assert.Equal(t, 2, resolves, "the rejected UUID must force a re-resolve")
	assert.Equal(t, 3, updates)
	assert.Equal(t, []string{"s-done-v1", "s-done-v1", "s-done-v2"}, srv.stateIDs())
}

// Same invalidation contract as above, but for the transport-error branch —
// the mutation dying with an HTTP error rather than a well-formed
// success:false. Both arms must drop the cache; this pins the second one.
func TestUpdateIssueStateInvalidatesCacheAfterTransportError(t *testing.T) {
	srv := newStateOpServer(t,
		func(n int) map[string]any {
			id := "s-done-v1"
			if n > 1 {
				id = "s-done-v2"
			}
			return resolveBody("team-1", map[string]string{"Done": id})
		},
		// Mutation 2 fails at the HTTP layer; 1 warms the cache, 3 must run
		// against a freshly resolved UUID.
		func(n int) map[string]any {
			if n == 2 {
				return nil // HTTP 500
			}
			return updateBody(true)
		},
	)
	client := stateCacheClient(t, srv)
	ctx := context.Background()

	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.Error(t, client.UpdateIssueState(ctx, "issue-1", "Done"))
	require.NoError(t, client.UpdateIssueState(ctx, "issue-1", "Done"))

	resolves, updates := srv.counts()
	assert.Equal(t, 2, resolves, "a transport error on a cached UUID must force a re-resolve")
	assert.Equal(t, 3, updates)
	assert.Equal(t, []string{"s-done-v1", "s-done-v1", "s-done-v2"}, srv.stateIDs())
}

// The cache is read and written from the orchestrator's worker goroutines.
func TestUpdateIssueStateCacheIsConcurrencySafe(t *testing.T) {
	srv := newStateOpServer(t,
		func(int) map[string]any {
			return resolveBody("team-1", map[string]string{"Done": "s-done"})
		},
		func(int) map[string]any { return updateBody(true) },
	)
	client := stateCacheClient(t, srv)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			// Half contend on one issue (cache hits), half on distinct issues
			// (concurrent writes) — exercising both sides of the mutex.
			issue := "issue-shared"
			if i%2 == 0 {
				issue = fmt.Sprintf("issue-%d", i)
			}
			assert.NoError(t, client.UpdateIssueState(context.Background(), issue, "Done"))
		})
	}
	wg.Wait()

	_, updates := srv.counts()
	assert.Equal(t, 20, updates)
}
