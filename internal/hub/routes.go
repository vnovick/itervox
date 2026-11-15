package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/apitokens"
)

// RouteScopesPath is the member route that lists its route scope table
// (internal/server route_scopes.go), a viewer route on every daemon.
const RouteScopesPath = "/api/v1/route-scopes"

// RouteScopesResponse is the body of GET /api/v1/route-scopes: every route
// as "METHOD /full/chi/pattern" → "viewer" | "operator" | "admin" |
// "public" | "agent_grant".
type RouteScopesResponse struct {
	Routes map[string]string `json:"routes"`
}

// Access classes of a member route that are not token scopes.
const (
	accessPublic     = "public"
	accessAgentGrant = "agent_grant"
)

// routeEntry is one parsed route of a member's table.
type routeEntry struct {
	method string
	segs   []string
	access string
}

// routeTable is a member's route scope table.
type routeTable struct {
	routes []routeEntry
}

var errEmptyRouteTable = errors.New("hub: the member's route scope table is empty")

// parseRouteTable reads a GET /api/v1/route-scopes body. Only /api/v1/
// routes are kept: the proxy serves nothing else.
func parseRouteTable(body []byte) (*routeTable, error) {
	var resp RouteScopesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("hub: route scope table: %w", err)
	}
	t := &routeTable{}
	for key, access := range resp.Routes {
		method, pattern, ok := strings.Cut(key, " ")
		if !ok || method == "*" || !strings.HasPrefix(pattern, "/api/v1/") || strings.Contains(pattern, "*") {
			continue
		}
		t.routes = append(t.routes, routeEntry{method: method, segs: strings.Split(pattern, "/"), access: access})
	}
	if len(t.routes) == 0 {
		return nil, errEmptyRouteTable
	}
	return t, nil
}

// accessRank orders access classes for "the most restrictive match wins":
// an unknown class or agent_grant outranks every scope, so any doubt about
// a path refuses it.
func accessRank(access string) int {
	switch access {
	case accessPublic:
		return 0
	case string(apitokens.ScopeViewer):
		return 1
	case string(apitokens.ScopeOperator):
		return 2
	case string(apitokens.ScopeAdmin):
		return 3
	}
	return 4
}

// match returns the access the member declares for method+path. Every
// pattern segment "{param}" matches exactly one non-empty path segment.
// When several patterns match (a literal and a parameter route), the most
// restrictive access wins. ok is false when nothing matches.
func (t *routeTable) match(method, path string) (access string, ok bool) {
	segs := strings.Split(path, "/")
	for _, r := range t.routes {
		if r.method != method || len(r.segs) != len(segs) {
			continue
		}
		matched := true
		for i, s := range r.segs {
			if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
				if segs[i] == "" {
					matched = false
					break
				}
				continue
			}
			if s != segs[i] {
				matched = false
				break
			}
		}
		if matched && (!ok || accessRank(r.access) > accessRank(access)) {
			access, ok = r.access, true
		}
	}
	return access, ok
}
