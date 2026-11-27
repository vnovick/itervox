package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protectFixture is a project whose directory lies inside a writable path
// of the run (isolation_extra_write names the directory holding it), with
// every live policy file, the token store, and the agent's own worktree
// copy of WORKFLOW.md.
type protectFixture struct {
	in                 IsolationInputs
	base, project, ws  string
	live, tokens, copy string
	protected          []string
}

func newProtectFixture(t *testing.T) protectFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := protectFixture{base: base, project: filepath.Join(base, "src", "project")}
	f.ws = filepath.Join(base, "workspaces", "ENG-1")
	home := filepath.Join(base, "home")
	for _, d := range []string{filepath.Join(f.project, ".itervox"), f.ws, filepath.Join(home, ".itervox"), filepath.Join(base, "tmp")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	f.live = filepath.Join(f.project, "WORKFLOW.md")
	f.protected = []string{f.live}
	for _, rel := range IsolationProjectProtect {
		f.protected = append(f.protected, filepath.Join(f.project, rel))
	}
	for _, rel := range IsolationHomeProtect {
		f.protected = append(f.protected, filepath.Join(home, rel))
	}
	for _, p := range f.protected {
		require.NoError(t, os.WriteFile(p, []byte("policy\n"), 0o644))
	}
	f.tokens = filepath.Join(f.project, ".itervox", "tokens.json")
	require.NoError(t, os.WriteFile(f.tokens, []byte("{}"), 0o600))
	// An existing file next to the policy files: under Landlock a directory
	// holding a carved-out path takes no new entries (the guide's limit), so
	// the check writes an existing one.
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "notes.txt"), nil, 0o644))
	f.copy = filepath.Join(f.ws, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f.copy, []byte("worktree copy\n"), 0o644))
	f.in = IsolationInputs{
		Workspace: f.ws, ProjectDir: f.project, WorkflowFile: f.live, Home: home,
		TempDir: filepath.Join(base, "tmp"),
		// The run may write the whole tree that holds the project (and
		// ~/.itervox, through the same rule): every live file is reachable.
		ExtraWrite: []string{filepath.Join(base, "src"), filepath.Join(home, ".itervox")},
	}
	return f
}

// TestIsolationProtectsLivePolicyFiles: with isolation: process, the
// daemon's live policy files a writable path of the run reaches are
// read-only (bwrap read-only binds after the writable bind, an SBPL write
// deny after the write allow, no Landlock write grant); the token store
// stays hidden; the agent's worktree copy of WORKFLOW.md stays writable;
// and nothing is listed when no writable path reaches the files.
func TestIsolationProtectsLivePolicyFiles(t *testing.T) {
	f := newProtectFixture(t)
	p := BuildIsolationPolicy(f.in)
	assert.ElementsMatch(t, f.protected, p.Protect, "every live policy file is protected")
	assert.NotContains(t, p.Protect, f.copy, "the agent's own worktree copy is not the daemon's file")
	assert.Contains(t, p.Deny, f.tokens, "the token store stays unreadable")
	assert.NotContains(t, p.Protect, f.tokens)

	args := bubblewrapArgs(p)
	sbpl := sandboxExecProfile(p)
	src := filepath.Join(f.base, "src")
	bindSrc := argsIndexSeq(args, []string{"--bind", src, src})
	require.GreaterOrEqual(t, bindSrc, 0)
	for _, file := range f.protected {
		ro := argsIndexSeq(args, []string{"--ro-bind", file, file})
		assert.Greater(t, ro, -1, "bwrap: %s is bound read-only", file)
		lit := "(literal " + sbplString(file) + ")"
		assert.Contains(t, sbpl, "(deny file-write* "+lit+")", "sbpl: %s", file)
	}
	assert.Greater(t, argsIndexSeq(args, []string{"--ro-bind", f.live, f.live}), bindSrc,
		"bwrap: the read-only bind comes after the writable one, so it wins")
	assert.Less(t, strings.Index(sbpl, "(allow file-read* file-write* (subpath "+sbplString(src)+"))"),
		strings.Index(sbpl, "(deny file-write* (literal "+sbplString(f.live)+"))"), "sbpl: last match wins")

	f.in.ExtraWrite = nil
	assert.Empty(t, BuildIsolationPolicy(f.in).Protect, "outside every writable path they are read-only already")
}

// TestIsolationRealProcessProtectsPolicyFiles: under a real sandbox the
// live policy files are readable and not writable, and cannot be replaced
// by rename; an existing file next to them and the agent's worktree copy stay
// writable. Skipped where neither bubblewrap nor Landlock works.
func TestIsolationRealProcessProtectsPolicyFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real-process protect checks run on Linux (bubblewrap or Landlock); sandbox-exec is covered by the profile test")
	}
	var mechs []IsolationMechanism
	for _, m := range platformIsolationMechanisms() {
		if err := m.Available(); err != nil {
			t.Logf("%s unavailable: %v", m.Name(), err)
			continue
		}
		mechs = append(mechs, m)
	}
	if len(mechs) == 0 {
		t.Skip("no sandbox mechanism works on this machine (bwrap missing or user namespaces disabled, no Landlock)")
	}
	for _, m := range mechs {
		t.Run(m.Name(), func(t *testing.T) {
			f := newProtectFixture(t)
			p := BuildIsolationPolicy(f.in)
			for _, file := range f.protected {
				out, err := runSandboxed(t, m, p, "echo changed > "+ShellQuote(file))
				assert.Error(t, err, "%s must not be writable: %s", file, out)
				data, _ := os.ReadFile(file)
				assert.Equal(t, "policy\n", string(data), "%s unchanged", file)
			}
			out, err := runSandboxed(t, m, p, "echo new > "+ShellQuote(f.live+".tmp")+" && mv "+ShellQuote(f.live+".tmp")+" "+ShellQuote(f.live))
			assert.Error(t, err, "replacing WORKFLOW.md by rename must fail: %s", out)
			data, _ := os.ReadFile(f.live)
			assert.Equal(t, "policy\n", string(data))
			out, err = runSandboxed(t, m, p, "cat "+ShellQuote(f.live))
			require.NoError(t, err, "the live policy stays readable: %s", out)
			assert.Equal(t, "policy\n", out)
			out, err = runSandboxed(t, m, p, "echo ok > "+ShellQuote(f.copy)+" && echo ok > "+ShellQuote(filepath.Join(f.project, "notes.txt")))
			require.NoError(t, err, "the worktree copy and the rest of the writable tree stay writable: %s", out)
		})
	}
}
