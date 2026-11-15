//go:build unix

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

// TestAddObserverFansOutAndRestores: an added observer sees every group
// next to the one already installed, and remove puts the previous back.
func TestAddObserverFansOutAndRestores(t *testing.T) {
	ledger, monitor := &recordingObserver{}, &recordingObserver{}
	restore := SetObserver(ledger)
	defer restore()
	remove := AddObserver(monitor)

	cmd := exec.CommandContext(context.Background(), "sleep", "0")
	Configure(cmd, time.Second)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := Track(WithLabel(context.Background(), "ENG-9"), cmd)
	_ = cmd.Wait()
	done()
	for name, o := range map[string]*recordingObserver{"ledger": ledger, "monitor": monitor} {
		if len(o.started) != 1 || o.started[0] != "ENG-9" || len(o.exited) != 1 {
			t.Fatalf("%s saw started=%v exited=%v, want one ENG-9 group", name, o.started, o.exited)
		}
	}

	remove()
	cmd2 := exec.CommandContext(context.Background(), "sleep", "0")
	Configure(cmd2, time.Second)
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	Track(WithLabel(context.Background(), "ENG-10"), cmd2)()
	_ = cmd2.Wait()
	if len(monitor.started) != 1 || len(ledger.started) != 2 {
		t.Fatalf("after remove: monitor=%v ledger=%v, want the ledger alone to see ENG-10", monitor.started, ledger.started)
	}

	// With no observer installed, AddObserver installs just its own.
	SetObserver(nil)
	only := &recordingObserver{}
	defer AddObserver(only)()
	Observers{nil, only}.GroupExited(3)
	if len(only.exited) != 1 {
		t.Fatalf("Observers skips nil entries and calls the rest: %v", only.exited)
	}
}
