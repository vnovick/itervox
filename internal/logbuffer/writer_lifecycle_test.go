package logbuffer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M0-close fix-F — the writer's bound, Close and panic containment.

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureWarnings(t *testing.T) *lockedLog {
	t.Helper()
	l := &lockedLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

// R3: when the disk stalls and the queue fills, the overflow is never
// silent — it is counted and warned about (rate-limited: once, not per
// line) — memory keeps every line, persistence of new lines resumes by
// itself once the disk recovers, Flush reports the loss, and a reader whose
// cursor predates the lost lines gets a gap from the disk window.
func TestAdd_QueueOverflowIsCountedWarnedAndRecovers(t *testing.T) {
	logs := captureWarnings(t)
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	const lineLen = len("line-0000")
	// Room for the in-flight line (its bytes stay charged until written)
	// plus three queued ones.
	b.w.limit = 4 * (lineLen + queuedLineOverhead)
	g := newGatedDisk(t, b, "line-0000")

	b.Add("ENG-1", "line-0000") // taken by the writer, which stalls on it
	g.waitEntered(t, "line-0000")
	for i := 1; i <= 10; i++ {
		b.Add("ENG-1", fmt.Sprintf("line-%04d", i))
	}
	assert.EqualValues(t, 7, b.DroppedDiskLines(), "lines 4..10 did not fit in the queue")
	assert.Len(t, b.Get("ENG-1"), 11, "memory keeps every line")
	assert.Equal(t, 1, strings.Count(logs.String(), "not written to disk"),
		"one rate-limited warning, not one per dropped line:\n%s", logs.String())

	g.open("line-0000")                                                     // the disk recovers
	require.ErrorIs(t, b.Flush(context.Background()), ErrLinesNotPersisted) // ... and drains the backlog
	b.Add("ENG-1", "after")
	err := b.Flush(context.Background())
	require.ErrorIs(t, err, ErrLinesNotPersisted, "Flush must report lines that never reached disk")
	assert.Equal(t, []string{"line-0000", "line-0001", "line-0002", "line-0003", "after"}, diskLines(t, dir, "ENG-1"),
		"queued lines and every line after recovery reach disk, in order")

	// The file is now behind the numbering (7 lines missing). A reader
	// caught up at line 2 must be told, not silently served the disk window.
	b.Remove("ENG-1")
	_, _, next, gap := b.GetSince("ENG-1", b.Epoch(), 2, true)
	assert.True(t, gap, "a cursor behind lost lines must gap")
	assert.EqualValues(t, 12, next, "next stays at the high-water mark")
	_, _, _, gap = b.GetSince("ENG-1", b.Epoch(), next, true)
	assert.False(t, gap, "the discontinuity is reported once")
}

// N7 / reload: after Close the writer starts no disk operation — not even
// for lines queued before Close — and later Adds stay in memory only, all
// counted. Close waits for the write in progress, bounded by its ctx.
func TestClose_StartsNoDiskWorkAfterwards(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	g := newGatedDisk(t, b, "a")
	b.Add("ENG-1", "a")
	g.waitEntered(t, "a")
	b.Add("ENG-1", "queued before close")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	closedAt := time.Now()
	require.ErrorIs(t, b.Close(ctx), context.DeadlineExceeded, "Close waits for the in-flight write, bounded by ctx")
	g.open("a")
	require.NoError(t, b.Close(context.Background()), "the writer exits once its in-flight write returns")

	b.Add("ENG-1", "after close")
	assert.Equal(t, []string{"a", "queued before close", "after close"}, b.Get("ENG-1"), "memory is unaffected")
	assert.Equal(t, []string{"a"}, diskLines(t, dir, "ENG-1"), "nothing is written after Close")
	for _, c := range g.callsSnapshot() {
		assert.False(t, c.at.After(closedAt), "disk write %v started after Close", c.lines)
	}
	assert.EqualValues(t, 2, b.DroppedDiskLines(), "the discarded and the post-Close line are counted")
	assert.ErrorIs(t, b.Flush(context.Background()), ErrClosed)
	assert.ErrorIs(t, b.Clear("ENG-1"), ErrClosed)
}

// CORE-008 pattern: a panic on the writer goroutine is recovered and
// reported, and nobody waits forever — a Flush queued behind the panicking
// write returns, and the Buffer keeps serving memory.
func TestWriter_PanicIsContainedAndReleasesWaiters(t *testing.T) {
	logs := captureWarnings(t)
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.diskAppend = func(dir, identifier string, lines []string) {
		for _, l := range lines {
			if l == "boom" {
				panic("disk exploded")
			}
		}
		_ = appendToDisk(dir, identifier, lines)
	}
	b.Add("ENG-1", "boom")
	ctx, cancel := context.WithTimeout(context.Background(), promptly)
	defer cancel()
	err := b.Flush(ctx)
	require.False(t, errors.Is(err, context.DeadlineExceeded), "Flush hung behind a panicked writer")
	require.Error(t, err)
	assert.Contains(t, logs.String(), "disk writer panicked")
	b.Add("ENG-1", "still logging")
	assert.Equal(t, []string{"boom", "still logging"}, b.Get("ENG-1"))
	assert.EqualValues(t, 2, b.DroppedDiskLines())
}
