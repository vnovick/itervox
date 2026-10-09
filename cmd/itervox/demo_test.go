package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDemoDaemonCompletesAnIssue (#76) starts the real `itervox demo` (the
// test binary re-executed into main()) with no agent CLI on PATH and no
// tracker credentials, waits until a demo issue reaches Done through the
// scripted agent and the review stand-in, and checks the dashboard is in demo
// mode and that nothing was written outside the scratch directory.
func TestDemoDaemonCompletesAnIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon")
	}
	scratch := t.TempDir()
	home := t.TempDir()
	raw, err := json.Marshal([]string{"demo", "--dir", scratch, "--no-open"})
	require.NoError(t, err)

	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "PATH", daemonChildArgsEnv, "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
			"LINEAR_API_KEY", "GITHUB_TOKEN":
			continue
		}
		env = append(env, kv)
	}
	// No claude or codex anywhere on PATH.
	env = append(env, "HOME="+home, "PATH=/usr/bin:/bin", daemonChildArgsEnv+"="+string(raw))

	logPath := filepath.Join(t.TempDir(), "demo.out")
	logf, err := os.Create(logPath)
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0])
	cmd.Env = env
	cmd.Dir = t.TempDir()
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }() // test-only reaper
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = logf.Close()
	})

	client := &http.Client{Timeout: 2 * time.Second}
	var url string
	var done string
	var demoMode bool
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && done == "" {
		time.Sleep(500 * time.Millisecond)
		if url == "" {
			b, err := os.ReadFile(filepath.Join(scratch, ".itervox", "dashboard_url"))
			if err != nil {
				continue
			}
			url = strings.TrimRight(strings.TrimSpace(string(b)), "/")
		}
		var issues []struct {
			Identifier string `json:"identifier"`
			State      string `json:"state"`
		}
		if getJSON(client, url+"/api/v1/issues", &issues) != nil {
			continue
		}
		for _, is := range issues {
			if is.State == "Done" {
				done = is.Identifier
			}
		}
		var snap struct {
			DemoMode bool `json:"demoMode"`
		}
		if getJSON(client, url+"/api/v1/state", &snap) == nil {
			demoMode = demoMode || snap.DemoMode
		}
	}
	out, _ := os.ReadFile(logPath)
	require.NotEmpty(t, done, "no demo issue reached Done; daemon output:\n%s", out)
	assert.True(t, strings.HasPrefix(done, "DEMO-"))
	assert.True(t, demoMode, "the dashboard snapshot must say demo mode")

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be written under HOME")
	for _, p := range []string{"WORKFLOW.md", "logs", "workspaces", ".itervox"} {
		_, statErr := os.Stat(filepath.Join(scratch, p))
		assert.NoError(t, statErr, "the demo writes %s inside the scratch directory", p)
	}
}

func getJSON(client *http.Client, url string, v any) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return os.ErrNotExist
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
