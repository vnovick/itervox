package logbuffer

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M1-B1 fix round 1 (M2): an identifier whose only file is the rotated
// <id>.log.1 — a rename that succeeded before the reopen failed, or a current
// file deleted externally — must still count as present: the existence probe,
// Clear and the empty-window read all treat .log.1 as presence, and Clear
// removes it so cleared lines never come back.

func writeRotatedOnly(t *testing.T, dir, identifier string, lines ...string) {
	t.Helper()
	require.NoError(t, os.WriteFile(issuePath(dir, identifier)+rotatedSuffix, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestRotatedOnly_ClearRemovesItAndClearedLinesStayGone(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	writeRotatedOnly(t, dir, "ROT-9", "old-1", "old-2")

	require.NoError(t, b.Clear("ROT-9"))
	_, err := os.Stat(issuePath(dir, "ROT-9") + rotatedSuffix)
	assert.True(t, os.IsNotExist(err), "Clear must remove a rotated-only file")

	b.Add("ROT-9", "new")
	b.Remove("ROT-9")
	assert.Equal(t, []string{"new"}, b.Get("ROT-9"), "lines the user cleared must not come back")
}

func TestRotatedOnly_FreshBufferServesIt(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	writeRotatedOnly(t, dir, "ROT-8", "a", "b", "c")

	assert.Equal(t, []string{"a", "b", "c"}, b.Get("ROT-8"), "a rotated-only file is still the issue's log")
	lines, _, next, gap := b.GetSince("ROT-8", b.Epoch(), 0, false)
	assert.False(t, gap)
	assert.Equal(t, []string{"a", "b", "c"}, lines)
	assert.Equal(t, int64(3), next, "numbering counts the rotated file")
	assert.Contains(t, b.Identifiers(), "ROT-8", "a rotated-only file is listed")

	b.Add("ROT-8", "d")
	_, _, next, _ = b.GetSince("ROT-8", b.Epoch(), 0, false)
	assert.Equal(t, int64(4), next)
}

func TestRotatedOnly_CurrentDeletedExternallyStillServesRotated(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	b.w.fileCap = 256
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ctx := context.Background()
	for i := range 20 {
		b.Add("ROT-7", fmt.Sprintf("line-%02d-%s", i, strings.Repeat("x", 40)))
	}
	require.NoError(t, b.Flush(ctx))
	rotated := rotatedLines(t, dir, "ROT-7")
	require.NotEmpty(t, rotated)
	require.NoError(t, os.Remove(issuePath(dir, "ROT-7")))
	b.Remove("ROT-7")

	got := b.Get("ROT-7")
	assert.Equal(t, rotated, got, "the rotated file must still be served when the current one is gone")
	assert.True(t, slices.Equal(rotated, got))
}
