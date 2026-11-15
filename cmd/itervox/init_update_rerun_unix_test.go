//go:build unix

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
)

// These run the real main() through the daemon-child hook, which the test
// binary has on unix only (daemon_child_test.go).

// TestInitUpdateNothingToMigrateCLIExitsZero: the second `itervox init
// --update` on a migrated project exits 0 from the real CLI.
func TestInitUpdateNothingToMigrateCLIExitsZero(t *testing.T) {
	path := writeLegacyProfilesWorkflow(t)
	var out, errOut bytes.Buffer
	require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut), errOut.String())
	code, stdout, stderr := runItervoxCLI(t, filepath.Dir(path), "init", "--update", "--workflow", path)
	assert.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	assert.Contains(t, stdout, "nothing to migrate")
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
