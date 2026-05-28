// Package agentactions issues, validates, and revokes short-lived per-run
// action grants used by local agent workers (Claude Code / Codex) to call
// daemon-side action endpoints like `create_issue`. Each grant is scoped to a
// specific issue identifier and run session, carries an allowed-action list,
// and expires after a caller-supplied TTL.
package agentactions

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
	"time"
)

// Grant is the server-side record tied to an action-grant token. It pins the
// token to an issue, run, action whitelist, and optional create-issue target
// state, and enforces a hard expiry.
type Grant struct {
	IssueIdentifier  string
	RunSessionID     string
	AllowedActions   []string
	CreateIssueState string
	MoveIssueState   string
	ExpiresAt        time.Time
}

// Store is an in-memory, concurrency-safe action-grant registry. All access is
// guarded by a single mutex — fine for the expected cardinality (one live grant
// per running worker).
type Store struct {
	mu     sync.Mutex
	grants map[string]Grant
}

// NewStore constructs an empty Store ready for use.
func NewStore() *Store {
	return &Store{
		grants: make(map[string]Grant),
	}
}

// Issue creates a new action grant bound to the given issue and run session.
// allowedActions is normalised (cloned + sorted). If ttl is zero or negative,
// a default of one hour is applied — callers should pass a positive value for
// production use; the fallback is a convenience for tests.
func (s *Store) Issue(issueIdentifier, runSessionID string, allowedActions []string, createIssueState string, ttl time.Duration) (string, error) {
	return s.IssueScoped(issueIdentifier, runSessionID, allowedActions, createIssueState, "", ttl)
}

// IssueScoped creates a new action grant with optional target-state scopes for
// state-mutating actions.
func (s *Store) IssueScoped(issueIdentifier, runSessionID string, allowedActions []string, createIssueState, moveIssueState string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(ttl)
	if ttl <= 0 {
		expiresAt = time.Now().Add(time.Hour)
	}

	actions := slices.Clone(allowedActions)
	slices.Sort(actions)
	s.grants[token] = Grant{
		IssueIdentifier:  issueIdentifier,
		RunSessionID:     runSessionID,
		AllowedActions:   actions,
		CreateIssueState: createIssueState,
		MoveIssueState:   moveIssueState,
		ExpiresAt:        expiresAt,
	}
	return token, nil
}

// Revoke immediately invalidates the given token. No-op if the token is empty,
// the receiver is nil, or the token was never issued.
func (s *Store) Revoke(token string) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	delete(s.grants, token)
	s.mu.Unlock()
}

// Cleanup deletes every grant whose ExpiresAt is at or before `now` and
// returns the number of removed entries. Designed to be driven from a
// periodic ticker so a long-running daemon never accumulates dead tokens
// from workers that never hit the action endpoint.
//
// Opportunistic deletion on Validate already handles tokens the agent
// actually exercised, but the majority of grants never call /agent-actions
// at all (create_issue is rare) so the lifetime-bound here is the only
// thing preventing slow growth. v0.2.0 audit P1-3.
func (s *Store) Cleanup(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for token, grant := range s.grants {
		if !now.Before(grant.ExpiresAt) {
			delete(s.grants, token)
			removed++
		}
	}
	return removed
}

// Validate checks whether the given token authorises the given action against
// the given issue at time now. Returns the Grant on success; on failure the
// second return value carries a short machine-readable reason
// ("missing_token", "unknown_token", "expired_token", "issue_mismatch",
// "action_not_allowed") and the third return is false. Expired tokens are
// deleted opportunistically on read.
func (s *Store) Validate(token, issueIdentifier, action string, now time.Time) (Grant, string, bool) {
	if s == nil || token == "" {
		return Grant{}, "missing_token", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	grant, ok := s.grants[token]
	if !ok {
		return Grant{}, "unknown_token", false
	}
	if now.After(grant.ExpiresAt) {
		delete(s.grants, token)
		return Grant{}, "expired_token", false
	}
	if grant.IssueIdentifier != issueIdentifier {
		return Grant{}, "issue_mismatch", false
	}
	if !slices.Contains(grant.AllowedActions, action) {
		return Grant{}, "action_not_allowed", false
	}
	return grant, "", true
}
