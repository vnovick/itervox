package hub

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/server"
)

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// TestHubApprovalChannelsCannotReachPolicyRoutes: the hub is an approval
// channel (its proxy carries dashboard answers, its Slack app answers
// through MemberActions), and neither can reach a route that writes policy.
// Every admin write route of a real member's route table — settings, rules,
// tokens, profiles, task templates — is refused by the proxy (even for an
// admin hub token) and by MemberActions before any request reaches the
// member; provide-input and dismiss-input go through.
func TestHubApprovalChannelsCannotReachPolicyRoutes(t *testing.T) {
	alpha := newFakeMember(t, "alpha", time.Now().Add(-time.Hour))
	reg := &memberList{}
	reg.add(alpha.rec)
	ch := &fakeInteractive{}
	th := startTestHub(t, Config{Members: reg.list, Channels: []Channel{ch}})
	waitProxied(t, th, "alpha")
	eventually(t, 5*time.Second, func() bool { act, _ := ch.get(); return act != nil }, "the channel runs")
	act, _ := ch.get()
	ref, ok := act.Member("alpha")
	require.True(t, ok)
	base := th.ts.URL + "/hub/p/alpha"

	n := 0
	for key, access := range server.RouteScopes() {
		method, pattern, _ := strings.Cut(key, " ")
		if access != string(apitokens.ScopeAdmin) || method == http.MethodGet || method == "*" {
			continue
		}
		n++
		path := routeParam.ReplaceAllString(pattern, "x1")
		seen := alpha.requests.Load()
		resp := doHub(t, base+path, method, th.secrets[apitokens.ScopeAdmin], strings.NewReader("{}"))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "proxy: %s", key)
		assert.Contains(t, readBody(t, resp), "admin_route_not_proxied", "proxy: %s", key)
		_, _, err := act.Call(t.Context(), ref, method, path, map[string]string{})
		assert.ErrorIs(t, err, ErrMemberActionRefused, "member actions: %s", key)
		assert.Equal(t, seen, alpha.requests.Load(), "refused before reaching the member: %s", key)
	}
	assert.GreaterOrEqual(t, n, 20, "the member's admin write routes were checked")

	for _, path := range []string{"/api/v1/issues/ENG-1/provide-input", "/api/v1/issues/ENG-1/dismiss-input"} {
		_, _, err := act.Call(t.Context(), ref, http.MethodPost, path, map[string]string{"message": "approve"})
		assert.NotErrorIs(t, err, ErrMemberActionRefused, "an answer reaches the member: %s", path)
	}
}
