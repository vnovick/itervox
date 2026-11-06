package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHostGOOS makes ValidateDispatch check the workflow as if the daemon
// ran on goos, for the rest of the test.
func setHostGOOS(t *testing.T, goos string) {
	t.Helper()
	prev := hostGOOS
	hostGOOS = goos
	t.Cleanup(func() { hostGOOS = prev })
}

// TestTmuxModeRefusedOnWindows: a workflow with sessions.tmux_control: true
// is refused when the daemon loads it on Windows (ValidateDispatch is the
// startup and reload gate), with a message naming the setting and why, and
// is accepted on Linux and macOS as before.
func TestTmuxModeRefusedOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n" +
		"sessions:\n  tmux_control: true\n---\n\nPrompt.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sessions.TmuxControl {
		t.Fatal("fixture must enable sessions.tmux_control")
	}

	setHostGOOS(t, "windows")
	err = ValidateDispatch(cfg)
	if err == nil {
		t.Fatal("ValidateDispatch accepted sessions.tmux_control: true on Windows")
	}
	for _, want := range []string{"sessions.tmux_control", "not available on Windows", "tmux does not run there"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}

	for _, goos := range []string{"linux", "darwin"} {
		setHostGOOS(t, goos)
		if err := ValidateDispatch(cfg); err != nil {
			t.Fatalf("%s: ValidateDispatch = %v, want nil", goos, err)
		}
	}

	// tmux control off: Windows accepts the workflow.
	setHostGOOS(t, "windows")
	cfg.Sessions.TmuxControl = false
	if err := ValidateDispatch(cfg); err != nil {
		t.Fatalf("windows without tmux_control: ValidateDispatch = %v, want nil", err)
	}
}
