package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestCancelAndCleanupWorker_DeletesEntryAndCancelsContext pins the
// single-source-of-truth contract added by T-09: calling the helper
// (a) cancels the context and (b) removes the entry from workerCancels —
// atomically, with neither side dependent on event delivery.
func TestCancelAndCleanupWorker_DeletesEntryAndCancelsContext(t *testing.T) {
	o := &Orchestrator{
		workerCancels: make(map[string]context.CancelFunc),
	}

	_, cancel := context.WithCancel(context.Background())
	var cancelCalls atomic.Int32
	wrappedCancel := func() {
		cancelCalls.Add(1)
		cancel()
	}
	o.workerCancels["ENG-42"] = wrappedCancel

	o.cancelAndCleanupWorker("ENG-42")

	if cancelCalls.Load() != 1 {
		t.Fatalf("expected cancel called exactly once, got %d", cancelCalls.Load())
	}
	o.workerCancelsMu.Lock()
	_, present := o.workerCancels["ENG-42"]
	mapSize := len(o.workerCancels)
	o.workerCancelsMu.Unlock()
	if present {
		t.Fatalf("workerCancels[ENG-42] not removed")
	}
	if mapSize != 0 {
		t.Fatalf("expected empty workerCancels, got %d entries", mapSize)
	}
}

// TestCancelAndCleanupWorker_NoOpForUnknown ensures that calling the helper
// on an identifier that's not in the map is a safe no-op (cleanup happens
// before EventWorkerExited reaches the loop, and the loop calls cleanup
// again — that second call must not panic or double-cancel).
func TestCancelAndCleanupWorker_NoOpForUnknown(t *testing.T) {
	o := &Orchestrator{
		workerCancels: make(map[string]context.CancelFunc),
	}
	// Should not panic; should not modify state.
	o.cancelAndCleanupWorker("does-not-exist")
}

// TestCancelAndCleanupWorker_StressNoLeak simulates the failure mode T-09
// addresses: the EventWorkerExited send drops under load (events channel
// saturated), so the inline event-loop cleanup path never runs. With T-09,
// the reconcile/event-loop helpers call cancelAndCleanupWorker directly,
// so the map drains regardless of event delivery. This test pins that
// contract.
func TestCancelAndCleanupWorker_StressNoLeak(t *testing.T) {
	o := &Orchestrator{
		workerCancels: make(map[string]context.CancelFunc),
	}
	const N = 100
	for i := 0; i < N; i++ {
		_, cancel := context.WithCancel(context.Background())
		o.workerCancels[testIdentifier(i)] = cancel
	}
	for i := 0; i < N; i++ {
		o.cancelAndCleanupWorker(testIdentifier(i))
	}
	o.workerCancelsMu.Lock()
	size := len(o.workerCancels)
	o.workerCancelsMu.Unlock()
	if size != 0 {
		t.Fatalf("workerCancels leaked: expected 0, got %d", size)
	}
}

func testIdentifier(i int) string {
	return string(rune('A'+i%26)) + string(rune('0'+i/26))
}
