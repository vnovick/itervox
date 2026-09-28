package agent_test

// M2-B3 (CORE-156): the prompt (SOUL + INSTRUCTIONS + template + every prior
// handoff) used to reach the agent CLI as ONE argv string. Linux caps a
// single argv string at MAX_ARG_STRLEN (131072 bytes including the NUL), so
// an SSH turn on a Linux worker failed closed with exit 98 for any prompt of
// 128 KiB or more, and a local turn failed to exec at all (E2BIG; on macOS
// ARG_MAX caps the whole argv+env at 1 MiB). The prompt now reaches the CLI
// on its stdin, which is at EOF right after the last prompt byte:
// `claude ... -p` (no prompt argument) and `codex exec ... -`.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// linuxArgStrLen is Linux's MAX_ARG_STRLEN: an argv string of this many
// bytes or more (the terminating NUL included) cannot be exec'd.
const linuxArgStrLen = 131072

// binaryishPrompt is n pseudo-random bytes from 0x01-0xff with no ' and no
// \ (and never a NUL, which rejectNULPrompt refuses): every control byte,
// CR/LF, and invalid UTF-8.
func binaryishPrompt(n int) string {
	rng := rand.New(rand.NewPCG(156, 2026))
	b := make([]byte, n)
	for i := range b {
		for {
			c := byte(1 + rng.IntN(255))
			if c != '\'' && c != '\\' {
				b[i] = c
				break
			}
		}
	}
	return string(b)
}

// largePrompts is the CORE-156 corpus: 1 MiB of quote/backslash-heavy code
// and 1 MiB of binary-ish bytes; both are 8x the Linux per-argument cap.
func largePrompts() []struct{ name, prompt string } {
	return []struct{ name, prompt string }{
		{"1MiB_code_like", codeLikePrompt(1 << 20)},
		{"1MiB_binaryish", binaryishPrompt(1 << 20)},
	}
}

// writeLargePromptAgent writes a fake agent CLI that records its argv (one
// argument per line, then its argument count), copies its stdin to
// outDir/prompt until EOF (so a prompt without EOF hangs the turn), prints
// firstLine and exits 0.
func writeLargePromptAgent(t *testing.T, dir, outDir, firstLine string) string {
	t.Helper()
	exe := filepath.Join(dir, "large-prompt-agent")
	script := fmt.Sprintf(`#!/bin/sh
: > %[1]s/started
printf '%%s\n' "$@" > %[1]s/argv
echo "$#" > %[1]s/argc
cat > %[1]s/prompt
echo '%[2]s'
`, outDir, firstLine)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// assertPromptByteExact compares the agent's recorded stdin with the prompt
// and reports the first differing offset instead of dumping a MiB.
func assertPromptByteExact(t *testing.T, outDir, prompt string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(outDir, "prompt"))
	require.NoError(t, err, "the agent never started")
	if bytes.Equal(got, []byte(prompt)) {
		return
	}
	i := 0
	for i < len(got) && i < len(prompt) && got[i] == prompt[i] {
		i++
	}
	t.Fatalf("prompt on the agent's stdin differs: want %d bytes, got %d bytes, first difference at offset %d", len(prompt), len(got), i)
}

// assertArgvCarriesNoPrompt checks the recorded argv: no argument reaches
// Linux's MAX_ARG_STRLEN, none carries prompt bytes, and the CLI was told to
// read its prompt from stdin (claude: -p with no prompt argument; codex:
// the "-" prompt argument), resuming sessionID when one was given.
func assertArgvCarriesNoPrompt(t *testing.T, outDir, runner, prompt string, sessionID *string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	require.NoError(t, err)
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	for i, a := range args {
		assert.Less(t, len(a)+1, linuxArgStrLen, "argv[%d] is %d bytes: over Linux's MAX_ARG_STRLEN", i+1, len(a))
		assert.False(t, len(a) > 64 && strings.Contains(prompt, a), "argv[%d] carries prompt bytes", i+1)
	}
	switch runner {
	case "claude":
		assert.Contains(t, args, "-p", "claude must run in print mode")
		assert.Equal(t, "-p", args[len(args)-1], "claude must get no prompt argument after -p (it reads stdin)")
	case "codex":
		assert.Equal(t, "-", args[len(args)-1], "codex must get the \"-\" prompt argument (read stdin)")
	}
	if sessionID != nil {
		assert.Contains(t, args, *sessionID, "a continuation must resume its session")
	}
}

// installLinuxUname makes the remote `bash -l` see `uname -s` = Linux (the
// login profile puts a fake uname first on PATH; macOS's /etc/profile
// rebuilds PATH, so the profile is the only reliable hook). This models a
// Linux worker, where the pre-CORE-156 wrapper refused a prompt of 128 KiB
// or more with exit 98.
func installLinuxUname(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uname"),
		[]byte("#!/bin/sh\nif [ \"$1\" = -s ]; then echo Linux; else exec /usr/bin/uname \"$@\"; fi\n"), 0o755))
	installLoginProfile(t, "PATH="+dir+":$PATH; export PATH\n")
}

// recordSSHExitStatus rewrites the faithful fake ssh (installed next to
// fakeLog) so it records the remote exit status it relays, and returns the
// record's path.
func recordSSHExitStatus(t *testing.T, fakeLog string) string {
	t.Helper()
	sshPath := filepath.Join(filepath.Dir(fakeLog), "ssh")
	rcLog := filepath.Join(filepath.Dir(fakeLog), "exit_status.log")
	body, err := os.ReadFile(sshPath)
	require.NoError(t, err)
	const last = `ITERVOX_FAKE_SSHD=1 exec "$FAKE_SSHD_BIN" "$*"`
	require.Contains(t, string(body), last)
	patched := strings.Replace(string(body), last,
		`ITERVOX_FAKE_SSHD=1 "$FAKE_SSHD_BIN" "$*"; rc=$?; echo "$rc" >> `+rcLog+`; exit "$rc"`, 1)
	require.NoError(t, os.WriteFile(sshPath, []byte(patched), 0o755))
	return rcLog
}

// sessionModes is a first turn and a continuation.
func sessionModes() []struct {
	name string
	id   *string
} {
	sid := "sess-156"
	return []struct {
		name string
		id   *string
	}{{"first", nil}, {"resume", &sid}}
}

// TestSSHLargePromptReachesAgentByteExact is CORE-156's SSH reproduction: a
// 1 MiB prompt on a Linux worker (fake uname) must reach the remote agent's
// stdin byte-exact, for claude and codex, first turn and resume, under both
// wrapper bashes (3.2's process substitution included). Before the fix the
// wrapper refused with exit 98 naming MAX_ARG_STRLEN.
func TestSSHLargePromptReachesAgentByteExact(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	for _, wb := range wrapperBashes(t) {
		for _, rc := range remoteRunnerCases() {
			for _, pc := range largePrompts() {
				for _, sm := range sessionModes() {
					t.Run(wb+"/"+rc.name+"/"+pc.name+"/"+sm.name, func(t *testing.T) {
						fakeLog := installFaithfulFakeSSH(t, "/bin/bash")
						useWrapperBash(t, wb, fakeLog)
						rcLog := recordSSHExitStatus(t, fakeLog)
						installLinuxUname(t)
						bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
						exe := writeLargePromptAgent(t, bin, out, rc.firstLine)

						start := time.Now()
						result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
							sm.id, pc.prompt, ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
							60000, 180000, agent.PermissionBypass)
						require.NoError(t, err)
						status, _ := os.ReadFile(rcLog)
						require.False(t, result.Failed, "ssh exit status %s; FailureText: %.400s", strings.TrimSpace(string(status)), result.FailureText)
						t.Logf("%d-byte prompt delivered in %s", len(pc.prompt), time.Since(start).Round(time.Millisecond))
						assertPromptByteExact(t, out, pc.prompt)
						assertArgvCarriesNoPrompt(t, out, rc.name, pc.prompt, sm.id)
					})
				}
			}
		}
	}
}

// localLoginShells lists the local $SHELL candidates for the shell path
// (`$SHELL -lc`): csh/tcsh reject -lc outright, so they never worked there.
func localLoginShells() []string {
	var out []string
	for _, sh := range []string{"/bin/sh", "/bin/bash", "/opt/homebrew/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh"} {
		if _, err := os.Stat(sh); err == nil {
			out = append(out, sh)
		}
	}
	return out
}

// localModes is the direct path (absolute command, exec'd without a shell)
// and the shell path under each local login shell ("/bin/sh <exe>" has a
// space, so it goes through `$SHELL -lc`).
func localModes() []struct{ name, shell string } {
	modes := []struct{ name, shell string }{{"direct", ""}}
	for _, sh := range localLoginShells() {
		modes = append(modes, struct{ name, shell string }{"shell_" + shellLabel(sh), sh})
	}
	return modes
}

// localCommand returns the agent command for a local mode, pinning $SHELL
// and HOME (so the user's own profiles do not run) for the shell path.
func localCommand(t *testing.T, exe, shell string) string {
	t.Helper()
	if shell == "" {
		return exe
	}
	t.Setenv("SHELL", shell)
	t.Setenv("HOME", t.TempDir())
	return "/bin/sh " + exe
}

// TestLocalLargePromptReachesAgentByteExact is CORE-156's local
// reproduction: the same 1 MiB prompts on a local turn, direct and through
// each login shell. Before the fix the prompt was one argv string and exec
// failed with "argument list too long".
func TestLocalLargePromptReachesAgentByteExact(t *testing.T) {
	for _, mode := range localModes() {
		for _, rc := range remoteRunnerCases() {
			for _, pc := range largePrompts() {
				for _, sm := range sessionModes() {
					t.Run(mode.name+"/"+rc.name+"/"+pc.name+"/"+sm.name, func(t *testing.T) {
						t.Setenv("TMPDIR", t.TempDir())
						bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
						exe := writeLargePromptAgent(t, bin, out, rc.firstLine)
						command := localCommand(t, exe, mode.shell)

						result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
							sm.id, pc.prompt, ws, command, "", "",
							30000, 60000, agent.PermissionBypass)
						require.NoError(t, err)
						require.False(t, result.Failed, "FailureText: %.400s", result.FailureText)
						assertPromptByteExact(t, out, pc.prompt)
						assertArgvCarriesNoPrompt(t, out, rc.name, pc.prompt, sm.id)
						left, err := os.ReadDir(os.Getenv("TMPDIR"))
						require.NoError(t, err)
						assert.Empty(t, left, "no prompt file may be left in TMPDIR")
					})
				}
			}
		}
	}
}

// writeCancelPromptAgent writes a fake agent that records its pid and
// process group, starts a grandchild sleep, optionally reads its stdin to
// EOF and records the byte count, prints firstLine and then thinks (sleeps).
// An agent that never reads leaves the prompt writer blocked on a full pipe.
func writeCancelPromptAgent(t *testing.T, dir, outDir, firstLine string, readPrompt bool) string {
	t.Helper()
	read := ""
	if readPrompt {
		read = fmt.Sprintf("wc -c | tr -d ' ' > %[1]s/read.tmp && mv %[1]s/read.tmp %[1]s/read\n", outDir)
	}
	exe := filepath.Join(dir, "cancel-prompt-agent")
	script := fmt.Sprintf(`#!/bin/sh
ps -o pgid= -p $$ | tr -d " " > %[1]s/agent.pgid
echo $$ > %[1]s/agent.pid.tmp && mv %[1]s/agent.pid.tmp %[1]s/agent.pid
/bin/sh -c '/bin/sleep 300 & echo $! > %[1]s/gc.pid.tmp && mv %[1]s/gc.pid.tmp %[1]s/gc.pid; wait' </dev/null &
%[3]secho '%[2]s'
/bin/sleep 300
`, outDir, firstLine, read)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// readCount polls path for a byte count (the agent's `wc -c` of its stdin).
func readCount(t *testing.T, path string, within time.Duration) int {
	t.Helper()
	return readPIDFile(t, path, within)
}

// TestPromptStdinDoesNotBreakCancel: with a 1 MiB prompt on the agent's
// stdin, a cancelled turn must still stop everything — whether the agent
// read the prompt to EOF (proving EOF arrived) or never read it (the prompt
// writer is then blocked on a full pipe). Over SSH the agent, its
// grandchild and its whole process group (the watcher and the prompt
// writer) must be gone within remoteKillBound; locally the CORE-001 group
// kill must stop the agent and grandchild, and no prompt file may remain.
func TestPromptStdinDoesNotBreakCancel(t *testing.T) {
	prompt := codeLikePrompt(1 << 20)
	reads := []struct {
		name string
		read bool
	}{{"reads_prompt", true}, {"never_reads", false}}

	for _, wb := range wrapperBashes(t) {
		for _, rc := range remoteRunnerCases() {
			for _, rd := range reads {
				t.Run("ssh/"+wb+"/"+rc.name+"/"+rd.name, func(t *testing.T) {
					useWrapperBash(t, wb, installFaithfulFakeSSH(t, "/bin/bash"))
					sessions := installSessionLog(t)
					bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
					exe := writeCancelPromptAgent(t, bin, out, rc.firstLine, rd.read)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					rt := &remoteTurn{done: make(chan struct{})}
					go func() {
						defer close(rt.done)
						_, _ = rc.runner.RunTurn(ctx, slog.Default(), nil, nil, prompt, ws, exe,
							"worker.example.test", filepath.Join(bin, "logs"), 600000, 0, agent.PermissionBypass)
						rt.returned = time.Now()
					}()
					rt.agentPID = readPIDFile(t, filepath.Join(out, "agent.pid"), 30*time.Second)
					rt.grandchildPID = readPIDFile(t, filepath.Join(out, "gc.pid"), 30*time.Second)
					rt.agentPGID = readPIDFile(t, filepath.Join(out, "agent.pgid"), 30*time.Second)
					t.Cleanup(func() {
						for _, pid := range []int{rt.agentPID, rt.grandchildPID} {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					})
					if rd.read {
						assert.Equal(t, len(prompt), readCount(t, filepath.Join(out, "read"), 30*time.Second),
							"the agent must read the whole prompt and then see EOF")
					}
					leaders := sessionLeaders(t, sessions)
					require.Len(t, leaders, 1)
					t.Cleanup(func() { _ = syscall.Kill(-leaders[0], syscall.SIGKILL) })

					cancel()
					agentGone, sessionGone := assertRemoteGone(t, rt, leaders)
					t.Logf("after RunTurn returned: agent and grandchild gone in %s, agent group and session in %s",
						agentGone.Round(time.Millisecond), sessionGone.Round(time.Millisecond))
					assert.Less(t, agentGone, termBound, "SIGTERM must stop the agent without waiting for the KILL grace")
				})
			}
		}
	}

	for _, mode := range []struct{ name, shell string }{{"direct", ""}, {"shell_bin_bash", "/bin/bash"}} {
		for _, rc := range remoteRunnerCases() {
			for _, rd := range reads {
				t.Run("local/"+mode.name+"/"+rc.name+"/"+rd.name, func(t *testing.T) {
					tmp := t.TempDir()
					t.Setenv("TMPDIR", tmp)
					bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
					exe := writeCancelPromptAgent(t, bin, out, rc.firstLine, rd.read)
					command := localCommand(t, exe, mode.shell)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan time.Time, 1)
					go func() {
						_, _ = rc.runner.RunTurn(ctx, slog.Default(), nil, nil, prompt, ws, command,
							"", "", 600000, 0, agent.PermissionBypass)
						done <- time.Now()
					}()
					agentPID := readPIDFile(t, filepath.Join(out, "agent.pid"), 30*time.Second)
					gcPID := readPIDFile(t, filepath.Join(out, "gc.pid"), 30*time.Second)
					t.Cleanup(func() {
						_ = syscall.Kill(agentPID, syscall.SIGKILL)
						_ = syscall.Kill(gcPID, syscall.SIGKILL)
					})
					if rd.read {
						assert.Equal(t, len(prompt), readCount(t, filepath.Join(out, "read"), 30*time.Second),
							"the agent must read the whole prompt and then see EOF")
					}
					left, err := os.ReadDir(tmp)
					require.NoError(t, err)
					assert.Empty(t, left, "the prompt file must be unlinked before the agent runs")

					cancelled := time.Now()
					cancel()
					var returned time.Time
					select {
					case returned = <-done:
					case <-time.After(15 * time.Second):
						t.Fatal("RunTurn did not return after cancel")
					}
					t.Logf("RunTurn returned %s after cancel", returned.Sub(cancelled).Round(time.Millisecond))
					deadline := time.Now().Add(remoteKillBound)
					for processAlive(agentPID) || processAlive(gcPID) {
						if time.Now().After(deadline) {
							t.Fatalf("agent %d (alive=%v) or grandchild %d (alive=%v) survived the cancel",
								agentPID, processAlive(agentPID), gcPID, processAlive(gcPID))
						}
						time.Sleep(25 * time.Millisecond)
					}
					left, err = os.ReadDir(tmp)
					require.NoError(t, err)
					assert.Empty(t, left, "no prompt file may be left in TMPDIR")
				})
			}
		}
	}
}

// TestLocalLoginProfileCannotEatPrompt: on the local login-shell path the
// prompt is fd 3, not stdin, so a login profile that reads stdin (here one
// line, then the rest) gets /dev/null as before and the agent still gets
// the whole prompt. Had the prompt been the shell's stdin, the profile
// would have silently consumed it.
func TestLocalLoginProfileCannotEatPrompt(t *testing.T) {
	prompt := "first line the profile would eat\n" + codeLikePrompt(200<<10)
	for _, rc := range remoteRunnerCases() {
		t.Run(rc.name, func(t *testing.T) {
			bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
			exe := writeLargePromptAgent(t, bin, out, rc.firstLine)
			command := localCommand(t, exe, "/bin/bash")
			require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".bash_profile"),
				[]byte("IFS= read -r itervox_profile_line || true\ncat > /dev/null\n"), 0o644))

			result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
				nil, prompt, ws, command, "", "", 30000, 60000, agent.PermissionBypass)
			require.NoError(t, err)
			require.False(t, result.Failed, "FailureText: %.400s", result.FailureText)
			assertPromptByteExact(t, out, prompt)
		})
	}
}
