package logbuffer

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M0-close re-check N1/N2 — whitebox concurrency tests driven through the
// Buffer's two test seams (diskAppend, beforeDiskRead).

// promptly is how long a call that must not wait on another goroutine's disk
// I/O may take. The blocked write in these tests never completes on its own,
// so any wait on it shows up as a timeout, not as a slow pass.
const promptly = 2 * time.Second

// blockDiskAppendOn replaces the disk-append step so that a batch containing
// line blocks until the returned release func is called; entered is closed
// when such a batch starts. Every other batch (and the blocked one, once
// released) goes to the real appender. Releases the gate on cleanup.
func blockDiskAppendOn(t *testing.T, b *Buffer, line string) (entered <-chan struct{}, release func()) {
	t.Helper()
	in := make(chan struct{})
	gate := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	b.diskAppend = func(dir, identifier string, lines []string) {
		for _, l := range lines {
			if l == line {
				enterOnce.Do(func() { close(in) })
				<-gate
				break
			}
		}
		_ = appendToDisk(dir, identifier, lines)
	}
	rel := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(rel)
	return in, rel
}

// diskLines reads identifier's log file as lines ("" file → nil).
func diskLines(t *testing.T, dir, identifier string) []string {
	t.Helper()
	data, err := os.ReadFile(issuePath(dir, identifier))
	if os.IsNotExist(err) || len(data) == 0 {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// N2: a worker goroutine stalled inside a slow disk append for issue X must
// not hold X's lock: (a) a concurrent GetSince for X (the SSE handler)
// returns promptly, and (b) a second Add for X from another goroutine (the
// orchestrator event loop, e.g. ReconcileStalls' stall warning, which runs
// BEFORE it cancels the stalled worker) returns promptly — it does not wait
// for the blocked write. Once the write is released, the disk holds both
// lines in sequence order.
func TestAdd_SlowDiskWriteBlocksNeitherReadersNorOtherAdds(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	entered, release := blockDiskAppendOn(t, b, "slow")

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		b.Add("ENG-1", "slow") // the worker, stuck in the disk write
	}()
	select {
	case <-entered:
	case <-time.After(promptly):
		t.Fatal("worker never reached the disk append")
	}

	// (a) SSE reader.
	type since struct {
		lines []string
		next  int64
		gap   bool
	}
	got := make(chan since, 1)
	go func() {
		lines, _, next, gap := b.GetSince("ENG-1", 0, 0, false)
		got <- since{lines, next, gap}
	}()
	select {
	case r := <-got:
		assert.Equal(t, []string{"slow"}, r.lines)
		assert.Equal(t, int64(1), r.next)
		assert.False(t, r.gap)
	case <-time.After(promptly):
		t.Fatal("GetSince blocked behind another goroutine's disk write (N2)")
	}

	// (b) the event loop's Add for the same issue.
	addDone := make(chan struct{})
	go func() {
		defer close(addDone)
		b.Add("ENG-1", "stall warning")
	}()
	select {
	case <-addDone:
	case <-time.After(promptly):
		t.Fatal("a second Add waited for another goroutine's blocked disk write (N2)")
	}
	lines, _, next, _ := b.GetSince("ENG-1", b.Epoch(), 1, true)
	assert.Equal(t, []string{"stall warning"}, lines)
	assert.Equal(t, int64(2), next)

	// Ordering: once released, disk lines land in sequence order.
	release()
	<-workerDone
	require.Eventually(t, func() bool { return len(diskLines(t, dir, "ENG-1")) == 2 }, promptly, 5*time.Millisecond)
	assert.Equal(t, []string{"slow", "stall warning"}, diskLines(t, dir, "ENG-1"))
}

// N2 ordering under contention: many goroutines appending to one issue while
// its first write is blocked. Disk order must equal sequence order (memory
// order), with nothing lost or duplicated.
func TestAdd_ContendedDiskAppendsKeepSequenceOrder(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	entered, release := blockDiskAppendOn(t, b, "first")

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		b.Add("ENG-1", "first")
	}()
	<-entered

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				b.Add("ENG-1", strings.Repeat("x", w+1)+":"+string(rune('a'+i%26)))
			}
		}()
	}
	allAdded := make(chan struct{})
	go func() {
		wg.Wait()
		close(allAdded)
	}()
	select {
	case <-allAdded: // none of them waited on the blocked write
	case <-time.After(promptly):
		release() // unblock the writers so the test can exit
		t.Fatal("contended Adds waited for another goroutine's blocked disk write (N2)")
	}
	release()
	<-firstDone

	want := b.Get("ENG-1") // the in-memory window: sequence order
	require.Len(t, want, 1+writers*perWriter)
	require.Eventually(t, func() bool { return len(diskLines(t, dir, "ENG-1")) == len(want) }, promptly, 5*time.Millisecond)
	assert.Equal(t, want, diskLines(t, dir, "ENG-1"), "disk order must equal sequence order")
}

// N1: snapshotWindow must not combine a diskOffset captured before a
// concurrent Clear with a file read after it. Codex's interleaving: seq 1,
// memory empty after Remove, client cursor 1; the reader captures
// (seq 1, offset 0) and unlocks; Clear re-bases offset to 1 and Add(A),
// Add(B) are assigned 2 and 3; the reader then reads the new file [A,B] and
// numbers it with the stale offset 0 — returning B (next 2, no gap), after
// which the next poll returns B again and A is never delivered. Every line
// after the cursor must be delivered exactly once, or a gap reported.
func TestGetSince_ConcurrentClearNeverSilentlySkipsALine(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.Add("ENG-1", "L1")
	b.Remove("ENG-1")
	ep := b.Epoch()

	paused := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	b.beforeDiskRead = func(string) {
		once.Do(func() {
			close(paused)
			<-resume
		})
	}

	type since struct {
		lines []string
		next  int64
		gap   bool
	}
	first := make(chan since, 1)
	go func() {
		lines, _, next, gap := b.GetSince("ENG-1", ep, 1, true)
		first <- since{lines, next, gap}
	}()
	select {
	case <-paused:
	case <-time.After(promptly):
		t.Fatal("reader never reached the disk read")
	}

	mutated := make(chan struct{})
	go func() {
		defer close(mutated)
		require.NoError(t, b.Clear("ENG-1"))
		b.Add("ENG-1", "A")
		b.Add("ENG-1", "B")
	}()
	// Let the Clear/Add/Add race the paused reader if the implementation
	// allows it (the pre-fix tree does); an implementation that orders
	// Clear after the in-flight read simply times out here.
	select {
	case <-mutated:
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	r := <-first
	<-mutated

	delivered := append([]string(nil), r.lines...)
	gapped := r.gap
	cursor := r.next
	for range 4 {
		lines, _, next, gap := b.GetSince("ENG-1", ep, cursor, true)
		gapped = gapped || gap
		delivered = append(delivered, lines...)
		cursor = next
	}
	if !gapped {
		assert.Equal(t, []string{"A", "B"}, delivered,
			"without a gap, every line after cursor 1 must be delivered exactly once, in order (N1)")
	}
	assert.Equal(t, int64(3), cursor)
}

// N2 regression guard for the disk-role design: an empty-window reader that
// holds the disk role while its file read is slow must not make Add wait
// either — Add only queues behind the role, and the queued line reaches disk
// once the reader releases it.
func TestAdd_DoesNotWaitForASlowEmptyWindowDiskRead(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.Add("ENG-1", "L1")
	b.Remove("ENG-1")

	paused := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	b.beforeDiskRead = func(string) {
		once.Do(func() {
			close(paused)
			<-resume
		})
	}
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})

	readDone := make(chan []string, 1)
	go func() { readDone <- b.Get("ENG-1") }()
	<-paused

	addDone := make(chan struct{})
	go func() {
		defer close(addDone)
		b.Add("ENG-1", "L2")
	}()
	select {
	case <-addDone:
	case <-time.After(promptly):
		t.Fatal("Add waited for an empty-window reader's disk read")
	}
	close(resume)
	assert.Equal(t, []string{"L1"}, <-readDone)
	require.Eventually(t, func() bool { return len(diskLines(t, dir, "ENG-1")) == 2 }, promptly, 5*time.Millisecond)
	assert.Equal(t, []string{"L1", "L2"}, diskLines(t, dir, "ENG-1"))
}
