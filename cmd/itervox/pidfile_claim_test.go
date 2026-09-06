package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claimWorkflow(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	return wf
}

// TestClaimPIDFileIsExclusiveUnderConcurrency is issue #64's acceptance, and
// the only test here that the previous read-check-write guard could not pass.
//
// The old sequence read the pid file, concluded nobody was there, and wrote it
// 73 lines of startup later. Two daemons starting together both made that
// decision and both proceeded, sharing one .itervox/ directory — interleaving
// HEARTBEAT.md, the outbox, and the automation queue.
//
// Many concurrent claimants must produce EXACTLY ONE winner. Asserting "at
// least one" would pass on the racy implementation, which is the whole point
// of the issue.
func TestClaimPIDFileIsExclusiveUnderConcurrency(t *testing.T) {
	wf := claimWorkflow(t)

	const claimants = 24
	var (
		mu       sync.Mutex
		won      int
		refused  int
		otherErr []error
		wg       sync.WaitGroup
		start    = make(chan struct{})
	)

	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together to widen the race window
			livePid, _, _, err := claimPIDFile(wf)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case livePid != 0 || strings.Contains(err.Error(), "another daemon"):
				refused++
			default:
				otherErr = append(otherErr, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Empty(t, otherErr, "no claimant may fail for a reason other than losing the claim")
	assert.Equal(t, 1, won,
		"exactly one claimant may win — more than one means two daemons would share .itervox/")
	assert.Equal(t, claimants-1, refused, "every loser must be told a daemon holds the file")
}

// TestClaimPIDFileReclaimsStaleFile pins that exclusivity did not come at the
// cost of recoverability: a daemon that was SIGKILLed leaves a pid file behind,
// and the next start must take it over rather than refusing forever.
func TestClaimPIDFileReclaimsStaleFile(t *testing.T) {
	wf := claimWorkflow(t)
	target, err := pidFilePath(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))

	// 2^31-1 is guaranteed free on any system that has not wrapped its PID space.
	require.NoError(t, os.WriteFile(target, []byte("2147483646\t"+wf+"\n"), 0o644))

	livePid, _, _, claimErr := claimPIDFile(wf)
	require.NoError(t, claimErr, "a stale pid file must not block startup")
	assert.Zero(t, livePid)

	data, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), stringFromInt(os.Getpid()),
		"the reclaiming process must own the file afterwards")
}

// TestClaimPIDFileReclaimsMalformedFile pins that an unparseable pid file is
// treated as stale. A file we cannot parse names no process to defer to, so
// refusing on it would wedge startup permanently after a crash mid-write —
// which the non-atomic write made possible.
func TestClaimPIDFileReclaimsMalformedFile(t *testing.T) {
	wf := claimWorkflow(t)
	target, err := pidFilePath(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("not-a-pid\n"), 0o644))

	_, _, _, claimErr := claimPIDFile(wf)
	require.NoError(t, claimErr, "a malformed pid file must be reclaimed, not fatal")
}

// TestClaimPIDFileRefusesWhenHolderAlive pins the other direction: a live
// holder must be respected. Our own PID is the only one we can guarantee is
// alive.
func TestClaimPIDFileRefusesWhenHolderAlive(t *testing.T) {
	wf := claimWorkflow(t)
	target, err := pidFilePath(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target,
		[]byte(stringFromInt(os.Getpid())+"\t"+wf+"\n"), 0o644))

	livePid, recorded, _, claimErr := claimPIDFile(wf)
	require.Error(t, claimErr)
	assert.Equal(t, os.Getpid(), livePid, "the operator message needs the holder's pid")
	assert.Equal(t, wf, recorded)
	assert.Contains(t, claimErr.Error(), "another daemon already running")
}

// TestClaimPIDFileLeavesNoFileWhenRefused pins that a refused claim does not
// disturb the incumbent's file. Clobbering it would hand ownership to a daemon
// that is about to exit, stranding the live one.
func TestClaimPIDFileLeavesNoFileWhenRefused(t *testing.T) {
	wf := claimWorkflow(t)
	target, err := pidFilePath(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	incumbent := stringFromInt(os.Getpid()) + "\t" + wf + "\n"
	require.NoError(t, os.WriteFile(target, []byte(incumbent), 0o644))

	_, _, _, claimErr := claimPIDFile(wf)
	require.Error(t, claimErr)

	data, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, incumbent, string(data),
		"a refused claim must leave the incumbent's pid file byte-identical")
}
