package logbuffer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Flush is a barrier: it does not return until every line Added before it
// was called is on disk, and then disk equals the in-memory sequence,
// exactly once. (Replaces fix-E's TestFlush_WritesLinesLeftQueuedPastTheInlineBudget,
// whose precondition — every Add returned, the tail queued, nobody writing
// it — cannot occur with a writer goroutine; TestWriter_QuietTailReachesDiskWithoutAnotherCall
// now pins that it cannot.)
func TestFlush_IsABarrierForEveryEarlierAdd(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	g := newGatedDisk(t, b, "own")
	b.Add("ENG-1", "own")
	g.waitEntered(t, "own")
	for _, l := range []string{"q1", "q2", "q3"} {
		b.Add("ENG-1", l)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- b.Flush(context.Background()) }()
	select {
	case err := <-flushed:
		t.Fatalf("Flush returned (%v) while an earlier line's write was still blocked", err)
	case <-time.After(100 * time.Millisecond):
	}
	g.open("own")
	require.NoError(t, <-flushed)

	want := []string{"own", "q1", "q2", "q3"}
	require.Equal(t, want, b.Get("ENG-1"), "in-memory sequence")
	assert.Equal(t, want, diskLines(t, dir, "ENG-1"), "after Flush every line is on disk, in seq order, exactly once")
	require.NoError(t, b.Flush(context.Background()))
	assert.Equal(t, want, diskLines(t, dir, "ENG-1"), "a second Flush writes nothing twice")
}

// Flush respects ctx: with the writer stuck in a write that never finishes,
// it returns ctx.Err() once ctx expires instead of blocking, and the queued
// line is still written once the disk recovers, not dropped. (Renamed from
// fix-E's TestFlush_ReturnsCtxErrWhenTheDiskRoleNeverFrees; same body.)
func TestFlush_ReturnsCtxErrWhenTheWriterIsStuck(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	entered, release := blockDiskAppendOn(t, b, "stuck")
	stuckDone := make(chan struct{})
	go func() {
		defer close(stuckDone)
		b.Add("ENG-1", "stuck")
	}()
	<-entered
	b.Add("ENG-1", "queued")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := b.Flush(ctx)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	require.Less(t, time.Since(start), promptly, "Flush must not outlive its ctx")

	release()
	<-stuckDone
	require.NoError(t, b.Flush(context.Background()))
	assert.Equal(t, []string{"stuck", "queued"}, diskLines(t, dir, "ENG-1"))
}

// Flush racing Add from several goroutines: after a final Flush the file is
// exactly the in-memory sequence — no gaps, duplicates or reordering. The
// disk step is slowed so lines are routinely still queued when a
// short-deadline Flush gives up.
func TestFlush_ConcurrentWithAddKeepsDiskEqualToSequence(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.diskAppend = func(dir, identifier string, lines []string) {
		time.Sleep(time.Millisecond)
		_ = appendToDisk(dir, identifier, lines)
	}
	ids := []string{"ENG-1", "ENG-2"}
	const writers, perWriter = 6, 40 // 240 lines per issue: within the window

	stopFlush := make(chan struct{})
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		for {
			select {
			case <-stopFlush:
				return
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			_ = b.Flush(ctx) // a timeout here only leaves lines queued
			cancel()
		}
	}()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				for _, id := range ids {
					b.Add(id, fmt.Sprintf("w%d-%03d", w, i))
				}
			}
		}()
	}
	wg.Wait()
	close(stopFlush)
	<-flushDone
	require.NoError(t, b.Flush(context.Background()))

	for _, id := range ids {
		want := b.Get(id)
		require.Len(t, want, writers*perWriter, id)
		got := diskLines(t, dir, id)
		assert.Equal(t, want, got, "%s: disk must equal the in-memory sequence", id)
		seen := make(map[string]bool, len(got))
		for _, l := range got {
			assert.False(t, seen[l], "%s: duplicate %q", id, l)
			seen[l] = true
			assert.True(t, strings.HasPrefix(l, "w"), l)
		}
	}
}
