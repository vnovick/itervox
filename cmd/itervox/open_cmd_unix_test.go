//go:build unix

package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openProject(t *testing.T, d *fakeDaemon, token string) (wf, logsDir string) {
	t.Helper()
	dir := t.TempDir()
	wf = filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(dashboardURLFilePath(wf), []byte(d.srv.URL+"/\n"), 0o644))
	logsDir = filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(logsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, apiTokenFileName), []byte(token+"\n"), 0o600))
	t.Setenv("ITERVOX_API_TOKEN", "")
	return wf, logsDir
}

func healthyDaemon(t *testing.T) *fakeDaemon {
	return newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			writeJSONBody(w, map[string]any{"status": "ok"})
			return
		}
		http.NotFound(w, r)
	})
}

func TestOpenPrintsAuthenticatedURL(t *testing.T) {
	d := healthyDaemon(t)
	wf, logsDir := openProject(t, d, "gen+tok/en")
	var out, errOut bytes.Buffer
	code := openCommand([]string{"--workflow", wf, "--logs-dir", logsDir, "--print"}, &out, &errOut, func(string) error {
		t.Fatal("--print must not open a browser")
		return nil
	})
	require.Equal(t, 0, code, errOut.String())
	assert.Equal(t, d.srv.URL+"/?token=gen%2Btok%2Fen\n", out.String())
	assert.Contains(t, errOut.String(), "contains a credential")
	assert.NotContains(t, errOut.String(), "gen+tok/en", "the token goes to stdout only")
}

func TestOpenOpensTheBrowserLoggedIn(t *testing.T) {
	d := healthyDaemon(t)
	wf, logsDir := openProject(t, d, "generated-token")
	var opened []string
	var out, errOut bytes.Buffer
	code := openCommand([]string{"--workflow", wf, "--logs-dir", logsDir}, &out, &errOut, func(u string) error {
		opened = append(opened, u)
		return nil
	})
	require.Equal(t, 0, code, errOut.String())
	assert.Equal(t, []string{d.srv.URL + "/?token=generated-token"}, opened)
	assert.NotContains(t, out.String()+errOut.String(), "generated-token")
	assert.Contains(t, out.String(), d.srv.URL)

	t.Run("a pinned ITERVOX_API_TOKEN wins over the file", func(t *testing.T) {
		t.Setenv("ITERVOX_API_TOKEN", "pinned")
		opened = nil
		require.Equal(t, 0, openCommand([]string{"--workflow", wf, "--logs-dir", logsDir}, &out, &errOut, func(u string) error {
			opened = append(opened, u)
			return nil
		}))
		assert.Equal(t, []string{d.srv.URL + "/?token=pinned"}, opened)
	})
}

func TestOpenRefusesWithoutARunningDaemon(t *testing.T) {
	never := func(string) error { t.Fatal("must not open"); return nil }
	var out, errOut bytes.Buffer
	wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
	assert.Equal(t, 1, openCommand([]string{"--workflow", wf, "--print"}, &out, &errOut, never))
	assert.Contains(t, errOut.String(), "no running daemon")

	d := healthyDaemon(t)
	wf, logsDir := openProject(t, d, "tok")
	d.srv.Close()
	errOut.Reset()
	assert.Equal(t, 1, openCommand([]string{"--workflow", wf, "--logs-dir", logsDir, "--print"}, &out, &errOut, never))
	assert.Contains(t, errOut.String(), "not answering")
	assert.False(t, strings.Contains(errOut.String(), "tok\n"))
}
