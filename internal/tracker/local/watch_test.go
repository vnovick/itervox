package local

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #133 — the local tracker wakes the orchestrator when an issue file changes.

func startWatch(t *testing.T, run func(ctx context.Context, changed func())) <-chan struct{} {
	t.Helper()
	changes := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		run(ctx, func() { changes <- struct{}{} })
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
	return changes
}

func waitChange(t *testing.T, changes <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(within):
		t.Fatalf("no change reported within %s: %s", within, what)
	}
}

func noChange(t *testing.T, changes <-chan struct{}, during time.Duration, what string) {
	t.Helper()
	select {
	case <-changes:
		t.Fatalf("unexpected change: %s", what)
	case <-time.After(during):
	}
}

func TestWatchReportsAnEditedFileAtOnce(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Backlog\n---\n")
	_, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	changes := startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, time.Hour, changed) })
	time.Sleep(50 * time.Millisecond) // the watcher is registered

	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	waitChange(t, changes, 3*time.Second, "an edit (the rescan is an hour away)")
	got, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)

	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "---\ntitle: Two\nstate: Todo\n---\n")
	waitChange(t, changes, 3*time.Second, "a new file")
	require.NoError(t, os.Remove(filepath.Join(dir, "ITX-2.md")))
	waitChange(t, changes, 3*time.Second, "a removed file")
}

// TestWatchRescanRepairsAMissedEvent: with no file event at all, the
// safety-net rescan still finds the edit and reports it.
func TestWatchRescanRepairsAMissedEvent(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Backlog\n---\n")
	_, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	changes := startWatch(t, func(ctx context.Context, changed func()) {
		tr.watch(ctx, nil, nil, 50*time.Millisecond, changed, nil) // every event missed
	})
	noChange(t, changes, 200*time.Millisecond, "nothing changed yet")
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	waitChange(t, changes, 2*time.Second, "the rescan")
}

// Itervox's own writes are not changes: they must not wake the orchestrator
// into a loop of its own making.
func TestWatchIgnoresItervoxsOwnWrites(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	_, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	changes := startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, 50*time.Millisecond, changed) })
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, tr.UpdateIssueState(t.Context(), "ITX-1", "In Progress"))
	_, err = tr.CreateComment(t.Context(), "ITX-1", "working on it")
	require.NoError(t, err)
	noChange(t, changes, 400*time.Millisecond, "Itervox's own writes")
}

// A tracker call that rescans first and finds the edit does not swallow it:
// the watcher still reports it.
func TestWatchReportsAChangeACallFoundFirst(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Backlog\n---\n")
	_, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	changes := startWatch(t, func(ctx context.Context, changed func()) {
		tr.watch(ctx, nil, nil, 100*time.Millisecond, changed, nil)
	})
	time.Sleep(20 * time.Millisecond) // the watcher has taken its starting count
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	got, err := tr.FetchCandidateIssues(t.Context()) // the dashboard reads first
	require.NoError(t, err)
	require.Len(t, got, 1)
	waitChange(t, changes, 2*time.Second, "the change the call already found")
	assert.Len(t, changes, 0)
}

// While the directory is watched, reads come from the watch, not a rescan
// per call: a file removed a moment ago is still served until its event.
func TestWatchServesReadsFromTheWatch(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	changes := startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, time.Hour, changed) })
	require.Eventually(t, func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.watched }, 2*time.Second, 5*time.Millisecond)

	got, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1, "the watch loaded what was on disk")
	require.NoError(t, os.Remove(filepath.Join(dir, "ITX-1.md")))
	got, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Len(t, got, 1, "no rescan on the call")
	waitChange(t, changes, 3*time.Second, "the removal event")
	got, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Removing the directory is a change, and a recreated directory is watched
// again (the old watch died with the old directory).
func TestWatchFollowsTheDirectoryAway(t *testing.T) {
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	changes := startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, 100*time.Millisecond, changed) })
	watched := func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.watched }
	require.Eventually(t, watched, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, os.RemoveAll(dir))
	waitChange(t, changes, 3*time.Second, "the directory removed")
	assert.False(t, watched(), "reads rescan while the directory is gone")
	got, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)

	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.Eventually(t, watched, 3*time.Second, 5*time.Millisecond, "the recreated directory is watched again")
	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "---\ntitle: Two\nstate: Todo\n---\n")
	waitChange(t, changes, 3*time.Second, "a file in the recreated directory")
	got, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

// Watch does not create the issues directory (a missing one reads as no
// issues); one created later is picked up.
func TestWatchDoesNotCreateTheDirectory(t *testing.T) {
	tr, dir := newTestTracker(t)
	changes := startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, 50*time.Millisecond, changed) })
	time.Sleep(150 * time.Millisecond)
	_, err := os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	waitChange(t, changes, 3*time.Second, "the first issue in a new directory")
}

// TestWatchDirectoryReadErrorIsReportedNotServed (#133 review): the issues
// directory is renamed aside and a regular file put at its path, which
// fsnotify could watch too. Reads report the read error and Problems names
// the directory, as without a watch; the cached issue is not served as
// current, and the file is not taken for a watched directory. Once the
// directory is back, the watch serves it again.
func TestWatchDirectoryReadErrorIsReportedNotServed(t *testing.T) {
	ctx := t.Context()
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
	startWatch(t, func(ctx context.Context, changed func()) { tr.Watch(ctx, 50*time.Millisecond, changed) })
	watched := func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.watched }
	require.Eventually(t, watched, 2*time.Second, 5*time.Millisecond)

	moved := dir + ".moved"
	require.NoError(t, os.Rename(dir, moved))
	writeRaw(t, dir, "not a directory")
	require.Eventually(t, func() bool {
		_, err := tr.FetchCandidateIssues(ctx)
		return err != nil
	}, 3*time.Second, 5*time.Millisecond, "the read error is reported")
	time.Sleep(200 * time.Millisecond) // several rescans: the file is never watched as the directory
	assert.False(t, watched(), "a file at the directory's path is not a healthy watch")
	_, err := tr.FetchCandidateIssues(ctx)
	assert.Error(t, err)
	_, err = tr.FetchIssueStatesByIDs(ctx, []string{"ITX-1"})
	assert.Error(t, err)
	_, err = tr.FetchIssueDetail(ctx, "ITX-1")
	assert.Error(t, err)
	problems := tr.Problems()
	require.NotEmpty(t, problems)
	assert.Equal(t, dir, problems[0].Path)

	require.NoError(t, os.Remove(dir))
	require.NoError(t, os.Rename(moved, dir))
	require.Eventually(t, watched, 3*time.Second, 5*time.Millisecond, "the directory is watched again")
	got, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Empty(t, tr.Problems())
}
