package logbuffer

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CORE-036 — per-issue log files: tail-read instead of whole-file read, size
// cap and rotation.

// countingFile counts every byte a read of a log file consumes.
type countingFile struct {
	f *os.File
	n *atomic.Int64
}

func (c countingFile) Read(p []byte) (int, error) {
	k, err := c.f.Read(p)
	c.n.Add(int64(k))
	return k, err
}

func (c countingFile) ReadAt(p []byte, off int64) (int, error) {
	k, err := c.f.ReadAt(p, off)
	c.n.Add(int64(k))
	return k, err
}

func (c countingFile) Close() error { return c.f.Close() }

// countReads swaps openLogRead for the test's duration and returns the byte
// counter.
func countReads(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	prev := openLogRead
	openLogRead = func(p string) (logReadFile, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return countingFile{f: f, n: &n}, nil
	}
	t.Cleanup(func() { openLogRead = prev })
	return &n
}

// writeLines writes count lines produced by mk to identifier's file in dir
// and returns the last maxLinesPerIssue of them plus their total size on disk.
func writeLines(t *testing.T, dir, identifier string, count int, mk func(i int) string) (tail []string, tailBytes int64) {
	t.Helper()
	f, err := os.Create(issuePath(dir, identifier))
	require.NoError(t, err)
	w := bufio.NewWriterSize(f, 1<<20)
	for i := range count {
		line := mk(i)
		_, err := w.WriteString(line + "\n")
		require.NoError(t, err)
		if i >= count-maxLinesPerIssue {
			tail = append(tail, line)
			tailBytes += int64(len(line) + 1)
		}
	}
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())
	return tail, tailBytes
}

// TestReadFromDiskReadsTailOnly: the empty-window disk read consumes only the
// last maxLinesPerIssue lines plus at most one 64 KiB read chunk, not the
// whole file.
func TestReadFromDiskReadsTailOnly(t *testing.T) {
	const chunk = 64 << 10

	t.Run("50MB of 100-byte lines", func(t *testing.T) {
		dir := t.TempDir()
		pad := strings.Repeat("x", 90)
		// 500_000 lines x 101 bytes ("%09d " + 90 + "\n") ≈ 50 MB.
		want, tailBytes := writeLines(t, dir, "BIG-1", 500_000, func(i int) string {
			return fmt.Sprintf("%09d %s", i, pad)
		})
		st, err := os.Stat(issuePath(dir, "BIG-1"))
		require.NoError(t, err)
		require.GreaterOrEqual(t, st.Size(), int64(50_000_000))

		n := countReads(t)
		got, _ := readFromDisk(dir, "BIG-1")
		assert.Equal(t, want, got, "the returned lines must equal the file's last 500")
		assert.LessOrEqual(t, n.Load(), tailBytes+chunk,
			"read %d bytes of a %d-byte file; the last 500 lines are %d bytes", n.Load(), st.Size(), tailBytes)
	})

	t.Run("520 lines of 32KiB", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Repeat("y", 32<<10-8)
		want, tailBytes := writeLines(t, dir, "WIDE-1", 520, func(i int) string {
			return fmt.Sprintf("%07d %s", i, body)
		})
		require.LessOrEqual(t, tailBytes, int64(16<<20)+500)

		n := countReads(t)
		got, _ := readFromDisk(dir, "WIDE-1")
		assert.Equal(t, want, got)
		assert.LessOrEqual(t, n.Load(), tailBytes+chunk,
			"read %d bytes; the last 500 lines are %d bytes", n.Load(), tailBytes)
	})
}

// TestReadFromDiskEdgeCases pins the tail reader's boundary behaviour against
// the line semantics countDiskLines (and so the sequence numbering) uses.
func TestReadFromDiskEdgeCases(t *testing.T) {
	long := strings.Repeat("L", 200<<10) // longer than both the read chunk and maxLineBytes
	cases := []struct {
		name string
		data string
		want []string
	}{
		{"missing file", "", nil},
		{"empty file", "\x00empty", nil},
		{"single line", "a\n", []string{"a"}},
		{"no trailing newline", "a\nb", []string{"a", "b"}},
		{"trailing empty line", "a\n\n", []string{"a", ""}},
		{"one line longer than the read window", "x\n" + long + "\ny\n", []string{"x", truncateLine(long), "y"}},
		{"file is one over-long line", long + "\n", []string{truncateLine(long)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			switch tc.data {
			case "":
			case "\x00empty":
				require.NoError(t, os.WriteFile(issuePath(dir, "E-1"), nil, 0o644))
			default:
				require.NoError(t, os.WriteFile(issuePath(dir, "E-1"), []byte(tc.data), 0o644))
			}
			got, _ := readFromDisk(dir, "E-1")
			assert.Equal(t, tc.want, got)
			if tc.data != "" && tc.data != "\x00empty" {
				assert.Equal(t, len(tc.want), countDiskLines(dir, "E-1"),
					"the tail reader and the line counter must agree on what a line is")
			}
			for _, l := range got {
				assert.LessOrEqual(t, len(l), maxLineBytes, "a line longer than maxLineBytes is truncated on read")
			}
		})
	}
}

// TestAppendToDiskRotatesAtCap: 8 goroutines append concurrently past a
// lowered file cap. The file rotates to <name>.log.1 at a line boundary, no
// file exceeds the cap, no line is lost, duplicated or split across the
// boundary, per-goroutine order survives, the rotated file keeps mode 0o644,
// and the sequence numbering stays absolute across the rotation — for a live
// Buffer and for a restarted one.
func TestAppendToDiskRotatesAtCap(t *testing.T) {
	const (
		workers   = 8
		perWorker = 100
		fileCap   = 64 << 10
	)
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	b.w.fileCap = fileCap
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ctx := context.Background()

	pad := strings.Repeat("p", 90)
	var wg sync.WaitGroup
	for g := range workers {
		wg.Go(func() {
			for i := range perWorker {
				b.Add("ROT-1", fmt.Sprintf("g%d-%04d-%s", g, i, pad))
			}
		})
	}
	wg.Wait()
	require.NoError(t, b.Flush(ctx))

	cur := issuePath(dir, "ROT-1")
	old := cur + ".1"
	curData, err := os.ReadFile(cur)
	require.NoError(t, err)
	oldData, err := os.ReadFile(old)
	require.NoError(t, err, "the file must have rotated to %s", filepath.Base(old))
	assert.LessOrEqual(t, len(curData), fileCap)
	assert.LessOrEqual(t, len(oldData), fileCap)
	st, err := os.Stat(old)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm())
	require.True(t, strings.HasSuffix(string(oldData), "\n"), "rotation must happen at a line boundary")

	all := strings.Split(strings.TrimSuffix(string(oldData)+string(curData), "\n"), "\n")
	require.Len(t, all, workers*perWorker, "no line lost or duplicated across the rotation")
	lineRE := regexp.MustCompile(`^g(\d)-(\d{4})-p{90}$`)
	next := make([]int, workers)
	for _, l := range all {
		m := lineRE.FindStringSubmatch(l)
		require.NotNil(t, m, "line split or corrupted: %q", l)
		g, _ := strconv.Atoi(m[1])
		i, _ := strconv.Atoi(m[2])
		require.Equal(t, next[g], i, "goroutine %d's lines out of order", g)
		next[g]++
	}

	// Numbering stays absolute across the rotation for the disk-served window.
	b.Remove("ROT-1")
	ep := b.Epoch()
	total := int64(workers * perWorker)
	win, _, seq, gap := b.GetSince("ROT-1", ep, 0, false)
	require.False(t, gap)
	assert.Equal(t, total, seq)
	assert.Equal(t, all[len(all)-maxLinesPerIssue:], win, "the window spans the rotation boundary")
	lines, _, next2, gap := b.GetSince("ROT-1", ep, total-100, true)
	assert.False(t, gap, "a cursor inside the retained window resumes across the rotation")
	assert.Equal(t, all[len(all)-100:], lines)
	assert.Equal(t, total, next2)
	_, _, _, gap = b.GetSince("ROT-1", ep, total-maxLinesPerIssue-1, true)
	assert.True(t, gap, "a cursor that fell off the retained window gets a gap, never a silent skip")

	// A restarted Buffer seeds its numbering from both files.
	require.NoError(t, b.Close(ctx))
	b2 := New()
	b2.SetLogDir(dir)
	t.Cleanup(func() { _ = b2.Close(context.Background()) })
	win2, _, seq2, _ := b2.GetSince("ROT-1", b2.Epoch(), 0, false)
	assert.Equal(t, total, seq2, "a restarted Buffer counts the rotated file too")
	assert.Equal(t, win, win2)
	b2.Add("ROT-1", "after-restart")
	_, _, seq3, _ := b2.GetSince("ROT-1", b2.Epoch(), 0, false)
	assert.Equal(t, total+1, seq3)
}

// TestClearDeletesRotatedFile: Clear and ClearAll remove the rotated file too,
// so a cleared issue's old lines never reappear in a later window.
func TestClearDeletesRotatedFile(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	b.w.fileCap = 1 << 10
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ctx := context.Background()
	for i := range 40 {
		b.Add("CLR-1", fmt.Sprintf("line-%03d-%s", i, strings.Repeat("z", 60)))
		b.Add("CLR-2", fmt.Sprintf("line-%03d-%s", i, strings.Repeat("z", 60)))
	}
	require.NoError(t, b.Flush(ctx))
	for _, id := range []string{"CLR-1", "CLR-2"} {
		_, err := os.Stat(issuePath(dir, id) + ".1")
		require.NoError(t, err)
	}
	require.NoError(t, b.Clear("CLR-1"))
	_, err := os.Stat(issuePath(dir, "CLR-1") + ".1")
	assert.True(t, os.IsNotExist(err), "Clear must delete the rotated file")
	assert.Empty(t, b.Get("CLR-1"))

	// A rotated file left by an earlier process is swept by ClearAll.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "OLD-9.log.1"), []byte("x\n"), 0o644))
	require.NoError(t, b.ClearAll())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "ClearAll must remove current and rotated files")
}
