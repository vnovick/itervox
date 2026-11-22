package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/guardrails/sshtest"
)

// remoteToken is the run's guardrail token in these tests: it must reach
// the shims on the worker and never any command line.
const remoteToken = "grd-tok-7f3a9c1e5b"

// fakeGuardrailDaemon serves the guardrail routes the shims call, checking
// the token, and approves every approval request on its first poll.
type fakeGuardrailDaemon struct {
	mu        sync.Mutex
	events    []Event
	approvals []ApprovalRequest
	probes    int
	badAuth   int
}

func (d *fakeGuardrailDaemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+remoteToken {
			d.badAuth++
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		prefix := "/api/v1/agent-actions/ENG-1/guardrail/"
		switch p := strings.TrimPrefix(r.URL.Path, prefix); {
		case r.Method == http.MethodPost && p == "events":
			var ev Event
			_ = json.NewDecoder(r.Body).Decode(&ev)
			d.events = append(d.events, ev)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && p == "approvals":
			var req ApprovalRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			d.approvals = append(d.approvals, req)
			_ = json.NewEncoder(w).Encode(ApprovalStatus{ID: "a1", Status: StatusPending})
		case r.Method == http.MethodGet && p == "approvals/probe":
			d.probes++
			_ = json.NewEncoder(w).Encode(ApprovalStatus{ID: "probe", Status: StatusUnknown})
		case r.Method == http.MethodGet && p == "approvals/a1":
			_ = json.NewEncoder(w).Encode(ApprovalStatus{ID: "a1", Status: StatusApproved})
		default:
			http.NotFound(w, r)
		}
	})
}

func (d *fakeGuardrailDaemon) kinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, ev := range d.events {
		out = append(out, ev.Kind)
	}
	return out
}

// remoteRig is a git setup on a shared file system (the workspace path is
// the same for the daemon and the worker), a fake ssh whose "worker" runs
// in a separate HOME, and fake gh and vercel programs.
type remoteRig struct {
	*rig
	home, argvLog, fakes, gitBin string
	ghLog, vercelLog, out        string
}

func newRemoteRig(t *testing.T) *remoteRig {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := &remoteRig{rig: &rig{t: t, root: root, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}}
	git(t, root, "init", "-q", "--bare", "-b", "main", r.remote)
	git(t, root, "init", "-q", "-b", "main", r.work)
	git(t, r.work, "remote", "add", "origin", r.remote)
	r.commit("base")
	git(t, r.work, "push", "-q", "origin", "main")
	git(t, r.work, "checkout", "-q", "-b", "itervox/eng-1")
	r.commit("work")
	r.gitBin = gitBin
	r.fakes = filepath.Join(root, "fakebin")
	r.ghLog, r.vercelLog, r.out = filepath.Join(root, "gh.log"), filepath.Join(root, "vercel.log"), filepath.Join(root, "agent.out")
	fakeProgram(t, r.fakes, "gh", "echo \"$*\" >> "+r.ghLog+"\n")
	fakeProgram(t, r.fakes, "vercel", "echo \"$*\" >> "+r.vercelLog+"\n")
	t.Setenv("PATH", r.fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.home, r.argvLog = sshtest.Install(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	return r
}

func (r *remoteRig) options(forward string) RemoteOptions {
	exe, err := os.Executable()
	require.NoError(r.t, err)
	return RemoteOptions{
		Host: "worker-1",
		Options: Options{
			Executable: exe, WorkDir: r.work, Identifier: "ENG-1", IssueBranch: "itervox/eng-1",
			ProtectedBranches: []string{"main", "master", "develop"}, ApprovalCommands: []string{"vercel --prod"},
			ApprovalsEnabled: true,
		},
		ForwardTarget: forward,
		Token:         remoteToken,
	}
}

// agentScript is the fake agent: every guarded operation, each result
// line-tagged in r.out. It runs on the "worker" through the real SSH turn
// transport.
func (r *remoteRig) agentScript() string {
	o := " >> " + r.out + " 2>&1; echo \"%s=$?\" >> " + r.out + "\n"
	line := func(tag, cmd string) string { return cmd + strings.Replace(o, "%s", tag, 1) }
	return "#!/bin/sh\n" +
		line("shim_main", "git push origin HEAD:main") +
		line("hook_other", r.gitBin+" push origin HEAD:refs/heads/other-branch") +
		line("tag", r.gitBin+" tag v9 && "+r.gitBin+" push origin refs/tags/v9") +
		line("merge", "gh pr merge 1 --squash") +
		line("deploy", "vercel --prod") +
		line("own", r.gitBin+" push origin HEAD:refs/heads/itervox/eng-1") +
		"printf 'token_in_env=%s\\n' \"$( [ -n \"$" + TokenEnv + "\" ] && echo yes || echo no)\" >> " + r.out + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"session_id\":\"s1\"}'\n"
}

// turn runs one agent turn on the worker through the claude runner's SSH
// path, with the remote guardrails' run environment.
func (r *remoteRig) turn(rt *RemoteRuntime) map[string]string {
	r.t.Helper()
	script := filepath.Join(r.root, "agent.sh")
	require.NoError(r.t, os.WriteFile(script, []byte(r.agentScript()), 0o755))
	ctx := agent.WithRunEnv(context.Background(), rt.AgentEnv())
	res, err := agent.NewClaudeRunner().RunTurn(ctx, slog.Default(), nil, nil, "hi", r.work, "/bin/sh "+script,
		"worker-1", "", 60000, 120000, agent.PermissionBypass)
	data, _ := os.ReadFile(r.out)
	require.NoError(r.t, err, "turn: %s\n%s", res.FailureText, data)
	got := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.Contains(k, " ") {
			got[k] = v
		}
	}
	got["output"] = string(data)
	return got
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

// 0.3.4: guardrails on an SSH worker. The binary is uploaded to the worker,
// the runtime is prepared there, and the agent's turn — over the real SSH
// transport, a fake ssh client running the remote command in another HOME
// — is guarded on the worker: a push to a protected branch through the git
// shim, and a push to another branch and a tag push with git called by
// absolute path (no shim involved) are refused by the worker's pre-push
// hook,
// `gh pr merge` by the gh shim, and `vercel --prod` waits for the
// approval, which reaches the daemon over the reverse-forwarded socket and
// then runs it exactly once. The issue branch itself is pushed. Refusals
// are reported through the callback with the run's token, which never
// appears in any ssh argument.
func TestRemoteGuardrailsRefuseOnTheWorker(t *testing.T) {
	r := newRemoteRig(t)
	d := &fakeGuardrailDaemon{}
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)

	rt, err := InstallRemote(context.Background(), r.options(srv.Listener.Addr().String()))
	require.NoError(t, err)
	require.True(t, rt.Callback(), "callback: %v", rt.CallbackErr())
	assert.FileExists(t, filepath.Join(r.home, filepath.FromSlash(rt.bin)), "the binary is cached in the worker's HOME")
	assert.True(t, strings.HasPrefix(filepath.Base(rt.Dir()), "itervox-guardrails-"), rt.Dir())
	policy, err := LoadPolicy(rt.policy)
	require.NoError(t, err)
	assert.True(t, policy.Remote)
	assert.Empty(t, policy.Token, "the token is not written on the worker")
	assert.Empty(t, policy.DaemonURL)
	assert.Equal(t, filepath.Join(r.work, ".git", "hooks"), policy.OrigHooksDir, "resolved by the worker's git, as locally")

	got := r.turn(rt)
	out := got["output"]
	assert.Equal(t, "1", got["shim_main"], out)
	assert.Equal(t, "1", got["hook_other"], out)
	assert.Equal(t, "1", got["tag"], out)
	assert.Equal(t, "1", got["merge"], out)
	assert.Equal(t, "0", got["deploy"], out)
	assert.Equal(t, "0", got["own"], out)
	assert.Equal(t, "yes", got["token_in_env"])
	assert.Contains(t, out, "push to refs/heads/main is refused")
	assert.Contains(t, out, "approved; running `vercel --prod` once")
	assert.Empty(t, r.remoteRef("refs/heads/other-branch"), "the hook refused the push")
	assert.Empty(t, r.remoteRef("refs/tags/v9"))
	assert.NotEmpty(t, r.remoteRef("refs/heads/itervox/eng-1"), "the issue branch is pushed")
	assert.Empty(t, readLines(r.ghLog), "gh pr merge never ran")
	assert.Equal(t, []string{"--prod"}, readLines(r.vercelLog), "the approved command ran exactly once")
	assert.ElementsMatch(t, []string{KindBranch, KindBranch, KindTag, KindSelfMerge}, d.kinds())
	assert.Zero(t, d.badAuth)
	assert.Equal(t, 1, d.probes)

	argv, err := os.ReadFile(r.argvLog)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(argv), remoteToken), "the token is never in an ssh argument")
	assert.Contains(t, string(argv), "ExitOnForwardFailure=yes")

	dir := rt.Dir()
	rt.Remove()
	assert.NoDirExists(t, dir, "the runtime directory is removed at run end")
}

// Without a callback (the worker's sshd refuses the reverse forward) the
// worker still refuses the pushes; the approval command is refused, never
// run, and the refusals land in the worker's refusal log.
func TestRemoteGuardrailsWithoutCallbackStillRefuse(t *testing.T) {
	r := newRemoteRig(t)
	d := &fakeGuardrailDaemon{}
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	t.Setenv(sshtest.EnvRefuseForward, "1")

	rt, err := InstallRemote(context.Background(), r.options(srv.Listener.Addr().String()))
	require.NoError(t, err)
	t.Cleanup(rt.Remove)
	assert.False(t, rt.Callback())
	require.Error(t, rt.CallbackErr())

	got := r.turn(rt)
	out := got["output"]
	assert.Equal(t, "1", got["hook_other"], out)
	assert.Equal(t, "1", got["tag"], out)
	assert.Equal(t, "1", got["merge"], out)
	assert.Equal(t, "1", got["deploy"], out)
	assert.Equal(t, "no", got["token_in_env"])
	assert.Empty(t, readLines(r.vercelLog), "no human could approve: the command never ran")
	assert.Empty(t, r.remoteRef("refs/heads/other-branch"))
	assert.Empty(t, d.kinds(), "nothing reached the daemon")
	assert.Equal(t, []string{"vercel --prod"}, rt.RefusedApprovals(), "read back from the worker")
}

// A worker of another platform than this binary is refused before
// anything is uploaded: its shims could not run there.
func TestInstallRemoteRefusesAnotherPlatform(t *testing.T) {
	r := newRemoteRig(t)
	fakeProgram(t, r.fakes, "uname", "case \"$1\" in -s) echo Plan9;; -m) echo mips;; esac\n")
	_, err := InstallRemote(context.Background(), r.options(""))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRemotePlatform), "%v", err)
	assert.Contains(t, err.Error(), "plan9/mips")
	assert.NoDirExists(t, filepath.Join(r.home, ".cache", "itervox"), "nothing was uploaded")
}

// The binary is uploaded once per content hash: a second run on the same
// worker reuses the cached copy; a corrupted cache entry is replaced.
func TestInstallRemoteReusesTheCachedBinary(t *testing.T) {
	r := newRemoteRig(t)
	uploads := func() int {
		data, _ := os.ReadFile(r.argvLog)
		return strings.Count(string(data), " && cat > ")
	}
	rt1, err := InstallRemote(context.Background(), r.options(""))
	require.NoError(t, err)
	t.Cleanup(rt1.Remove)
	require.Equal(t, 1, uploads())
	rt2, err := InstallRemote(context.Background(), r.options(""))
	require.NoError(t, err)
	t.Cleanup(rt2.Remove)
	assert.Equal(t, 1, uploads(), "the cached binary is reused")
	assert.NotEqual(t, rt1.Dir(), rt2.Dir(), "each run has its own runtime directory")

	// Same size, different bytes: the worker's binary reports another hash.
	cached := filepath.Join(r.home, filepath.FromSlash(rt1.bin))
	data, err := os.ReadFile(cached)
	require.NoError(t, err)
	data[len(data)-1] ^= 0xff
	require.NoError(t, os.WriteFile(cached, data, 0o700))
	rt3, err := InstallRemote(context.Background(), r.options(""))
	require.NoError(t, err)
	t.Cleanup(rt3.Remove)
	assert.Equal(t, 2, uploads(), "a corrupted cache entry is uploaded again")
}

func TestPlatformOf(t *testing.T) {
	for _, c := range []struct{ s, m, goos, goarch string }{
		{"Linux", "x86_64", "linux", "amd64"},
		{"Linux", "aarch64", "linux", "arm64"},
		{"Darwin", "arm64", "darwin", "arm64"},
		{"Darwin", "x86_64", "darwin", "amd64"},
		{"FreeBSD", "amd64", "freebsd", "amd64"},
		{"Linux", "armv7l", "linux", "arm"},
	} {
		goos, goarch := platformOf(c.s, c.m)
		assert.Equal(t, c.goos+"/"+c.goarch, goos+"/"+goarch, c.s+" "+c.m)
	}
}
