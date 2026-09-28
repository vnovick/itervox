package agent_test

// M1-B9 (CORE-155): on an SSH worker a cancelled turn used to kill only the
// LOCAL ssh client (CORE-001's group kill). With no PTY (ssh -T) nothing
// SIGHUPs the remote side, so the remote agent — and everything it spawned —
// kept running on the worker. The remote wrapper now runs the agent in its
// own process group and kills that group (TERM, then KILL after a grace)
// when the ssh channel's stdin reaches EOF, which is what sshd delivers when
// the client dies. Every row runs through the faithful fake ssh, whose fake
// sshd runs the remote side in its own session and closes its stdin when
// the client process dies (ssh_fake_test.go).

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// remoteKillBound is how long after RunTurn returns every remote process of
// a cancelled turn may take to disappear: the wrapper's TERM grace (2 s)
// plus scheduling slack for a loaded CI host.
const remoteKillBound = 6 * time.Second

// processAlive reports whether pid names a live (non-zombie) process.
func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false // ps exits non-zero when no such process exists
	}
	st := strings.TrimSpace(string(out))
	return st != "" && !strings.HasPrefix(st, "Z")
}

// livePIDsInGroup lists the live (non-zombie) members of process group pgid.
func livePIDsInGroup(t *testing.T, pgid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,pgid=,stat=").Output()
	require.NoError(t, err)
	var live []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != strconv.Itoa(pgid) || strings.HasPrefix(f[2], "Z") {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			live = append(live, pid)
		}
	}
	return live
}

// readPIDFile polls path until it holds a pid or the deadline passes.
// startupWait bounds how long a test waits for a remote agent to START
// (M3-close item 12). Starting is ssh wrapper + login shell + agent exec and
// is pure scheduling latency: under a loaded host (a parallel verify, other
// agents probing) 20 s was not enough, so the wait scales. It is base times
// ITERVOX_TEST_TIMEOUT_FACTOR (when > 1), raised to a quarter of the time
// left before the test binary's -timeout deadline, capped at 2 min. Only
// startup waits use it: the kill bounds (remoteKillBound) stay strict,
// because they are the behaviour under test.
func startupWait(t *testing.T, base time.Duration) time.Duration {
	t.Helper()
	d := base
	if f, err := strconv.ParseFloat(os.Getenv("ITERVOX_TEST_TIMEOUT_FACTOR"), 64); err == nil && f > 1 {
		d = time.Duration(float64(d) * f)
	}
	if dl, ok := t.Deadline(); ok {
		if quarter := time.Until(dl) / 4; quarter > d {
			d = quarter
		}
	}
	return min(max(d, base), startupWaitMax)
}

// startupWaitMax caps startupWait so a genuinely broken startup still fails
// in bounded time.
const startupWaitMax = 2 * time.Minute

func readPIDFile(t *testing.T, path string, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared: the remote agent did not start", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sessionLeaders returns the remote session leader pids the fake sshd has
// recorded so far.
func sessionLeaders(t *testing.T, path string) []int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(f)
		require.NoError(t, err)
		pids = append(pids, pid)
	}
	return pids
}

// installSessionLog makes the fake sshd record each remote session leader,
// and reaps anything the session left behind when the test ends (on the
// unfixed tree the remote agent survives the turn by design of the bug).
func installSessionLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.log")
	t.Setenv("FAKE_SSHD_SESSION_LOG", path)
	return path
}

// writeSleepingAgentIgnoring writes a remote fake agent that records its pid
// and process group, starts a child shell that starts a background
// grandchild `sleep` and records the grandchild's pid, prints firstLine, and
// then "thinks" (sleeps) without writing anything — the case sshd's EPIPE
// never reaches. The agent and its descendants ignore the given signals
// (space-separated names; "" none): "TERM" leaves only the KILL escalation
// able to stop them.
func writeSleepingAgentIgnoring(t *testing.T, dir, outDir, firstLine, ignore string) string {
	t.Helper()
	trap := ""
	if ignore != "" {
		trap = "trap '' " + ignore + "\n"
	}
	exe := filepath.Join(dir, "sleeping-agent")
	script := fmt.Sprintf(`#!/bin/sh
%[3]sps -o pgid= -p $$ | tr -d " " > %[1]s/agent.pgid
echo $$ > %[1]s/agent.pid.tmp && mv %[1]s/agent.pid.tmp %[1]s/agent.pid
/bin/sh -c '/bin/sleep 300 & echo $! > %[1]s/gc.pid.tmp && mv %[1]s/gc.pid.tmp %[1]s/gc.pid; wait' &
echo '%[2]s'
/bin/sleep 300
`, outDir, firstLine, trap)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// remoteTurn is one SSH turn started in the background by startRemoteTurn.
type remoteTurn struct {
	agentPID, grandchildPID int
	agentPGID               int // the agent's process group (the wrapper's kill target)
	done                    chan struct{}
	returned                time.Time
}

// startRemoteTurn runs rc's RunTurn over the faithful fake ssh with a
// sleeping remote agent, and returns once the agent and its grandchild are
// both running. Any remote pid still alive at the end of the test is killed.
func startRemoteTurn(t *testing.T, ctx context.Context, rc remoteRunnerCase, ignoreTerm bool) *remoteTurn {
	t.Helper()
	ignore := ""
	if ignoreTerm {
		ignore = "TERM"
	}
	return startRemoteTurnIgnoring(t, ctx, rc, ignore)
}

// startRemoteTurnIgnoring is startRemoteTurn with an agent that ignores the
// given signals (see writeSleepingAgentIgnoring).
func startRemoteTurnIgnoring(t *testing.T, ctx context.Context, rc remoteRunnerCase, ignore string) *remoteTurn {
	t.Helper()
	bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
	exe := writeSleepingAgentIgnoring(t, bin, out, rc.firstLine, ignore)
	rt := &remoteTurn{done: make(chan struct{})}
	go func() {
		defer close(rt.done)
		_, _ = rc.runner.RunTurn(ctx, slog.Default(), nil, nil, "think for a long time", ws, exe,
			"worker.example.test", filepath.Join(bin, "logs"), 600000, 0, agent.PermissionBypass)
		rt.returned = time.Now()
	}()
	rt.agentPID = readPIDFile(t, filepath.Join(out, "agent.pid"), startupWait(t, 20*time.Second))
	rt.grandchildPID = readPIDFile(t, filepath.Join(out, "gc.pid"), startupWait(t, 20*time.Second))
	rt.agentPGID = readPIDFile(t, filepath.Join(out, "agent.pgid"), startupWait(t, 20*time.Second))
	t.Cleanup(func() {
		for _, pid := range []int{rt.agentPID, rt.grandchildPID} {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return rt
}

// assertRemoteGone waits for the turn to return and then requires both
// remote pids, and every remaining member of each remote session group (the
// login shell, the wrapper and its watcher), to be gone within
// remoteKillBound. It returns how long after RunTurn returned the agent and
// grandchild were gone, and how long until the whole session was.
func assertRemoteGone(t *testing.T, rt *remoteTurn, leaders []int) (agentGone, sessionGone time.Duration) {
	t.Helper()
	select {
	case <-rt.done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunTurn did not return after cancel")
	}
	deadline := rt.returned.Add(remoteKillBound)
	agentGone = -1
	for {
		var alive []string
		for name, pid := range map[string]int{"agent": rt.agentPID, "grandchild": rt.grandchildPID} {
			if processAlive(pid) {
				alive = append(alive, fmt.Sprintf("%s pid %d", name, pid))
			}
		}
		if len(alive) == 0 && agentGone < 0 {
			agentGone = time.Since(rt.returned)
		}
		// The agent's group also holds the watcher, which outlives a TERM by
		// its 2 s grace; it counts toward the whole-session time only.
		if live := livePIDsInGroup(t, rt.agentPGID); rt.agentPGID > 0 && len(live) > 0 {
			alive = append(alive, fmt.Sprintf("agent process group %d: %v", rt.agentPGID, live))
		}
		for _, leader := range leaders {
			if live := livePIDsInGroup(t, leader); len(live) > 0 {
				alive = append(alive, fmt.Sprintf("remote session group %d: %v", leader, live))
			}
		}
		if len(alive) == 0 {
			return agentGone, time.Since(rt.returned)
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running %s after RunTurn returned: %s", remoteKillBound, strings.Join(alive, "; "))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// termBound is how soon a remote agent that honours SIGTERM (and its
// grandchild) must be gone: well inside the 2 s KILL grace, so a pass proves
// the TERM path, not the escalation.
const termBound = 1500 * time.Millisecond

// TestSSHCancelKillsRemoteAgent is CORE-155's reproduction: cancel an SSH
// turn whose remote agent is thinking (sleeping, writing nothing) and has a
// grandchild; both must be gone within remoteKillBound. The ignore_term rows
// prove the KILL escalation; the others that TERM is enough and prompt.
func TestSSHCancelKillsRemoteAgent(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	for _, wb := range wrapperBashes(t) {
		for _, shell := range []string{"/bin/bash", "/bin/sh"} {
			if wb == "bin_bash" && shell != "/bin/bash" {
				continue // the login shell only hands the wrapper to bash: one row per wrapper bash is enough
			}
			for _, rc := range remoteRunnerCases() {
				for _, ignoreTerm := range []bool{false, true} {
					name := wb + "/" + shellLabel(shell) + "/" + rc.name + "/honours_term"
					if ignoreTerm {
						name = wb + "/" + shellLabel(shell) + "/" + rc.name + "/ignores_term"
					}
					t.Run(name, func(t *testing.T) {
						useWrapperBash(t, wb, installFaithfulFakeSSH(t, shell))
						sessions := installSessionLog(t)
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						rt := startRemoteTurn(t, ctx, rc, ignoreTerm)
						leaders := sessionLeaders(t, sessions)
						require.Len(t, leaders, 1, "one ssh connection, one remote session")
						t.Cleanup(func() { _ = syscall.Kill(-leaders[0], syscall.SIGKILL) })

						cancel()
						agentGone, sessionGone := assertRemoteGone(t, rt, leaders)
						t.Logf("after RunTurn returned: remote agent %d and grandchild %d gone in %s, whole remote session in %s",
							rt.agentPID, rt.grandchildPID, agentGone.Round(time.Millisecond), sessionGone.Round(time.Millisecond))
						if !ignoreTerm {
							assert.Less(t, agentGone, termBound, "SIGTERM must stop an agent that honours it without waiting for the KILL grace")
						}
					})
				}
			}
		}
	}
}

// TestSSHDaemonShutdownKillsRemoteAgents: daemon shutdown cancels the root
// context every worker's turn derives from. Several concurrent SSH turns
// (both runners) under one parent context must all have their remote agents
// and grandchildren killed when that parent is cancelled.
func TestSSHDaemonShutdownKillsRemoteAgents(t *testing.T) {
	installFaithfulFakeSSH(t, "/bin/bash")
	sessions := installSessionLog(t)
	root, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	// Started one after another (each start waits for its remote pids), so
	// all four run concurrently by the time the daemon shuts down.
	var turns []*remoteTurn
	for i := range 4 {
		turnCtx, cancel := context.WithCancel(root) // a worker's own turn context
		t.Cleanup(cancel)
		turns = append(turns, startRemoteTurn(t, turnCtx, remoteRunnerCases()[i%2], i >= 2))
	}
	require.Len(t, turns, 4)
	leaders := sessionLeaders(t, sessions)
	require.Len(t, leaders, 4, "four ssh connections, four remote sessions")
	for _, l := range leaders {
		t.Cleanup(func() { _ = syscall.Kill(-l, syscall.SIGKILL) })
	}

	shutdown()
	for i, rt := range turns {
		agentGone, sessionGone := assertRemoteGone(t, rt, leaders)
		t.Logf("turn %d: after RunTurn returned: remote agent %d and grandchild %d gone in %s, whole remote session in %s",
			i, rt.agentPID, rt.grandchildPID, agentGone.Round(time.Millisecond), sessionGone.Round(time.Millisecond))
	}
}

// writeCompletingAgent writes a remote fake agent that thinks briefly
// (longer than the watcher needs to start), writes a handoff into the
// workspace, prints firstLine, and exits with exitCode.
func writeCompletingAgent(t *testing.T, dir, firstLine string, exitCode int) string {
	t.Helper()
	return writeCompletingAgentFor(t, dir, firstLine, exitCode, 1)
}

// writeCompletingAgentFor is writeCompletingAgent with a chosen thinking
// time; it also records the agent's process group in <dir>/agent.pgid, and
// leaves a background child in that group (<dir>/leftover.pid) that
// outlives the agent — a normally completed turn must never be signalled,
// so the child must survive it.
func writeCompletingAgentFor(t *testing.T, dir, firstLine string, exitCode, thinkSeconds int) string {
	t.Helper()
	exe := filepath.Join(dir, "completing-agent")
	script := fmt.Sprintf(`#!/bin/sh
ps -o pgid= -p $$ | tr -d " " > %[4]s/agent.pgid
/bin/sleep 30 >/dev/null 2>&1 </dev/null &
echo $! > %[4]s/leftover.pid
/bin/sleep %[3]d
mkdir -p .itervox/handoff
printf 'handoff body\n' > .itervox/handoff/2026-09-26T00-00-00Z_worker.md
echo '%[1]s'
exit %[2]d
`, firstLine, exitCode, thinkSeconds, dir)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// TestSSHNormalCompletionUnaffected: the kill-on-EOF watcher must not touch
// a turn that ends by itself — the exit status (0, and a failure status
// through codex's pipefail/tee pipeline), the parsed output and the handoff
// written into the workspace are unchanged — and it must not outlive the
// turn: the remote session is empty once RunTurn returns.
func TestSSHNormalCompletionUnaffected(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	for _, shell := range availableLoginShells() {
		for _, rc := range remoteRunnerCases() {
			for _, exitCode := range []int{0, 7} {
				t.Run(fmt.Sprintf("%s/%s/exit_%d", shellLabel(shell), rc.name, exitCode), func(t *testing.T) {
					installFaithfulFakeSSH(t, shell)
					sessions := installSessionLog(t)
					bin, ws := t.TempDir(), t.TempDir()
					exe := writeCompletingAgent(t, bin, rc.firstLine, exitCode)

					result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
						nil, "finish normally", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
						30000, 60000, agent.PermissionBypass)
					require.NoError(t, err)

					wantSession := map[string]string{"claude": "s1", "codex": "t1"}[rc.name]
					assert.Equal(t, wantSession, result.SessionID, "the agent's output must reach the parser intact")
					if exitCode == 0 {
						assert.False(t, result.Failed, "FailureText: %s", result.FailureText)
					} else {
						assert.True(t, result.Failed, "a non-zero remote exit must fail the turn")
						assert.Contains(t, result.FailureText, fmt.Sprintf("exit status %d", exitCode), "the agent's own exit status must survive the wrapper")
					}
					leftover := readPIDFile(t, filepath.Join(bin, "leftover.pid"), 5*time.Second)
					t.Cleanup(func() { _ = syscall.Kill(leftover, syscall.SIGKILL) })
					handoff, err := os.ReadFile(filepath.Join(ws, ".itervox", "handoff", "2026-09-26T00-00-00Z_worker.md"))
					require.NoError(t, err, "the agent must run to completion and write its handoff")
					assert.Equal(t, "handoff body\n", string(handoff))

					leaders := sessionLeaders(t, sessions)
					require.Len(t, leaders, 1)
					deadline := time.Now().Add(2 * time.Second)
					for len(livePIDsInGroup(t, leaders[0])) > 0 {
						if time.Now().After(deadline) {
							t.Fatalf("remote session group %d still has members after the turn: %v", leaders[0], livePIDsInGroup(t, leaders[0]))
						}
						time.Sleep(25 * time.Millisecond)
					}
				})
			}
		}
	}
}

// wrapperBashes lists which bash runs the remote wrapper: the first `bash`
// on PATH, and — when it is a different binary — /bin/bash, reached by
// putting the fake ssh's directory then /bin and /usr/bin first on PATH.
// On macOS that is bash 3.2, whose `read -t` returns 1 on a timeout (bash 4+
// returns >128) and which has no BASHPID, so the round-2 watcher logic must
// be exercised under both.
func wrapperBashes(t *testing.T) []string {
	t.Helper()
	first, err := exec.LookPath("bash")
	require.NoError(t, err)
	out := []string{"path_bash"}
	if a, errA := filepath.EvalSymlinks(first); errA == nil {
		if b, errB := filepath.EvalSymlinks("/bin/bash"); errB == nil && a != b {
			out = append(out, "bin_bash")
		}
	}
	return out
}

// useWrapperBash applies a wrapperBashes choice after installFaithfulFakeSSH
// (fakeLog is its return value, which lives in the fake's directory).
func useWrapperBash(t *testing.T, which, fakeLog string) {
	t.Helper()
	if which == "bin_bash" {
		t.Setenv("PATH", filepath.Dir(fakeLog)+string(os.PathListSeparator)+"/bin:/usr/bin:/usr/sbin:/sbin")
	}
}

// installLoginProfile makes the wrapper's `bash -l` run body as the remote
// user's ~/.bash_profile.
func installLoginProfile(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(body), 0o644))
	t.Setenv("HOME", home)
}

// TestSSHWatcherIgnoresReadonlyTMOUT (M1-B9 round 2, V1): CIS-hardened
// hosts set `readonly TMOUT=900` in the login profile, and bash's `read`
// uses TMOUT as its default timeout. A watcher whose read timed out used
// to fall through to the kill and stop a live turn after TMOUT seconds.
// With TMOUT=1 a 3 s turn must finish normally, and a cancel must still
// kill the remote agent within the bound.
func TestSSHWatcherIgnoresReadonlyTMOUT(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	const profile = "TMOUT=1\nreadonly TMOUT\nexport TMOUT\n"
	for _, wb := range wrapperBashes(t) {
		for _, shell := range []string{"/bin/bash", "/bin/sh"} {
			if wb == "bin_bash" && shell != "/bin/bash" {
				continue // the login shell only hands the wrapper to bash: one row per wrapper bash is enough
			}
			for _, rc := range remoteRunnerCases()[:1] { // claude: TMOUT acts on the shared wrapper
				t.Run(wb+"/"+shellLabel(shell)+"/"+rc.name+"/completes", func(t *testing.T) {
					useWrapperBash(t, wb, installFaithfulFakeSSH(t, shell))
					installLoginProfile(t, profile)
					bin, ws := t.TempDir(), t.TempDir()
					exe := writeCompletingAgentFor(t, bin, rc.firstLine, 0, 3)
					start := time.Now()
					result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
						nil, "think past TMOUT", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
						30000, 60000, agent.PermissionBypass)
					took := time.Since(start)
					require.NoError(t, err)
					assert.False(t, result.Failed, "a readonly TMOUT must not kill a live turn; FailureText: %s", result.FailureText)
					assert.Equal(t, map[string]string{"claude": "s1", "codex": "t1"}[rc.name], result.SessionID)
					assert.GreaterOrEqual(t, took, 3*time.Second, "the agent must run its full 3 s (three TMOUTs)")
					_, statErr := os.Stat(filepath.Join(ws, ".itervox", "handoff", "2026-09-26T00-00-00Z_worker.md"))
					assert.NoError(t, statErr, "the agent must run to completion")
					t.Logf("turn with readonly TMOUT=1 finished in %s", took.Round(time.Millisecond))
				})
				t.Run(wb+"/"+shellLabel(shell)+"/"+rc.name+"/cancel_still_kills", func(t *testing.T) {
					useWrapperBash(t, wb, installFaithfulFakeSSH(t, shell))
					installLoginProfile(t, profile)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					rt := startRemoteTurn(t, ctx, rc, false)
					time.Sleep(2 * time.Second) // twice TMOUT: the watcher must not have fired
					require.True(t, processAlive(rt.agentPID), "the remote agent died on its own before cancel (TMOUT fired the watcher)")
					cancel()
					agentGone, _ := assertRemoteGone(t, rt, nil)
					t.Logf("after cancel: remote agent tree gone in %s", agentGone.Round(time.Millisecond))
				})
			}
		}
	}
}

// TestSSHCancelNeverSignalsOutsideSession (M1-B9 round 2, V2): Dropbear
// calls setsid once per CONNECTION and runs every session channel in that
// one process group, so a `kill 0` from the wrapper hit the connection
// server and every sibling session on it (with ssh ControlMaster, every
// agent on the worker). The wrapper may signal only its own agent's
// process group: after a cancel the agent tree is gone, and the connection
// server and a sibling session are alive — also after the 2 s KILL.
func TestSSHCancelNeverSignalsOutsideSession(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	for _, wb := range wrapperBashes(t) {
		for _, shell := range []string{"/bin/bash", "/bin/sh"} {
			if wb == "bin_bash" && shell != "/bin/bash" {
				continue // the login shell only hands the wrapper to bash: one row per wrapper bash is enough
			}
			for _, rc := range remoteRunnerCases() {
				t.Run(wb+"/"+shellLabel(shell)+"/"+rc.name, func(t *testing.T) {
					useWrapperBash(t, wb, installFaithfulFakeSSH(t, shell))
					t.Setenv("FAKE_SSHD_MODEL", "dropbear")
					serverLog := filepath.Join(t.TempDir(), "dropbear.log")
					t.Setenv("FAKE_SSHD_DROPBEAR_LOG", serverLog)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					rt := startRemoteTurn(t, ctx, rc, false)
					b, err := os.ReadFile(serverLog)
					require.NoError(t, err, "the Dropbear-model server never started")
					f := strings.Fields(string(b))
					require.Len(t, f, 2)
					server, err := strconv.Atoi(f[0])
					require.NoError(t, err)
					sibling, err := strconv.Atoi(f[1])
					require.NoError(t, err)
					t.Cleanup(func() {
						_ = syscall.Kill(-server, syscall.SIGKILL)
						_ = syscall.Kill(sibling, syscall.SIGKILL)
					})
					if rt.agentPGID == server {
						// The agent runs in the connection's own group (a wrapper
						// without a group of its own): that group must stay alive,
						// so "agent tree gone" is judged by the agent and
						// grandchild pids alone.
						rt.agentPGID = 0
					}

					cancel()
					agentGone, groupGone := assertRemoteGone(t, rt, nil)
					t.Logf("agent and grandchild gone %s, agent group empty %s after RunTurn returned", agentGone.Round(time.Millisecond), groupGone.Round(time.Millisecond))
					// assertRemoteGone waited for the agent's group to empty, which happens
					// only when the watcher's final SIGKILL (after the 2 s grace) takes the
					// watcher itself: every signal it will ever send has been sent.
					assert.True(t, processAlive(server), "the connection server (pid %d) was signalled", server)
					assert.True(t, processAlive(sibling), "a sibling session (pid %d) on the same connection was signalled", sibling)
				})
			}
		}
	}
}

// TestSSHWatcherStopsPromptlyWithProfileBackgroundJob (M1-B9 round 2, V3):
// a login profile that leaves a background job used to become job %1, so
// the wrapper's `kill -PIPE %1` hit it instead of the watcher, which then
// fired on the (normally completed) session when the channel closed. The
// watcher must stop by an identity the wrapper controls and never fire
// after a normal turn: the watcher is gone promptly from the agent's
// process group, and neither the profile's background job nor a child the
// agent left behind was signalled.
func TestSSHWatcherStopsPromptlyWithProfileBackgroundJob(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	for _, wb := range wrapperBashes(t) {
		for _, shell := range []string{"/bin/bash", "/bin/sh", "/bin/tcsh"} {
			if wb == "bin_bash" && shell != "/bin/bash" {
				continue // the login shell only hands the wrapper to bash: one row per wrapper bash is enough
			}
			if _, err := os.Stat(shell); err != nil {
				continue
			}
			for _, rc := range remoteRunnerCases() {
				t.Run(wb+"/"+shellLabel(shell)+"/"+rc.name, func(t *testing.T) {
					useWrapperBash(t, wb, installFaithfulFakeSSH(t, shell))
					bgPIDFile := filepath.Join(t.TempDir(), "profile-bg.pid")
					installLoginProfile(t, "/bin/sleep 30 >/dev/null 2>&1 </dev/null &\necho $! > "+bgPIDFile+"\n")
					bin, ws := t.TempDir(), t.TempDir()
					exe := writeCompletingAgentFor(t, bin, rc.firstLine, 0, 1)

					result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
						nil, "finish normally", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
						30000, 60000, agent.PermissionBypass)
					require.NoError(t, err)
					require.False(t, result.Failed, "FailureText: %s", result.FailureText)
					returned := time.Now()
					bgPID := readPIDFile(t, bgPIDFile, 5*time.Second)
					t.Cleanup(func() { _ = syscall.Kill(bgPID, syscall.SIGKILL) })
					leftover := readPIDFile(t, filepath.Join(bin, "leftover.pid"), 5*time.Second)
					t.Cleanup(func() { _ = syscall.Kill(leftover, syscall.SIGKILL) })
					agentPGID := readPIDFile(t, filepath.Join(bin, "agent.pgid"), 5*time.Second)

					others := func() []int {
						var out []int
						for _, pid := range livePIDsInGroup(t, agentPGID) {
							if pid != leftover {
								out = append(out, pid)
							}
						}
						return out
					}
					deadline := returned.Add(remoteKillBound)
					for len(others()) > 0 {
						if time.Now().After(deadline) {
							t.Fatalf("the agent's process group %d (the watcher's) still has members %s after the turn: %v", agentPGID, remoteKillBound, others())
						}
						time.Sleep(25 * time.Millisecond)
					}
					t.Logf("watcher gone %s after RunTurn returned", time.Since(returned).Round(time.Millisecond))
					// A watcher that fired would still be in the group (sleeping out its
					// 2 s grace) and its SIGTERM would already have hit the leftover, which
					// honours TERM: both checks hold right now.
					assert.True(t, processAlive(bgPID), "the profile's background job (pid %d) was signalled after a normal turn", bgPID)
					assert.True(t, processAlive(leftover), "a child the agent left behind (pid %d) was signalled after a normal turn: the watcher fired", leftover)
				})
			}
		}
	}
}

// runProfileRows is the shared body of the round-3 login-profile rows: under
// the given ~/.bash_profile, a turn that thinks for thinkSeconds must
// complete normally, and a cancelled turn must still have its remote agent
// killed within the bound. Each row runs under both wrapper bashes.
func runProfileRows(t *testing.T, profile string, thinkSeconds int) {
	for _, wb := range wrapperBashes(t) {
		for _, rc := range remoteRunnerCases()[:1] { // claude: the profile acts on the shared wrapper, not the runner
			t.Run(wb+"/"+rc.name+"/completes", func(t *testing.T) {
				useWrapperBash(t, wb, installFaithfulFakeSSH(t, "/bin/sh"))
				installLoginProfile(t, profile)
				bin, ws := t.TempDir(), t.TempDir()
				exe := writeCompletingAgentFor(t, bin, rc.firstLine, 0, thinkSeconds)
				start := time.Now()
				result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, "think for a while", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
					30000, 60000, agent.PermissionBypass)
				took := time.Since(start)
				require.NoError(t, err)
				assert.False(t, result.Failed, "the profile must not stop a live turn; FailureText: %s", result.FailureText)
				assert.Equal(t, map[string]string{"claude": "s1", "codex": "t1"}[rc.name], result.SessionID)
				assert.GreaterOrEqual(t, took, time.Duration(thinkSeconds)*time.Second, "the agent must run its full %d s", thinkSeconds)
				t.Logf("turn finished in %s", took.Round(time.Millisecond))
			})
			t.Run(wb+"/"+rc.name+"/cancel_kills", func(t *testing.T) {
				useWrapperBash(t, wb, installFaithfulFakeSSH(t, "/bin/sh"))
				installLoginProfile(t, profile)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				rt := startRemoteTurn(t, ctx, rc, false)
				time.Sleep(4 * time.Second) // past the round-2 watcher's first 3 s read timeout
				require.True(t, processAlive(rt.agentPID), "the remote agent died on its own before cancel")
				cancel()
				agentGone, _ := assertRemoteGone(t, rt, nil)
				t.Logf("after cancel: remote agent tree gone in %s", agentGone.Round(time.Millisecond))
			})
		}
	}
}

// TestSSHWatcherSurvivesErrexitProfile (M1-B9 round 3, N1): a login profile
// that turns on errexit/nounset/pipefail made the round-2 watcher exit at
// its first read timeout (a non-zero status under `set -e`), so a later
// cancel left the remote agent running. The watcher must be immune to the
// profile's shell options.
func TestSSHWatcherSurvivesErrexitProfile(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	runProfileRows(t, "set -eu -o pipefail\n", 4)
}

// TestSSHWatcherIgnoresUnsetSECONDS (M1-B9 round 3, N4): on bash 3.2 the
// round-2 watcher told a read timeout from EOF by $SECONDS, so a profile
// that unsets SECONDS made every timeout look like EOF and killed a live
// turn after ~3 s. The watcher must not depend on SECONDS (or the clock).
func TestSSHWatcherIgnoresUnsetSECONDS(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	runProfileRows(t, "unset SECONDS\n", 5)
}

// writeCompletingAgentIgnoring writes a remote fake agent that records its
// process group in <dir>/agent.pgid, ignores the given signal, thinks for
// thinkSeconds, prints firstLine and exits 0.
func writeCompletingAgentIgnoring(t *testing.T, dir, firstLine, sig string, thinkSeconds int) string {
	t.Helper()
	exe := filepath.Join(dir, "ignoring-agent")
	script := fmt.Sprintf(`#!/bin/sh
trap '' %[3]s
ps -o pgid= -p $$ | tr -d " " > %[2]s/agent.pgid
/bin/sleep %[4]d
echo '%[1]s'
exit 0
`, firstLine, dir, sig, thinkSeconds)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// signalByName maps the table's signal names to their numbers.
var signalByName = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
	"ALRM": syscall.SIGALRM, "PIPE": syscall.SIGPIPE, "TERM": syscall.SIGTERM,
}

// TestSSHWatcherSurvivesSignalsToAgentGroup (M1-B9 round 4, D3): the watcher
// lives in the agent's process group, so a signal the agent (or one of its
// tools) sends to its own group — `kill 0`, `kill -USR1 0` — reaches it.
// Round 3's watcher died silently from HUP/USR1/USR2/PIPE/TERM (and ALRM on
// bash 5), after which a cancel left the agent running; on bash 3.2 ALRM
// ended its `read -t` like EOF and it killed the live turn. For every signal,
// on both wrapper bashes:
//   - completes: an agent that ignores the signal gets it mid-turn from its
//     group and must still finish its turn normally (rc 0) — the wrapper's
//     own job shell must not die of it either;
//   - cancel_kills: an agent that ignores the signal gets it mid-turn, stays
//     alive (the watcher did not fire), and a later cancel still kills it
//     and its grandchild within the bound (the watcher survived).
func TestSSHWatcherSurvivesSignalsToAgentGroup(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	rc := remoteRunnerCases()[0]
	for _, wb := range wrapperBashes(t) {
		for _, name := range []string{"HUP", "USR1", "USR2", "ALRM", "PIPE", "TERM"} {
			sig := signalByName[name]
			t.Run(wb+"/"+name+"/completes", func(t *testing.T) {
				useWrapperBash(t, wb, installFaithfulFakeSSH(t, "/bin/sh"))
				bin, ws := t.TempDir(), t.TempDir()
				exe := writeCompletingAgentIgnoring(t, bin, rc.firstLine, name, 2)
				type res struct {
					r   agent.TurnResult
					err error
				}
				done := make(chan res, 1)
				go func() {
					r, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
						nil, "think", ws, exe, "worker.example.test", "", 30000, 60000, agent.PermissionBypass)
					done <- res{r, err}
				}()
				pgid := readPIDFile(t, filepath.Join(bin, "agent.pgid"), startupWait(t, 20*time.Second))
				time.Sleep(300 * time.Millisecond)
				require.NoError(t, syscall.Kill(-pgid, sig), "signal the agent's own process group")
				got := <-done
				require.NoError(t, got.err)
				assert.False(t, got.r.Failed, "SIG%s to the agent's group must not end a turn whose agent ignores it; FailureText: %s", name, got.r.FailureText)
				assert.Equal(t, "s1", got.r.SessionID)
			})
			t.Run(wb+"/"+name+"/cancel_kills", func(t *testing.T) {
				useWrapperBash(t, wb, installFaithfulFakeSSH(t, "/bin/sh"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				rt := startRemoteTurnIgnoring(t, ctx, rc, name)
				require.NoError(t, syscall.Kill(-rt.agentPGID, sig), "signal the agent's own process group")
				time.Sleep(800 * time.Millisecond)
				require.True(t, processAlive(rt.agentPID), "the agent ignores SIG%s, so it must still be running (the watcher must not have fired)", name)
				cancel()
				agentGone, _ := assertRemoteGone(t, rt, nil)
				t.Logf("after SIG%s then cancel: agent tree gone in %s", name, agentGone.Round(time.Millisecond))
			})
		}
	}
}

// TestStartupWait_ScalesUnderLoad pins the load-tolerant startup bound
// (M3-close item 12): never below the base, scaled by the factor, capped.
func TestStartupWait_ScalesUnderLoad(t *testing.T) {
	base := 20 * time.Second
	if got := startupWait(t, base); got < base || got > startupWaitMax {
		t.Fatalf("startupWait = %v, want within [%v, %v]", got, base, startupWaitMax)
	}
	t.Setenv("ITERVOX_TEST_TIMEOUT_FACTOR", "3")
	if got := startupWait(t, base); got < 60*time.Second {
		t.Fatalf("factor 3: startupWait = %v, want >= 60s", got)
	}
	t.Setenv("ITERVOX_TEST_TIMEOUT_FACTOR", "1000")
	if got := startupWait(t, base); got != startupWaitMax {
		t.Fatalf("huge factor: startupWait = %v, want the %v cap", got, startupWaitMax)
	}
}
