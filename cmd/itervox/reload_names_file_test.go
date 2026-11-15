//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// TestChangedConfigFilesLabel: the reload log names each changed file
// relative to the project when it is inside it.
func TestChangedConfigFilesLabel(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	project := config.ProjectSettingsPath(wf)
	user := filepath.Join(t.TempDir(), ".itervox", "settings.jsonc")
	for _, tc := range []struct {
		changed []string
		want    string
	}{
		{nil, "WORKFLOW.md"},
		{[]string{wf}, "WORKFLOW.md"},
		{[]string{project}, ".itervox/settings.jsonc"},
		{[]string{user}, user},
		{[]string{wf, project}, "WORKFLOW.md and .itervox/settings.jsonc"},
		{[]string{wf, project, user}, "WORKFLOW.md, .itervox/settings.jsonc and " + user},
	} {
		if got := changedConfigFilesLabel(wf, tc.changed); got != tc.want {
			t.Errorf("changedConfigFilesLabel(%q) = %q, want %q", tc.changed, got, tc.want)
		}
	}
	if got := mergeChangedFiles([]string{wf}, []string{project, wf}); len(got) != 2 {
		t.Errorf("mergeChangedFiles = %q, want each file once", got)
	}
}

// TestSettingsEditReloadNamesTheSettingsFile: a hand edit of
// .itervox/settings.jsonc reloads through the same drain as a WORKFLOW.md
// edit, and the drain's log line names the file that changed instead of
// claiming WORKFLOW.md did.
func TestSettingsEditReloadNamesTheSettingsFile(t *testing.T) {
	p := newDrainProject(t, 1)
	_, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "30s"})
	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })

	settings := config.ProjectSettingsPath(p.Workflow)
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{\n  // hand edit\n  \"agent\": {\"max_retries\": 7},\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	want := "reload: .itervox/settings.jsonc changed — draining"
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(readTrim(t, stderrPath), want) {
		if time.Now().After(deadline) {
			var drains []string
			for _, line := range strings.Split(readTrim(t, stderrPath), "\n") {
				if strings.Contains(line, "draining") {
					drains = append(drains, line)
				}
			}
			t.Fatalf("no %q line after 20s; drain lines:\n%s", want, strings.Join(drains, "\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := readTrim(t, stderrPath); strings.Contains(got, "reload: WORKFLOW.md changed") {
		t.Errorf("WORKFLOW.md did not change, but the log says it did:\n%s", got)
	}
	waitFor(t, 30*time.Second, "the reload applies", func() bool {
		return strings.Contains(readTrim(t, stderrPath), "reload: drain complete, applying .itervox/settings.jsonc")
	})
}
