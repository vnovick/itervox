package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/workflow"
)

// #245: FileSetWatch watches WORKFLOW.md and the settings files as one set.
// These tests wait on the Settled hook (bounded) instead of sleeping.

type fileSetRun struct {
	mu      sync.Mutex
	changes [][]string
	judged  map[string]int
}

func (r *fileSetRun) snapshot() (int, map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[string]int, len(r.judged))
	for k, v := range r.judged {
		m[k] = v
	}
	return len(r.changes), m
}

func (r *fileSetRun) waitJudged(t *testing.T, mark map[string]int, paths ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, p := range paths {
			if r.judged[p] <= mark[p] {
				return false
			}
		}
		return true
	}, 10*time.Second, 5*time.Millisecond)
}

func startFileSet(t *testing.T, files ...workflow.WatchedFile) *fileSetRun {
	t.Helper()
	return startFileSetDebounce(t, 60*time.Millisecond, files...)
}

// startFileSetDebounce is startFileSet with the quiet period given. A test
// that needs two writes to land in ONE quiet period uses a long one, so a
// loaded machine descheduling the test between them cannot split them.
func startFileSetDebounce(t *testing.T, debounce time.Duration, files ...workflow.WatchedFile) *fileSetRun {
	t.Helper()
	r := &fileSetRun{judged: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = workflow.FileSetWatch{
			Files: files, Poll: 5 * time.Millisecond, Debounce: debounce,
			Settled: func(judged []string) {
				r.mu.Lock()
				defer r.mu.Unlock()
				for _, p := range judged {
					r.judged[p]++
				}
			},
		}.Run(ctx, func(changed []string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.changes = append(r.changes, changed)
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// writeAtomic replaces path in one rename, so the watcher never reads a
// half-written file (os.WriteFile truncates first).
func writeAtomic(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, atomicfs.WriteFile(path, data, 0o644))
}

func loadedHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return workflow.AbsentHash
	}
	require.NoError(t, err)
	return workflow.FileHash(b, true)
}

// Two files changed together settle into one onChange naming both.
func TestFileSetWatchCoalescesEditsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "WORKFLOW.md"), filepath.Join(dir, ".itervox", "settings.jsonc")
	writeAtomic(t, a, []byte("a"))
	require.NoError(t, os.MkdirAll(filepath.Dir(b), 0o755))
	writeAtomic(t, b, []byte("{}"))
	r := startFileSetDebounce(t, time.Second,
		workflow.WatchedFile{Path: a, Loaded: loadedHash(t, a)},
		workflow.WatchedFile{Path: b, Loaded: loadedHash(t, b), Optional: true})

	_, mark := r.snapshot()
	writeAtomic(t, b, []byte(`{"agent": {}}`))
	writeAtomic(t, a, []byte("a2"))
	r.waitJudged(t, mark, a, b)
	n, _ := r.snapshot()
	require.Equal(t, 1, n)
	require.ElementsMatch(t, []string{a, b}, r.changes[0])
}

// An Optional file is watched while missing (with its directory): creating
// it and deleting it are both changes.
func TestFileSetWatchOptionalFileCreateAndDelete(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, ".itervox", "settings.local.jsonc")
	r := startFileSet(t, workflow.WatchedFile{Path: local, Loaded: workflow.AbsentHash, Optional: true})

	_, mark := r.snapshot()
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
	writeAtomic(t, local, []byte("{}"))
	r.waitJudged(t, mark, local)
	n, mark := r.snapshot()
	require.Equal(t, 1, n)

	require.NoError(t, os.Remove(local))
	r.waitJudged(t, mark, local)
	n, _ = r.snapshot()
	require.Equal(t, 2, n)
}

// A settings write registered as this process's own (config.SetSettings and
// the daemon's restore register through RecordSelfWrite) does not reload,
// including the creation of a file that did not exist.
func TestFileSetWatchSuppressesRegisteredSettingsWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".itervox", "settings.jsonc")
	r := startFileSet(t, workflow.WatchedFile{Path: path, Loaded: workflow.AbsentHash, Optional: true})

	_, mark := r.snapshot()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	post := []byte(`{"agent": {"max_turns": 3}}`)
	writeAtomic(t, path, post)
	workflow.RecordSelfWrite(path, nil, false, post, true)
	r.waitJudged(t, mark, path)
	n, mark := r.snapshot()
	require.Zero(t, n, "a registered write must not reload")

	// Positive control: an unregistered edit of the same file reloads.
	writeAtomic(t, path, []byte(`{"agent": {"max_turns": 4}}`))
	r.waitJudged(t, mark, path)
	n, _ = r.snapshot()
	require.Equal(t, 1, n)
}

// A file that changed between the load and the watcher's start (its first
// reading differs from Loaded) is a change pending from the start.
func TestFileSetWatchReloadsOnEditBeforeStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.jsonc")
	writeAtomic(t, path, []byte("{}"))
	loaded := loadedHash(t, path)
	writeAtomic(t, path, []byte(`{"agent": {}}`))
	r := startFileSet(t, workflow.WatchedFile{Path: path, Loaded: loaded, Optional: true})
	r.waitJudged(t, map[string]int{}, path)
	n, _ := r.snapshot()
	require.Equal(t, 1, n)
}
