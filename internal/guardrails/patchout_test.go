package guardrails

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// push: daemon: the agent's git push (any form, including an absolute git
// that only the pre-push hook sees) and every gh/glab command that opens,
// merges or changes a pull request or writes through the API are refused
// with the patch-out message, reported as patch_out; the real programs
// never run and nothing reaches the remote. Read-only commands and local
// git still work.
func TestPatchOutShimRefusesAgentPush(t *testing.T) {
	d := newFakeDaemon(t)
	r := newRig(t, func(o *Options) { o.DaemonURL, o.Token, o.PatchOut = d.srv.URL, "tok", true })
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	for _, name := range []string{"gh", "glab"} {
		fakeProgram(t, fakes, name, "echo "+name+" \"$@\" >> "+ran+"\n")
	}
	r.commit("feature")
	refused := []string{
		"git push origin HEAD",
		"git push -u origin itervox/eng-1",
		"git -C . push origin HEAD:refs/heads/itervox/eng-1",
		"git send-pack origin HEAD",
		"gh pr create --title x --body y",
		"gh pr merge 12 --squash",
		"gh -R acme/app pr edit 3 --base main",
		"gh pr comment 3 --body hi",
		"gh api -X POST repos/acme/app/pulls -f title=x",
		"gh api repos/acme/app/pulls -f title=x",
		"gh api graphql -f query='mutation { createPullRequest(input: {}) { clientMutationId } }'",
		"glab mr create --title x",
		"glab api --method PUT projects/1/merge_requests/2",
	}
	for _, script := range refused {
		out, code := r.agent(script, fakes)
		assert.Equal(t, 1, code, "%s: %s", script, out)
		assert.Contains(t, out, PatchOutMessage, script)
	}
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "no real gh/glab ran")
	assert.Empty(t, r.remoteRef("refs/heads/itervox/eng-1"), "nothing reached the remote")

	// git by its absolute path skips the shim; the pre-push hook refuses.
	realGit, err := resolveReal("git", os.Getenv("PATH"))
	require.NoError(t, err)
	out, code := r.agent(realGit + " push origin HEAD")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, PatchOutMessage, out)
	assert.Empty(t, r.remoteRef("refs/heads/itervox/eng-1"))

	kinds := d.kinds()
	require.Len(t, kinds, len(refused)+1)
	for _, k := range kinds {
		assert.Equal(t, KindPatchOut, k)
	}

	// Still allowed: reads and local git.
	for _, script := range []string{"gh pr view 3", "gh api repos/acme/app/pulls", "gh api -X GET search/issues -f q=x", "glab mr list"} {
		_, code := r.agent(script, fakes)
		assert.Zero(t, code, script)
	}
	out, code = r.agent("git status --short && git log -1 --format=%s")
	require.Zero(t, code, out)
	assert.Equal(t, "feature\n", out)
}

// No push mode (the default): the shims and hook behave exactly as before
// push: daemon existed: the agent pushes its issue branch and gh pr create
// is forwarded, and the agent environment is the two variables it always
// was, with nothing removed.
func TestPatchOutDefaultUnchanged(t *testing.T) {
	r := newRig(t, nil)
	assert.False(t, r.rt.Policy().PatchOut)
	fakes := filepath.Join(r.root, "fakebin")
	fakeProgram(t, fakes, "gh", "exit 0\n")
	r.commit("feature")
	out, code := r.agent("git push -q origin HEAD")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	_, code = r.agent("gh pr create --title x --body y", fakes)
	assert.Zero(t, code)

	env := r.rt.AgentEnv()
	assert.Equal(t, []string{"GIT_CONFIG_PARAMETERS=" + gitConfigParam("core.hooksPath", r.rt.HooksDir()), "ITERVOX_API_TOKEN="}, env.Vars)
	assert.Empty(t, env.Unset)
	assert.Equal(t, r.rt.BinDir(), env.PathPrepend)
	_, err := os.Stat(filepath.Join(r.rt.Dir(), forgeConfigDir))
	assert.True(t, os.IsNotExist(err), "no forge config dir without push: daemon")
}

// push: daemon: the agent environment drops git's credential helpers and
// points gh and glab at an empty configuration, and removes the forge and
// tracker token variables plus the run's extra names; core.hooksPath stays
// last in GIT_CONFIG_PARAMETERS.
func TestPatchOutAgentEnvShape(t *testing.T) {
	r := newRig(t, func(o *Options) { o.PatchOut, o.StripEnv = true, []string{"MY_TRACKER_KEY"} })
	env := r.rt.AgentEnv()
	var gcp string
	vars := map[string]string{}
	for _, kv := range env.Vars {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	gcp = vars["GIT_CONFIG_PARAMETERS"]
	assert.True(t, strings.HasPrefix(gcp, "'credential.helper'=''"), gcp)
	assert.True(t, strings.HasSuffix(gcp, gitConfigParam("core.hooksPath", r.rt.HooksDir())), gcp)
	assert.Equal(t, "0", vars["GIT_TERMINAL_PROMPT"])
	for _, k := range []string{"GH_CONFIG_DIR", "GLAB_CONFIG_DIR"} {
		info, err := os.Stat(vars[k])
		require.NoError(t, err, k)
		assert.True(t, info.IsDir())
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "LINEAR_API_KEY", "SSH_AUTH_SOCK", "MY_TRACKER_KEY"} {
		assert.Contains(t, env.Unset, name)
	}
	assert.NotContains(t, env.Unset, "ITERVOX_ACTION_TOKEN", "the run's scoped action token stays")
}

func TestEnvNamesWithValue(t *testing.T) {
	environ := []string{"A=secret-value", "B=other", "C=secret-value", "D="}
	assert.Equal(t, []string{"A", "C"}, EnvNamesWithValue(environ, "secret-value"))
	assert.Empty(t, EnvNamesWithValue(environ, ""))
}
