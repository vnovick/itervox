package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// gatedCommentTracker holds its keyless CreateComment until release closes,
// so a test can place the fallback's completion after shutdown.
type gatedCommentTracker struct {
	*tracker.MemoryTracker
	release chan struct{}
}

func (g gatedCommentTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	<-g.release
	return g.MemoryTracker.CreateComment(ctx, issueID, body)
}

// TestQuestionFallbackAtShutdownCorrectsLedger pins BH5. A keyless fallback
// post that succeeds after Run's context is done used to race a send into the
// (buffered, unread) event channel against shutdown.Done(): half the time the
// result vanished into the buffer with nothing logged, and either way the
// persisted entry kept a key the real comment never carries, so after a
// restart no tracker reply could ever match the question. The result must
// not be sent to the dead loop, and the ledger must end up matching the
// posted comment by id.
func TestQuestionFallbackAtShutdownCorrectsLedger(t *testing.T) {
	for i := range 20 { // the pre-fix select was random: repeat to make it deterministic
		issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
		cfg := testConfig()
		mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
		release := make(chan struct{})
		orch, _, state := brokenOutboxHarness(t, gatedCommentTracker{mt, release})
		ledger := filepath.Join(t.TempDir(), "input_required.json")
		orch.SetInputRequiredFile(ledger)
		ctx, cancel := context.WithCancel(context.Background())
		orch.runCtx.Store(&ctx)
		state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

		state = orch.handleEvent(ctx, state, exitNeedingInput(issue))
		key := state.InputRequiredIssues["ENG-1"].QuestionCommentKey
		require.NotEmpty(t, key)
		orch.storeSnap(state) // the loop's last ledger version: entry keeps its key
		cancel()              // shutdown: the loop stops reading events
		close(release)        // the fallback post now succeeds
		orch.commentWg.Wait()

		assert.Zero(t, len(orch.events), "iteration %d: a result sent after shutdown sits unread in the buffer", i)
		detail, err := mt.FetchIssueDetail(context.Background(), issue.ID)
		require.NoError(t, err)
		require.Len(t, detail.Comments, 1)

		// A fresh generation loading the ledger must see the posted comment's
		// id, not the key no tracker comment carries.
		next := New(cfg, mt, &blockedRunner{}, nil)
		next.SetInputRequiredFile(ledger)
		loaded := next.loadInputRequiredFromDisk(NewState(cfg)).InputRequiredIssues["ENG-1"]
		require.NotNil(t, loaded)
		require.Empty(t, loaded.QuestionCommentKey, "iteration %d: ledger still carries the key after a successful post", i)
		require.Equal(t, detail.Comments[0].ID, loaded.QuestionCommentID)
	}
}
