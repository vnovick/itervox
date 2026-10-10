package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestDeliverExitSurvivesCancelMidSend: a worker whose context is cancelled
// while its exit waits for room in the event queue still delivers it as long
// as the loop runs (the forced-stop exit collection reads it). Before, the
// cancel ended the send, the exit was lost, and Run waited out the whole
// collection deadline.
func TestDeliverExitSurvivesCancelMidSend(t *testing.T) {
	cfg := dependencyAuditConfig()
	o := New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	running := make(chan struct{}) // the loop is running: loopExited open
	o.loopExited.Store(&running)
	for len(o.events) < cap(o.events) {
		o.events <- OrchestratorEvent{Type: EventDispatchAutomation}
	}

	ctx, cancel := context.WithCancel(context.Background())
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1"}
	sent := make(chan struct{})
	go func() {
		o.deliverExit(ctx, issue, OrchestratorEvent{Type: EventWorkerExited, Identifier: "ENG-1"})
		close(sent)
	}()
	time.Sleep(20 * time.Millisecond) // deliverExit is blocked on the full queue
	cancel()
	time.Sleep(20 * time.Millisecond)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerExited {
				<-sent
				return
			}
		case <-deadline:
			t.Fatal("the exit was dropped when the worker's context was cancelled mid-send")
		}
	}
}
