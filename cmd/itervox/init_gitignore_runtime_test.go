package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runtimeFilesThatMustBeIgnored are the lock and runtime files a daemon,
// `itervox init` and the CLI commands create inside a project, relative to
// the directory of WORKFLOW.md. None of them may be picked up by `git add .`.
var runtimeFilesThatMustBeIgnored = []string{
	".WORKFLOW.md.lock",                // workflow.WithEditLock (CORE-149)
	"WORKFLOW.md.bak",                  // init --update backup
	".itervox/tokens.json.lock",        // apitokens store lock (#214)
	".itervox/tokens.json",             // API token hashes
	".itervox/daemon.pid",              // pid file
	".itervox/runtime/daemon.pid.lock", // pid lock (CORE-039)
	".itervox/dashboard_url",           // dashboard address
	".itervox/HEARTBEAT.md",            // liveness file
	".itervox/run/issue-ENG-1.lock",    // itervox run's per-issue lock
	".itervox/settings.local.jsonc",    // local settings layer
	".itervox/.env",                    // secrets
}

// TestInitIgnoresRuntimeFilesWithoutARootGitignore: in a repository with no
// root .gitignore, `itervox init` (finalizeItervoxGitignore) and `itervox
// init --update` create one, so the WORKFLOW.md edit lock next to
// WORKFLOW.md is ignored like every other runtime file.
func TestInitIgnoresRuntimeFilesWithoutARootGitignore(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		require.NoError(t, finalizeItervoxGitignore(filepath.Join(dir, ".itervox")))
		for _, rel := range runtimeFilesThatMustBeIgnored {
			assert.True(t, gitPathIgnored(t, dir, rel), "%s is not ignored", rel)
		}
	})
	t.Run("init --update", func(t *testing.T) {
		path := writeLegacyProfilesWorkflow(t)
		dir := filepath.Dir(path)
		initGitRepo(t, dir)
		var out, errOut bytes.Buffer
		require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut), errOut.String())
		for _, rel := range runtimeFilesThatMustBeIgnored {
			assert.True(t, gitPathIgnored(t, dir, rel), "%s is not ignored", rel)
		}
	})
	t.Run("not a git repository: no .gitignore is created", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, patchRootGitignoreForAgents(dir))
		_, err := os.Stat(filepath.Join(dir, ".gitignore"))
		assert.True(t, os.IsNotExist(err), "a .gitignore only matters to git")
	})
}
