package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolationRunnerCase is one runner of the launch-path matrix (#258).
type isolationRunnerCase struct {
	name      string
	runner    Runner
	firstLine string
	// directArgs / shellCmd are today's command construction for the
	// direct and login-shell paths (first turn, bypass, no steering).
	directArgs func(ws string) []string
	shellCmd   func(command, ws string) string
}

const isoPrompt = "do the thing"

func isolationRunnerCases() []isolationRunnerCase {
	return []isolationRunnerCase{
		{
			name: "claude", runner: NewClaudeRunner(), firstLine: `{"type":"system","session_id":"s1"}`,
			directArgs: func(string) []string { return buildDirectArgsLive(nil, isoPrompt, PermissionBypass, false) },
			shellCmd: func(command, _ string) string {
				return buildShellCmdLive(command, nil, isoPrompt, localPromptRedirect, PermissionBypass, false)
			},
		},
		{
			name: "codex", runner: NewCodexRunner(), firstLine: `{"type":"thread.started","thread_id":"t1"}`,
			directArgs: func(ws string) []string { return buildCodexDirectArgs(nil, ws, PermissionBypass) },
			shellCmd: func(command, ws string) string {
				return buildCodexShellCmd(command, nil, ws, localPromptRedirect, PermissionBypass)
			},
		},
	}
}

// writeArgvAgent writes a fake agent CLI that records its argv (one per
// line) to out, drains its stdin, prints firstLine and exits 0.
func writeArgvAgent(t *testing.T, dir, out, firstLine string) string {
	t.Helper()
	exe := filepath.Join(dir, "argv-agent")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\ncat > /dev/null\necho '%s'\n", ShellQuote(out), firstLine)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// writeRecordingShell writes a fake login shell that records its argv to
// out and runs the command with /bin/sh, and makes it $SHELL (and HOME a
// temp dir, so no real profile runs).
func writeRecordingShell(t *testing.T, dir, out string) string {
	t.Helper()
	sh := filepath.Join(dir, "rec-shell")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nexec /bin/sh \"$@\"\n", ShellQuote(out))
	require.NoError(t, os.WriteFile(sh, []byte(script), 0o755))
	t.Setenv("SHELL", sh)
	t.Setenv("HOME", t.TempDir())
	return sh
}

// recordingMechanism is a fake sandbox: it wraps argv in a script that
// records what it was asked to run (and the writable list it got), then
// runs it unconfined, so a test sees exactly what each launch path hands
// to the sandbox.
type recordingMechanism struct{ script string }

func newRecordingMechanism(t *testing.T, dir, log string) recordingMechanism {
	t.Helper()
	script := filepath.Join(dir, "fake-sandbox")
	body := fmt.Sprintf("#!/bin/sh\n{ printf '%%s\\n' \"$@\"; echo --END--; } >> %s\n"+
		"while [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", ShellQuote(log))
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return recordingMechanism{script: script}
}

func (recordingMechanism) Name() string     { return "recorder" }
func (recordingMechanism) Available() error { return nil }
func (m recordingMechanism) Wrap(argv []string, p IsolationPolicy) ([]string, error) {
	return append([]string{m.script, "write=" + strings.Join(p.Write, ":"), "--"}, argv...), nil
}

// readArgvLines reads a recorded argv file.
func readArgvLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "nothing was recorded at %s", path)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// TestIsolationNoneLeavesCommandUnchanged is #258's no-change guarantee:
// with no isolation (nothing in the context, or isolation "none" resolved
// to nil) every runner and local launch path execs exactly today's
// command — the same program and the same arguments, byte for byte — and
// agentCommand is exec.CommandContext.
func TestIsolationNoneLeavesCommandUnchanged(t *testing.T) {
	none, err := PrepareIsolation("none", IsolationInputs{}, func() (IsolationMechanism, error) {
		t.Fatal("isolation none must not probe for a sandbox")
		return nil, nil
	})
	require.NoError(t, err)
	require.Nil(t, none, `isolation "none" resolves to no sandbox`)
	ctxs := map[string]context.Context{
		"no_isolation":   context.Background(),
		"isolation_none": WithIsolation(context.Background(), none),
	}
	for ctxName, ctx := range ctxs {
		for _, rc := range isolationRunnerCases() {
			for _, path := range []string{"direct", "login_shell"} {
				t.Run(ctxName+"/"+rc.name+"/"+path, func(t *testing.T) {
					bin, ws := t.TempDir(), t.TempDir()
					agentArgv := filepath.Join(bin, "agent.argv")
					shellArgv := filepath.Join(bin, "shell.argv")
					exe := writeArgvAgent(t, bin, agentArgv, rc.firstLine)
					command := exe
					if path == "login_shell" {
						writeRecordingShell(t, bin, shellArgv)
						command = "/bin/sh " + exe
					}
					res, err := rc.runner.RunTurn(ctx, slog.Default(), nil, nil, isoPrompt, ws, command, "", "",
						30000, 60000, PermissionBypass)
					require.NoError(t, err)
					require.False(t, res.Failed, res.FailureText)
					if path == "direct" {
						assert.Equal(t, rc.directArgs(ws), readArgvLines(t, agentArgv),
							"the direct path must exec today's argv")
						return
					}
					assert.Equal(t, []string{"-lc", rc.shellCmd(command, ws)}, readArgvLines(t, shellArgv),
						"the login-shell path must exec today's shell command")
				})
			}
		}
	}

	// And the builder itself: no isolation is exec.CommandContext.
	argv := []string{"/bin/echo", "a b", "'c'"}
	got, err := agentCommand(context.Background(), context.Background(), argv, "/ignored")
	require.NoError(t, err)
	assert.Equal(t, "/bin/echo", got.Path)
	assert.Equal(t, argv, got.Args)
	shell, err := IsolateShellCommand(nil, "claude --x 'y'")
	require.NoError(t, err)
	assert.Equal(t, "claude --x 'y'", shell, "the tmux command line is unchanged without isolation")
}

// TestIsolationProcessWrapsEveryLaunchPath: with process isolation every
// local launch path — direct and login shell, for claude and codex — hands
// its whole command to the sandbox (the tmux path is covered by the test of
// the same name in cmd/itervox). Claude's log directory is added to the
// writable list for the turn.
func TestIsolationProcessWrapsEveryLaunchPath(t *testing.T) {
	for _, rc := range isolationRunnerCases() {
		for _, path := range []string{"direct", "login_shell"} {
			t.Run(rc.name+"/"+path, func(t *testing.T) {
				bin, ws := t.TempDir(), t.TempDir()
				logDir := filepath.Join(t.TempDir(), "cli-logs")
				wrapLog := filepath.Join(bin, "sandbox.log")
				agentArgv := filepath.Join(bin, "agent.argv")
				exe := writeArgvAgent(t, bin, agentArgv, rc.firstLine)
				m := newRecordingMechanism(t, bin, wrapLog)
				command, shell := exe, ""
				if path == "login_shell" {
					shell = writeRecordingShell(t, bin, filepath.Join(bin, "shell.argv"))
					command = "/bin/sh " + exe
				}
				iso, err := PrepareIsolation("process", IsolationInputs{Workspace: ws},
					func() (IsolationMechanism, error) { return m, nil })
				require.NoError(t, err)
				ctx := WithIsolation(context.Background(), iso)

				res, err := rc.runner.RunTurn(ctx, slog.Default(), nil, nil, isoPrompt, ws, command, "", logDir,
					30000, 60000, PermissionBypass)
				require.NoError(t, err)
				require.False(t, res.Failed, res.FailureText)

				rec := readArgvLines(t, wrapLog)
				require.Equal(t, "--END--", rec[len(rec)-1])
				sep := slices.Index(rec, "--")
				require.Positive(t, sep, "the sandbox got no command: %q", rec)
				wrapped := rec[sep+1 : len(rec)-1]
				if path == "direct" {
					assert.Equal(t, append([]string{exe}, rc.directArgs(ws)...), wrapped,
						"the sandbox must run the agent binary with today's argv")
				} else {
					assert.Equal(t, []string{shell, "-lc", rc.shellCmd(command, ws)}, wrapped,
						"the sandbox must run the whole login-shell command")
				}
				realWS, _ := filepath.EvalSymlinks(ws)
				assert.Contains(t, rec[0], realWS, "the workspace is writable")
				if rc.name == "claude" {
					realLog, _ := filepath.EvalSymlinks(logDir)
					assert.Contains(t, rec[0], realLog, "claude's CLAUDE_CODE_LOG_DIR is writable for the turn")
				}
				assert.FileExists(t, agentArgv, "the agent ran inside the sandbox")
			})
		}
	}
}

// TestIsolationRunnerRefusesUnresolvedSandbox: a turn whose Isolation has
// no mechanism, or that runs on an SSH worker, fails before any process
// starts instead of running unconfined.
func TestIsolationRunnerRefusesUnresolvedSandbox(t *testing.T) {
	for _, rc := range isolationRunnerCases() {
		bin := t.TempDir()
		marker := filepath.Join(bin, "agent.argv")
		exe := writeArgvAgent(t, bin, marker, rc.firstLine)
		ctx := WithIsolation(context.Background(), &Isolation{})
		res, err := rc.runner.RunTurn(ctx, slog.Default(), nil, nil, isoPrompt, bin, exe, "", "", 30000, 60000, PermissionBypass)
		require.ErrorIs(t, err, ErrIsolationUnavailable, rc.name)
		assert.True(t, res.Failed)
		res, err = rc.runner.RunTurn(ctx, slog.Default(), nil, nil, isoPrompt, bin, exe, "worker-1", "", 30000, 60000, PermissionBypass)
		require.ErrorIs(t, err, ErrIsolationUnavailable, rc.name)
		assert.Contains(t, res.FailureText, "SSH")
		assert.NoFileExists(t, marker, "%s: no agent may start", rc.name)
	}
}

// isolationListFixture is a fake machine for the policy-list test: a
// worktree whose git dir lives in a shared clone under the workspace root,
// agent state dirs and credentials in $HOME, project secrets.
type isolationListFixture struct {
	in                                      IsolationInputs
	ws, gitDir, common, wsRoot, home, netrc string
	extraWrite, extraDeny, envFile, tokFile string
	stateDirs, credDirs                     []string
}

func newIsolationListFixture(t *testing.T) isolationListFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := isolationListFixture{home: filepath.Join(base, "home")}
	f.wsRoot = filepath.Join(f.home, ".itervox", "workspaces", "proj")
	f.ws = filepath.Join(f.wsRoot, "ENG-1")
	f.common = filepath.Join(f.wsRoot, ".bare")
	f.gitDir = filepath.Join(f.common, "worktrees", "ENG-1")
	project := filepath.Join(base, "project")
	f.envFile = filepath.Join(project, ".itervox", ".env")
	f.tokFile = filepath.Join(project, ".itervox", "mcp-token.json")
	f.extraWrite = filepath.Join(base, "npm-cache")
	f.extraDeny = filepath.Join(f.home, "secrets")
	f.netrc = filepath.Join(f.home, ".netrc")
	f.stateDirs = []string{filepath.Join(f.home, ".claude"), filepath.Join(f.home, ".codex"), filepath.Join(f.home, ".cache")}
	f.credDirs = []string{filepath.Join(f.home, ".ssh"), filepath.Join(f.home, ".aws"), filepath.Join(f.home, ".config", "gh")}
	dirs := append([]string{f.ws, f.gitDir, filepath.Join(f.common, "objects"), filepath.Join(f.common, "refs"),
		filepath.Join(f.common, "logs"), filepath.Dir(f.envFile), f.extraWrite, f.extraDeny}, f.stateDirs...)
	for _, d := range append(dirs, f.credDirs...) {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	for _, file := range []string{f.envFile, f.tokFile, f.netrc, filepath.Join(f.home, ".claude.json")} {
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(f.ws, ".git"), []byte("gitdir: "+f.gitDir+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.gitDir, "commondir"), []byte("../..\n"), 0o644))
	f.in = IsolationInputs{
		Workspace: f.ws, WorkspaceRoot: filepath.Join(f.home, ".itervox", "workspaces"), ProjectDir: project,
		Home: f.home, TempDir: filepath.Join(base, "tmp"),
		ExtraWrite: []string{f.extraWrite}, ExtraDeny: []string{f.extraDeny},
	}
	require.NoError(t, os.MkdirAll(f.in.TempDir, 0o755))
	return f
}

// argsContainSeq reports whether seq appears contiguously in args, and at
// which index.
func argsIndexSeq(args, seq []string) int {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return i
		}
	}
	return -1
}

// TestIsolationPolicyWriteAndDenyLists: the generated bwrap arguments and
// the macOS sandbox profile make the workspace, the worktree's git dirs
// and the agent state dirs writable, the deny list unreadable, and apply
// isolation_extra_write / isolation_extra_deny.
func TestIsolationPolicyWriteAndDenyLists(t *testing.T) {
	f := newIsolationListFixture(t)
	p := BuildIsolationPolicy(f.in)

	writable := append([]string{f.ws, f.gitDir, filepath.Join(f.common, "objects"), filepath.Join(f.common, "refs"),
		filepath.Join(f.common, "logs"), f.in.TempDir, f.extraWrite, filepath.Join(f.home, ".claude.json")}, f.stateDirs...)
	deniedDirs := append([]string{f.in.WorkspaceRoot, f.extraDeny}, f.credDirs...)
	deniedFiles := []string{f.envFile, f.tokFile, f.netrc}

	args := bubblewrapArgs(p)
	sbpl := sandboxExecProfile(p)
	for _, w := range writable {
		assert.GreaterOrEqual(t, argsIndexSeq(args, []string{"--bind", w, w}), 0, "bwrap: %s must be writable", w)
		if fi, _ := os.Stat(w); fi.IsDir() {
			assert.Contains(t, sbpl, "(allow file-read* file-write* (subpath "+sbplString(w)+"))", "sbpl: %s", w)
		} else {
			assert.Contains(t, sbpl, "(allow file-read* file-write* (regex "+sbplString("^"+regexQuote(w))+"))", "sbpl: %s", w)
		}
	}
	assert.GreaterOrEqual(t, argsIndexSeq(args, []string{"--ro-bind", f.common, f.common}), 0,
		"bwrap: the shared git dir is readable again under the hidden workspace root")
	assert.Contains(t, sbpl, "(allow file-read* (subpath "+sbplString(f.common)+"))")
	for _, d := range deniedDirs {
		assert.GreaterOrEqual(t, argsIndexSeq(args, []string{"--tmpfs", d}), 0, "bwrap: %s must be hidden", d)
		assert.GreaterOrEqual(t, argsIndexSeq(args, []string{"--remount-ro", d}), 0, "bwrap: %s's cover must be read-only", d)
		assert.Contains(t, sbpl, "(deny file-read* file-write* (subpath "+sbplString(d)+"))", "sbpl: %s", d)
	}
	for _, file := range deniedFiles {
		assert.GreaterOrEqual(t, argsIndexSeq(args, []string{"--ro-bind", "/dev/null", file}), 0, "bwrap: %s must be hidden", file)
		assert.Contains(t, sbpl, "(deny file-read* file-write* (literal "+sbplString(file)+"))", "sbpl: %s", file)
	}

	// The more specific rule wins: the workspace is bound after the
	// workspace root is hidden (bwrap mount order; sbpl last match wins).
	hideRoot := argsIndexSeq(args, []string{"--tmpfs", f.in.WorkspaceRoot})
	bindWS := argsIndexSeq(args, []string{"--bind", f.ws, f.ws})
	assert.Less(t, hideRoot, bindWS, "bwrap: hide the workspace root, then bind this workspace into it")
	assert.Less(t, strings.Index(sbpl, "(deny file-read* file-write* (subpath "+sbplString(f.in.WorkspaceRoot)+"))"),
		strings.Index(sbpl, "(allow file-read* file-write* (subpath "+sbplString(f.ws)+"))"))
	meta := "(allow file-read-metadata (literal " + sbplString(f.wsRoot) + "))"
	assert.Equal(t, 1, strings.Count(sbpl, meta), "sbpl: a re-exposed path's ancestors stay statable, once each")
	assert.True(t, strings.HasPrefix(sbpl, "(version 1)\n(allow default)\n(deny file-write*)\n"),
		"sbpl: writes are denied by default:\n%s", sbpl)
	assert.Equal(t, []string{"--die-with-parent", "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev"}, args[:7],
		"bwrap: / is read-only")
	// SBPL quoting: a path is one string literal, and a file rule's regex
	// matches the path literally.
	assert.Equal(t, `"/a \"b\" \\c"`, sbplString(`/a "b" \c`))
	assert.Equal(t, `/h/\.claude\.json`, regexQuote("/h/.claude.json"))
	assert.Contains(t, sbpl, `(allow file-read* file-write* (regex "^`+strings.ReplaceAll(regexQuote(filepath.Join(f.home, ".claude.json")), `\`, `\\`)+`"))`)
}

// TestIsolationPrepareReportsUnavailable: PrepareIsolation fails closed
// with ErrIsolationUnavailable and every mechanism's reason.
func TestIsolationPrepareReportsUnavailable(t *testing.T) {
	_, err := PrepareIsolation("process", IsolationInputs{}, func() (IsolationMechanism, error) {
		return SelectIsolationMechanism([]IsolationMechanism{unavailableMechanism{"bubblewrap"}, unavailableMechanism{"landlock"}})
	})
	require.ErrorIs(t, err, ErrIsolationUnavailable)
	assert.Contains(t, err.Error(), "bubblewrap: not here")
	assert.Contains(t, err.Error(), "landlock: not here")
}

type unavailableMechanism struct{ name string }

func (u unavailableMechanism) Name() string   { return u.name }
func (unavailableMechanism) Available() error { return errors.New("not here") }
func (unavailableMechanism) Wrap([]string, IsolationPolicy) ([]string, error) {
	return nil, errors.New("unavailable")
}
