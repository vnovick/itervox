package workspace

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

// hookProcessGroupKillRounds mirrors the agent package's
// processGroupKillRounds: with a single kill(-pgid, SIGKILL) a forking child
// escaped roughly one run in nine, so a regression survives all rounds with
// probability well under 1%.
const hookProcessGroupKillRounds = 40

// TestSetHookProcessGroupKillsForkRaceEscapees is CORE-150's reproduction,
// in the style of internal/agent's TestSetProcessGroupKillsForkRaceEscapees:
// a hook whose shell forks `sleep 30` children in a tight loop is cancelled,
// and after cmd.Wait returns the group must have no signalable member. A
// child that was being forked when a single SIGKILL was fanned out misses
// it, joins the group, and survives reparented to init.
func TestSetHookProcessGroupKillsForkRaceEscapees(t *testing.T) {
	for i := range hookProcessGroupKillRounds {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "while :; do /bin/sleep 30 & done")
		setHookProcessGroup(cmd)
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
				t.Fatalf("round %d: hook process group %d still has a live member 2s after cancel+Wait; a child forked during the kill escaped it\n%s",
					i, pgid, hookRowsInGroup(string(out), pgid))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// hookRowsInGroup keeps the `ps -o pid=,ppid=,pgid=,...` rows whose pgid
// column is pgid, so a failure shows which member survived.
func hookRowsInGroup(psOut string, pgid int) string {
	var b strings.Builder
	want := strconv.Itoa(pgid)
	for line := range strings.SplitSeq(psOut, "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[2] == want {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
