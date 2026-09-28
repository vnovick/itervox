// Package procgroup runs a subprocess in its own process group and kills the
// whole group on cancellation, re-sending SIGKILL until no member is left.
//
// It is the single shared home for this logic (CORE-150): both the agent
// runners (internal/agent) and the workspace hooks (internal/workspace) spawn
// shell pipelines whose children must die with them, and the package order
// forbids workspace importing agent. It imports only the standard library so
// it sits below every package that spawns subprocesses.
package procgroup

import (
	"os/exec"
	"syscall"
	"time"
)

// KillSweep bounds how long Kill keeps re-sending SIGKILL after the first
// one, and KillSweepInterval is the pause between sends. The sweep normally
// ends on its first re-send (ESRCH); the bound only matters for a member
// that cannot die (e.g. stuck in an uninterruptible wait), and it must stay
// well below any WaitDelay passed to Configure because exec.Cmd starts the
// WaitDelay timer only after Cancel returns.
const (
	KillSweep         = time.Second
	KillSweepInterval = 10 * time.Millisecond
)

// Configure makes cmd run in its own process group (Setpgid) and makes
// context cancellation kill the entire group via Kill rather than only the
// direct child — without it, cancelling a "bash -lc '<agent>'" kills bash
// and leaves the agent running. waitDelay bounds how long cmd.Wait keeps
// waiting on stdout/stderr pipes after Cancel fires: a descendant that
// inherited a pipe would otherwise hold Wait open indefinitely.
//
// Configure overwrites cmd.SysProcAttr; call it before cmd.Start.
func Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// The group leader's pid is the pgid (Setpgid with Pgid 0).
		return Kill(cmd.Process.Pid)
	}
	cmd.WaitDelay = waitDelay
}

// Kill SIGKILLs every member of process group pgid and keeps re-sending
// SIGKILL until the group has no live member left.
//
// A single kill(-pgid, SIGKILL) is not atomic with respect to a fork() that
// a group member has in flight: the child being created is not yet on the
// group's member list when the signal is fanned out, so it misses the
// SIGKILL, joins the group, and is reparented to init when its parent dies.
// This is not theoretical: the CORE-001 scanner_error rows caught the fake
// agent's `sleep 30` surviving this way, alive in the group 12s after the
// kill (a probe forking in a loop escaped a single killpg in 35 of 300
// runs, and 0 of 300 with this sweep). The escapee also holds the stdout and
// stderr pipes, so cmd.Wait stalls for the full WaitDelay as well.
//
// A second kill cannot miss such a child: it joined the group during the
// fork, before its parent could act on the pending SIGKILL. The sweep stops
// on the first error — ESRCH (no member left) or, on darwin, EPERM (only
// zombies left). The pgid cannot be recycled while the sweep still finds a
// member; after the group empties, reuse within one sweep interval would
// need the kernel's pid space to wrap.
//
// The first send's error is returned unchanged (exec.Cmd reports it), exactly
// as a single-kill implementation would.
func Kill(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		return err
	}
	deadline := time.Now().Add(KillSweep)
	for time.Now().Before(deadline) {
		time.Sleep(KillSweepInterval)
		if syscall.Kill(-pgid, syscall.SIGKILL) != nil {
			return nil
		}
	}
	return nil
}
