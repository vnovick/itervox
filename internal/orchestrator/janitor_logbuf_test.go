package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-035: logBuf.Remove ran only on the SUCCESS path, so failed, stalled,
// cancelled and input-required issues kept their in-memory ring (up to 500
// lines x 64 KiB) for the rest of the generation. The janitor now frees the
// ring for issues that are no longer tracked (terminal, or absent from two
// consecutive polls) unless they are running, paused, awaiting input or have
// a pending input resume. The on-disk log is untouched: Remove never drops a
// line already queued for the disk writer.
func TestJanitorEvictsLogBufferForTerminalIssue(t *testing.T) {
	cfg := automationBaseCfg()
	active := domain.Issue{ID: "id-active", Identifier: "ENG-ACTIVE", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{active}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.DryRun = true
	buf := logbuffer.New()
	logDir := t.TempDir()
	buf.SetLogDir(logDir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	o.SetLogBuffer(buf)

	state := NewState(cfg)
	add := func(ident string) {
		for i := 0; i < 3; i++ {
			buf.Add(ident, "time=t level=ERROR msg=\"worker: failed\" ident="+ident)
		}
	}
	// Failed, now terminal and idle: evict.
	add("ENG-FAILED")
	state.PrevIssueStates["ENG-FAILED"] = "Cancelled"
	// Absent from two consecutive polls (deleted issue), idle: evict.
	add("ENG-GONE")
	state.PrevActiveIdentifiers = map[string]struct{}{"ENG-ACTIVE": {}}
	// Still tracked: keep.
	add("ENG-ACTIVE")

	state = o.onTick(context.Background(), state)

	resident := map[string]bool{}
	for _, id := range buf.ResidentIdentifiers() {
		resident[id] = true
	}
	assert.False(t, resident["ENG-FAILED"], "terminal non-success issue's ring must be freed")
	assert.False(t, resident["ENG-GONE"], "absent issue's ring must be freed")
	assert.True(t, resident["ENG-ACTIVE"], "tracked issue keeps its ring")

	// Freeing the ring lost nothing on disk: the lines queued before the
	// eviction still land, and Get serves them from the file.
	require.NoError(t, buf.Flush(context.Background()))
	data, err := os.ReadFile(filepath.Join(logDir, "ENG-FAILED.log"))
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(data), "worker: failed"))
	assert.Len(t, buf.Get("ENG-FAILED"), 3, "evicted logs remain readable from disk")
}

// The eviction never frees a ring that may still be appended to or inspected
// live, even for an untracked identifier: running, retry pending, paused,
// awaiting input, or with a pending input resume.
func TestEvictIdleLogBuffersSkipsLiveIssues(t *testing.T) {
	cfg := automationBaseCfg()
	o := New(cfg, nil, nil, nil)
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	o.SetLogBuffer(buf)
	state := NewState(cfg)
	for _, ident := range []string{"RUN", "RETRY", "PAUSED", "INPUT", "RESUME", "IDLE"} {
		buf.Add(ident, "line")
	}
	state.Running["id-run"] = &RunEntry{Issue: domain.Issue{ID: "id-run", Identifier: "RUN"}}
	state.RetryAttempts["id-retry"] = &RetryEntry{IssueID: "id-retry", Identifier: "RETRY"}
	state.PausedIdentifiers["PAUSED"] = "id-paused"
	state.InputRequiredIssues["INPUT"] = &InputRequiredEntry{Identifier: "INPUT"}
	state.PendingInputResumes["RESUME"] = &PendingInputResumeEntry{Identifier: "RESUME"}

	evicted := o.evictIdleLogBuffers(&state, func(string) bool { return true })

	assert.Equal(t, 1, evicted)
	resident := map[string]bool{}
	for _, id := range buf.ResidentIdentifiers() {
		resident[id] = true
	}
	assert.Equal(t, map[string]bool{"RUN": true, "RETRY": true, "PAUSED": true, "INPUT": true, "RESUME": true}, resident)
}
