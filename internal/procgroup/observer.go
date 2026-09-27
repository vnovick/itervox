package procgroup

import (
	"context"
	"os/exec"
	"sync/atomic"
)

// Observer is told about every LOCAL process group a caller Tracks (CORE-042).
// cmd/itervox installs one that persists the groups to a ledger so a daemon
// restarted after kill -9, an OOM kill or a crash can reap the groups the dead
// daemon left behind: Setpgid puts each agent and hook in its own group, so
// nothing signals it when the daemon dies without running cmd.Cancel.
//
// Both methods are called from the goroutine that started the command and
// must not block for long.
type Observer interface {
	GroupStarted(pgid, pid int, label string)
	GroupExited(pgid int)
}

type observerBox struct{ obs Observer }

var observer atomic.Pointer[observerBox]

// SetObserver installs obs process-wide (nil uninstalls) and returns a
// function that restores the previous observer. There is one daemon per
// process, so one observer is enough; tests restore it with the returned
// function.
func SetObserver(obs Observer) (restore func()) {
	var box *observerBox
	if obs != nil {
		box = &observerBox{obs: obs}
	}
	prev := observer.Swap(box)
	return func() { observer.Store(prev) }
}

type labelKey struct{}

// WithLabel returns ctx carrying label (typically the issue identifier),
// which Track passes to the observer so the ledger can say whose group it is.
func WithLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}

// Track reports cmd's process group to the installed observer. Call it right
// after a successful cmd.Start on a command configured with Configure, and
// call the returned function once cmd.Wait has returned. With no observer
// installed, or a command that did not start, both are no-ops.
//
// Only local groups belong here: for an SSH worker the local process is the
// ssh client and the agent's group lives on the remote host (CORE-155 stops
// it through the remote wrapper), so a local ledger cannot reap it.
func Track(ctx context.Context, cmd *exec.Cmd) (done func()) {
	box := observer.Load()
	if box == nil || cmd == nil || cmd.Process == nil {
		return func() {}
	}
	label, _ := ctx.Value(labelKey{}).(string)
	// Configure sets Setpgid with Pgid 0, so the leader's pid is the pgid.
	pgid := cmd.Process.Pid
	box.obs.GroupStarted(pgid, cmd.Process.Pid, label)
	return func() { box.obs.GroupExited(pgid) }
}
