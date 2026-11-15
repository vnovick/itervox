// Package apitokens is the dashboard/API token store (#214): named tokens
// with a scope (viewer, operator or admin), kept in .itervox/tokens.json as
// SHA-256 hashes only.
//
// The bootstrap token (ITERVOX_API_TOKEN, the auto-generated ephemeral token,
// or the macOS app's supervisor token) is NOT in this store: it stays a
// process-scoped admin credential the server compares directly. Stored tokens
// add viewer and operator access and admin tokens that can be rotated or
// revoked without restarting the daemon.
//
// The package imports only the standard library and internal/atomicfs.
package apitokens

import (
	"fmt"
	"strings"
)

// Scope is what a token may do. Scopes are ordered: each one includes
// everything the scopes below it allow.
type Scope string

const (
	// ScopeViewer may read state (GET routes and the SSE streams). Secret
	// values are redacted from what it reads.
	ScopeViewer Scope = "viewer"
	// ScopeOperator adds dispatch, steering, input replies, approvals,
	// dispatch pause and resume, and memory edits.
	ScopeOperator Scope = "operator"
	// ScopeAdmin adds settings, token management, tmux control, deploy and
	// the automations configuration. The bootstrap token is always admin.
	ScopeAdmin Scope = "admin"
)

// Scopes lists every valid scope, lowest first.
var Scopes = []Scope{ScopeViewer, ScopeOperator, ScopeAdmin}

// rank orders the scopes; 0 is not a scope.
func (s Scope) rank() int {
	switch s {
	case ScopeViewer:
		return 1
	case ScopeOperator:
		return 2
	case ScopeAdmin:
		return 3
	}
	return 0
}

// Valid reports whether s is one of Scopes.
func (s Scope) Valid() bool { return s.rank() > 0 }

// Allows reports whether a caller holding s may use a route that requires
// required. An invalid scope on either side allows nothing (fail closed).
func (s Scope) Allows(required Scope) bool {
	return s.rank() > 0 && required.rank() > 0 && s.rank() >= required.rank()
}

// ParseScope parses a scope name (case-insensitive, surrounding space
// ignored).
func ParseScope(raw string) (Scope, error) {
	s := Scope(strings.ToLower(strings.TrimSpace(raw)))
	if !s.Valid() {
		return "", fmt.Errorf("apitokens: unknown scope %q (want viewer, operator or admin)", raw)
	}
	return s, nil
}
