package referee

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetectToolsFindsBuiltinsFromTheRepo: a package.json dependency or a
// tool's config file is enough; an unrelated repository has none.
func TestDetectToolsFindsBuiltinsFromTheRepo(t *testing.T) {
	both := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(both, "package.json"), []byte(`{"devDependencies":{"convex":"^1.0.0"}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(both, "vercel.json"), []byte(`{}`), 0o644))
	assert.Equal(t, []string{"convex", "vercel"}, DetectTools(both))

	none := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(none, "package.json"), []byte(`{"dependencies":{"react":"19"}}`), 0o644))
	assert.Empty(t, DetectTools(none))
	assert.Empty(t, DetectTools(filepath.Join(none, "missing")))
}
