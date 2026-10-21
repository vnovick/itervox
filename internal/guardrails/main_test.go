package guardrails

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/gitexec"
)

// TestMain lets the test binary stand in for the itervox binary: shims and
// hooks written by Prepare exec it with a guardrail entrypoint, exactly as
// they exec itervox in production.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && IsEntrypoint(os.Args[1]) {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	gitexec.UnsetInProcess()
	os.Exit(m.Run())
}

// rig is a real git setup: a bare remote, a clone on the issue branch, and
// a prepared guardrails runtime for it.
type rig struct {
	t      *testing.T
	root   string
	remote string
	work   string
	rt     *Runtime
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitexec.Command(context.Background(), dir, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func newRig(t *testing.T, mutate func(*Options)) *rig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := &rig{t: t, root: root, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	git(t, root, "init", "-q", "--bare", "-b", "main", r.remote)
	git(t, root, "init", "-q", "-b", "main", r.work)
	git(t, r.work, "remote", "add", "origin", r.remote)
	r.commit("base")
	git(t, r.work, "push", "-q", "origin", "main")
	git(t, r.work, "checkout", "-q", "-b", "itervox/eng-1")
	exe, err := os.Executable()
	require.NoError(t, err)
	opts := Options{
		ParentDir:         root,
		Executable:        exe,
		WorkDir:           r.work,
		Identifier:        "ENG-1",
		IssueBranch:       "itervox/eng-1",
		ProtectedBranches: []string{"main", "master", "develop"},
		ApprovalCommands:  []string{"vercel --prod"},
	}
	if mutate != nil {
		mutate(&opts)
	}
	r.rt, err = Prepare(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(r.rt.Remove)
	return r
}

func (r *rig) commit(msg string) {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.work, "f.txt"), []byte(msg+"\n"), 0o644))
	git(r.t, r.work, "add", "f.txt")
	git(r.t, r.work, "commit", "-q", "-m", msg)
}

// agent runs script as the agent would: through the run's command prefix,
// in the work tree, with extraPath ahead of the inherited PATH (fake
// programs) but behind the shims.
func (r *rig) agent(script string, extraPath ...string) (string, int) {
	r.t.Helper()
	path := os.Getenv("PATH")
	for _, p := range extraPath {
		path = p + string(os.PathListSeparator) + path
	}
	cmd := exec.Command("/bin/sh", "-c", r.rt.CommandPrefix()+"/bin/sh -c "+agent.ShellQuote(script))
	cmd.Dir = r.work
	cmd.Env = append(gitexec.Environ(), "PATH="+path, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(r.t, err, &exitErr, "agent script: %s", out)
		code = exitErr.ExitCode()
	}
	return string(out), code
}

func (r *rig) remoteRef(ref string) string {
	r.t.Helper()
	cmd := gitexec.Command(context.Background(), r.remote, "rev-parse", "-q", "--verify", ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fakeProgram writes an executable sh script name into dir.
func fakeProgram(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755))
}
