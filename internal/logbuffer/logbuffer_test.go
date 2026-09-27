package logbuffer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/logbuffer"
)

func TestAddPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	buf.Add("ENG-1", "hello world")
	require.NoError(t, buf.Flush(context.Background())) // Add only queues the write (M0-close fix-F)

	data, err := os.ReadFile(filepath.Join(dir, "ENG-1.log"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello world")
}

func TestAddToReadOnlyDirDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	// Block MkdirAll by placing a regular file at the intended log-dir path.
	badDir := filepath.Join(dir, "logs")
	require.NoError(t, os.WriteFile(badDir, []byte("block"), 0o444))

	buf := logbuffer.New()
	buf.SetLogDir(badDir) // this path is a file, not a dir — MkdirAll will fail
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	// Must not panic.
	buf.Add("ENG-1", "should not panic")
}

func TestGetReturnsAddedLines(t *testing.T) {
	buf := logbuffer.New()
	buf.SetLogDir(t.TempDir())
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	lines := []string{"alpha", "beta", "gamma"}
	for _, l := range lines {
		buf.Add("ENG-2", l)
	}

	got := buf.Get("ENG-2")
	require.Len(t, got, 3)
	assert.Equal(t, lines, got)
}

func TestGetUnknownIssueReturnsNil(t *testing.T) {
	buf := logbuffer.New()
	assert.Nil(t, buf.Get("NONEXISTENT"))
}

func TestMultipleIssuesAreIsolated(t *testing.T) {
	buf := logbuffer.New()
	buf.SetLogDir(t.TempDir())
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	buf.Add("A-1", "line for A")
	buf.Add("B-1", "line for B")

	assert.Equal(t, []string{"line for A"}, buf.Get("A-1"))
	assert.Equal(t, []string{"line for B"}, buf.Get("B-1"))
}

func TestRemoveClearsMemoryButPreservesDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	buf.Add("ENG-3", "persist me")
	buf.Remove("ENG-3")

	// In-memory gone but logDir is set, so Get falls back to disk (by design).
	assert.Contains(t, buf.Get("ENG-3"), "persist me")

	// Disk file still present after a simulated restart (new buffer, same dir).
	buf2 := logbuffer.New()
	buf2.SetLogDir(dir)
	t.Cleanup(func() { _ = buf2.Close(context.Background()) })
	got := buf2.Get("ENG-3")
	assert.Contains(t, got, "persist me")
}

func TestNoDiskPersistenceWhenLogDirNotSet(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("ENG-4", "in memory only")
	assert.Equal(t, []string{"in memory only"}, buf.Get("ENG-4"))
}

func TestClearDeletesMemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	buf.Add("ENG-5", "to be cleared")
	require.NoError(t, buf.Flush(context.Background())) // Add only queues the write (M0-close fix-F)

	// Verify disk file exists.
	diskPath := filepath.Join(dir, "ENG-5.log")
	_, err := os.Stat(diskPath)
	require.NoError(t, err)

	err = buf.Clear("ENG-5")
	require.NoError(t, err)

	// Memory gone.
	assert.Nil(t, buf.Get("ENG-5"))

	// Disk file gone.
	_, err = os.Stat(diskPath)
	assert.True(t, os.IsNotExist(err), "disk file should be deleted after Clear")
}

func TestClearNonExistentIsOK(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	// Clearing an identifier that was never added should not error.
	err := buf.Clear("NEVER-ADDED")
	require.NoError(t, err)
}

func TestClearWithoutLogDirOnlyClearsMemory(t *testing.T) {
	buf := logbuffer.New() // no SetLogDir
	buf.Add("ENG-6", "in memory only")
	err := buf.Clear("ENG-6")
	require.NoError(t, err)
	assert.Nil(t, buf.Get("ENG-6"))
}

// TestAdd_TruncatesOversizedLine pins the per-line cap added by T-11. A
// pathological agent emitting one giant line must not pin huge memory in
// the ring buffer. Truncation is observable: the stored line ends with
// "[truncated N bytes]" and is at most the cap in length.
func TestAdd_TruncatesOversizedLine(t *testing.T) {
	buf := logbuffer.New()
	const oneMB = 1024 * 1024
	big := strings.Repeat("A", oneMB)
	buf.Add("ENG-7", big)

	got := buf.Get("ENG-7")
	require.Len(t, got, 1)
	stored := got[0]
	// Cap is 64 KiB — well below 1 MiB.
	assert.LessOrEqual(t, len(stored), 64*1024,
		"truncation cap must hold; got %d bytes", len(stored))
	assert.Contains(t, stored, "[truncated", "truncation suffix must be present")
}

// TestAdd_SmallLinePassesThroughUnchanged guards the cap from accidentally
// truncating ordinary lines.
func TestAdd_SmallLinePassesThroughUnchanged(t *testing.T) {
	buf := logbuffer.New()
	const small = "this is a perfectly reasonable log line"
	buf.Add("ENG-8", small)

	got := buf.Get("ENG-8")
	require.Len(t, got, 1)
	assert.Equal(t, small, got[0], "small line must be byte-identical")
}

func TestEviction(t *testing.T) {
	buf := logbuffer.New()
	// Add more than maxLinesPerIssue (500) lines.
	for i := 0; i < 600; i++ {
		buf.Add("ENG-EVICT", fmt.Sprintf("line-%d", i))
	}
	got := buf.Get("ENG-EVICT")
	require.Len(t, got, 500, "should cap at maxLinesPerIssue=500")
	// Oldest 100 lines should be evicted; first retained line is line-100.
	assert.Equal(t, "line-100", got[0])
	assert.Equal(t, "line-599", got[len(got)-1])
}

func TestConcurrentAccess(t *testing.T) {
	buf := logbuffer.New()
	buf.SetLogDir(t.TempDir())
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	const goroutines = 20
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			identifier := fmt.Sprintf("CONC-%d", id%5) // 5 identifiers shared
			for i := 0; i < iterations; i++ {
				buf.Add(identifier, fmt.Sprintf("g%d-line-%d", id, i))
				_ = buf.Get(identifier)
				if i%50 == 0 {
					buf.Remove(identifier)
				}
			}
		}(g)
	}
	wg.Wait()
	// No panic or race = success.
}

func TestIdentifiers_MemoryOnly(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("ID-A", "a")
	buf.Add("ID-B", "b")
	buf.Add("ID-C", "c")

	ids := buf.Identifiers()
	assert.Len(t, ids, 3)
	assert.ElementsMatch(t, []string{"ID-A", "ID-B", "ID-C"}, ids)
}

func TestIdentifiers_WithDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	// Add one in-memory + on-disk.
	buf.Add("MEM-1", "hello")

	// Manually create a disk-only log file (simulates previous run).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DISK-ONLY.log"), []byte("old line\n"), 0o644))

	ids := buf.Identifiers()
	assert.ElementsMatch(t, []string{"MEM-1", "DISK-ONLY"}, ids)
}

func TestIdentifiers_DedupesMemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	buf.Add("SHARED", "line")
	// SHARED exists both in memory and on disk now. Should appear only once.
	ids := buf.Identifiers()
	count := 0
	for _, id := range ids {
		if id == "SHARED" {
			count++
		}
	}
	assert.Equal(t, 1, count, "SHARED should appear exactly once in Identifiers()")
}

func TestIdentifiers_IgnoresNonLogFiles(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	// Create a non-.log file and a subdirectory — both should be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))

	ids := buf.Identifiers()
	assert.Empty(t, ids)
}

func TestClearAll_MemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	buf.Add("C-1", "line1")
	buf.Add("C-2", "line2")
	require.NoError(t, buf.Flush(context.Background())) // Add only queues the write (M0-close fix-F)

	// Verify files exist.
	_, err := os.Stat(filepath.Join(dir, "C-1.log"))
	require.NoError(t, err)

	err = buf.ClearAll()
	require.NoError(t, err)

	// Memory cleared.
	assert.Nil(t, buf.Get("C-1"))
	assert.Nil(t, buf.Get("C-2"))

	// Disk files cleared.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	logFiles := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".log" {
			logFiles++
		}
	}
	assert.Equal(t, 0, logFiles, "all .log files should be deleted")
}

func TestClearAll_NoLogDir(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("X-1", "line")
	err := buf.ClearAll()
	require.NoError(t, err)
	assert.Nil(t, buf.Get("X-1"))
}

func TestClearAll_NonExistentDir(t *testing.T) {
	buf := logbuffer.New()
	buf.SetLogDir("/tmp/nonexistent-logbuffer-test-dir-abc123")
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	err := buf.ClearAll()
	require.NoError(t, err)
}

func TestGetFallsBackToDiskWhenMemoryEmpty(t *testing.T) {
	dir := t.TempDir()

	// Write a log file directly (simulating a previous run).
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "DISK-ID.log"),
		[]byte("disk-line-1\ndisk-line-2\n"),
		0o644,
	))

	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	got := buf.Get("DISK-ID")
	assert.Equal(t, []string{"disk-line-1", "disk-line-2"}, got)
}

func TestGetFallsBackToDiskAfterEvictedMemory(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	// Add a line, then remove from memory. Disk should still have it.
	buf.Add("FB-1", "persisted")
	buf.Remove("FB-1")

	got := buf.Get("FB-1")
	assert.Contains(t, got, "persisted")
}

func TestDiskReadTrimsToMaxLines(t *testing.T) {
	dir := t.TempDir()

	// Write more than 500 lines to a disk file.
	var sb strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&sb, "disk-line-%d\n", i)
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "BIG.log"),
		[]byte(sb.String()),
		0o644,
	))

	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	got := buf.Get("BIG")
	require.Len(t, got, 500)
	assert.Equal(t, "disk-line-100", got[0])
	assert.Equal(t, "disk-line-599", got[len(got)-1])
}

func TestIdentifiers_ColonInIdentifier(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	buf.Add("ORG:PROJ-1", "line")

	// Verify round-trip via Identifiers (colon -> underscore -> colon).
	ids := buf.Identifiers()
	assert.Contains(t, ids, "ORG:PROJ-1")
}

func TestGetEmptyBufferWithDiskFallbackEmpty(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })

	// Create the identifier in memory but with zero lines, then check Get.
	// getOrCreate is triggered by Add, so we need an identifier that exists
	// but has been cleared.
	buf.Add("EMPTY-BUF", "temp")
	require.NoError(t, buf.Clear("EMPTY-BUF"))

	// Now Get should return nil (no memory, no disk file).
	assert.Nil(t, buf.Get("EMPTY-BUF"))
}

// ─── GetSince (CORE-003) ─────────────────────────────────────────────────

// TestGetSince_FreshConnectReturnsFullWindowNoGap pins the "first connect"
// contract: hasCursor=false always returns the current window verbatim with
// gap=false, regardless of how much history has already been evicted.
func TestGetSince_FreshConnectReturnsFullWindowNoGap(t *testing.T) {
	buf := logbuffer.New()
	for i := 0; i < 3; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}

	lines, epoch, next, gap := buf.GetSince("ENG-1", 0, 0, false)
	assert.Equal(t, []string{"line-0", "line-1", "line-2"}, lines)
	assert.Equal(t, buf.Epoch(), epoch)
	assert.Equal(t, int64(3), next)
	assert.False(t, gap)
}

// TestGetSince_ReturnsOnlyNewLinesSinceCursor pins the core sequence
// contract: a cursor at the current epoch inside the retained window
// resumes exactly after the last delivered seq, never replaying already-
// seen lines and never dropping newly appended ones.
func TestGetSince_ReturnsOnlyNewLinesSinceCursor(t *testing.T) {
	buf := logbuffer.New()
	for i := 0; i < 3; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	_, epoch, next, _ := buf.GetSince("ENG-1", 0, 0, false)
	require.Equal(t, int64(3), next)

	buf.Add("ENG-1", "line-3")
	buf.Add("ENG-1", "line-4")

	lines, epoch2, next2, gap := buf.GetSince("ENG-1", epoch, next, true)
	assert.Equal(t, []string{"line-3", "line-4"}, lines)
	assert.Equal(t, epoch, epoch2)
	assert.Equal(t, int64(5), next2)
	assert.False(t, gap)

	// Polling again with the new cursor and no new lines yields an empty
	// (not nil-vs-empty-sensitive) delta and no gap.
	lines3, _, next3, gap3 := buf.GetSince("ENG-1", epoch2, next2, true)
	assert.Empty(t, lines3)
	assert.Equal(t, next2, next3)
	assert.False(t, gap3)
}

// TestGetSince_CursorFallenOutOfWindowIsGap pins the "cursor older than the
// retained window" gap condition: appending more than maxLinesPerIssue new
// lines without a poll in between must not silently skip data — the client
// must be told about the discontinuity and replayed the current window.
func TestGetSince_CursorFallenOutOfWindowIsGap(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("ENG-1", "line-0")
	_, epoch, next, _ := buf.GetSince("ENG-1", 0, 0, false)
	require.Equal(t, int64(1), next)

	// Push 700 more lines with no intervening poll — the 1-line window this
	// cursor pointed into is long gone (buffer caps at 500).
	for i := 1; i <= 700; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}

	lines, _, next2, gap := buf.GetSince("ENG-1", epoch, next, true)
	assert.True(t, gap, "cursor pointing before the retained window must be a gap")
	require.Len(t, lines, 500)
	assert.Equal(t, "line-201", lines[0])
	assert.Equal(t, "line-700", lines[len(lines)-1])
	assert.Equal(t, int64(701), next2)
}

// TestGetSince_ClearPreservesSequenceStaleCursorBecomesGap pins the
// documented Clear contract: Clear empties the window but never rewinds the
// sequence, so a cursor that had fallen behind before the Clear (lines it
// never received are now gone for good) must resolve to a gap — not a
// silent replay, and not a panic from an out-of-range slice index.
func TestGetSince_ClearPreservesSequenceStaleCursorBecomesGap(t *testing.T) {
	buf := logbuffer.New()
	for i := 0; i < 5; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	// Simulate a client that had only caught up to seq 3 (lines 4 and 5
	// were never delivered) at the moment Clear wipes the window.
	epoch := buf.Epoch()
	var laggingCursor int64 = 3

	require.NoError(t, buf.Clear("ENG-1"))

	// A brand new line after Clear must NOT reuse seq 1 — it continues from
	// the preserved counter.
	buf.Add("ENG-1", "line-after-clear")
	lines, _, next, gap := buf.GetSince("ENG-1", epoch, laggingCursor, true)
	assert.True(t, gap, "a cursor that had fallen behind before Clear must be treated as a gap")
	assert.Equal(t, []string{"line-after-clear"}, lines)
	assert.Equal(t, int64(6), next, "seq must continue past the pre-Clear high-water mark, never rewind")

	// A client that was fully caught up (cursor == the pre-Clear high-water
	// mark) lost nothing and must NOT see a spurious gap.
	lines2, _, next2, gap2 := buf.GetSince("ENG-1", epoch, int64(5), true)
	assert.False(t, gap2, "a cursor already caught up before Clear must not gap")
	assert.Equal(t, []string{"line-after-clear"}, lines2)
	assert.Equal(t, int64(6), next2)
}

// TestGetSince_ForeignEpochIsAlwaysGap pins the restart contract: a cursor
// whose epoch does not match the buffer's current epoch must always be
// treated as a gap, regardless of the numeric cursor value (it may
// coincidentally fall inside the current window's numeric range).
func TestGetSince_ForeignEpochIsAlwaysGap(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("ENG-1", "line-0")
	buf.Add("ENG-1", "line-1")

	foreignEpoch := buf.Epoch() + 1 // guaranteed different (wraps if Epoch()==max)
	lines, epoch, next, gap := buf.GetSince("ENG-1", foreignEpoch, 1, true)
	assert.True(t, gap)
	assert.Equal(t, buf.Epoch(), epoch)
	assert.Equal(t, []string{"line-0", "line-1"}, lines)
	assert.Equal(t, int64(2), next)
}

// TestGetSince_DiskFallbackAssignsFreshSequences pins the disk-fallback
// contract for a brand-new client: when the in-memory window is empty,
// GetSince falls back to disk exactly like Get, and numbers the loaded
// lines with the file's ABSOLUTE line count (here, 2 lines written and 2
// lines on disk, so absolute and "fresh 1..N" coincide — see
// TestGetSince_DiskFallbackAbsoluteNumberingResumesWithoutGap for a case
// where trimming makes the distinction visible). A fresh connect
// (hasCursor=false) always gets gap=false regardless of the window's
// source — there is no prior cursor for a discontinuity to be visible
// against.
func TestGetSince_DiskFallbackAssignsFreshSequences(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	buf.Add("ENG-1", "persisted-1")
	buf.Add("ENG-1", "persisted-2")
	buf.Remove("ENG-1") // memory window empties (seq preserved); disk file kept

	lines, epoch, next, gap := buf.GetSince("ENG-1", 0, 0, false)
	assert.Equal(t, []string{"persisted-1", "persisted-2"}, lines)
	assert.Equal(t, buf.Epoch(), epoch)
	assert.Equal(t, int64(2), next)
	assert.False(t, gap)
}

// TestGetSince_RemoveThenReAddDoesNotAliasStaleCursor pins CORE-003 review
// round G1's Important #1 (variant 1): Remove used to delete the issueBuf's
// sequence counter, so a run started under the same identifier after a
// Remove (a retry, reviewer, or PR-continuation run — internal/orchestrator
// calls Remove on every successful worker completion) renumbered from seq
// 1. A stale cursor from the PRIOR run could then coincidentally fall
// in-range for the NEW run's fresh numbering, silently serving the tail of
// the new run's output while its own earlier lines were skipped with no
// gap ever signaled. Reproduced against the pre-fix code: reconnecting at
// cursor=300 after Remove + 400 fresh-numbered lines returned only 100
// lines (run 2's lines 301..400) with gap=false — run 2's lines 1..300
// were never delivered. Preserving seq across Remove (see Remove's doc
// comment) closes this: run 2's lines are numbered 301..700, so the stale
// cursor lands on genuinely new (not aliased) data.
func TestGetSince_RemoveThenReAddDoesNotAliasStaleCursor(t *testing.T) {
	buf := logbuffer.New()
	for i := 0; i < 300; i++ {
		buf.Add("ENG-1", fmt.Sprintf("run1-%d", i))
	}
	_, epoch, cursor, _ := buf.GetSince("ENG-1", 0, 0, false)
	require.Equal(t, int64(300), cursor, "viewer caught up at the end of run 1")

	buf.Remove("ENG-1") // worker.go:1012-1014's post-completion eviction

	for i := 0; i < 400; i++ {
		buf.Add("ENG-1", fmt.Sprintf("run2-%d", i))
	}

	lines, _, next, gap := buf.GetSince("ENG-1", epoch, cursor, true)
	assert.False(t, gap, "seq is preserved across Remove, so nothing was actually lost")
	require.Len(t, lines, 400, "every line of run 2 must be delivered — none silently skipped")
	assert.Equal(t, "run2-0", lines[0], "run 2's FIRST line must not be aliased away by a reset counter")
	assert.Equal(t, "run2-399", lines[399])
	assert.Equal(t, int64(700), next, "seq must continue past the pre-Remove high-water mark (300+400), never reset to 400")
}

// TestGetSince_DiskFallbackOvertakingStaleCursorIsGap (CORE-003 review round
// G1's Important #1 variant 2) is SUPERSEDED by
// TestGetSince_DiskFallbackAbsoluteNumberingResumesWithoutGap and
// TestGetSince_DiskFallbackCursorBelowRetainedWindowIsGap below. Round G1's
// fix (an unconditional gap for any disk-sourced window) closed the
// aliasing this test pinned, but the SAME mechanism was the CORE-003 review
// round G5 regression: it forced a gap on EVERY poll of a disk-sourced
// window, not once per discontinuity — reachable on every completed
// (Removed), disk-backed issue in production. Round G5's fix (disk
// numbering is now ABSOLUTE — readFromDisk reports the file's true line
// count, not the trimmed window length — so it participates in the
// ordinary cursor/base comparison instead of being flagged wholesale) makes
// this test's premise obsolete: cursor=300 against the SAME 600-line/
// Removed setup below is no longer a gap, because nothing was actually
// lost — see the replacement tests for the corrected, asserted behavior in
// both directions (still-valid cursor vs. genuinely-evicted cursor).

// TestGetSince_DiskFallbackAbsoluteNumberingResumesWithoutGap pins CORE-003
// review round G5's fix: a disk-sourced window's sequence numbers are
// absolute (the file's true line count), not a fresh 1..N re-numbering of
// the trimmed window. cursor=300 against a 600-line file (trimmed to the
// newest 500 on read) must resume at absolute line 301 (original lines
// 301..600, 0-indexed "line-300".."line-599") — a legitimate, lossless
// continuation, not a gap. This is a deliberate choice: nothing was evicted
// between seq 300 and the window's start (base=100), so signaling a gap
// here would be spurious and — since this exact shape (a completed,
// disk-backed issue polled repeatedly) is the hot path in production —
// would reintroduce round G5's "gaps forever" regression.
func TestGetSince_DiskFallbackAbsoluteNumberingResumesWithoutGap(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 600; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	epoch := buf.Epoch()

	buf.Remove("ENG-1") // memory window empties; only disk remains (all 600 lines, seq preserved at 600)

	lines, _, next, gap := buf.GetSince("ENG-1", epoch, 300, true)
	assert.False(t, gap, "cursor=300 is still within the reconcilable absolute numbering — nothing was lost")
	require.Len(t, lines, 300)
	assert.Equal(t, "line-300", lines[0])
	assert.Equal(t, "line-599", lines[len(lines)-1])
	assert.Equal(t, int64(600), next)
}

// TestGetSince_DiskFallbackCursorBelowRetainedWindowIsGap pins the other
// half of CORE-003 review round G5's fix: a cursor that points BEFORE the
// disk-sourced window's absolute base (data genuinely trimmed off the file
// read, i.e. off the retained window) must still gap — absolute numbering
// does not mean "never gap", it means "gap only when data was genuinely
// lost", exactly like a live in-memory window.
func TestGetSince_DiskFallbackCursorBelowRetainedWindowIsGap(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 600; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	epoch := buf.Epoch()

	buf.Remove("ENG-1") // memory window empties; disk window is absolute lines 101..600 (base=100)

	lines, _, next, gap := buf.GetSince("ENG-1", epoch, 50, true)
	assert.True(t, gap, "cursor=50 points before the retained window's absolute base (100) — those lines are genuinely gone")
	require.Len(t, lines, 500)
	assert.Equal(t, "line-100", lines[0])
	assert.Equal(t, "line-599", lines[len(lines)-1])
	assert.Equal(t, int64(600), next)
}

// TestGetSince_DiskBehindPreservedSeqGapsExactlyOnce replaces
// TestGetSince_DiskBehindPreservedSeqIsAnIrreconcilableGap (M0-close fix-A,
// G2). The old test pinned `next == 1` for a disk file that has fallen behind
// the preserved high-water mark — a REWIND, which made every subsequent poll
// (cursor 1 < preserved 5) irreconcilable again: a gap on every poll,
// forever. The corrected contract: an irreconcilable window still gaps a
// cursor that is behind, but `next` stays at the high-water mark, so the
// discontinuity is reported once and a caught-up cursor stays quiet.
func TestGetSince_DiskBehindPreservedSeqGapsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 5; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	epoch := buf.Epoch()
	buf.Remove("ENG-1")                                 // seq preserved at 5; disk still has all 5 lines
	require.NoError(t, buf.Flush(context.Background())) // the 5 lines are on disk before the external truncation

	// The disk file falls behind the known high-water mark (externally
	// truncated): its numbering cannot be trusted to compute a base.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ENG-1.log"), []byte("line-0\n"), 0o644))

	lines, _, next, gap := buf.GetSince("ENG-1", epoch, 3, true)
	assert.True(t, gap, "a cursor behind an irreconcilable window must gap")
	assert.Equal(t, []string{"line-0"}, lines)
	assert.Equal(t, int64(5), next, "next must not rewind below the high-water mark")

	gaps := 0
	cursor := next
	for poll := 0; poll < 5; poll++ {
		lines, _, next, gap = buf.GetSince("ENG-1", epoch, cursor, true)
		if gap {
			gaps++
		}
		assert.Empty(t, lines, "poll %d: nothing new since the gap", poll)
		assert.Equal(t, int64(5), next)
		cursor = next
	}
	assert.Zero(t, gaps, "the discontinuity was already reported; later polls must be quiet")

	// A caught-up cursor (5) never gaps against the truncated file either.
	_, _, next, gap = buf.GetSince("ENG-1", epoch, 5, true)
	assert.False(t, gap)
	assert.Equal(t, int64(5), next)
}

// TestGetSince_ClearAddRemoveGapsAtMostOnce reproduces M0-close G2: seq 5,
// Clear (seq preserved, file deleted), Add (seq 6; the new file has 1 line),
// Remove (memory empty). The disk window's single line IS seq 6 — it must
// be numbered so: a cursor caught up at the Clear (5) receives it with no
// gap, and a stale cursor from before the Clear gaps once, then goes quiet.
// Pre-fix: every poll returned the line with next=1, gap=true, forever.
func TestGetSince_ClearAddRemoveGapsAtMostOnce(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 5; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	epoch := buf.Epoch()
	require.NoError(t, buf.Clear("ENG-1"))
	buf.Add("ENG-1", "after-clear")
	buf.Remove("ENG-1")

	for _, start := range []int64{5, 2} {
		t.Run(fmt.Sprintf("cursor_%d", start), func(t *testing.T) {
			cursor, gaps, delivered := start, 0, []string{}
			for poll := 0; poll < 6; poll++ {
				lines, _, next, gap := buf.GetSince("ENG-1", epoch, cursor, true)
				if gap {
					gaps++
				}
				delivered = append(delivered, lines...)
				assert.Equal(t, int64(6), next, "poll %d: next is the absolute seq of after-clear", poll)
				cursor = next
			}
			assert.Equal(t, []string{"after-clear"}, delivered, "the line is delivered exactly once")
			if start == 5 {
				assert.Zero(t, gaps, "a cursor caught up at the Clear lost nothing")
			} else {
				assert.Equal(t, 1, gaps, "a pre-Clear stale cursor gaps exactly once")
			}
		})
	}
}

// TestGetSince_FreshBufferDiskCursorDoesNotSkipNewLines reproduces M0-close
// G1: after a daemon restart a fresh Buffer serves an existing 300-line disk
// file and issues current-epoch cursor 300. Pre-fix, the new issueBuf then
// numbered its Adds 1..400, so GetSince(cursor 300) returned only new lines
// 301..400 with gap=false — the first 300 new lines vanished silently. The
// issueBuf's seq must continue from the disk file's line count.
func TestGetSince_FreshBufferDiskCursorDoesNotSkipNewLines(t *testing.T) {
	dir := t.TempDir()
	prev := logbuffer.New() // the previous daemon process
	prev.SetLogDir(dir)
	t.Cleanup(func() { _ = prev.Close(context.Background()) })
	for i := 0; i < 300; i++ {
		prev.Add("ENG-1", fmt.Sprintf("old-%d", i))
	}
	require.NoError(t, prev.Flush(context.Background())) // the shutdown path's flush (cmd/itervox joinRun)

	buf := logbuffer.New() // restarted daemon
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	lines, epoch, cursor, gap := buf.GetSince("ENG-1", 0, 0, false)
	require.Len(t, lines, 300)
	require.False(t, gap)
	require.Equal(t, int64(300), cursor, "the disk-sourced first connect issues cursor 300")

	for i := 0; i < 400; i++ {
		buf.Add("ENG-1", fmt.Sprintf("new-%d", i))
	}

	lines, _, next, gap := buf.GetSince("ENG-1", epoch, cursor, true)
	assert.False(t, gap, "nothing was lost between cursor 300 and the new lines")
	require.Len(t, lines, 400, "every new line must be delivered — none silently skipped")
	assert.Equal(t, "new-0", lines[0])
	assert.Equal(t, "new-399", lines[399])
	assert.Equal(t, int64(700), next, "numbering continues from the disk file's 300 lines")
}

// TestGetSince_RestartMemoryAndDiskNumberingAgree pins getOrCreate's disk
// seeding on its own (M0-close G1, no disk-sourced connect beforehand): a
// restarted Buffer appends 400 lines to an existing 300-line file; a client
// caught up on the in-memory window must still be caught up once Remove
// switches the window to disk. Unseeded, memory numbered the lines 1..400
// while the file numbers them 301..700, so the caught-up cursor (400)
// re-received 300 already-delivered lines with gap=false.
func TestGetSince_RestartMemoryAndDiskNumberingAgree(t *testing.T) {
	dir := t.TempDir()
	prev := logbuffer.New()
	prev.SetLogDir(dir)
	t.Cleanup(func() { _ = prev.Close(context.Background()) })
	for i := 0; i < 300; i++ {
		prev.Add("ENG-1", fmt.Sprintf("old-%d", i))
	}
	require.NoError(t, prev.Flush(context.Background())) // the shutdown path's flush (cmd/itervox joinRun)
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 400; i++ {
		buf.Add("ENG-1", fmt.Sprintf("new-%d", i))
	}
	_, epoch, cursor, _ := buf.GetSince("ENG-1", 0, 0, false)
	assert.Equal(t, int64(700), cursor, "memory numbering continues the file's")

	buf.Remove("ENG-1")
	lines, _, next, gap := buf.GetSince("ENG-1", epoch, cursor, true)
	assert.False(t, gap)
	assert.Empty(t, lines, "a caught-up client must not be re-sent lines when the window moves to disk")
	assert.Equal(t, int64(700), next)
}

// TestGetSince_ServedDiskNumberingIsNeverReissued pins snapshotWindow's
// recording of a served disk numbering: when the file is AHEAD of the
// issueBuf's seq (lines appended outside this Buffer), the numbers handed to
// a client must not be re-issued by later Adds — the next line continues
// after them.
func TestGetSince_ServedDiskNumberingIsNeverReissued(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 3; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	buf.Remove("ENG-1")
	require.NoError(t, buf.Flush(context.Background())) // the 3 lines are on disk before the external append
	f, err := os.OpenFile(filepath.Join(dir, "ENG-1.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("ext-1\next-2\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, epoch, cursor, _ := buf.GetSince("ENG-1", 0, 0, false)
	require.Equal(t, int64(5), cursor)

	buf.Add("ENG-1", "after")
	lines, _, next, gap := buf.GetSince("ENG-1", epoch, cursor, true)
	assert.False(t, gap)
	assert.Equal(t, []string{"after"}, lines)
	assert.Equal(t, int64(6), next)
}

// TestGetSince_ClearPreservesSequenceStaleCursorBecomesGap_WithLogDir is the
// SetLogDir variant of TestGetSince_ClearPreservesSequenceStaleCursorBecomesGap,
// pinning CORE-003 review round G1's Important #2: production always
// configures a log directory (cmd/itervox/main.go's rotatingFile.Filename is
// never empty), and Clear deletes that identifier's on-disk file — so the
// non-logDir test above could not see that snapshotWindow's "window empty ->
// fall back to disk" path found nothing and reported seq 0 instead of the
// preserved value, transiently rewinding `next` and gapping a SECOND time
// for the same discontinuity. Reproduced against the pre-fix code: after
// Clear (logDir set) a fully-caught-up cursor got gap=true/next=0 instead of
// gap=false/next=5, and the very next line landed a second spurious gap.
func TestGetSince_ClearPreservesSequenceStaleCursorBecomesGap_WithLogDir(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	t.Cleanup(func() { _ = buf.Close(context.Background()) })
	for i := 0; i < 5; i++ {
		buf.Add("ENG-1", fmt.Sprintf("line-%d", i))
	}
	epoch := buf.Epoch()

	require.NoError(t, buf.Clear("ENG-1")) // deletes the on-disk file too

	// A client fully caught up at the moment of Clear must see no gap, and
	// `next` must stay at the pre-Clear high-water mark — even though disk
	// fallback finds nothing (the file is gone).
	lines, _, next, gap := buf.GetSince("ENG-1", epoch, int64(5), true)
	assert.False(t, gap, "a cursor already caught up before Clear must not gap, even with a log dir configured")
	assert.Empty(t, lines)
	assert.Equal(t, int64(5), next, "seq must not rewind to 0 just because disk fallback found nothing")

	buf.Add("ENG-1", "line-after-clear")
	lines2, _, next2, gap2 := buf.GetSince("ENG-1", epoch, int64(5), true)
	assert.False(t, gap2, "must not gap a second time for the same discontinuity")
	assert.Equal(t, []string{"line-after-clear"}, lines2)
	assert.Equal(t, int64(6), next2)
}

func BenchmarkLogBuffer_Add(b *testing.B) {
	buf := logbuffer.New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Add("ENG-1", "benchmark log line")
	}
}

func BenchmarkLogBuffer_AddMultipleIssues(b *testing.B) {
	buf := logbuffer.New()
	ids := []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "ENG-5"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Add(ids[i%len(ids)], "benchmark log line")
	}
}
