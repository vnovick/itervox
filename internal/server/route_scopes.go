package server

import "github.com/vnovick/itervox/internal/apitokens"

// routeAccess is what a route requires: one of the token scopes, or one of
// the two access classes that never pass through scopeMiddleware.
type routeAccess string

const (
	accessViewer   = routeAccess(apitokens.ScopeViewer)
	accessOperator = routeAccess(apitokens.ScopeOperator)
	accessAdmin    = routeAccess(apitokens.ScopeAdmin)
	// accessPublic: no bearer token — probes, the SPA shell, and tracker
	// webhooks (which carry the webhook secret's HMAC instead).
	accessPublic routeAccess = "public"
	// accessAgentGrant: the per-run agent-action / guardrail grant the
	// handler validates itself (internal/agentactions).
	accessAgentGrant routeAccess = "agent_grant"
)

// anyMethod keys a route registered with Handle (every method).
const anyMethod = "*"

// routeScopes is THE scope declaration of every route (#214), keyed by
// "METHOD pattern" with the full chi pattern. scopeMiddleware consults it on
// every authenticated request and refuses a route missing from it;
// TestEveryRouteDeclaresAScope walks the real router and fails CI for a
// route registered without a line here (and for a stale line).
//
// Rules of thumb: viewer is GET and SSE only (secrets redacted); operator
// adds dispatch, steering, input replies, approvals, pause/resume, memory
// edits and log clearing; admin adds settings, tokens, tmux control,
// workspace deletion and the automations configuration.
var routeScopes = map[string]routeAccess{
	// Outside the bearer group.
	"GET /api/v1/health":              accessPublic,
	"GET /api/v1/ready":               accessPublic,
	"* /*":                            accessPublic, // the embedded SPA shell and its assets
	"POST /api/v1/webhooks/{tracker}": accessPublic,
	"POST /api/v1/agent-actions/{identifier}/comment":                 accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/comment_pr":              accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/merge_pr":                accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/create-issue":            accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/move-state":              accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/provide-input":           accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/guardrail/events":        accessAgentGrant,
	"POST /api/v1/agent-actions/{identifier}/guardrail/approvals":     accessAgentGrant,
	"GET /api/v1/agent-actions/{identifier}/guardrail/approvals/{id}": accessAgentGrant,

	// Viewer: reads and streams.
	"GET /metrics":                                  accessViewer,
	"GET /api/v1/whoami":                            accessViewer,
	"GET /api/v1/state":                             accessViewer,
	"GET /api/v1/summary":                           accessViewer,
	"GET /api/v1/feed":                              accessViewer,
	"GET /api/v1/events":                            accessViewer,
	"GET /api/v1/history":                           accessViewer,
	"GET /api/v1/history/prompt-versions":           accessViewer,
	"GET /api/v1/repo/readiness":                    accessViewer,
	"GET /api/v1/repo/evals":                        accessViewer,
	"GET /api/v1/repo/hotspots":                     accessViewer,
	"GET /api/v1/sessions":                          accessViewer,
	"GET /api/v1/sessions/{id}/transcript":          accessViewer,
	"GET /api/v1/issues":                            accessViewer,
	"GET /api/v1/issues/{identifier}":               accessViewer,
	"GET /api/v1/issues/{identifier}/logs":          accessViewer,
	"GET /api/v1/issues/{identifier}/log-stream":    accessViewer,
	"GET /api/v1/issues/{identifier}/sublogs":       accessViewer,
	"GET /api/v1/issues/{identifier}/sublog-stream": accessViewer,
	"GET /api/v1/logs/identifiers":                  accessViewer,
	"GET /api/v1/logs":                              accessViewer,
	"GET /api/v1/projects":                          accessViewer,
	"GET /api/v1/projects/filter":                   accessViewer,
	"GET /api/v1/tracker/scope":                     accessViewer,
	"GET /api/v1/tracker/labels":                    accessViewer,
	"GET /api/v1/tracker/users":                     accessViewer,
	"GET /api/v1/tracker/teams":                     accessViewer,
	"GET /api/v1/settings/models":                   accessViewer,
	"GET /api/v1/settings/reviewer":                 accessViewer,
	"GET /api/v1/settings/profiles":                 accessViewer, // commands redacted below admin
	"GET /api/v1/settings/schema":                   accessViewer,
	"GET /api/v1/settings/layers":                   accessViewer, // raw files and secrets redacted below admin
	"GET /api/v1/deps/analyze/{jobId}":              accessViewer,
	"GET /api/v1/rules":                             accessViewer,
	"GET /api/v1/rules/preview":                     accessViewer,
	"GET /api/v1/memory":                            accessViewer,
	"GET /api/v1/skills/inventory":                  accessViewer,
	"GET /api/v1/skills/issues":                     accessViewer,
	"GET /api/v1/skills/analytics":                  accessViewer,
	"GET /api/v1/skills/analytics/recommendations":  accessViewer,
	"GET /api/v1/skills/profile-backends":           accessViewer,

	// Operator: run the fleet.
	"GET /api/v1/sessions/{id}/screen":                       accessOperator, // a live terminal pane
	"POST /api/v1/sessions/{id}/message":                     accessOperator, // steering
	"POST /api/v1/dispatch/pause":                            accessOperator,
	"POST /api/v1/dispatch/resume":                           accessOperator,
	"POST /api/v1/refresh":                                   accessOperator,
	"POST /api/v1/client-errors":                             accessOperator,
	"DELETE /api/v1/issues/{identifier}":                     accessOperator,
	"POST /api/v1/issues/{identifier}/cancel":                accessOperator,
	"POST /api/v1/issues/{identifier}/resume":                accessOperator,
	"POST /api/v1/issues/{identifier}/reanalyze":             accessOperator,
	"POST /api/v1/issues/{identifier}/terminate":             accessOperator,
	"POST /api/v1/issues/{identifier}/ai-review":             accessOperator,
	"PATCH /api/v1/issues/{identifier}/state":                accessOperator,
	"POST /api/v1/issues/{identifier}/profile":               accessOperator,
	"POST /api/v1/issues/{identifier}/backend":               accessOperator,
	"POST /api/v1/issues/{identifier}/provide-input":         accessOperator,
	"POST /api/v1/issues/{identifier}/dismiss-input":         accessOperator,
	"POST /api/v1/issues/{identifier}/comment":               accessOperator,
	"POST /api/v1/issues/{identifier}/deps-override":         accessOperator,
	"DELETE /api/v1/issues/{identifier}/deps-override":       accessOperator,
	"POST /api/v1/issues/{identifier}/failures/ack":          accessOperator,
	"DELETE /api/v1/issues/{identifier}/logs":                accessOperator,
	"DELETE /api/v1/issues/{identifier}/sublogs":             accessOperator,
	"DELETE /api/v1/issues/{identifier}/sublogs/{sessionId}": accessOperator,
	"DELETE /api/v1/logs":                                    accessOperator,
	"POST /api/v1/backend-health/clear":                      accessOperator,
	"POST /api/v1/outbox/{id}/retry":                         accessOperator,
	"DELETE /api/v1/outbox/{id}":                             accessOperator,
	"POST /api/v1/tracker/filter/preview":                    accessOperator, // read-only, but a POST
	"POST /api/v1/deps/analyze":                              accessOperator,
	"DELETE /api/v1/deps/analyze/{jobId}":                    accessOperator,
	"POST /api/v1/memory":                                    accessOperator,
	"PUT /api/v1/memory/{id}":                                accessOperator,
	"DELETE /api/v1/memory/{id}":                             accessOperator,
	"POST /api/v1/memory/{id}/pin":                           accessOperator,
	"POST /api/v1/memory/{id}/promote":                       accessOperator,
	"POST /api/v1/memory/{id}/approve":                       accessOperator,
	"POST /api/v1/memory/{id}/reject":                        accessOperator,
	"POST /api/v1/skills/scan":                               accessOperator,
	"POST /api/v1/skills/check-refs":                         accessOperator, // read-only, but a POST

	// Admin: configuration, tokens, tmux control, destructive maintenance.
	"POST /api/v1/sessions":                                        accessAdmin, // starts a tmux session
	"DELETE /api/v1/sessions/{id}":                                 accessAdmin,
	"POST /api/v1/sessions/{id}/keys":                              accessAdmin,
	"GET /api/v1/tokens":                                           accessAdmin,
	"POST /api/v1/tokens":                                          accessAdmin,
	"DELETE /api/v1/tokens/{id}":                                   accessAdmin,
	"POST /api/v1/tokens/{id}/rotate":                              accessAdmin,
	"PUT /api/v1/projects/filter":                                  accessAdmin,
	"PUT /api/v1/tracker/scope":                                    accessAdmin,
	"DELETE /api/v1/workspaces":                                    accessAdmin,
	"POST /api/v1/settings/inline-input":                           accessAdmin,
	"POST /api/v1/settings/workers":                                accessAdmin,
	"POST /api/v1/settings/workspace/auto-clear":                   accessAdmin,
	"POST /api/v1/settings/deps-analysis-mode":                     accessAdmin,
	"POST /api/v1/settings/models/refresh":                         accessAdmin,
	"PUT /api/v1/settings/reviewer":                                accessAdmin,
	"PUT /api/v1/settings/profiles/{name}":                         accessAdmin,
	"DELETE /api/v1/settings/profiles/{name}":                      accessAdmin,
	"PUT /api/v1/settings/automations":                             accessAdmin,
	"POST /api/v1/automations/{id}/test":                           accessAdmin,
	"PUT /api/v1/settings/tracker/states":                          accessAdmin,
	"PUT /api/v1/settings/tracker/failed-state":                    accessAdmin,
	"PUT /api/v1/settings/agent/max-retries":                       accessAdmin,
	"PUT /api/v1/settings/agent/max-switches-per-issue-per-window": accessAdmin,
	"PUT /api/v1/settings/agent/switch-window-hours":               accessAdmin,
	"POST /api/v1/settings/ssh-hosts":                              accessAdmin,
	"DELETE /api/v1/settings/ssh-hosts/{host}":                     accessAdmin,
	"PUT /api/v1/settings/dispatch-strategy":                       accessAdmin,
	"PUT /api/v1/settings/layers/{layer}":                          accessAdmin,
	"PUT /api/v1/rules/{layer}":                                    accessAdmin,
	"POST /api/v1/skills/fix":                                      accessAdmin, // writes skill files
}

// scopedRouteScope returns the token scope the route method+pattern
// requires. declared is false for a route missing from routeScopes, and for
// a public or agent-grant route (which never belongs behind the bearer
// group): both are refused by scopeMiddleware.
func scopedRouteScope(method, pattern string) (apitokens.Scope, bool) {
	access, ok := routeScopes[method+" "+pattern]
	if !ok {
		return "", false
	}
	scope := apitokens.Scope(access)
	if !scope.Valid() {
		return "", false
	}
	return scope, true
}
