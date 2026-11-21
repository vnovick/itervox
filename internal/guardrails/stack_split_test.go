package guardrails

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// guardrails.lanes.split: the pipeline side is the human-lane files; a
// lockfile stays with the code even when a pattern makes it human-lane, and
// so does a path the caller keeps (Itervox's bookkeeping); no path is
// dropped or divided.
func TestStackSplitLockfileStaysWithCode(t *testing.T) {
	l := Lanes{Human: []string{".github/**", "pnpm-lock.yaml", "ci/**"}}
	paths := []string{"src/app.ts", "pnpm-lock.yaml", ".github/workflows/ci.yml", "ci/go.sum", ".itervox/handoff/a.md", "ci/run.sh"}
	code, pipeline := l.SplitPaths(paths, func(p string) bool { return strings.HasPrefix(p, ".itervox/handoff/") })
	assert.Equal(t, []string{"src/app.ts", "pnpm-lock.yaml", "ci/go.sum", ".itervox/handoff/a.md"}, code)
	assert.Equal(t, []string{".github/workflows/ci.yml", "ci/run.sh"}, pipeline)
	assert.True(t, IsLockfile(`web\package-lock.json`))
	assert.False(t, IsLockfile("lock.go"))

	code, pipeline = l.SplitPaths([]string{"pnpm-lock.yaml", "src/a.go"}, nil)
	assert.Empty(t, pipeline, "only lockfiles in the human lane: nothing to split")
	assert.Len(t, code, 2)
}
