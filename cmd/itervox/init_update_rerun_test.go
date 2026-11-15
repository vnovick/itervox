package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// legacyProfilesWorkflow is a pre-schema-2 workflow whose profiles carry only
// a prompt (no command): the daemon before schema 2 dropped such a profile
// (parseAgentProfiles skipped an entry without a command), so its issues ran
// on agent.command. A codex-backend profile without a command ran nothing
// sensible on a claude agent.command; it gets the codex CLI.
const legacyProfilesWorkflow = `---
tracker:
  kind: local
  active_states: ["Todo"]
  terminal_states: ["Done"]
agent:
  command: claude --model sonnet
  max_concurrent_agents: 2
  profiles:
    implementer:
      prompt: |
        You are the implementer. Write code.
    codexer:
      backend: codex
      prompt: You are the codex profile.
    wrapped:
      command: ./bin/my-agent
      prompt: You are wrapped.
server:
  port: 0
---
Work on {{ issue.identifier }}.
`

func writeLegacyProfilesWorkflow(t *testing.T) string {
	t.Helper()
	t.Setenv(config.UserSettingsEnv, "off")
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(legacyProfilesWorkflow), 0o644))
	return path
}

// TestInitUpdateGivesLegacyProfilesACommand: a migrated profile with no
// command gets agent.command (or its backend's CLI), the migration says so,
// and the migrated workflow passes the daemon's own load + dispatch
// validation.
func TestInitUpdateGivesLegacyProfilesACommand(t *testing.T) {
	path := writeLegacyProfilesWorkflow(t)
	var out, errOut bytes.Buffer
	require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut), errOut.String())

	cfg, err := config.Load(path)
	require.NoError(t, err, "the migrated workflow must load:\n%s", readString(t, path))
	require.NoError(t, config.ValidateDispatch(cfg), "the migrated workflow must pass validation")
	assert.Equal(t, "claude --model sonnet", cfg.Agent.Profiles["implementer"].Command, "agent.command, as the old daemon ran it")
	assert.Equal(t, "codex", cfg.Agent.Profiles["codexer"].Command, "a codex profile gets the codex CLI")
	assert.Equal(t, "./bin/my-agent", cfg.Agent.Profiles["wrapped"].Command, "an explicit command is kept")

	printed := out.String() + errOut.String()
	assert.Contains(t, printed, `agent.profiles.implementer had no command; set it to "claude --model sonnet"`)
	assert.Contains(t, printed, `agent.profiles.codexer had no command; set it to "codex"`)
	assert.NotContains(t, printed, "agent.profiles.wrapped had no command")
}

// TestInitUpdateNothingToMigrateExitsZero: a second `init --update` on an
// already-migrated project has nothing to do. It says so and succeeds, and
// neither reads nor rewrites WORKFLOW.md.bak — whether the backup is still
// there or was removed as the first run advised.
func TestInitUpdateNothingToMigrateExitsZero(t *testing.T) {
	path := writeLegacyProfilesWorkflow(t)
	var out, errOut bytes.Buffer
	require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut), errOut.String())
	bak := path + ".bak"
	bakBefore, err := os.ReadFile(bak)
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(bak, past, past))
	wfBefore := readString(t, path)

	t.Run("backup still there", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut)
		require.NoError(t, err, "nothing to migrate is not an error; out:\n%s", out.String())
		assert.Contains(t, out.String(), "nothing to migrate")
		assert.Equal(t, wfBefore, readString(t, path), "WORKFLOW.md is not rewritten")
		bakAfter, err := os.ReadFile(bak)
		require.NoError(t, err)
		assert.Equal(t, string(bakBefore), string(bakAfter), "the backup is not touched")
		st, err := os.Stat(bak)
		require.NoError(t, err)
		assert.True(t, st.ModTime().Equal(past), "the backup is not rewritten (mtime %v, want %v)", st.ModTime(), past)
	})
	t.Run("backup removed", func(t *testing.T) {
		require.NoError(t, os.Remove(bak))
		var out, errOut bytes.Buffer
		require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut))
		assert.Contains(t, out.String(), "nothing to migrate")
		assert.NoFileExists(t, bak, "no backup is written when nothing changes")
	})

	t.Run("the CLI exits 0", func(t *testing.T) {
		code, stdout, stderr := runItervoxCLI(t, filepath.Dir(path), "init", "--update", "--workflow", path)
		assert.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
		assert.Contains(t, stdout, "nothing to migrate")
	})
}

// TestInitUpdateErrorHasOnePrefix: a refusal is printed with the command
// prefix once ("itervox init --update: itervox init --update: ..." before).
func TestInitUpdateErrorHasOnePrefix(t *testing.T) {
	path := writeLegacyProfilesWorkflow(t)
	require.NoError(t, os.WriteFile(path+".bak", []byte("an older backup\n"), 0o644))
	code, _, stderr := runItervoxCLI(t, filepath.Dir(path), "init", "--update", "--workflow", path)
	assert.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, "WORKFLOW.md.bak already exists")
	assert.Equal(t, 1, strings.Count(stderr, "itervox init --update:"), "stderr:\n%s", stderr)
}

// runItervoxCLI runs the real main() (the test binary re-executed through
// the daemon-child hook) with args in dir and returns its exit code and
// output.
func runItervoxCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), daemonChildArgsEnv+"="+string(raw), "HOME="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return code, stdout.String(), stderr.String()
}
