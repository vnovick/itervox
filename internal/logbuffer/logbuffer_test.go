package logbuffer_test

import (
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
	buf.Add("ENG-1", "hello world")

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
	// Must not panic.
	buf.Add("ENG-1", "should not panic")
}

func TestGetReturnsAddedLines(t *testing.T) {
	buf := logbuffer.New()
	buf.SetLogDir(t.TempDir())

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
	buf.Add("A-1", "line for A")
	buf.Add("B-1", "line for B")

	assert.Equal(t, []string{"line for A"}, buf.Get("A-1"))
	assert.Equal(t, []string{"line for B"}, buf.Get("B-1"))
}

func TestRemoveClearsMemoryButPreservesDisk(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)
	buf.Add("ENG-3", "persist me")
	buf.Remove("ENG-3")

	// In-memory gone but logDir is set, so Get falls back to disk (by design).
	assert.Contains(t, buf.Get("ENG-3"), "persist me")

	// Disk file still present after a simulated restart (new buffer, same dir).
	buf2 := logbuffer.New()
	buf2.SetLogDir(dir)
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
	buf.Add("ENG-5", "to be cleared")

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

	buf.Add("C-1", "line1")
	buf.Add("C-2", "line2")

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

	got := buf.Get("DISK-ID")
	assert.Equal(t, []string{"disk-line-1", "disk-line-2"}, got)
}

func TestGetFallsBackToDiskAfterEvictedMemory(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)

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

	got := buf.Get("BIG")
	require.Len(t, got, 500)
	assert.Equal(t, "disk-line-100", got[0])
	assert.Equal(t, "disk-line-599", got[len(got)-1])
}

func TestIdentifiers_ColonInIdentifier(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)

	buf.Add("ORG:PROJ-1", "line")

	// Verify round-trip via Identifiers (colon -> underscore -> colon).
	ids := buf.Identifiers()
	assert.Contains(t, ids, "ORG:PROJ-1")
}

func TestGetEmptyBufferWithDiskFallbackEmpty(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	buf.SetLogDir(dir)

	// Create the identifier in memory but with zero lines, then check Get.
	// getOrCreate is triggered by Add, so we need an identifier that exists
	// but has been cleared.
	buf.Add("EMPTY-BUF", "temp")
	require.NoError(t, buf.Clear("EMPTY-BUF"))

	// Now Get should return nil (no memory, no disk file).
	assert.Nil(t, buf.Get("EMPTY-BUF"))
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
