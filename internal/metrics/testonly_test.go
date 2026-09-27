package metrics

import "sync/atomic"

// resetForTest clears every counter. Tests only.
func resetForTest() {
	for _, c := range []*counterVec{workerExits, trackerRequests, clientErrors} {
		c.mu.Lock()
		c.vals = map[string]*atomic.Uint64{}
		c.mu.Unlock()
	}
	goroutinePanics.Store(0)
	eventsDropped.Store(0)
}

// GoroutinePanicCount reads the recovered-panic counter (tests only).
func GoroutinePanicCount() uint64 { return goroutinePanics.Load() }
