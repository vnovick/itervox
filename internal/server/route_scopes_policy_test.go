package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// policyWritingRoute reports whether a route can change what governs
// agents: a settings save (any layer, profiles, reviewer, automations,
// tracker states, hosts), a rules layer, a lesson promoted to a rule, a
// task template, a skill fix, or a token. These are admin routes.
func policyWritingRoute(method, pattern string) bool {
	if method == http.MethodGet {
		return false
	}
	for _, p := range []string{"/api/v1/settings/", "/api/v1/rules/", "/promote-to-rule", "/api/v1/tokens",
		"/api/v1/task-templates/", "/api/v1/skills/fix", "/api/v1/tracker/scope", "/api/v1/projects/filter"} {
		if strings.Contains(pattern, p) {
			return true
		}
	}
	return false
}

// TestPolicyWritingRoutesAreAdminOnly: every route that can write policy
// requires admin, and every route an approval or answer travels — the
// dashboard's provide-input and dismiss-input, which the Mac app, the hub
// proxy and the hub's Slack app (hub.MemberActions) also reach — is
// operator. The hub caps every caller at operator on a member and refuses
// admin routes (proxy: admin_route_not_proxied; MemberActions: viewer and
// operator routes only), so no approval channel can reach a policy write.
// The agent's own provide-input is an agent-grant route, which never
// answers a guardrail approval (orchestrator ProvideAgentInput).
func TestPolicyWritingRoutesAreAdminOnly(t *testing.T) {
	n := 0
	for key, access := range routeScopes {
		method, pattern, _ := strings.Cut(key, " ")
		if !policyWritingRoute(method, pattern) {
			continue
		}
		n++
		assert.Equal(t, accessAdmin, access, "%s writes policy: it must be admin", key)
	}
	assert.GreaterOrEqual(t, n, 20, "the policy-writing routes were found")
	for _, key := range []string{
		"POST /api/v1/issues/{identifier}/provide-input",
		"POST /api/v1/issues/{identifier}/dismiss-input",
	} {
		assert.Equal(t, accessOperator, routeScopes[key], key)
	}
	assert.Equal(t, accessAgentGrant, routeScopes["POST /api/v1/agent-actions/{identifier}/provide-input"])
}
