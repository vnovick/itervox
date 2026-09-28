package workspace

import (
	"context"
	"sync"
	"testing"

	"github.com/vnovick/itervox/internal/procgroup"
)

type hookObserver struct {
	mu      sync.Mutex
	started []string
	exited  int
}

func (h *hookObserver) GroupStarted(_, _ int, label string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = append(h.started, label)
}

func (h *hookObserver) GroupExited(int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.exited++
}

// TestRunHookTracksProcessGroup (CORE-042): a workspace hook's process group
// is reported to the orphan ledger while it runs and forgotten after.
func TestRunHookTracksProcessGroup(t *testing.T) {
	obs := &hookObserver{}
	restore := procgroup.SetObserver(obs)
	defer restore()
	ctx := procgroup.WithLabel(context.Background(), "HOOK-1")
	if err := RunHook(ctx, "true", t.TempDir(), 5000); err != nil {
		t.Fatal(err)
	}
	if len(obs.started) != 1 || obs.started[0] != "HOOK-1" || obs.exited != 1 {
		t.Fatalf("started=%v exited=%d, want [HOOK-1] and 1", obs.started, obs.exited)
	}
}
