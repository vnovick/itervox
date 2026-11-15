package referee

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLanesClassifiesDiffAndExitCodes: with no policy file on the base ref
// the default human lane applies; the exit code is 0 for auto, 10 for
// second_opinion and 20 for human, and --json has the stable shape.
func TestLanesClassifiesDiffAndExitCodes(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{"main.go": "package main\n"})

	r.git("checkout", "-q", "-b", "auto")
	r.commit("code", map[string]string{"main.go": "package main // changed\n", "lib/util.go": "package lib\n"})
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "lane: auto")
	assert.Contains(t, out, "auto (2):\n  lib/util.go\n  main.go\n")
	assert.Contains(t, out, "policy: default (.referee.yml not on the base ref)")

	r.git("checkout", "-q", "-b", "pipeline")
	r.commit("ci", map[string]string{
		".github/workflows/ci.yml": "on: push\n",
		"web/vitest.config.ts":     "export default {}\n",
	})
	code, out, errOut = referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rep), out)
	assert.Equal(t, float64(1), rep["version"])
	assert.Equal(t, "human", rep["lane"])
	assert.Equal(t, float64(20), rep["exit_code"])
	assert.Equal(t, []any{".github/workflows/ci.yml", "web/vitest.config.ts"}, rep["human"])
	assert.Equal(t, []any{}, rep["second_opinion"], "empty lanes are [] not null")
	assert.Equal(t, []any{"lib/util.go", "main.go"}, rep["auto"])
	for _, k := range []string{"base", "base_commit", "merge_base", "head", "head_commit", "policy"} {
		assert.NotEmpty(t, rep[k], k)
	}

	// A base policy with a second_opinion lane.
	r.git("checkout", "-q", "main")
	r.commit("policy", map[string]string{".referee.yml": "second_opinion:\n  - \"lib/**\"\n"})
	r.git("checkout", "-q", "-b", "second")
	r.commit("lib", map[string]string{"lib/util.go": "package lib\n"})
	code, out, errOut = referee(t, nil, "lanes", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 10, code, out+errOut)
	assert.Contains(t, out, "second_opinion (1):\n  lib/util.go\n")
}

// TestLanesPolicyIsReadFromBaseRefNotHead: the branch under review edits
// .referee.yml to empty the human lane (in its commit AND in the working
// tree) and touches a file the base policy puts in the human lane. The
// head's policy is ignored: the lane is human, by the base's policy, and
// the policy edit itself is human lane too.
func TestLanesPolicyIsReadFromBaseRefNotHead(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		".referee.yml": "human:\n  - \"deploy/**\"\nsecond_opinion: []\n",
		"README.md":    "hi\n",
	})
	r.git("checkout", "-q", "-b", "agent")
	r.commit("weaken the policy and touch deploy", map[string]string{
		".referee.yml":     "human: []\n",
		"deploy/prod.yaml": "replicas: 1\n",
	})
	// The working tree too, uncommitted: still ignored.
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, ".referee.yml"), []byte("human: []\nsecond_opinion: []\n"), 0o644))

	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep lanesReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, "human", rep.Lane)
	assert.Equal(t, []string{".referee.yml", "deploy/prod.yaml"}, rep.Human)
	assert.Contains(t, rep.Policy, ".referee.yml@", "the policy came from the base commit")
}

// TestLanesStepSummary: with GITHUB_STEP_SUMMARY set, lanes appends a
// Markdown summary of the lanes and their paths.
func TestLanesStepSummary(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{"a.go": "package a\n"})
	r.git("checkout", "-q", "-b", "ci")
	r.commit("ci", map[string]string{"Makefile": "test:\n\tgo test ./...\n", "a.go": "package a // x\n"})
	summary := filepath.Join(t.TempDir(), "summary.md")
	require.NoError(t, os.WriteFile(summary, []byte("earlier step\n"), 0o644))

	code, _, errOut := referee(t, map[string]string{"GITHUB_STEP_SUMMARY": summary}, "lanes", "--dir", r.dir, "--base", "main")
	require.Equal(t, 20, code, errOut)
	raw, err := os.ReadFile(summary)
	require.NoError(t, err)
	md := string(raw)
	assert.Contains(t, md, "earlier step\n### referee lanes", "appended, not overwritten")
	assert.Contains(t, md, "Lane: **human**")
	assert.Contains(t, md, "<summary>human (1)</summary>\n\n- `Makefile`\n")
	assert.Contains(t, md, "<summary>auto (1)</summary>\n\n- `a.go`\n")
}

// TestLanesBaseDefaultsToGitHubBaseRef: in a pull_request workflow --base
// defaults to origin/$GITHUB_BASE_REF; with neither it is a usage error.
func TestLanesBaseDefaultsToGitHubBaseRef(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{"a.go": "package a\n"})
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	code, out, errOut := referee(t, map[string]string{"GITHUB_BASE_REF": "main"}, "lanes", "--dir", r.dir)
	assert.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "diff: origin/main...HEAD")
	code, _, errOut = referee(t, nil, "lanes", "--dir", r.dir)
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "--base is required")
}

// TestParsePolicy: absent keys take the defaults, [] turns a lane off,
// unknown keys and malformed patterns are errors, JSON parses.
func TestParsePolicy(t *testing.T) {
	l, err := parsePolicy(nil)
	require.NoError(t, err)
	assert.Contains(t, l.Human, ".github/**")
	assert.Empty(t, l.SecondOpinion)

	l, err = parsePolicy([]byte("human: []\n"))
	require.NoError(t, err)
	assert.Empty(t, l.Human)

	l, err = parsePolicy([]byte(`{"human": ["ci/**", " ci/** "], "second_opinion": ["src/auth/**"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"ci/**"}, l.Human)
	assert.Equal(t, []string{"src/auth/**"}, l.SecondOpinion)

	for _, bad := range []string{"humans: [x]\n", "human: [\"[unclosed\"]\n", "human: [\"!\"]\n", "human: notalist\n"} {
		_, err := parsePolicy([]byte(bad))
		assert.Error(t, err, bad)
	}
}
