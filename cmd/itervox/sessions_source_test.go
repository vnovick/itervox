package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tmuxctl"
)

// fakeTmuxOnPath puts a `tmux` script first on PATH that logs each call's
// command (after any -S socket) to the returned file and answers -V; list-panes prints
// nothing.
func fakeTmuxOnPath(t *testing.T) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = -S ] && shift 2\n" +
		"echo \"$1\" >> '" + logPath + "'\n" +
		"[ \"$1\" = -V ] && echo 'tmux 3.4'\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func tmuxCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Fields(string(data))
}

// TestTmuxVersionProbedOnce (#210): `tmux -V` runs once for the daemon's
// lifetime once tmux is found — not on every 15 s discovery pass, status
// read or write — and list-panes is skipped while no tmux server socket
// exists.
func TestTmuxVersionProbedOnce(t *testing.T) {
	logPath := fakeTmuxOnPath(t)
	sock := filepath.Join(t.TempDir(), "s")
	d := &tmuxDetector{client: tmuxctl.Client{Socket: sock}}
	src := sessionSource{detect: d}

	for range 5 {
		scan, err := src.Discover(time.Now())
		require.NoError(t, err)
		assert.True(t, scan.TmuxListed, "no server is an empty listing")
		assert.Empty(t, scan.TmuxPanes)
	}
	assert.True(t, d.current().Available)
	assert.Equal(t, "tmux 3.4", d.refresh().Version)
	d.mu.Lock()
	d.at = time.Now().Add(-2 * tmuxStatusTTL) // a stale answer is still the lifetime answer
	d.mu.Unlock()
	assert.True(t, d.current().Available)
	assert.Equal(t, []string{"-V"}, tmuxCalls(t, logPath), "one version probe, no list-panes without a server socket")

	require.NoError(t, os.WriteFile(sock, nil, 0o600)) // a server appears
	_, err := src.Discover(time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"-V", "list-panes"}, tmuxCalls(t, logPath), "with a server socket the panes are listed")
}

// TestTmuxDetectedAfterInstall: a "not installed" answer is not cached for
// the lifetime — tmux installed later is found without a restart.
func TestTmuxDetectedAfterInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux
	d := &tmuxDetector{}
	assert.False(t, d.refresh().Available)
	logPath := fakeTmuxOnPath(t)
	assert.True(t, d.refresh().Available, "installed tmux is found on the next detection")
	assert.Equal(t, []string{"-V"}, tmuxCalls(t, logPath))
}
