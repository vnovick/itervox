package logbuffer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M0-close fix-F — reproductions for Codex re-check 2's writer findings
// (N4-N7, N9) and the R1/R4 requirements, driven through the Buffer's test
// seams (diskAppend, beforeDiskRead, beforeSweep).

// gatedDisk replaces the disk-append step. A batch containing a gated line
// blocks until that line's gate is opened; entered(line) is closed when such
// a batch starts. Every call is recorded with its start time. Gates are
// opened on cleanup so no writer is left blocked.
type gatedDisk struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	entered map[string]chan struct{}
	calls   []diskCall
}

type diskCall struct {
	at    time.Time
	lines []string
}

func newGatedDisk(t *testing.T, b *Buffer, gated ...string) *gatedDisk {
	t.Helper()
	g := &gatedDisk{gates: map[string]chan struct{}{}, entered: map[string]chan struct{}{}}
	for _, l := range gated {
		g.gates[l] = make(chan struct{})
		g.entered[l] = make(chan struct{})
	}
	b.diskAppend = func(dir, identifier string, lines []string) {
		g.mu.Lock()
		g.calls = append(g.calls, diskCall{at: time.Now(), lines: slices.Clone(lines)})
		var wait []chan struct{}
		for _, l := range lines {
			if gate, ok := g.gates[l]; ok {
				select {
				case <-g.entered[l]:
				default:
					close(g.entered[l])
				}
				wait = append(wait, gate)
			}
		}
		g.mu.Unlock()
		for _, gate := range wait {
			<-gate
		}
		_ = appendToDisk(dir, identifier, lines)
	}
	t.Cleanup(func() {
		for _, l := range gated {
			g.open(l)
		}
	})
	return g
}

func (g *gatedDisk) open(line string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.gates[line]:
	default:
		close(g.gates[line])
	}
}

func (g *gatedDisk) waitEntered(t *testing.T, line string) {
	t.Helper()
	select {
	case <-g.entered[line]:
	case <-time.After(promptly):
		t.Fatalf("the disk write of %q never started", line)
	}
}

func (g *gatedDisk) callsSnapshot() []diskCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

// returnsPromptly runs fn on a goroutine and reports whether it returned
// within promptly.
func returnsPromptly(fn func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(promptly):
		return false
	}
}

// memLines returns identifier's in-memory window.
func memLines(b *Buffer, identifier string) []string {
	v, ok := b.issues.Load(identifier)
	if !ok {
		return nil
	}
	ib := v.(*issueBuf)
	ib.mu.RLock()
	defer ib.mu.RUnlock()
	return slices.Clone(ib.lines)
}

// R1: Add performs no disk I/O. With every disk write stalled, even the
// first Add on an idle Buffer — the one that used to find the writer idle
// and write its own line — returns at once.
func TestAdd_NeverWaitsOnAStalledDisk(t *testing.T) {
	b := New()
	b.SetLogDir(t.TempDir())
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	newGatedDisk(t, b, "first", "second")

	require.True(t, returnsPromptly(func() { b.Add("ENG-1", "first") }),
		"the first Add on an idle Buffer waited on its own disk write (R1)")
	require.True(t, returnsPromptly(func() { b.Add("ENG-1", "second") }),
		"a later Add waited on a disk write (R1)")
	assert.Equal(t, []string{"first", "second"}, b.Get("ENG-1"))
}

// N6: an Add made when older lines are still queued must not write that
// backlog before returning. The backlog is built with no goroutine holding
// the disk (the pre-fix-F budget-overrun state); then the backlog's write
// stalls. The stall-warning Add — the event loop's, logged before
// cancelOrFallback — must still return at once.
func TestAdd_DoesNotWriteAnOlderBacklogInline(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	g := newGatedDisk(t, b, "own", "q1", "q2")

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		b.Add("ENG-1", "own")
	}()
	g.waitEntered(t, "own")
	b.Add("ENG-1", "q1")
	g.open("own")
	g.waitEntered(t, "q1")
	b.Add("ENG-1", "q2")
	b.Add("ENG-1", "q3")
	time.Sleep(150 * time.Millisecond) // past the pre-fix-F 100ms inline drain budget
	g.open("q1")
	<-holderDone

	require.True(t, returnsPromptly(func() { b.Add("ENG-1", "stall warning") }),
		"Add wrote an older backlog before returning (N6)")
	assert.Equal(t, []string{"own", "q1", "q2", "q3", "stall warning"}, b.Get("ENG-1"))
}

// R4 (Codex's quiet-tail finding): once the disk recovers, every queued line
// reaches disk without any later Add, read or Flush for the issue. Built on
// the pre-fix-F budget-overrun state, in which [q2 q3] stayed queued with no
// owner until something else touched the issue.
func TestWriter_QuietTailReachesDiskWithoutAnotherCall(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	g := newGatedDisk(t, b, "own", "q1")

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		b.Add("ENG-1", "own")
	}()
	g.waitEntered(t, "own")
	b.Add("ENG-1", "q1")
	g.open("own")
	g.waitEntered(t, "q1")
	b.Add("ENG-1", "q2")
	b.Add("ENG-1", "q3")
	time.Sleep(150 * time.Millisecond)
	g.open("q1")
	<-holderDone

	want := []string{"own", "q1", "q2", "q3"}
	ok := assert.Eventually(t, func() bool { return slices.Equal(diskLines(t, dir, "ENG-1"), want) },
		promptly, 10*time.Millisecond)
	if !ok {
		t.Errorf("quiet tail stranded: disk=%v want %v (R4)", diskLines(t, dir, "ENG-1"), want)
	}
}

// N4: a full disk queue must not stop persistence for good. The disk stalls
// while a worker logs more than the queue holds; lines past the bound are
// dropped from disk (kept in memory). Once the disk recovers, a NEW line
// must reach disk.
func TestAdd_PersistenceResumesAfterTheQueueFilled(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	g := newGatedDisk(t, b, "own", "q1")

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		b.Add("ENG-1", "own")
	}()
	g.waitEntered(t, "own")
	b.Add("ENG-1", "q1")
	g.open("own")
	g.waitEntered(t, "q1")
	// More than either bound: pre-fix-F's 2000 queued lines, and fix-F's
	// byte bound. 2100 x 5000 bytes = 10.5 MB.
	big := strings.Repeat("x", 5000)
	for i := range 2100 {
		b.Add("ENG-1", fmt.Sprintf("%04d:%s", i, big))
	}
	time.Sleep(150 * time.Millisecond)
	g.open("q1") // the disk recovers
	<-holderDone
	// Let whatever is going to drain the backlog do so — without any call
	// on the Buffer, which on the pre-fix-F tree would itself drain it.
	waitDiskStable(t, dir, "ENG-1")

	b.Add("ENG-1", "after recovery")
	// Flush reports the earlier drops (ErrLinesNotPersisted) — the pre-fix-F
	// Flush had no such report and returns nil.
	if err := b.Flush(context.Background()); err != nil {
		require.ErrorIs(t, err, ErrLinesNotPersisted)
	}
	disk := diskLines(t, dir, "ENG-1")
	require.NotEmpty(t, disk)
	last := disk[len(disk)-1]
	assert.Equal(t, "after recovery", last[:min(len(last), 20)],
		"a line Added after the disk recovered never reached disk: persistence stopped for good (N4)")
}

// waitDiskStable returns once identifier's file has stopped growing for
// 100ms (or after 10s).
func waitDiskStable(t *testing.T, dir, identifier string) {
	t.Helper()
	prev := -1
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		n := len(diskLines(t, dir, identifier))
		if n == prev {
			return
		}
		prev = n
		time.Sleep(100 * time.Millisecond)
	}
}

// N7: Flush must return ctx.Err() promptly when its ctx expires while a
// write is in flight, and must not itself start disk work after that
// expiry. Setup: "a" stalls; q1 is queued; Flush is waiting when "a" is
// released, so the next write is [q1], which stalls past Flush's deadline;
// q2 is queued meanwhile.
func TestFlush_ReturnsOnExpiryAndStartsNoWriteAfterIt(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	g := newGatedDisk(t, b, "a", "q1")

	go b.Add("ENG-1", "a")
	g.waitEntered(t, "a")
	b.Add("ENG-1", "q1")

	const flushTimeout = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	flushErr := make(chan error, 1)
	go func() { flushErr <- b.Flush(ctx) }()
	time.Sleep(50 * time.Millisecond) // Flush is now waiting on the in-flight write
	g.open("a")
	g.waitEntered(t, "q1")
	b.Add("ENG-1", "q2")

	var err error
	var flushReturned time.Time
	select {
	case err = <-flushErr:
		flushReturned = time.Now()
	case <-time.After(flushTimeout + promptly):
		t.Errorf("Flush did not return within %v of its ctx expiring while a write was in flight (N7)", promptly)
		g.open("q1")
		err = <-flushErr
		flushReturned = time.Now()
	}
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "Flush returned %v, want ctx.Err()", err)
	g.open("q1")

	for _, c := range g.callsSnapshot() {
		if c.at.After(deadline) && c.at.Before(flushReturned) {
			t.Errorf("Flush started a disk write of %v after its ctx expired (N7)", c.lines)
		}
	}
}

// N9: ClearAll's directory sweep must be ordered against the writer. An Add
// racing ClearAll must leave memory and disk agreeing: either the line was
// cleared from both, or it survives in both.
func TestClearAll_RacingAddKeepsDiskEqualToMemory(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.Add("ENG-1", "old")
	require.NoError(t, b.Flush(context.Background()))

	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	b.beforeSweep = func() {
		once.Do(func() {
			close(paused)
			<-resume
		})
	}
	clearErr := make(chan error, 1)
	go func() { clearErr <- b.ClearAll() }()
	select {
	case <-paused:
	case <-time.After(promptly):
		t.Fatal("ClearAll never reached its directory sweep")
	}
	b.Add("ENG-1", "A")
	close(resume)
	require.NoError(t, <-clearErr)
	require.NoError(t, b.Flush(context.Background()))

	assert.Equal(t, memLines(b, "ENG-1"), diskLines(t, dir, "ENG-1"),
		"memory and disk disagree after ClearAll raced an Add (N9)")
}

// N5 (N1's fresh-Buffer variant): a restarted Buffer serving an existing
// file for an identifier it has no issueBuf for must not let a Clear+Add
// that race the read make a later line silently disappear. Every line after
// the served cursor must be delivered exactly once, or a gap reported.
func TestGetSince_FreshBufferConcurrentClearNeverSilentlySkipsALine(t *testing.T) {
	dir := t.TempDir()
	prev := New() // the previous daemon process
	prev.SetLogDir(dir)
	t.Cleanup(func() { _ = prev.Close(context.Background()) })
	for i := range 100 {
		prev.Add("ENG-1", fmt.Sprintf("old-%d", i))
	}
	require.NoError(t, prev.Flush(context.Background()))

	b := New() // restarted daemon: no issueBuf for ENG-1 yet
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ep := b.Epoch()
	paused, resume := make(chan struct{}), make(chan struct{})
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
		lines, _, next, gap := b.GetSince("ENG-1", 0, 0, false)
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
		assert.NoError(t, b.Clear("ENG-1"))
		b.Add("ENG-1", "A")
	}()
	select { // an implementation that orders the Clear after the read times out here
	case <-mutated:
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	r := <-first
	<-mutated

	gapped, cursor := false, r.next
	var delivered []string
	for range 4 {
		lines, _, next, gap := b.GetSince("ENG-1", ep, cursor, true)
		gapped = gapped || gap
		delivered = append(delivered, lines...)
		cursor = next
	}
	if !gapped {
		assert.Equal(t, []string{"A"}, delivered,
			"without a gap, the line Added after the served cursor must be delivered exactly once (N5)")
	}
}
