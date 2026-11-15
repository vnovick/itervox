package referee

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFailClosedExitCodes: every usage, git or I/O error exits 2, never 0
// (nor 10, nor 1): a CI step that cannot decide must not pass.
func TestFailClosedExitCodes(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{".referee.yml": "human: [\"[unclosed\"]\n", "a.go": "package a\n"})
	good := newTestRepo(t)
	good.commit("base", map[string]string{"a.go": "package a\n"})
	notRepo := t.TempDir()
	policyFile := filepath.Join(t.TempDir(), "p.yml")
	require.NoError(t, os.WriteFile(policyFile, []byte("human: []\n"), 0o644))

	cases := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"judge"}},
		{"lanes unknown flag", []string{"lanes", "--bogus"}},
		{"lanes no base", []string{"lanes", "--dir", good.dir}},
		{"lanes bad base", []string{"lanes", "--dir", good.dir, "--base", "no-such-branch"}},
		{"lanes option-shaped base", []string{"lanes", "--dir", good.dir, "--base", "--output=/tmp/x"}},
		{"lanes bad head", []string{"lanes", "--dir", good.dir, "--base", "main", "--head", "deadbeef"}},
		{"lanes malformed base policy", []string{"lanes", "--dir", r.dir, "--base", "main"}},
		{"lanes not a repo", []string{"lanes", "--dir", notRepo, "--base", "main"}},
		{"lanes stray argument", []string{"lanes", "--dir", good.dir, "--base", "main", "extra"}},
		{"lanes unwritable summary", []string{"lanes", "--dir", good.dir, "--base", "main"}},
		{"audit no base", []string{"audit", "--dir", good.dir}},
		{"audit bad base", []string{"audit", "--dir", good.dir, "--base", "no-such-branch"}},
		{"audit not a repo", []string{"audit", "--dir", notRepo, "--base", "main"}},
		{"pin no hash", []string{"pin", "--file", policyFile}},
		{"pin short hash", []string{"pin", "--file", policyFile, "--policy-sha256", "abc"}},
		{"pin missing file", []string{"pin", "--file", filepath.Join(notRepo, "missing"), "--policy-sha256", "0000000000000000000000000000000000000000000000000000000000000000"}},
		{"run no key", []string{"run", "--name", "t", "--out", filepath.Join(notRepo, "r.jsonl"), "--", "true"}},
		{"run missing key", []string{"run", "--name", "t", "--key", filepath.Join(notRepo, "missing.key"), "--out", filepath.Join(notRepo, "r.jsonl"), "--", "true"}},
		{"run no command", []string{"run", "--name", "t", "--key", "k", "--out", "o"}},
		{"keygen no out", []string{"keygen"}},
		{"verify no pubkey", []string{"verify", filepath.Join(notRepo, "r.jsonl")}},
		{"verify missing file", []string{"verify", filepath.Join(notRepo, "r.jsonl"), "--pubkey", filepath.Join(notRepo, "p.pub")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{}
			if c.name == "lanes unwritable summary" {
				env["GITHUB_STEP_SUMMARY"] = filepath.Join(notRepo, "no-such-dir", "summary.md")
			}
			code, out, errOut := referee(t, env, c.args...)
			assert.Equal(t, exitError, code, "stdout: %s\nstderr: %s", out, errOut)
		})
	}
}
