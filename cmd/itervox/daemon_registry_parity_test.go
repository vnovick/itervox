//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registryParityFixture is testdata/daemon_registry_parity.json, which the
// Mac app's in-process registry reader (desktop/src-tauri/src/registry.rs,
// #212) loads too: both must list exactly `expected`.
type registryParityFixture struct {
	Projects []struct {
		Dir        string  `json:"dir"`
		PIDFile    *string `json:"pid_file"`
		HoldLock   bool    `json:"hold_lock"`
		RuntimeDir bool    `json:"runtime_dir"`
	} `json:"projects"`
	Registry  map[string]json.RawMessage `json:"registry"`
	Remaining []string                   `json:"remaining"`
	Expected  []daemonRecord             `json:"expected"`
}

// loadRegistryParityFixture reads the fixture with its placeholders filled.
func loadRegistryParityFixture(t *testing.T, root string, live, dead int) registryParityFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "daemon_registry_parity.json"))
	require.NoError(t, err)
	text := strings.NewReplacer(
		"__ROOT__", root,
		"__LIVE_PID__", strconv.Itoa(live),
		"__DEAD_PID__", strconv.Itoa(dead),
	).Replace(string(raw))
	var fx registryParityFixture
	require.NoError(t, json.Unmarshal([]byte(text), &fx))
	return fx
}

// reapedPID is the pid of a child that has exited and been waited for.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

// TestDaemonRegistryParityFixture lists the shared fixture's registry the
// way `itervox status --all --json` does (no process scan) and checks the
// rows and the pruned records the Mac app's reader must match.
func TestDaemonRegistryParityFixture(t *testing.T) {
	root := t.TempDir()
	fx := loadRegistryParityFixture(t, root, os.Getpid(), reapedPID(t))

	for _, p := range fx.Projects {
		itervoxDir := filepath.Join(root, p.Dir, ".itervox")
		require.NoError(t, os.MkdirAll(itervoxDir, 0o700))
		if p.PIDFile != nil {
			require.NoError(t, os.WriteFile(filepath.Join(itervoxDir, "daemon.pid"), []byte(*p.PIDFile), 0o600))
		}
		if p.RuntimeDir || p.HoldLock {
			require.NoError(t, os.MkdirAll(filepath.Join(itervoxDir, "runtime"), 0o700))
		}
		if p.HoldLock {
			release, acquired, err := tryLockPIDFile(filepath.Join(itervoxDir, "runtime", "daemon.pid.lock"))
			require.NoError(t, err)
			require.True(t, acquired)
			t.Cleanup(release)
		}
	}
	regDir := filepath.Join(root, "daemons")
	require.NoError(t, os.MkdirAll(regDir, 0o700))
	for name, body := range fx.Registry {
		content := []byte(body)
		var s string
		if json.Unmarshal(body, &s) == nil {
			content = []byte(s) // a string value is the file's raw content
		}
		require.NoError(t, os.WriteFile(filepath.Join(regDir, name), content, 0o600))
	}

	got := collectDaemons(regDir, func() []int { return nil }, func(int) string { return "" })
	assert.Equal(t, fx.Expected, got)

	entries, err := os.ReadDir(regDir)
	require.NoError(t, err)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	assert.Equal(t, fx.Remaining, left, "stale and unreadable records are pruned")
}
