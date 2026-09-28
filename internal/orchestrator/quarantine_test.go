package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// quarantineCopies returns the quarantine copies of path, oldest first.
func quarantineCopies(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".quarantine.*")
	require.NoError(t, err)
	sort.Strings(matches)
	return matches
}

// latestQuarantine returns the newest quarantine copy's content.
func latestQuarantine(t *testing.T, path string) string {
	t.Helper()
	copies := quarantineCopies(t, path)
	require.NotEmpty(t, copies, "no quarantine copy of %s", path)
	data, err := os.ReadFile(copies[len(copies)-1])
	require.NoError(t, err)
	return string(data)
}

// TestQuarantineKeepsEveryCopyBounded (CORE-173 c): a second quarantine of
// the same state file used to overwrite <file>.quarantine, losing the first
// bad file. Each quarantine is now a timestamped copy, and only the newest
// maxQuarantineCopies per file are kept.
func TestQuarantineKeepsEveryCopyBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend_health.json")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.SetBackendHealthFile(path)

	for i := 1; i <= 2; i++ {
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`{"version":9%d}`, i)), 0o644))
		_ = o.loadBackendHealthFromDisk(NewState(o.cfg))
	}
	copies := quarantineCopies(t, path)
	require.Len(t, copies, 2, "the second quarantine must not overwrite the first")
	first, _ := os.ReadFile(copies[0])
	assert.Equal(t, `{"version":91}`, string(first))
	assert.Equal(t, `{"version":92}`, latestQuarantine(t, path))

	for i := 3; i <= maxQuarantineCopies+3; i++ {
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`{"version":9%d}`, i)), 0o644))
		_ = o.loadBackendHealthFromDisk(NewState(o.cfg))
	}
	copies = quarantineCopies(t, path)
	assert.Len(t, copies, maxQuarantineCopies, "the copies are bounded")
	assert.Equal(t, fmt.Sprintf(`{"version":9%d}`, maxQuarantineCopies+3), latestQuarantine(t, path))
}
