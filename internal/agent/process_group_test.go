package agent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// processGroupKillRounds is how many times TestSetProcessGroupKillsForkRaceEscapees
// cancels a group that is forking in a tight loop. With a single
// kill(-pgid, SIGKILL) a child escaped in roughly one run in nine on the
// machine that found this (35 of 300), so a regression survives all rounds
// with probability well under 1%.
const processGroupKillRounds = 40

// TestSetProcessGroupKillsForkRaceEscapees pins killProcessGroup's sweep: a
// member forked concurrently with the group SIGKILL must not survive it.
//
// The command is a shell that forks `sleep 30` children as fast as it can, so
// every cancel lands while a fork is likely in flight. After cmd.Wait returns,
// the group must have no signalable member within a short window. A child
// that escaped the kill is a live `sleep 30` and stays signalable for 30s, so
// a failure is unambiguous rather than a reaping-latency artefact.
func TestSetProcessGroupKillsForkRaceEscapees(t *testing.T) {
	for i := range processGroupKillRounds {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "while :; do /bin/sleep 30 & done")
		setProcessGroup(cmd)
		require.NoError(t, cmd.Start())
		pgid := cmd.Process.Pid
		// Always sweep the group on the way out so a failing round cannot
		// leak `sleep 30` processes into the rest of the run.
		t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

		time.Sleep(20 * time.Millisecond) // let the loop get forking
		cancel()
		_ = cmd.Wait()

		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(-pgid, 0) == nil {
			if time.Now().After(deadline) {
				out, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=,stat=,command=").Output()
				t.Fatalf("round %d: process group %d still has a live member 2s after cancel+Wait; a child forked during the kill escaped it\n%s",
					i, pgid, filterByPgid(string(out), pgid))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// filterByPgid keeps the `ps -o pid=,ppid=,pgid=,...` rows whose pgid column
// is pgid, so a failure shows which member survived and its state.
func filterByPgid(psOut string, pgid int) string {
	var b strings.Builder
	want := strconv.Itoa(pgid)
	for line := range strings.SplitSeq(psOut, "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[2] == want {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
