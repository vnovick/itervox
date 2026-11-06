package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// sourceRecordingTracker records the source issue CreateIssue was given.
type sourceRecordingTracker struct {
	*tracker.MemoryTracker
	source string
}

func (s *sourceRecordingTracker) CreateIssue(ctx context.Context, source string, in tracker.NewIssue) (*domain.Issue, error) {
	s.source = source
	return s.MemoryTracker.CreateIssue(ctx, source, in)
}

// TestCreateVerifySkillIssueGoesThroughTracker (#218): "Generate verify
// skill" creates the issue through tracker.CreateIssue in the enforcement
// issue state (#217's path), in the project of a tracked issue.
func TestCreateVerifySkillIssueGoesThroughTracker(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	tr := &sourceRecordingTracker{MemoryTracker: tracker.NewMemoryTracker(
		[]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	o := New(cfg, tr, nil, nil)
	created, err := o.CreateVerifySkillIssue(t.Context())
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, VerifySkillIssueTitle, created.Title)
	assert.Equal(t, "Backlog", created.State, "the enforcement issue state: the first backlog state")
	require.NotNil(t, created.Description)
	assert.Contains(t, *created.Description, ".claude/skills/verify/SKILL.md")
	assert.Equal(t, "id1", tr.source, "created in the project of a tracked issue")

	_, err = New(cfg, nil, nil, nil).CreateVerifySkillIssue(t.Context())
	assert.Error(t, err)
}
