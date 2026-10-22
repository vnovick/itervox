package hub

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// waitProxied waits until the hub lists name as proxied.
func waitProxied(t *testing.T, th *testHub, name string) {
	t.Helper()
	eventually(t, 5*time.Second, func() bool {
		m, ok := th.h.members.get(name)
		return ok && m.routes != nil && m.health == healthOK
	}, "member "+name+" proxied")
}

// TestHubProxyEnforcesHubTokenScopeAndInjectsMemberToken (#228): the hub
// checks the hub token's scope against the member's own route table before
// anything reaches the member, never forwards the caller's Authorization,
// injects its stored member token of the caller's (capped) scope, refuses
// what must not be proxied, and never turns a member 401 into a dashboard
// logout.
func TestHubProxyEnforcesHubTokenScopeAndInjectsMemberToken(t *testing.T) {
	alpha := newFakeMember(t, "alpha", time.Now().Add(-time.Hour))
	reg := &memberList{}
	reg.add(alpha.rec)
	th := startTestHub(t, Config{Members: reg.list})
	waitProxied(t, th, "alpha")
	base := th.ts.URL + "/hub/p/alpha"

	// No hub token: 401 before the member is involved.
	before := alpha.auth()
	seen := alpha.requests.Load()
	resp := doHub(t, base+"/api/v1/state", http.MethodGet, "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	// A member token is not a hub token.
	resp = doHub(t, base+"/api/v1/state", http.MethodGet, memberBootstrap, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, before, alpha.auth(), "refused requests never reach the member")
	assert.Equal(t, seen, alpha.requests.Load(), "refused requests never reach the member")

	// Viewer reads; the member sees the hub's viewer token, never the hub token.
	resp = doHub(t, base+"/api/v1/whoami", http.MethodGet, th.secrets[apitokens.ScopeViewer], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	type principal struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
	}
	var who struct{ principal }
	who = struct{ principal }{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&who.principal))
	assert.Equal(t, "viewer", who.Scope)
	assert.Equal(t, "hub-viewer", who.Name)
	assert.NotContains(t, alpha.auth(), th.secrets[apitokens.ScopeViewer], "the caller's token is never forwarded")
	assert.True(t, strings.HasPrefix(alpha.auth(), "Bearer "+apitokens.SecretPrefix), "a stored member token is injected")

	// Viewer cannot pause: 403 from the hub itself, naming the scope; the
	// request never reaches the member.
	seen = alpha.requests.Load()
	resp = doHub(t, base+"/api/v1/dispatch/pause", http.MethodPost, th.secrets[apitokens.ScopeViewer], nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "operator", resp.Header.Get(RequiredScopeHeader))
	assert.Contains(t, readBody(t, resp), "insufficient_scope")
	assert.Zero(t, alpha.disp.pauses.Load())
	assert.Equal(t, seen, alpha.requests.Load(), "the hub refuses before proxying")

	// Operator pauses, with the hub's operator token on the member.
	resp = doHub(t, base+"/api/v1/dispatch/pause", http.MethodPost, th.secrets[apitokens.ScopeOperator], nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	assert.Equal(t, int32(1), alpha.disp.pauses.Load())
	resp = doHub(t, base+"/api/v1/whoami", http.MethodGet, th.secrets[apitokens.ScopeOperator], nil)
	who = struct{ principal }{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&who.principal))
	assert.Equal(t, "operator", who.Scope)

	// An admin hub token is capped at operator on a member: member admin
	// routes are refused by the hub.
	resp = doHub(t, base+"/api/v1/tokens", http.MethodGet, th.secrets[apitokens.ScopeAdmin], nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "admin_route_not_proxied")
	resp = doHub(t, base+"/api/v1/whoami", http.MethodGet, th.secrets[apitokens.ScopeAdmin], nil)
	who = struct{ principal }{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&who.principal))
	assert.Equal(t, "operator", who.Scope, "admin hub tokens use the operator member token")

	// Never proxied: agent grants and tracker webhooks; unknown routes and
	// projects; paths that could change which route matches.
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/agent-actions/ENG-1/comment", http.StatusForbidden, "not_proxied"},
		{http.MethodPost, "/api/v1/webhooks/linear", http.StatusForbidden, "not_proxied"},
		{http.MethodGet, "/api/v1/no-such-route", http.StatusNotFound, "route_not_found"},
		{http.MethodGet, "/api/v1/issues/a%2Fcancel", http.StatusBadRequest, "bad_path"},
		{http.MethodGet, "/api/v2/state", http.StatusBadRequest, "bad_path"},
	} {
		resp = doHub(t, base+tc.path, tc.method, th.secrets[apitokens.ScopeAdmin], strings.NewReader("{}"))
		assert.Equal(t, tc.status, resp.StatusCode, "%s %s", tc.method, tc.path)
		assert.Contains(t, readBody(t, resp), tc.code, "%s %s", tc.method, tc.path)
	}
	resp = doHub(t, th.ts.URL+"/hub/p/nope/api/v1/state", http.MethodGet, th.secrets[apitokens.ScopeAdmin], nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The member revokes the hub's operator token: 502 (not 401, which
	// would log the dashboard out), then the hub replaces its token.
	tokens, err := alpha.store.List()
	require.NoError(t, err)
	for _, tok := range tokens {
		if tok.Name == "hub-operator" {
			_, err := alpha.store.Revoke(tok.ID, "test")
			require.NoError(t, err)
		}
	}
	time.Sleep(300 * time.Millisecond) // the member re-reads tokens.json within 200 ms
	resp = doHub(t, base+"/api/v1/whoami", http.MethodGet, th.secrets[apitokens.ScopeOperator], nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	eventually(t, 3*time.Second, func() bool {
		r := doHub(t, base+"/api/v1/whoami", http.MethodGet, th.secrets[apitokens.ScopeOperator], nil)
		return r.StatusCode == http.StatusOK
	}, "the hub recovers with a new member token")
}

// TestHubProxyPassesSSE (#228): a member's SSE stream reaches the client
// through the proxy event by event (no buffering until the stream ends).
func TestHubProxyPassesSSE(t *testing.T) {
	alpha := newFakeMember(t, "alpha", time.Now().Add(-time.Hour))
	reg := &memberList{}
	reg.add(alpha.rec)
	th := startTestHub(t, Config{Members: reg.list})
	waitProxied(t, th, "alpha")

	resp := doHub(t, th.ts.URL+"/hub/p/alpha/api/v1/events", http.MethodGet, th.secrets[apitokens.ScopeViewer], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	nextData := func() string {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				require.True(t, ok, "stream ended")
				if strings.HasPrefix(l, "data:") {
					return l
				}
			case <-timeout:
				t.Fatal("no SSE event through the proxy within 3s")
			}
		}
	}
	snapshotEvent := func() {
		t.Helper()
		for !strings.Contains(nextData(), `"projectName":"alpha"`) {
		}
	}
	snapshotEvent() // the member's snapshot arrives while the stream stays open
	alpha.srv.Notify()
	snapshotEvent() // a later event is flushed through as it happens
}
