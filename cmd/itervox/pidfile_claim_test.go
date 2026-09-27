package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// Release any pid lock a claim in this test took (CORE-039): the lock is
	// held by this process until removePIDFile, like a daemon's.
	t.Cleanup(func() { removePIDFile(wf) })
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
// holder must be respected. Two shapes of "live holder" exist since CORE-039:
//   - a lock-holding daemon (here: a first claim, which holds the flock through
//     its own open file description exactly as a separate daemon would);
//   - a pre-CORE-039 daemon with a legacy record and a live pid (here: a real
//     foreign process). Seeding our own pid no longer works: a same-pid record
//     is stale by construction, which is the CORE-039 fix.
func TestClaimPIDFileRefusesWhenHolderAlive(t *testing.T) {
	t.Run("lock holder", func(t *testing.T) {
		wf := claimWorkflow(t)
		_, _, _, first := claimPIDFile(wf)
		require.NoError(t, first)
		holderPid, _, _, rerr := readPIDFile(wf)
		require.NoError(t, rerr)

		livePid, recorded, _, claimErr := claimPIDFile(wf)
		require.Error(t, claimErr)
		assert.Equal(t, holderPid, livePid, "the operator message needs the holder's pid")
		abs, _ := filepath.Abs(wf)
		assert.Equal(t, abs, recorded)
		assert.Contains(t, claimErr.Error(), "another daemon already running")
	})
	t.Run("legacy record with live pid", func(t *testing.T) {
		wf := claimWorkflow(t)
		foreign := startForeignProcess(t)
		seedPIDFile(t, wf, strconv.Itoa(foreign)+"\t"+wf+"\n")

		livePid, recorded, _, claimErr := claimPIDFile(wf)
		require.Error(t, claimErr)
		assert.Equal(t, foreign, livePid, "the operator message needs the holder's pid")
		assert.Equal(t, wf, recorded)
		assert.Contains(t, claimErr.Error(), "another daemon already running")
	})
}

// TestClaimPIDFileLeavesNoFileWhenRefused pins that a refused claim does not
// disturb the incumbent's file. Clobbering it would hand ownership to a daemon
// that is about to exit, stranding the live one.
func TestClaimPIDFileLeavesNoFileWhenRefused(t *testing.T) {
	t.Run("lock holder", func(t *testing.T) {
		wf := claimWorkflow(t)
		_, _, _, first := claimPIDFile(wf)
		require.NoError(t, first)
		target, err := pidFilePath(wf)
		require.NoError(t, err)
		incumbent, err := os.ReadFile(target)
		require.NoError(t, err)

		_, _, _, claimErr := claimPIDFile(wf)
		require.Error(t, claimErr)

		data, readErr := os.ReadFile(target)
		require.NoError(t, readErr)
		assert.Equal(t, string(incumbent), string(data),
			"a refused claim must leave the incumbent's pid file byte-identical")
	})
	t.Run("legacy record with live pid", func(t *testing.T) {
		wf := claimWorkflow(t)
		incumbent := strconv.Itoa(startForeignProcess(t)) + "\t" + wf + "\n"
		target := seedPIDFile(t, wf, incumbent)

		_, _, _, claimErr := claimPIDFile(wf)
		require.Error(t, claimErr)

		data, readErr := os.ReadFile(target)
		require.NoError(t, readErr)
		assert.Equal(t, incumbent, string(data),
			"a refused claim must leave the incumbent's pid file byte-identical")
	})
}

// startForeignProcess starts a live process that is NOT this test binary and
// returns its pid. It stands in for "some other process is alive with the
// recorded pid" — either a legacy daemon that predates the lock, or an
// unrelated process that inherited a recycled pid.
func startForeignProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// seedPIDFile writes raw content into wf's pid file, creating .itervox/.
func seedPIDFile(t *testing.T, wf, content string) string {
	t.Helper()
	target, err := pidFilePath(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
	return target
}

// TestClaimPIDFileTreatsOwnPIDAsStale is CORE-039's acceptance. A container
// entrypoint gets the same pid on every restart, and Docker's stop timeout can
// SIGKILL before the deferred removePIDFile runs, so the restarted daemon finds
// a file holding ITS OWN pid. No lock backs that file (the kernel released the
// dead daemon's flock), so it is stale and must be reclaimed — refusing here is
// a crash loop.
func TestClaimPIDFileTreatsOwnPIDAsStale(t *testing.T) {
	wf := claimWorkflow(t)
	abs, err := filepath.Abs(wf)
	require.NoError(t, err)
	target := seedPIDFile(t, wf,
		strconv.Itoa(os.Getpid())+"\t"+abs+"\t"+pidRecordLockMarker+"\n")

	livePid, _, _, claimErr := claimPIDFile(wf)
	require.NoError(t, claimErr, "a same-pid restart must reclaim its own stale pid file")
	assert.Zero(t, livePid)

	data, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\t"+abs+"\t"+pidRecordLockMarker+"\n", string(data))

	// The reclaim must leave a REAL claim behind: a second claimant (a separate
	// open file description, exactly as a second daemon would have) is refused.
	_, _, _, again := claimPIDFile(wf)
	require.Error(t, again, "the reclaimed file must be held, not merely rewritten")
	assert.Contains(t, again.Error(), "another daemon already running")
}

// TestClaimPIDFileReclaimsReusedForeignPID pins the pid-reuse half: a record
// written by a lock-holding daemon names a pid that some UNRELATED process now
// owns. processAlive says "alive", but the lock is free, so the daemon that
// wrote the record is gone and the file is stale. Before the lock, this case
// refused startup until an operator deleted the file by hand.
func TestClaimPIDFileReclaimsReusedForeignPID(t *testing.T) {
	wf := claimWorkflow(t)
	abs, err := filepath.Abs(wf)
	require.NoError(t, err)
	foreign := startForeignProcess(t)
	require.True(t, processAlive(foreign))
	seedPIDFile(t, wf, strconv.Itoa(foreign)+"\t"+abs+"\t"+pidRecordLockMarker+"\n")

	_, _, _, claimErr := claimPIDFile(wf)
	require.NoError(t, claimErr, "a locked-format record whose lock is free is stale even if its pid is alive")
}

// TestClaimPIDFileLegacyRecordCompat pins the migration rule for the
// pre-CORE-039 two-field record (`pid\tworkflow`), which an older daemon
// writes WITHOUT holding the lock:
//   - live foreign pid  -> refused (an older daemon may be running across an upgrade)
//   - our own pid       -> stale by construction, reclaimed
//   - readPIDFile (stop/status/doctor) parses both shapes to the same workflow
func TestClaimPIDFileLegacyRecordCompat(t *testing.T) {
	t.Run("live foreign pid is refused", func(t *testing.T) {
		wf := claimWorkflow(t)
		foreign := startForeignProcess(t)
		legacy := strconv.Itoa(foreign) + "\t" + wf + "\n"
		target := seedPIDFile(t, wf, legacy)

		livePid, recorded, _, claimErr := claimPIDFile(wf)
		require.Error(t, claimErr)
		assert.Contains(t, claimErr.Error(), "another daemon already running")
		assert.Equal(t, foreign, livePid)
		assert.Equal(t, wf, recorded)
		data, readErr := os.ReadFile(target)
		require.NoError(t, readErr)
		assert.Equal(t, legacy, string(data), "a refused claim must not touch the legacy holder's file")

		// The refusal must also release the lock, or the legacy daemon's
		// successor could never start from this process.
		_ = os.Remove(target)
		_, _, _, retry := claimPIDFile(wf)
		require.NoError(t, retry, "a refused claim must not strand the lock")
	})

	t.Run("own pid is reclaimed", func(t *testing.T) {
		wf := claimWorkflow(t)
		seedPIDFile(t, wf, strconv.Itoa(os.Getpid())+"\t"+wf+"\n")
		_, _, _, claimErr := claimPIDFile(wf)
		require.NoError(t, claimErr, "a legacy file holding our own pid cannot belong to another live process")
	})

	t.Run("readPIDFile parses both shapes", func(t *testing.T) {
		wf := claimWorkflow(t)
		abs, err := filepath.Abs(wf)
		require.NoError(t, err)
		for _, content := range []string{
			"4242\t" + abs + "\n",
			"4242\t" + abs + "\t" + pidRecordLockMarker + "\n",
		} {
			seedPIDFile(t, wf, content)
			pid, recorded, _, rerr := readPIDFile(wf)
			require.NoError(t, rerr, content)
			assert.Equal(t, 4242, pid, content)
			assert.Equal(t, abs, recorded,
				"stop.go compares the recorded workflow to filepath.Abs; the marker must not leak into it")
		}
	})
}
