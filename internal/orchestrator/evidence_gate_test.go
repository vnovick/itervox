package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

func gitRepoWithCommit(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "c")
	return dir, run("rev-parse", "HEAD")
}

func writeEvidence(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, evidenceRelPathFor("implementer"))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

func noChecks(context.Context, string) (workspace.PRChecks, error) {
	return workspace.PRChecks{}, errors.New("must not be called")
}

// TestCheckEvidenceCommitStamp (#80): in a git worktree, evidence counts
// only for the commit it names; a short prefix of HEAD is accepted.
func TestCheckEvidenceCommitStamp(t *testing.T) {
	dir, head := gitRepoWithCommit(t)
	rel := evidenceRelPathFor("implementer")
	entry := `"checks":[{"name":"test","command":"make test","output":"ok","passed":true}]`

	writeEvidence(t, dir, `{"commit":"`+head+`",`+entry+`}`)
	assert.True(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok())

	writeEvidence(t, dir, `{"commit":"`+head[:8]+`",`+entry+`}`)
	assert.True(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok(), "short SHA prefix")

	writeEvidence(t, dir, `{"commit":"0123456789abcdef",`+entry+`}`)
	v := checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks)
	require.False(t, v.ok())
	assert.Contains(t, v.Missing[0], "is for commit \"0123456789abcdef\", not the current "+head[:12])

	writeEvidence(t, dir, `{`+entry+`}`)
	assert.False(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok(), "no commit stamp in a git worktree")
}

// TestCheckEvidenceSurvivesBookkeepingCommits (#80): evidence stamped on the
// agent's last commit still counts after commits that only touch .itervox/
// (the worker's handoff commit, or the agent committing the evidence file),
// but not after a code change.
func TestCheckEvidenceSurvivesBookkeepingCommits(t *testing.T) {
	dir, stamp := gitRepoWithCommit(t)
	rel := evidenceRelPathFor("implementer")
	commit := func(path string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err != nil { // keep an existing file (the evidence) as is
			require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
			require.NoError(t, os.WriteFile(full, []byte(path), 0o644))
		}
		for _, args := range [][]string{
			{"add", "-f", path},
			{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", path},
		} {
			out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
	}
	writeEvidence(t, dir, `{"commit":"`+stamp+`","checks":[{"name":"test","command":"make test","output":"ok","passed":true}]}`)

	commit(".itervox/handoff/2026-10-09T12-00-00Z_implementer.md")
	commit(rel)
	assert.True(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok(),
		"only .itervox/ bookkeeping changed since the stamp")

	commit("main.go")
	v := checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks)
	require.False(t, v.ok(), "code changed after the evidence was recorded")
	assert.Contains(t, v.Missing[0], "is for commit")

	// Moving code into .itervox/ is a code change too: git's rename
	// detection would list only the destination path.
	_, codeStamp := gitHEAD(t, dir)
	writeEvidence(t, dir, `{"commit":"`+codeStamp+`","checks":[{"name":"test","command":"make test","output":"ok","passed":true}]}`)
	require.True(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok())
	gitRun(t, dir, "mv", "main.go", ".itervox/handoff/main.go")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "move")
	assert.False(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).ok(),
		"code moved into .itervox/handoff/ after the stamp")
}

// TestCheckEvidenceRejectsSymbolicStamp (#80): the stamp must be a hex
// commit id; a ref name would follow the branch and never go stale.
func TestCheckEvidenceRejectsSymbolicStamp(t *testing.T) {
	dir, _ := gitRepoWithCommit(t)
	rel := evidenceRelPathFor("implementer")
	branch := gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	for _, stamp := range []string{"HEAD", branch, branch + "~0", "HEAD^{commit}"} {
		writeEvidence(t, dir, `{"commit":"`+stamp+`","checks":[{"name":"test","command":"make test","output":"ok","passed":true}]}`)
		v := checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks)
		assert.False(t, v.ok(), "stamp %q", stamp)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func gitHEAD(t *testing.T, dir string) (string, string) {
	t.Helper()
	return dir, gitRun(t, dir, "rev-parse", "HEAD")
}

func TestCheckEvidenceEntries(t *testing.T) {
	dir := t.TempDir() // not a git work tree: the commit stamp cannot be checked
	rel := evidenceRelPathFor("implementer")
	assert.Contains(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).Missing[0], "no evidence file")

	writeEvidence(t, dir, `not json`)
	assert.Contains(t, checkEvidence(context.Background(), dir, rel, []string{"test"}, "", noChecks).Missing[0], "is not valid JSON")

	writeEvidence(t, dir, `{"checks":[{"name":"Test","command":"make test","output":"ok","passed":true},`+
		`{"name":"lint","command":"","output":"ok","passed":true}]}`)
	v := checkEvidence(context.Background(), dir, rel, []string{"test", "lint"}, "", noChecks)
	require.Len(t, v.Missing, 1, "names match case-insensitively; an entry without a command does not count")
	assert.Contains(t, v.Missing[0], "`lint`: no passing entry")
}

// TestCheckEvidenceCI (#80): "ci" is met only by a PR whose checks all pass.
func TestCheckEvidenceCI(t *testing.T) {
	rel := evidenceRelPathFor("implementer")
	for _, tc := range []struct {
		name, pr string
		sum      workspace.PRChecks
		err      error
		want     string
	}{
		{"no PR", "", workspace.PRChecks{}, nil, "no pull request was found"},
		{"green", "u", workspace.PRChecks{Total: 3, Passed: 3}, nil, ""},
		{"pending", "u", workspace.PRChecks{Total: 3, Passed: 2, Pending: 1}, nil, "1 of 3 checks still running"},
		{"failed", "u", workspace.PRChecks{Total: 3, Passed: 1, Failed: 2}, nil, "2 of 3 checks failed"},
		{"unreadable", "u", workspace.PRChecks{}, errors.New("no checks reported"), "could not read the checks"},
	} {
		v := checkEvidence(context.Background(), t.TempDir(), rel, []string{"ci"}, tc.pr,
			func(context.Context, string) (workspace.PRChecks, error) { return tc.sum, tc.err })
		if tc.want == "" {
			assert.True(t, v.ok(), tc.name)
			continue
		}
		require.Len(t, v.Missing, 1, tc.name)
		assert.Contains(t, v.Missing[0], tc.want, tc.name)
	}
}

func TestBuildEvidenceBlock(t *testing.T) {
	assert.Empty(t, buildEvidenceBlock(nil, "x"))
	block := buildEvidenceBlock([]string{"test", "ci"}, ".itervox/evidence/implementer.json")
	assert.Contains(t, block, "- run.evidence_path: `.itervox/evidence/implementer.json`")
	assert.Contains(t, block, "- required checks: `test`")
	assert.Contains(t, block, "git rev-parse HEAD")
	assert.Contains(t, block, "`ci`: the pull request's CI checks must all pass")
	ciOnly := buildEvidenceBlock([]string{"ci"}, "x")
	assert.NotContains(t, ciOnly, "run.evidence_path", "ci alone needs no evidence file")
}
