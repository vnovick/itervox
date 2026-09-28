package logbuffer

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M1-B1 fix round 1 (M4): tailLines used to hold the read chunks, a
// bytes.Join copy of them and a string copy of that — about 3x the bytes it
// read. It now builds each line straight from the chunks, so it allocates
// about the bytes read plus the returned lines.
func TestTailLines_AllocatesAboutTwiceTheBytesRead(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("y", 32<<10-8)
	_, tailBytes := writeLines(t, dir, "ALLOC-1", 520, func(i int) string {
		return fmt.Sprintf("%07d %s", i, body)
	})
	p := issuePath(dir, "ALLOC-1")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	lines, _, ok := tailLines(p, maxLinesPerIssue)
	runtime.ReadMemStats(&after)
	require.True(t, ok)
	require.Len(t, lines, maxLinesPerIssue)

	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	t.Logf("tailLines allocated %d bytes for %d bytes of lines (%.2fx)", allocated, tailBytes, float64(allocated)/float64(tailBytes))
	// Read ≈ tailBytes (+ < one chunk); returned strings ≈ tailBytes. The old
	// Join + string copies added two more copies (≈ 4x in total).
	assert.LessOrEqual(t, allocated, 2*tailBytes+2*int64(tailReadChunk)+(1<<20),
		"tailLines allocated %d bytes for %d bytes of lines (%.2fx)", allocated, tailBytes, float64(allocated)/float64(tailBytes))
}

// naiveTail is the reference: read the whole file, split it with
// countDiskLines' line semantics, keep the last n lines, truncate each.
func naiveTail(data []byte, n int) []string {
	if len(data) == 0 {
		return nil
	}
	all := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	for i, l := range all {
		all[i] = truncateLine(l)
	}
	return all
}

// TestTailLines_MatchesNaiveSplit is a property test: for random files —
// empty lines, runs of newlines, no trailing newline, and newlines placed
// exactly on (and either side of) chunk boundaries — with tiny read chunks,
// tailLines returns exactly what a whole-file split does.
func TestTailLines_MatchesNaiveSplit(t *testing.T) {
	prev := tailReadChunk
	t.Cleanup(func() { tailReadChunk = prev })
	dir := t.TempDir()
	p := filepath.Join(dir, "PROP-1.log")
	r := rand.New(rand.NewPCG(1, 2))
	const cases = 20000
	for c := range cases {
		tailReadChunk = 1 + r.IntN(9)
		var buf bytes.Buffer
		size := r.IntN(80)
		for buf.Len() < size {
			switch r.IntN(4) {
			case 0:
				buf.WriteByte('\n')
			default:
				buf.WriteByte(byte('a' + r.IntN(3)))
			}
		}
		data := buf.Bytes()
		// Force newlines onto chunk boundaries measured from the end.
		if len(data) > 0 && r.IntN(2) == 0 {
			for off := len(data) - tailReadChunk; off >= 0; off -= tailReadChunk {
				data[off] = '\n'
				if r.IntN(3) == 0 && off+1 < len(data) {
					data[off+1] = '\n'
				}
			}
		}
		require.NoError(t, os.WriteFile(p, data, 0o644))
		n := 1 + r.IntN(6)
		got, atStart, ok := tailLines(p, n)
		require.True(t, ok)
		want := naiveTail(data, n)
		if !assert.Equal(t, want, got, "case %d: chunk=%d n=%d data=%q", c, tailReadChunk, n, data) {
			return
		}
		total := countDiskLines(dir, "PROP-1")
		assert.Equal(t, total <= n, atStart, "case %d: atStart must say whether the whole file was consumed", c)
	}
	// Over-long lines at the production chunk size, spanning several chunks.
	tailReadChunk = prev
	long := strings.Repeat("L", 3*prev+17)
	for _, data := range []string{long + "\n", "a\n" + long + "\nb", long, "\n" + long + "\n\n"} {
		require.NoError(t, os.WriteFile(p, []byte(data), 0o644))
		got, _, _ := tailLines(p, 3)
		assert.Equal(t, naiveTail([]byte(data), 3), got)
	}
}
