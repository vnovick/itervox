package guardrails

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #204: an agent's push reaches only the issue branch. Real git, a real
// bare remote, the real pre-push hook installed through core.hooksPath by
// the run's command prefix: a push to main, to another existing branch and
// a tag push are refused and leave the remote untouched; a push to the
// issue branch goes through and the repository's own pre-push hook still
// runs after ours.
func TestPrePushRefusesNonIssueBranch(t *testing.T) {
	r := newRig(t, nil)
	git(t, r.work, "push", "-q", "origin", "main:refs/heads/release")
	// The repository's own hook, which must keep running (chained).
	repoHook := filepath.Join(r.work, ".git", "hooks", "pre-push")
	marker := filepath.Join(r.root, "repo-hook-ran")
	require.NoError(t, os.WriteFile(repoHook, []byte("#!/bin/sh\ncat > "+marker+"\n"), 0o755))
	release := r.remoteRef("refs/heads/release")
	mainBefore := r.remoteRef("refs/heads/main")
	r.commit("agent work")

	out, code := r.agent("git push origin HEAD:main")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, `agents may push only to the issue branch "itervox/eng-1"`)
	assert.Contains(t, out, "git push origin HEAD:refs/heads/itervox/eng-1")
	assert.Equal(t, mainBefore, r.remoteRef("refs/heads/main"), "main must not move")

	out, code = r.agent("git push origin HEAD:release")
	assert.NotZero(t, code, out)
	assert.Equal(t, release, r.remoteRef("refs/heads/release"), "another branch must not move")

	out, code = r.agent("git tag v9 && git push origin v9")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "pushing tag refs/tags/v9 is refused")
	assert.Empty(t, r.remoteRef("refs/tags/v9"))

	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "a refused push never reaches the repository's hook")

	out, code = r.agent("git push -u origin HEAD")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	got, err := os.ReadFile(marker)
	require.NoError(t, err, "the repository's own pre-push hook runs after the guardrail")
	assert.Contains(t, string(got), "refs/heads/itervox/eng-1", "the chained hook gets the same input")
}

// #204: force pushes and deletions of the issue branch are refused unless
// guardrails.allow_force_push; a fast-forward is fine.
func TestPrePushRefusesForcePush(t *testing.T) {
	r := newRig(t, nil)
	r.commit("one")
	out, code := r.agent("git push -q origin HEAD")
	require.Zero(t, code, out)
	pushed := r.remoteRef("refs/heads/itervox/eng-1")

	git(t, r.work, "commit", "-q", "--amend", "-m", "rewritten")
	out, code = r.agent("git push --force origin HEAD")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "force push to refs/heads/itervox/eng-1 is refused")
	assert.Equal(t, pushed, r.remoteRef("refs/heads/itervox/eng-1"), "history must not be rewritten")

	out, code = r.agent("git push --force-with-lease origin HEAD")
	assert.NotZero(t, code, out)
	assert.Equal(t, pushed, r.remoteRef("refs/heads/itervox/eng-1"))

	out, code = r.agent("git push origin --delete itervox/eng-1")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "deleting refs/heads/itervox/eng-1 is refused")
	assert.Equal(t, pushed, r.remoteRef("refs/heads/itervox/eng-1"))

	// --no-verify would skip the hook: the git shim refuses it.
	out, code = r.agent("git push --no-verify --force origin HEAD")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "replacing or skipping Itervox's git hooks is refused")
	assert.Equal(t, pushed, r.remoteRef("refs/heads/itervox/eng-1"))

	// A new commit on top of what was pushed is a fast-forward.
	git(t, r.work, "reset", "-q", "--hard", pushed)
	r.commit("two")
	out, code = r.agent("git push origin HEAD")
	require.Zero(t, code, out)

	// With allow_force_push the same rewrite goes through.
	allowed := newRig(t, func(o *Options) { o.AllowForcePush = true })
	allowed.commit("one")
	_, code = allowed.agent("git push -q origin HEAD")
	require.Zero(t, code)
	git(t, allowed.work, "commit", "-q", "--amend", "-m", "rewritten")
	out, code = allowed.agent("git push --force origin HEAD")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, allowed.work, "rev-parse", "HEAD"), allowed.remoteRef("refs/heads/itervox/eng-1"))
}

// An agent that creates its own branch for the issue may push it, once per
// run; it may not then push a second new branch.
func TestPrePushAdoptsOneNewBranch(t *testing.T) {
	r := newRig(t, nil)
	git(t, r.work, "checkout", "-q", "-b", "eng-1-fix")
	r.commit("work")
	out, code := r.agent("git push -u origin HEAD")
	require.Zero(t, code, out)
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"))
	r.commit("more")
	out, code = r.agent("git push origin HEAD")
	require.Zero(t, code, out, "the adopted branch keeps taking pushes")

	git(t, r.work, "checkout", "-q", "-b", "eng-1-other")
	out, code = r.agent("git push -u origin HEAD")
	assert.NotZero(t, code, out)
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-other"))
}

type fakeRepo struct {
	ff     bool
	branch string
}

func (f fakeRepo) FastForward(string, string) bool { return f.ff }
func (f fakeRepo) CurrentBranch() string           { return f.branch }

func TestCheckPushRules(t *testing.T) {
	p := Policy{Dir: t.TempDir(), IssueBranch: "itervox/eng-1", ProtectedBranches: []string{"main"}}
	sha := "1111111111111111111111111111111111111111"
	zero := "0000000000000000000000000000000000000000"
	cases := []struct {
		name string
		u    RefUpdate
		repo fakeRepo
		kind string
	}{
		{"issue branch ff", RefUpdate{"refs/heads/x", sha, "refs/heads/itervox/eng-1", sha}, fakeRepo{ff: true}, ""},
		{"issue branch rewrite", RefUpdate{"refs/heads/x", sha, "refs/heads/itervox/eng-1", sha}, fakeRepo{}, KindForcePush},
		{"create issue branch", RefUpdate{"refs/heads/x", sha, "refs/heads/itervox/eng-1", zero}, fakeRepo{}, ""},
		{"main", RefUpdate{"refs/heads/x", sha, "refs/heads/main", sha}, fakeRepo{ff: true}, KindBranch},
		{"new main is never adopted", RefUpdate{"refs/heads/main", sha, "refs/heads/main", zero}, fakeRepo{branch: "main"}, KindBranch},
		{"tag", RefUpdate{"refs/tags/v1", sha, "refs/tags/v1", zero}, fakeRepo{}, KindTag},
		{"delete", RefUpdate{"(delete)", zero, "refs/heads/itervox/eng-1", sha}, fakeRepo{}, KindDelete},
		{"notes ref", RefUpdate{"refs/notes/x", sha, "refs/notes/x", zero}, fakeRepo{}, KindBranch},
		{"new branch not checked out", RefUpdate{"refs/heads/y", sha, "refs/heads/y", zero}, fakeRepo{branch: "z"}, KindBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs, _ := p.CheckPush([]RefUpdate{tc.u}, tc.repo)
			if tc.kind == "" {
				assert.Empty(t, vs)
				return
			}
			require.Len(t, vs, 1)
			assert.Equal(t, tc.kind, vs[0].Kind)
		})
	}
}
