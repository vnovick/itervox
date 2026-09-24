package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

// ---------------------------------------------------------------------------
// Blocker-state TTL cache
//
// populateBlockerStates used to issue one uncached REST GET per unique
// blocker per poll. The widened phrase matcher (depends on/requires/waiting
// on...) multiplies the referenced-blocker set, which at typical board sizes
// and the 30s default poll interval approaches GitHub's 5,000 req/hr budget.
// These tests pin the request COUNT, not just the outcome — the whole point
// of the cache is the request it does not send.
// ---------------------------------------------------------------------------

// blockerServer serves a single blocked issue (referencing blockerID) plus
// the blocker issue itself, and counts GETs to the blocker endpoint.
//
// blockerState holds the DOMAIN state the blocker should resolve to
// ("in progress" or "closed" in these tests) — not the raw GitHub open/closed
// field. deriveState (normalize.go) computes the domain state from the raw
// "state" field plus labels: an open issue needs a matching active-state
// label, a closed issue falls back to the first configured terminal state.
// ghIssueForDomainState translates a domain state into the raw
// state+labels combination that produces it, matching defaultConfig's
// ActiveStates (["todo","in progress"]) / TerminalStates (["closed"]).
type blockerServer struct {
	*httptest.Server
	blockerCalls  atomic.Int32
	blockerStatus atomic.Int32 // HTTP status for the blocker endpoint; 0 => 200
	blockerState  atomic.Value // domain state string, e.g. "in progress" or "closed"
}

func ghIssueForDomainState(number int, title, domainState string) map[string]interface{} {
	if domainState == "closed" {
		return ghIssue(number, title, "closed", nil)
	}
	return ghIssue(number, title, "open", []string{domainState})
}

func newBlockerServer(t *testing.T, blockedNumber int, blockerNumber int, body string) *blockerServer {
	t.Helper()
	s := &blockerServer{}
	s.blockerState.Store("in progress")

	var listCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1)%2 == 1 {
			issue := ghIssue(blockedNumber, "Blocked issue", "open", []string{"todo"})
			issue["body"] = body
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	blockerPath := "/repos/owner/repo/issues/" + strconv.Itoa(blockerNumber)
	mux.HandleFunc(blockerPath, func(w http.ResponseWriter, r *http.Request) {
		s.blockerCalls.Add(1)
		status := int(s.blockerStatus.Load())
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssueForDomainState(blockerNumber, "Blocker", s.blockerState.Load().(string)))
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Two populate calls (via two FetchCandidateIssues polls) within the TTL for
// the same blocker must issue exactly ONE GET against GitHub.
func TestPopulateBlockerStatesCachesAcrossCalls(t *testing.T) {
	srv := newBlockerServer(t, 1, 10, "Blocked by #10")
	client := ghclient.NewClient(defaultConfig(srv.URL))
	ctx := context.Background()

	issues1, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues1, 1)
	require.Len(t, issues1[0].BlockedBy, 1)
	require.NotNil(t, issues1[0].BlockedBy[0].State)
	assert.Equal(t, "in progress", *issues1[0].BlockedBy[0].State)

	issues2, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues2, 1)
	require.Len(t, issues2[0].BlockedBy, 1)
	require.NotNil(t, issues2[0].BlockedBy[0].State, "cached read must still populate state")
	assert.Equal(t, "in progress", *issues2[0].BlockedBy[0].State)

	assert.Equal(t, int32(1), srv.blockerCalls.Load(),
		"second populate within TTL must be served from cache — one GET total")
}

// Past the TTL, the cache entry must be treated as stale and re-fetched.
func TestPopulateBlockerStatesRefetchesPastTTL(t *testing.T) {
	srv := newBlockerServer(t, 1, 10, "Blocked by #10")
	client := ghclient.NewClient(defaultConfig(srv.URL))
	ctx := context.Background()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client.SetNow(func() time.Time { return now })

	issues1, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues1[0].BlockedBy, 1)
	assert.Equal(t, int32(1), srv.blockerCalls.Load())

	// Still within TTL: no second GET.
	now = now.Add(4 * time.Minute)
	_, err = client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), srv.blockerCalls.Load(), "within TTL must not re-fetch")

	// Past TTL (5 minutes): must re-fetch.
	now = now.Add(2 * time.Minute)
	srv.blockerState.Store("closed")
	issues3, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues3[0].BlockedBy, 1)
	require.NotNil(t, issues3[0].BlockedBy[0].State)
	assert.Equal(t, "closed", *issues3[0].BlockedBy[0].State,
		"past TTL, the refreshed state must be observed")
	assert.Equal(t, int32(2), srv.blockerCalls.Load(), "past TTL must re-fetch exactly once more")
}

// A failed GET must NOT be cached: the next populate call must re-fetch
// rather than reusing (or pinning) the failure for the TTL window.
func TestPopulateBlockerStatesFailureNotCached(t *testing.T) {
	srv := newBlockerServer(t, 1, 10, "Blocked by #10")
	srv.blockerStatus.Store(http.StatusInternalServerError)
	client := ghclient.NewClient(defaultConfig(srv.URL))
	ctx := context.Background()

	issues1, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues1[0].BlockedBy, 1)
	assert.Nil(t, issues1[0].BlockedBy[0].State, "failed fetch must leave state unknown")
	assert.Equal(t, int32(1), srv.blockerCalls.Load())

	// Recover the blocker endpoint and populate again — a cached failure
	// would suppress this fetch for the whole TTL; it must not.
	srv.blockerStatus.Store(0)
	issues2, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues2[0].BlockedBy, 1)
	require.NotNil(t, issues2[0].BlockedBy[0].State, "failure must not be cached — retry must resolve")
	assert.Equal(t, "in progress", *issues2[0].BlockedBy[0].State)
	assert.Equal(t, int32(2), srv.blockerCalls.Load(), "a failed fetch must re-hit the blocker endpoint next poll")
}

// A SUCCESSFUL fetch of an open blocker with no active/terminal label
// resolves to domain state "" (deriveState's untriaged-issue case) — the
// exact same empty string a FAILED fetch produces. A cache-write gate that
// keys on "state != """ (rather than fetch success) cannot tell these two
// cases apart and treats the successful-but-unresolved case as uncacheable,
// re-fetching it on every single poll. This is the COMMON case for
// "depends on #N" phrase references — an untriaged prerequisite — so it is
// exactly the population the TTL cache exists to stop re-hitting GitHub for.
func TestPopulateBlockerStatesCachesOpenUnlabeledBlocker(t *testing.T) {
	var blockerCalls atomic.Int32
	var listCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1)%2 == 1 {
			issue := ghIssue(1, "Blocked issue", "open", []string{"todo"})
			issue["body"] = "Blocked by #10"
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	mux.HandleFunc("/repos/owner/repo/issues/10", func(w http.ResponseWriter, r *http.Request) {
		blockerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Open, but no active/terminal label -> deriveState returns "" even
		// though the GET itself succeeded (200, well-formed issue body).
		_ = json.NewEncoder(w).Encode(ghIssue(10, "Untriaged blocker", "open", []string{"needs-triage"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	ctx := context.Background()

	issues1, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues1, 1)
	require.Len(t, issues1[0].BlockedBy, 1)
	assert.Nil(t, issues1[0].BlockedBy[0].State,
		"an untriaged blocker (no active/terminal label) has no resolvable state")

	issues2, err := client.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, issues2, 1)
	require.Len(t, issues2[0].BlockedBy, 1)
	assert.Nil(t, issues2[0].BlockedBy[0].State,
		"second poll within TTL must still report no resolvable state (served from cache)")

	assert.Equal(t, int32(1), blockerCalls.Load(),
		"a successfully fetched but state-unresolved blocker must be cached — one GET across two polls within TTL")
}
