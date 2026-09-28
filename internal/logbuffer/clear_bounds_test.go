package logbuffer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CORE-152 — residue of M0-close fix-G: Clear of an identifier with neither an
// issueBuf nor a log file created an issueBuf (and writer file state), so the
// authenticated clear-logs route could grow both maps with made-up
// identifiers. Clear of such an identifier must be a no-op, while a real issue
// keeps its issueBuf (CORE-003 seq retention) and a pre-existing on-disk file
// with no issueBuf is still deleted.
func TestClear_UnknownIdentifierDoesNotGrowMaps(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ctx := context.Background()

	b.Add("ENG-1", "L1")
	require.NoError(t, b.Flush(ctx))
	issues0, files0 := b.mapSizes()
	require.Equal(t, 1, issues0)
	require.Equal(t, 1, files0)

	for i := range 50 {
		require.NoError(t, b.Clear(fmt.Sprintf("BOGUS-%d", i)))
	}
	require.NoError(t, b.Flush(ctx))
	issues, files := b.mapSizes()
	assert.Equal(t, issues0, issues, "Clear of unknown identifiers must not create issueBufs")
	assert.Equal(t, files0, files, "Clear of unknown identifiers must not create writer file state")

	// A real issue: Clear keeps its issueBuf, so numbering continues (CORE-003).
	require.NoError(t, b.Clear("ENG-1"))
	b.Add("ENG-1", "L2")
	_, _, next, _ := b.GetSince("ENG-1", b.Epoch(), 0, false)
	assert.Equal(t, int64(2), next, "a cleared real issue must keep its sequence numbering")

	// A file left by an earlier process, with no issueBuf, is still deleted.
	old := filepath.Join(dir, "OLD-1.log")
	require.NoError(t, os.WriteFile(old, []byte("a\nb\n"), 0o644))
	require.NoError(t, b.Clear("OLD-1"))
	_, err := os.Stat(old)
	assert.True(t, os.IsNotExist(err), "Clear must still delete an on-disk file with no issueBuf")

	// After Close, Clear of an unknown identifier still reports the closed Buffer.
	require.NoError(t, b.Close(ctx))
	assert.ErrorIs(t, b.Clear("BOGUS-after-close"), ErrClosed)
}
