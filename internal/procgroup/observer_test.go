package procgroup

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type recordingObserver struct {
	mu      sync.Mutex
	started []string
	exited  []int
	pgids   []int
}

func (r *recordingObserver) GroupStarted(pgid, pid int, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, label)
	r.pgids = append(r.pgids, pgid)
}

func (r *recordingObserver) GroupExited(pgid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exited = append(r.exited, pgid)
}

// TestTrackReportsGroupToObserver (CORE-042): Track reports the started
// group with the ctx label, and the returned func reports its exit.
func TestTrackReportsGroupToObserver(t *testing.T) {
	obs := &recordingObserver{}
	restore := SetObserver(obs)
	defer restore()

	cmd := exec.CommandContext(context.Background(), "sleep", "0")
	Configure(cmd, time.Second)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := Track(WithLabel(context.Background(), "ABC-7"), cmd)
	_ = cmd.Wait()
	done()

	if len(obs.started) != 1 || obs.started[0] != "ABC-7" || obs.pgids[0] != cmd.Process.Pid {
		t.Fatalf("started = %v pgids = %v, want [ABC-7] [%d]", obs.started, obs.pgids, cmd.Process.Pid)
	}
	if len(obs.exited) != 1 || obs.exited[0] != cmd.Process.Pid {
		t.Fatalf("exited = %v, want [%d]", obs.exited, cmd.Process.Pid)
	}
}

// TestTrackWithoutObserverIsNoop: no observer, no calls, no panic.
func TestTrackWithoutObserverIsNoop(t *testing.T) {
	restore := SetObserver(nil)
	defer restore()
	Track(context.Background(), exec.Command("true"))()
}
