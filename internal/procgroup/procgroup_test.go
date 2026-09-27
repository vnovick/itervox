package procgroup

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// killRounds matches the agent/workspace fork-race rows: with a single
// kill(-pgid, SIGKILL) a forking child escaped roughly one run in nine, so a
// regression survives every round with probability well under 1%.
const killRounds = 40

// TestConfigureKillsForkRaceEscapees pins Kill's sweep directly at the
// shared helper: a group forking `sleep 30` children in a tight loop is
// cancelled, and after cmd.Wait the group must have no signalable member.
func TestConfigureKillsForkRaceEscapees(t *testing.T) {
	for i := range killRounds {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "while :; do /bin/sleep 30 & done")
		Configure(cmd, 5*time.Second)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pgid := cmd.Process.Pid
		t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

		time.Sleep(20 * time.Millisecond)
		cancel()
		_ = cmd.Wait()

		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(-pgid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: process group %d still has a live member 2s after cancel+Wait", i, pgid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestConfigureSetsGroupAndWaitDelay pins the three fields Configure owns.
func TestConfigureSetsGroupAndWaitDelay(t *testing.T) {
	cmd := exec.Command("true")
	Configure(cmd, 3*time.Second)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("Configure must set Setpgid")
	}
	if cmd.Cancel == nil {
		t.Fatal("Configure must install a Cancel func")
	}
	if cmd.WaitDelay != 3*time.Second {
		t.Fatalf("WaitDelay = %s, want 3s", cmd.WaitDelay)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel before Start must be a no-op, got %v", err)
	}
}

// TestKillReportsFirstSendError pins that the first send's error (here
// ESRCH for a group that never existed) is returned unchanged.
func TestKillReportsFirstSendError(t *testing.T) {
	// Spawn and reap a process so its pid names no live group.
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := Kill(cmd.Process.Pid); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Kill on an empty group = %v, want ESRCH", err)
	}
}

// TestKillSweepConstantsUnchanged pins the agent-facing timing contract
// moved here from internal/agent (10 ms re-send, 1 s cap).
func TestKillSweepConstantsUnchanged(t *testing.T) {
	if KillSweep != time.Second || KillSweepInterval != 10*time.Millisecond {
		t.Fatalf("KillSweep=%s KillSweepInterval=%s, want 1s and 10ms", KillSweep, KillSweepInterval)
	}
}
