package logbuffer

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// soakDuration is how long TestSoak_ConcurrentOpsKeepDiskEqualToMemory runs
// its mixed workload.
const soakDuration = 2 * time.Second

// M0-close fix-F soak: many issues, concurrent Add / Clear / ClearAll /
// Remove / GetSince / Flush under -race. Checked continuously: a streaming
// reader never has a line silently skipped or duplicated — within a run of
// gap-free GetSince results, each writer's lines for an issue arrive exactly
// once and in the order that writer Added them. Checked at the end, after a
// final Flush: for every issue the file is the in-memory sequence — the
// retained window is the file's tail, and the file's numbering ends exactly
// at the issue's high-water mark (no silent gap between memory and disk).
func TestSoak_ConcurrentOpsKeepDiskEqualToMemory(t *testing.T) {
	soakConcurrentOps(t, 0)
}

// CORE-036: the same soak with a 256-byte file cap, so every issue's file
// rotates many times while streams read, Clear, ClearAll and Remove race it.
// The streaming-reader and end-state checks are unchanged: rotation must never
// cause a silent skip, a duplicate or a rewound cursor, and the retained
// window must be the tail of the rotated + current files.
func TestSoak_RotationKeepsStreamsGapCorrect(t *testing.T) {
	soakConcurrentOps(t, 256)
}

// soakConcurrentOps runs the soak; fileCap > 0 lowers the per-file cap.
func soakConcurrentOps(t *testing.T, fileCap int64) {
	if testing.Short() {
		t.Skip("soak test")
	}
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	if fileCap > 0 {
		b.w.fileCap = fileCap
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })

	const issues, writers = 8, 6
	ids := make([]string, issues)
	for i := range ids {
		ids[i] = fmt.Sprintf("ENG-%d", i+1)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var violations, adds, clears, clearAlls, reads, gaps atomic.Int64
	fail := func(format string, args ...any) {
		if violations.Add(1) <= 10 {
			t.Errorf(format, args...)
		}
	}
	loop := func(pause time.Duration, fn func(r *rand.Rand)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
			for {
				select {
				case <-stop:
					return
				default:
				}
				fn(r)
				if pause > 0 {
					time.Sleep(time.Duration(r.Int64N(int64(pause))))
				}
			}
		}()
	}

	// Writers: writer w's k-th line for issue X is "w<w>:<k>".
	for w := range writers {
		next := make([]int, issues)
		loop(time.Millisecond, func(r *rand.Rand) {
			i := r.IntN(issues)
			b.Add(ids[i], "w"+strconv.Itoa(w)+":"+strconv.Itoa(next[i]))
			next[i]++
			adds.Add(1)
		})
	}
	loop(20*time.Millisecond, func(r *rand.Rand) { b.Remove(ids[r.IntN(issues)]) })
	loop(10*time.Millisecond, func(r *rand.Rand) {
		if err := b.Clear(ids[r.IntN(issues)]); err != nil {
			fail("Clear: %v", err)
		}
		clears.Add(1)
	})
	loop(100*time.Millisecond, func(*rand.Rand) {
		if err := b.ClearAll(); err != nil {
			fail("ClearAll: %v", err)
		}
		clearAlls.Add(1)
	})
	loop(10*time.Millisecond, func(r *rand.Rand) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.IntN(5)+1)*time.Millisecond)
		defer cancel()
		_ = b.Flush(ctx) // a timeout only means lines are still queued
	})
	// Streaming readers, one per issue.
	for _, id := range ids {
		cursor, have, epoch := int64(0), false, uint32(0)
		last := map[string]int{} // writer -> last k delivered since the last gap
		loop(time.Millisecond, func(*rand.Rand) {
			lines, ep, next, gap := b.GetSince(id, epoch, cursor, have)
			reads.Add(1)
			if gap {
				gaps.Add(1)
			}
			if gap || !have {
				clear(last)
			}
			for _, l := range lines {
				w, kStr, ok := strings.Cut(l, ":")
				if !ok {
					fail("%s: malformed line %q", id, l)
					continue
				}
				k, _ := strconv.Atoi(kStr)
				if prev, seen := last[w]; seen && !gap && k != prev+1 {
					fail("%s: writer %s line %d followed line %d with no gap (silent skip or duplicate)", id, w, k, prev)
				}
				last[w] = k
			}
			if next < cursor && have && !gap {
				fail("%s: cursor rewound from %d to %d without a gap", id, cursor, next)
			}
			cursor, have, epoch = next, true, ep
		})
	}

	time.Sleep(soakDuration)
	close(stop)
	wg.Wait()
	require.NoError(t, b.Flush(context.Background()))
	require.Zero(t, b.DroppedDiskLines(), "the soak never fills the queue")

	for _, id := range ids {
		v, ok := b.issues.Load(id)
		require.True(t, ok, id)
		ib := v.(*issueBuf)
		disk := diskLines(t, dir, id)
		ib.mu.RLock()
		window, n, base := append([]string(nil), ib.lines...), ib.n, ib.base
		ib.mu.RUnlock()
		fs := b.w.files[issuePath(dir, id)] // safe: Flush's barrier ordered the writer's writes before this read
		require.NotNil(t, fs, id)
		assert.Equal(t, n, fs.offset+int64(len(disk)),
			"%s: the file's numbering must end at the high-water mark (seq %d)", id, base+n)
		if len(window) > 0 {
			retained := append(rotatedLines(t, dir, id), disk...)
			k := len(window)
			if fileCap > 0 {
				// A tiny cap retains fewer lines on disk than the in-memory
				// window holds; compare the overlap.
				k = min(k, len(retained))
			}
			require.GreaterOrEqual(t, len(retained), k, id)
			assert.Equal(t, window[len(window)-k:], retained[len(retained)-k:], "%s: the retained window must be the files' tail", id)
		}
		// Moving the window to disk must not gap a caught-up client.
		b.Remove(id)
		_, _, next, gap := b.GetSince(id, b.Epoch(), base+n, true)
		assert.False(t, gap, "%s: caught-up cursor gapped when the window moved to disk", id)
		assert.Equal(t, base+n, next, id)
	}
	if fileCap > 0 {
		assert.Positive(t, b.w.rotations.Load(), "the lowered cap must actually rotate files during the soak")
	}
	t.Logf("soak: rotations=%d", b.w.rotations.Load())
	t.Logf("soak: %d issues, %d writers, %v: adds=%d clears=%d clearAlls=%d reads=%d gaps=%d violations=%d",
		issues, writers, soakDuration, adds.Load(), clears.Load(), clearAlls.Load(), reads.Load(), gaps.Load(), violations.Load())
}

// rotatedLines returns the lines of identifier's rotated file (CORE-036).
func rotatedLines(t *testing.T, dir, identifier string) []string {
	t.Helper()
	data, err := os.ReadFile(issuePath(dir, identifier) + rotatedSuffix)
	if os.IsNotExist(err) || len(data) == 0 {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
