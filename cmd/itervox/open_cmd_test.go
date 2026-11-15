package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openProject writes a project whose .itervox/dashboard_url names d and whose
// logs dir holds an auto-generated api-token, as a running daemon leaves
// them. ITERVOX_API_TOKEN is unset, as in the operator's shell.
// TestOpenPrintsAuthenticatedURL: `itervox open --print` prints the running
// daemon's dashboard URL with the auto-generated token as ?token= (the
// dashboard's AuthGate stores it and strips it from the address bar), and
// says on stderr that the URL is a credential.
// TestOpenOpensTheBrowserLoggedIn: without --print the URL goes to the OS
// opener only; nothing printed carries the token.
// TestOpenRefusesWithoutARunningDaemon: no dashboard_url, or one that does
// not answer, is an error and opens nothing.
// TestInitNextStepsPointAtItervoxOpen: init's closing steps send the
// operator to `itervox open`, not to the token-less dashboard_url.
func TestInitNextStepsPointAtItervoxOpen(t *testing.T) {
	for _, kind := range []string{"github", "linear", "local"} {
		var b strings.Builder
		printInitNextSteps(&b, kind, ".itervox", ".itervox/.env", "WORKFLOW.md")
		assert.Contains(t, b.String(), "logged in (from another terminal): itervox open\n", kind)
		assert.Contains(t, b.String(), "contains your API token", kind)
		assert.NotContains(t, b.String(), "dashboard_url", kind)
	}
	var b strings.Builder
	printInitNextSteps(&b, "github", ".itervox", ".itervox/.env", "ops/WORKFLOW.md")
	assert.Contains(t, b.String(), "itervox open --workflow ops/WORKFLOW.md")
}

// TestDoctorSaysWhereTheAPITokenIs: doctor names the auto-generated token's
// file (or ITERVOX_API_TOKEN) and points at `itervox open`, never printing
// the token.
func TestDoctorSaysWhereTheAPITokenIs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: local\n---\nPrompt.\n"), 0o644))
	t.Setenv("ITERVOX_API_TOKEN", "")
	tokenFile := filepath.Join(defaultLogsDir(wf), apiTokenFileName)

	report, _ := runDoctorChecks(wf, nil)
	assert.Contains(t, report, "API token: auto-generated when the daemon starts, written to "+tokenFile)
	assert.Contains(t, report, "`itervox open` opens the dashboard logged in")

	require.NoError(t, os.MkdirAll(filepath.Dir(tokenFile), 0o700))
	require.NoError(t, os.WriteFile(tokenFile, []byte("secret-generated-token\n"), 0o600))
	report, _ = runDoctorChecks(wf, nil)
	assert.Contains(t, report, "API token: auto-generated, in "+tokenFile)
	assert.NotContains(t, report, "secret-generated-token")

	t.Setenv("ITERVOX_API_TOKEN", "pinned-secret")
	report, _ = runDoctorChecks(wf, nil)
	assert.Contains(t, report, "API token: ITERVOX_API_TOKEN")
	assert.NotContains(t, report, "pinned-secret")
}

// TestOpenerCommandPerOS: the OS opener for each platform.
func TestOpenerCommandPerOS(t *testing.T) {
	u := "http://127.0.0.1:8090/?token=x"
	assert.Equal(t, []string{"open", u}, browserOpenerArgs("darwin", u))
	assert.Equal(t, []string{"xdg-open", u}, browserOpenerArgs("linux", u))
	assert.Equal(t, []string{"rundll32", "url.dll,FileProtocolHandler", u}, browserOpenerArgs("windows", u))
}
