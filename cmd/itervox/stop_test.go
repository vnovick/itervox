//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopHelperLockEnv makes TestHelperProcessHoldsPIDLock, when this test binary
// is re-executed, take the pid lock at the given path and hold it until killed
// — a stand-in for a live lock-holding daemon.
const stopHelperLockEnv = "ITERVOX_TEST_STOP_HOLD_PID_LOCK"

func TestHelperProcessHoldsPIDLock(t *testing.T) {
	lockPath := os.Getenv(stopHelperLockEnv)
	if lockPath == "" {
		t.Skip("helper process for the itervox stop tests; runs only when re-executed")
	}
	release, acquired, err := tryLockPIDFile(lockPath)
	if err != nil || !acquired {
		t.Fatalf("lock failed: acquired=%v err=%v", acquired, err)
	}
	// The release closure is what keeps the lock file's *os.File reachable.
	// Discarding it let the GC finalize the file and close the fd, which
	// drops the flock, so under a loaded run `itervox stop` found the lock
	// free and reported the holder stale (TestStopSignalsLockHolder flake,
	// M1-close E gate run). Production keeps it in heldPIDLocks. The forced
	// collections below pin that regression: without the defer they make
	// the test fail every time.
	defer release()
	runtime.GC()
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	fmt.Println("locked")
	// Not `select {}`: with every goroutine blocked, the runtime's deadlock
	// detector killed this helper ("all goroutines are asleep") right after
	// it printed "locked", releasing the lock — the test only passed when
	// `itervox stop` won that race. A sleeping goroutine is not a deadlock;
	// the parent kills the helper in t.Cleanup (or stop signals it).
	time.Sleep(10 * time.Minute)
}

// watchedProcess is a child whose exit the test can observe without racing
// the kernel (a zombie still answers signal 0, so processAlive is not enough).
type watchedProcess struct {
	pid    int
	exited chan struct{}
}

func watch(t *testing.T, cmd *exec.Cmd) watchedProcess {
	t.Helper()
	w := watchedProcess{pid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(w.exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-w.exited })
	return w
}

// startUnrelatedProcess stands in for an unrelated process of the same user
// (the editor, a shell) that has inherited a dead daemon's pid.
func startUnrelatedProcess(t *testing.T) watchedProcess {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	return watch(t, cmd)
}

// startLockHolder re-executes the test binary as a process that holds the
// project's pid lock, i.e. a live CORE-039 daemon as far as stop can tell.
func startLockHolder(t *testing.T, pidPath string) watchedProcess {
	t.Helper()
	lockPath := pidLockPath(pidPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o700))
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessHoldsPIDLock$")
	cmd.Env = append(os.Environ(), stopHelperLockEnv+"="+lockPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	w := watch(t, cmd)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "locked", strings.TrimSpace(line))
	return w
}

// stopFixture is a project dir with WORKFLOW.md; wf is the path handed to
// stop and recorded in pid files.
func stopFixture(t *testing.T) (wf, pidPath string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	wf = filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	pidPath, err = pidFilePath(wf)
	require.NoError(t, err)
	return wf, pidPath
}

func noDiscover(string) []int { return nil }

func runStopForTest(wf string, opts stopOptions) (int, string) {
	if opts.discover == nil {
		opts.discover = noDiscover
	}
	if opts.grace == 0 {
		opts.grace = 5 * time.Second
	}
	var out bytes.Buffer
	code := stopProject(wf, opts, &out)
	return code, out.String()
}

func assertStillAlive(t *testing.T, p watchedProcess) {
	t.Helper()
	select {
	case <-p.exited:
		t.Fatalf("pid %d was signalled by itervox stop", p.pid)
	case <-time.After(200 * time.Millisecond):
	}
}

func assertExits(t *testing.T, p watchedProcess) {
	t.Helper()
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		t.Fatalf("pid %d was not stopped", p.pid)
	}
}

// TestStopDoesNotSignalReusedPIDWhenLockFree is CORE-163's acceptance. The
// daemon that wrote a lock-marked record is gone (nobody holds the lock) and
// an unrelated live process now owns its pid. stop must remove the stale
// record and signal nothing — before the fix it SIGTERMed the process.
func TestStopDoesNotSignalReusedPIDWhenLockFree(t *testing.T) {
	for _, runtimeDirExists := range []bool{true, false} {
		t.Run(fmt.Sprintf("runtime_dir_exists=%v", runtimeDirExists), func(t *testing.T) {
			wf, pidPath := stopFixture(t)
			victim := startUnrelatedProcess(t)
			seedPIDFile(t, wf, fmt.Sprintf("%d\t%s\t%s\n", victim.pid, wf, pidRecordLockMarker))
			if runtimeDirExists {
				require.NoError(t, os.MkdirAll(filepath.Dir(pidLockPath(pidPath)), 0o700))
			}

			code, out := runStopForTest(wf, stopOptions{})

			assertStillAlive(t, victim)
			assert.Equal(t, 0, code, out)
			assert.Contains(t, out, "stale")
			_, err := os.Stat(pidPath)
			assert.True(t, os.IsNotExist(err), "stale record must be removed, stat err=%v", err)
		})
	}
}

// Positive control: while a process holds the pid lock, stop signals the pid
// its lock-marked record names, and removes the record afterwards.
func TestStopSignalsLockHolder(t *testing.T) {
	wf, pidPath := stopFixture(t)
	holder := startLockHolder(t, pidPath)
	seedPIDFile(t, wf, fmt.Sprintf("%d\t%s\t%s\n", holder.pid, wf, pidRecordLockMarker))

	code, out := runStopForTest(wf, stopOptions{})

	assertExits(t, holder)
	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, fmt.Sprintf("PID %d: terminated sent", holder.pid))
	_, err := os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "record must be removed after the daemon stopped, stat err=%v", err)
}

// A pre-CORE-039 two-field record carries no lock evidence: stop lists the
// pid and refuses (exit 1, record kept) unless --legacy is given.
func TestStopLegacyRecordRequiresLegacyFlag(t *testing.T) {
	wf, pidPath := stopFixture(t)
	proc := startUnrelatedProcess(t)
	seedPIDFile(t, wf, fmt.Sprintf("%d\t%s\n", proc.pid, wf))

	code, out := runStopForTest(wf, stopOptions{})
	assertStillAlive(t, proc)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "--legacy")
	_, err := os.Stat(pidPath)
	require.NoError(t, err, "a refused legacy record must be kept for the operator")

	code, out = runStopForTest(wf, stopOptions{legacy: true})
	assertExits(t, proc)
	assert.Equal(t, 0, code, out)
	_, err = os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "stat err=%v", err)
}

// The directory-scan fallback obeys the same rule: with the lock free its
// hits are not lock holders and are not signalled; with the lock held and no
// record naming the holder, they are.
func TestStopDirectoryScanRequiresLock(t *testing.T) {
	t.Run("lock_free", func(t *testing.T) {
		wf, _ := stopFixture(t)
		proc := startUnrelatedProcess(t)
		code, out := runStopForTest(wf, stopOptions{discover: func(string) []int { return []int{proc.pid} }})
		assertStillAlive(t, proc)
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "--legacy")
	})
	t.Run("lock_held_no_record", func(t *testing.T) {
		wf, pidPath := stopFixture(t)
		holder := startLockHolder(t, pidPath)
		code, out := runStopForTest(wf, stopOptions{discover: func(string) []int { return []int{holder.pid} }})
		assertExits(t, holder)
		assert.Equal(t, 0, code, out)
	})
	t.Run("lock_held_record_names_holder", func(t *testing.T) {
		wf, pidPath := stopFixture(t)
		holder := startLockHolder(t, pidPath)
		bystander := startUnrelatedProcess(t)
		seedPIDFile(t, wf, fmt.Sprintf("%d\t%s\t%s\n", holder.pid, wf, pidRecordLockMarker))
		code, out := runStopForTest(wf, stopOptions{discover: func(string) []int { return []int{bystander.pid, holder.pid} }})
		assertExits(t, holder)
		assertStillAlive(t, bystander)
		assert.Equal(t, 1, code, out)
	})
}
