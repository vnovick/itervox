package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestInputRequiredCommentBodyIsBounded (CORE-123): the explicit
// result.InputRequired path hands the agent's text through verbatim, and the
// question is a never-give-up outbox entry at the head of the issue's FIFO.
// A body the tracker rejects as too large (GitHub: 65,536 chars → 422) would
// block every later write for the issue, so the body is bounded here.
func TestInputRequiredCommentBodyIsBounded(t *testing.T) {
	// Multi-byte runes with an odd-length ASCII prefix, so a naive byte cut
	// lands mid-rune.
	huge := "x" + strings.Repeat("é", 50*1024) // ~100 KiB
	entry := &InputRequiredEntry{Context: huge + "\nWhich file should I edit?"}

	for _, inline := range []bool{false, true} {
		body := buildInputRequiredComment(entry, inline)
		assert.Less(t, len(body), 9*1024, "body must be bounded, got %d bytes", len(body))
		assert.True(t, strings.HasPrefix(body, itervoxCommentPrefix), "header kept")
		assert.True(t, strings.HasSuffix(body, "to continue._"), "footer kept")
		assert.True(t, utf8.ValidString(body), "cut on a rune boundary")
		assert.Contains(t, body, "Which file should I edit?", "the tail — where the question is — survives")
		assert.Contains(t, body, "Itervox dashboard", "points at the full text")
		assert.True(t, tracker.IsManagedComment(domain.Comment{Body: tracker.MarkManagedComment(body)}), "still a managed comment")
	}

	short := &InputRequiredEntry{Context: "Which file?"}
	assert.Equal(t,
		itervoxCommentPrefix+"\n\nWhich file?\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._",
		buildInputRequiredComment(short, false), "a short context is untouched")
}

// warnRecorder captures slog records for one test. Tests using it do not
// call t.Parallel.
type warnRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *warnRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *warnRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		r.mu.Lock()
		r.msgs = append(r.msgs, rec.Message)
		r.mu.Unlock()
	}
	return nil
}
func (r *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *warnRecorder) WithGroup(string) slog.Handler      { return r }

func (r *warnRecorder) count(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

// TestInputRequiredQuestionStuckLogsWarnOnceWhenDegraded (CORE-123): a
// question the tracker keeps rejecting (locked or deleted issue) sits at the
// head of the issue's outbox FIFO, blocking the reply and the completion
// transition behind it. The wait used to log only at Debug on every tick;
// once the entry is Degraded the operator must hear about it — once, not per
// tick — so they can discard it from the Outbox panel.
func TestInputRequiredQuestionStuckLogsWarnOnceWhenDegraded(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	orch, ob, state, issue := inputRequiredHarness(t)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	queued := ob.Snapshot()
	require.Len(t, queued, 1)
	for range 5 {
		ob.MarkFailed(queued[0].ID, errors.New("github_create_comment: status 403"), time.Now())
	}
	require.True(t, ob.Snapshot()[0].Degraded(), "precondition: the question's entry is degraded")

	for range 3 {
		state = orch.checkTrackerReplies(context.Background(), state)
	}

	assert.Equal(t, 1, rec.count("input-required question is stuck in the outbox"),
		"exactly one Warn across three degraded ticks")
	assert.Contains(t, state.InputRequiredIssues, "ENG-1", "still waiting")
}
