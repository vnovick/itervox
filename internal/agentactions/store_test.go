package agentactions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueThenValidate_HappyPath(t *testing.T) {
	s := NewStore()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input", "create_issue"}, "Todo", time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	grant, reason, ok := s.Validate(token, "ENG-1", "provide_input", time.Now())
	require.True(t, ok)
	assert.Empty(t, reason)
	assert.Equal(t, "ENG-1", grant.IssueIdentifier)
	assert.Equal(t, "run-1", grant.RunSessionID)
	assert.Equal(t, "Todo", grant.CreateIssueState)
}

func TestIssueScoped_StoresMoveIssueState(t *testing.T) {
	s := NewStore()
	token, err := s.IssueScoped("ENG-1", "run-1", []string{"move_state"}, "", "Todo", time.Hour)
	require.NoError(t, err)

	grant, reason, ok := s.Validate(token, "ENG-1", "move_state", time.Now())
	require.True(t, ok)
	assert.Empty(t, reason)
	assert.Equal(t, "Todo", grant.MoveIssueState)
}

func TestIssue_TTLZero_FallsBackTo1Hour(t *testing.T) {
	s := NewStore()
	start := time.Now()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input"}, "", 0)
	require.NoError(t, err)

	// Valid now, still valid after 59 minutes, invalid after 61.
	_, _, ok := s.Validate(token, "ENG-1", "provide_input", start.Add(59*time.Minute))
	assert.True(t, ok, "grant should still be valid 59 minutes in")
	_, reason, ok := s.Validate(token, "ENG-1", "provide_input", start.Add(61*time.Minute))
	assert.False(t, ok)
	// Expired grants are opportunistically deleted on read; the second read
	// for the same token returns unknown_token, not expired_token.
	_, reason2, _ := s.Validate(token, "ENG-1", "provide_input", start.Add(62*time.Minute))
	assert.Equal(t, "expired_token", reason, "first read after expiry should report expired_token")
	assert.Equal(t, "unknown_token", reason2, "second read should find the token already removed")
}

func TestIssue_TTLNegative_FallsBackTo1Hour(t *testing.T) {
	s := NewStore()
	start := time.Now()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input"}, "", -5*time.Minute)
	require.NoError(t, err)
	_, _, ok := s.Validate(token, "ENG-1", "provide_input", start.Add(30*time.Minute))
	assert.True(t, ok, "negative TTL must use the 1-hour default, not reject immediately")
}

func TestValidate_MissingToken(t *testing.T) {
	s := NewStore()
	_, reason, ok := s.Validate("", "ENG-1", "provide_input", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "missing_token", reason)
}

func TestValidate_UnknownToken(t *testing.T) {
	s := NewStore()
	_, reason, ok := s.Validate("deadbeef", "ENG-1", "provide_input", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "unknown_token", reason)
}

func TestValidate_IssueMismatch(t *testing.T) {
	s := NewStore()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input"}, "", time.Hour)
	require.NoError(t, err)
	_, reason, ok := s.Validate(token, "ENG-2", "provide_input", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "issue_mismatch", reason)
}

func TestValidate_ActionNotAllowed(t *testing.T) {
	s := NewStore()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input"}, "", time.Hour)
	require.NoError(t, err)
	_, reason, ok := s.Validate(token, "ENG-1", "create_issue", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "action_not_allowed", reason)
}

func TestRevoke_RemovesGrant(t *testing.T) {
	s := NewStore()
	token, err := s.Issue("ENG-1", "run-1", []string{"provide_input"}, "", time.Hour)
	require.NoError(t, err)
	s.Revoke(token)
	_, reason, ok := s.Validate(token, "ENG-1", "provide_input", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "unknown_token", reason)
}

func TestRevoke_NilReceiver_IsNoop(t *testing.T) {
	// Explicit nil-receiver guard in Revoke — exercises the defensive path.
	var s *Store
	assert.NotPanics(t, func() { s.Revoke("anything") })
}

func TestRevoke_EmptyToken_IsNoop(t *testing.T) {
	s := NewStore()
	assert.NotPanics(t, func() { s.Revoke("") })
}

func TestValidate_NilReceiver_ReturnsMissingToken(t *testing.T) {
	var s *Store
	_, reason, ok := s.Validate("something", "ENG-1", "provide_input", time.Now())
	assert.False(t, ok)
	assert.Equal(t, "missing_token", reason)
}

func TestIssue_AllowedActionsAreClonedAndSorted(t *testing.T) {
	s := NewStore()
	input := []string{"provide_input", "create_issue"}
	token, err := s.Issue("ENG-1", "run-1", input, "", time.Hour)
	require.NoError(t, err)

	// Mutate the caller's slice — the grant's copy must be unaffected.
	input[0] = "TAMPERED"

	grant, _, ok := s.Validate(token, "ENG-1", "create_issue", time.Now())
	require.True(t, ok)
	// Sorted ascending: "create_issue" < "provide_input".
	assert.Equal(t, []string{"create_issue", "provide_input"}, grant.AllowedActions)
}

// v0.2.0 audit P1-3 — Cleanup must drop expired tokens regardless of whether
// Validate has been called on them. Without this janitor, grants issued to
// workers that never hit the action endpoint accumulate forever.
func TestCleanupRemovesExpiredGrants(t *testing.T) {
	s := NewStore()
	now := time.Now()

	// Three grants: two short-TTL (will expire), one long-TTL (must survive
	// the Cleanup window).
	expiredA, err := s.Issue("ENG-1", "run-1", []string{"comment"}, "", time.Minute)
	require.NoError(t, err)
	expiredB, err := s.Issue("ENG-2", "run-2", []string{"comment"}, "", 5*time.Minute)
	require.NoError(t, err)
	live, err := s.Issue("ENG-3", "run-3", []string{"comment"}, "", 24*time.Hour)
	require.NoError(t, err)

	// Advance past the short-TTL grants but before the live one's expiry.
	advanced := now.Add(time.Hour)
	removed := s.Cleanup(advanced)
	assert.Equal(t, 2, removed, "exactly the two expired grants should be removed")

	// Both expired tokens must now resolve to unknown_token (deleted), and the
	// live token must still validate successfully.
	_, reasonA, okA := s.Validate(expiredA, "ENG-1", "comment", now)
	assert.False(t, okA)
	assert.Equal(t, "unknown_token", reasonA)

	_, reasonB, okB := s.Validate(expiredB, "ENG-2", "comment", now)
	assert.False(t, okB)
	assert.Equal(t, "unknown_token", reasonB)

	_, _, okLive := s.Validate(live, "ENG-3", "comment", now)
	assert.True(t, okLive)
}

func TestCleanupOnNilStoreIsSafe(t *testing.T) {
	var s *Store
	assert.Equal(t, 0, s.Cleanup(time.Now()))
}
