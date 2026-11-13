package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolationFixture is a fake machine for the real-process isolation tests
// (#258): a fake $HOME holding the workspace root with this run's workspace
// and another issue's, a sibling directory, and a planted ~/.ssh key.
type isolationFixture struct {
	home, ws, otherWS, sibling, sshKey, tmp string
	policy                                  IsolationPolicy
}

const plantedSecret = "planted-" + "fixture-not-a-key"

func newIsolationFixture(t *testing.T) isolationFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var -> /private/var
	require.NoError(t, err)
	f := isolationFixture{
		home: filepath.Join(base, "home"),
		tmp:  filepath.Join(base, "tmp"),
	}
	wsRoot := filepath.Join(f.home, ".itervox", "workspaces", "proj")
	f.ws = filepath.Join(wsRoot, "ISSUE-1")
	f.otherWS = filepath.Join(wsRoot, "ISSUE-2")
	f.sibling = filepath.Join(f.home, "notes")
	f.sshKey = filepath.Join(f.home, ".ssh", "id_test")
	for _, d := range []string{f.ws, f.otherWS, f.sibling, filepath.Dir(f.sshKey), f.tmp} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(f.sshKey, []byte(plantedSecret), 0o600))
	// An agent state file (IsolationHomeWrite): a single writable file,
	// which sandbox-exec expresses as a regex rule.
	require.NoError(t, os.WriteFile(filepath.Join(f.home, ".claude.json"), []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.otherWS, "code.go"), []byte(plantedSecret), 0o644))
	f.policy = BuildIsolationPolicy(IsolationInputs{
		Workspace: f.ws, WorkspaceRoot: filepath.Join(f.home, ".itervox", "workspaces"),
		Home: f.home, TempDir: f.tmp,
	})
	return f
}

// runSandboxed runs `sh -c script` inside m with policy p and returns its
// combined output and error.
func runSandboxed(t *testing.T, m IsolationMechanism, p IsolationPolicy, script string) (string, error) {
	t.Helper()
	argv, err := m.Wrap([]string{"/bin/sh", "-c", script}, p)
	require.NoError(t, err)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// assertConfined is the acceptance: the sandboxed shell writes in its
// workspace and the temp dir, cannot write a sibling directory of $HOME,
// cannot read the planted ~/.ssh key, and cannot read another workspace.
func assertConfined(t *testing.T, m IsolationMechanism, f isolationFixture) {
	t.Helper()
	out, err := runSandboxed(t, m, f.policy, "echo ok > "+ShellQuote(filepath.Join(f.ws, "out"))+
		" && echo ok > "+ShellQuote(filepath.Join(f.tmp, "out")))
	require.NoError(t, err, "the workspace and temp dir must be writable: %s", out)
	data, err := os.ReadFile(filepath.Join(f.ws, "out"))
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(data))

	out, err = runSandboxed(t, m, f.policy, "echo '{}' > "+ShellQuote(filepath.Join(f.home, ".claude.json")))
	require.NoError(t, err, "the agent CLI's state file must be writable: %s", out)

	out, err = runSandboxed(t, m, f.policy, "echo leak > "+ShellQuote(filepath.Join(f.sibling, "f")))
	assert.Error(t, err, "a sibling directory in $HOME must not be writable: %s", out)
	assert.NoFileExists(t, filepath.Join(f.sibling, "f"))

	out, err = runSandboxed(t, m, f.policy, "cat "+ShellQuote(f.sshKey))
	assert.Error(t, err, "the planted ~/.ssh key must not be readable")
	assert.NotContains(t, out, plantedSecret)

	out, err = runSandboxed(t, m, f.policy, "cat "+ShellQuote(filepath.Join(f.otherWS, "code.go")))
	assert.Error(t, err, "another workspace must not be readable")
	assert.NotContains(t, out, plantedSecret)

	out, err = runSandboxed(t, m, f.policy, "sh -c 'echo nested > "+filepath.Join(f.sibling, "g")+"'")
	assert.Error(t, err, "a child process is confined too: %s", out)
	assert.NoFileExists(t, filepath.Join(f.sibling, "g"))
}

// TestIsolationRealProcessBubblewrap is the Linux real-process acceptance
// (#258) for bubblewrap. Skipped when bwrap is missing or cannot create a
// sandbox on this machine.
func TestIsolationRealProcessBubblewrap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap is Linux only")
	}
	m := newBubblewrap()
	if err := m.Available(); err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	assertConfined(t, m, newIsolationFixture(t))
}

// TestIsolationRealProcessSandboxExec is the macOS real-process acceptance
// (#258): the same checks under sandbox-exec. Runs on the macOS CI job.
func TestIsolationRealProcessSandboxExec(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	m := newSandboxExec()
	require.NoError(t, m.Available(), "sandbox-exec must work on the macOS runner")
	assertConfined(t, m, newIsolationFixture(t))
}
