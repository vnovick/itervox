package agentactions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentActionsStoreCleanupRemovesExpired — todolist4 P1-3 named test.
// The behaviour is already exercised by TestCleanupRemovesExpiredGrants; this
// is the literally-named regression guard the acceptance demanded.
func TestAgentActionsStoreCleanupRemovesExpired(t *testing.T) {
	s := NewStore()
	now := time.Now()

	short, err := s.Issue("ENG-1", "run-1", []string{"comment"}, "", time.Minute)
	require.NoError(t, err)
	long, err := s.Issue("ENG-2", "run-2", []string{"comment"}, "", 24*time.Hour)
	require.NoError(t, err)

	removed := s.Cleanup(now.Add(time.Hour))
	assert.Equal(t, 1, removed)

	_, _, okShort := s.Validate(short, "ENG-1", "comment", now)
	assert.False(t, okShort)
	_, _, okLong := s.Validate(long, "ENG-2", "comment", now)
	assert.True(t, okLong)
}
