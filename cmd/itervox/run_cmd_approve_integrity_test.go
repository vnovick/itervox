package main

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/workflow"
)

// policyFingerprint hashes every file under roots (files or directories).
func policyFingerprint(t *testing.T, roots ...string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = sha256.Sum256(data)
			return nil
		}))
	}
	return out
}

// TestRunApproveAllowsOneCommandOnceAndWritesNoPolicy: `itervox run
// --approve <command>` is an approval like any other — it lets that one
// command run once in that run (a second attempt in the same run is refused
// and named again), and it changes no policy: WORKFLOW.md, the settings and
// rules layers, the playbooks, the profiles and the referee policy are
// byte-identical afterwards, and no settings save was made.
func TestRunApproveAllowsOneCommandOnceAndWritesNoPolicy(t *testing.T) {
	r := newRunRig(t, "", "guardrails:\n  enabled: true\n  approval_commands: [\"deploy prod\"]\n",
		func(rt runTurn) (agent.TurnResult, error) {
			for range 2 { // the agent tries the approved command twice
				cmd := exec.CommandContext(rt.ctx, "/bin/sh", "-c", "deploy prod")
				cmd.Dir = rt.ws
				env := agent.RunEnvFrom(rt.ctx)
				cmd.Env = append(os.Environ(), env.Vars...)
				if env.PathPrepend != "" {
					cmd.Env = append(cmd.Env, "PATH="+env.PathPrepend+string(os.PathListSeparator)+os.Getenv("PATH"))
				}
				out, err := cmd.CombinedOutput()
				rt.log.Info("agent: deploy", "output", string(out), "error", err)
			}
			if err := commitFeature(rt.ws, "feature.txt"); err != nil {
				return agent.TurnResult{Failed: true, FailureText: err.Error()}, err
			}
			return agent.TurnResult{ResultText: "Deployed.", AllTextBlocks: []string{"Deployed."}}, nil
		})
	deployLog := filepath.Join(r.root, "deploy.log")
	require.NoError(t, os.WriteFile(filepath.Join(r.root, "bin", "deploy"), []byte("#!/bin/sh\necho \"$*\" >> "+deployLog+"\n"), 0o755))
	home := os.Getenv("HOME")
	files := map[string]string{
		filepath.Join(r.repo, ".itervox", "settings.local.jsonc"):       "{\"agent\": {}}\n",
		filepath.Join(r.repo, ".itervox", "rules.json"):                 "{\"rules\": []}\n",
		filepath.Join(r.repo, ".itervox", "playbooks", "bugfix.md"):     "# Bugfix\n",
		filepath.Join(r.repo, ".referee.yml"):                           "human: [\".github/**\"]\n",
		filepath.Join(home, ".itervox", "settings.jsonc"):               "{}\n",
		filepath.Join(home, ".itervox", "rules.json"):                   "{\"rules\": []}\n",
		filepath.Join(r.repo, ".itervox", "agents", "coder", "SOUL.md"): "You code.\n",
	}
	for p, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	roots := []string{filepath.Join(r.repo, "WORKFLOW.md"), filepath.Join(r.repo, ".referee.yml"),
		filepath.Join(r.repo, ".itervox", "settings.local.jsonc"), filepath.Join(r.repo, ".itervox", "rules.json"),
		filepath.Join(r.repo, ".itervox", "playbooks"), filepath.Join(r.repo, ".itervox", "agents"),
		filepath.Join(home, ".itervox", "settings.jsonc"), filepath.Join(home, ".itervox", "rules.json")}
	before, writes := policyFingerprint(t, roots...), workflow.SelfWriteCount()

	code, out, errOut := r.run("", "--issue", "ENG-1", "--approve", "deploy prod")
	data, err := os.ReadFile(deployLog)
	require.NoError(t, err, "the approved command ran; stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Equal(t, "prod\n", string(data), "exactly once: the approval was spent by its first use")
	assert.Equal(t, runExitNeedsInput, code, "the second attempt needs a new approval; stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Contains(t, out, "approval needed for `deploy prod`")

	assert.Equal(t, before, policyFingerprint(t, roots...), "the policy files are byte-identical")
	assert.Equal(t, writes, workflow.SelfWriteCount(), "no settings save")
	assert.False(t, strings.Contains(string(mustRead(t, filepath.Join(r.repo, "WORKFLOW.md"))), "pre_approved"),
		"the approval is not written into the config")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
