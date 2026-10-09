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
			"LINEAR_API_KEY", "GITHUB_TOKEN", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
			"XDG_CACHE_HOME", "XDG_DATA_HOME", "GH_CONFIG_DIR":
			continue
		}
		env = append(env, kv)
	}
	// No claude or codex anywhere on PATH, but a gh that, like the real
	// one, creates its state directory under $XDG_STATE_HOME (default
	// ~/.local/state) on every call. CI runners ship gh, so a demo that let
	// it write there failed the HOME check below.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(
		"#!/bin/sh\nmkdir -p \"${XDG_STATE_HOME:-$HOME/.local/state}/gh\"\nexit 1\n"), 0o755))
	env = append(env, "HOME="+home, "PATH="+bin+":/usr/bin:/bin", daemonChildArgsEnv+"="+string(raw))

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

// TestPrepareDemoNeverOverwritesAWorkflow (review of #76): --dir pointed at
// a real project is refused without touching its WORKFLOW.md; a previous
// demo's workflow is reused unchanged; an empty directory gets a new one.
func TestPrepareDemoNeverOverwritesAWorkflow(t *testing.T) {
	for _, k := range []string{"ITERVOX_SERVER_PORT", "ITERVOX_SERVER_HOST", "PORT", "ITERVOX_API_TOKEN", "ITERVOX_DRY_RUN"} {
		t.Setenv(k, "") // restored after prepareDemo unsets them
	}
	t.Cleanup(func() { demoSession = nil })

	project := t.TempDir()
	mine := []byte("---\nitervox_schema_version: 2\n---\nMINE\n")
	require.NoError(t, os.WriteFile(filepath.Join(project, "WORKFLOW.md"), mine, 0o644))
	var out strings.Builder
	_, ok, err := prepareDemo([]string{"--dir", project, "--no-open"}, &out)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "is not a demo workflow")
	after, _ := os.ReadFile(filepath.Join(project, "WORKFLOW.md"))
	assert.Equal(t, string(mine), string(after), "the project's workflow is untouched")

	empty := t.TempDir()
	args, ok, err := prepareDemo([]string{"--dir", empty, "--no-open"}, &out)
	require.NoError(t, err)
	require.True(t, ok)
	wf := filepath.Join(empty, "WORKFLOW.md")
	assert.Equal(t, []string{"itervox", "-workflow", wf, "-logs-dir", filepath.Join(empty, "logs")}, args)
	first, err := os.ReadFile(wf)
	require.NoError(t, err)
	assert.Contains(t, string(first), demoWorkflowMarker)
	info1, _ := os.Stat(wf)

	time.Sleep(20 * time.Millisecond)
	_, ok, err = prepareDemo([]string{"--dir", empty, "--no-open"}, &out)
	require.NoError(t, err)
	require.True(t, ok)
	info2, _ := os.Stat(wf)
	assert.Equal(t, info1.ModTime(), info2.ModTime(), "a demo workflow is reused, not rewritten")
}
