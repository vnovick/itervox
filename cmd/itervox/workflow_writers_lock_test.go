package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/workflow"
)

// G3 (CORE-006): the two cmd/itervox WORKFLOW.md writers — the available-
// models refresh (dashboard "refresh models" + `itervox models refresh`) and
// the project-filter persist (dashboard + TUI) — must serialize with the
// locked internal/workflow patchers. An atomic rename alone does not: a
// writer that reads the file while a locked patcher is between its read and
// its write overwrites that patcher's edit with stale bytes.
//
// The overlap is forced, not hoped for. The locked partner is a real
// workflow.ApplyAndWriteFrontMatter call whose first mutator signals "I
// hold the lock and have read the file" and then blocks. Only then does the
// writer under test run. A writer that bypasses the lock completes while
// the partner is parked and the partner then clobbers it; a writer that
// takes the lock waits, reads the partner's result, and both edits survive.
// One write per key, a fresh file per trial.
func TestCmdWorkflowWritersSerializeWithLockedPatchers(t *testing.T) {
	const fixture = "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n" +
		"  # project_slug:  # Optional\n  active_states: [\"Todo\"]\n  terminal_states: [\"Done\"]\n" +
		"agent:\n  command: claude\n  max_concurrent_agents: 1\n---\n\nBody.\n"

	rows := []struct {
		name  string
		write func(path string) error
		want  string
	}{
		{"mergeAvailableModelsIntoWorkflow", func(p string) error {
			_, err := mergeAvailableModelsIntoWorkflow(p, map[string][]agent.ModelOption{
				"claude": {{ID: "model-g3", Label: "Model G3"}},
			})
			return err
		}, "model-g3"},
		{"updateWorkflowProjectSlug", func(p string) error {
			return updateWorkflowProjectSlug(p, []string{"slug-g3"})
		}, "project_slug: slug-g3"},
	}
	const trials = 3
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			for i := range trials {
				f := filepath.Join(dir, fmt.Sprintf("WORKFLOW-%d.md", i))
				if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
					t.Fatal(err)
				}

				holding := make(chan struct{})
				release := make(chan struct{})
				partnerDone := make(chan error, 1)
				park := func(front []string) ([]string, error) {
					close(holding)
					<-release
					return front, nil
				}
				go func() { // locked partner: read, park, then write
					partnerDone <- workflow.ApplyAndWriteFrontMatter(f, park,
						workflow.MutateIntField("max_concurrent_agents", 7))
				}()
				<-holding

				writerDone := make(chan error, 1)
				go func() { writerDone <- row.write(f) }()
				// A lock-respecting writer cannot finish while the partner
				// holds the lock; give a bypassing one ample time to.
				var writerErr error
				writerFinishedEarly := false
				select {
				case writerErr = <-writerDone:
					writerFinishedEarly = true
				case <-time.After(300 * time.Millisecond):
				}
				close(release)
				if err := <-partnerDone; err != nil {
					t.Fatalf("trial %d: locked partner: %v", i, err)
				}
				if !writerFinishedEarly {
					writerErr = <-writerDone
				}
				if writerErr != nil {
					t.Fatalf("trial %d: %s: %v", i, row.name, writerErr)
				}

				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				got := string(data)
				for _, want := range []string{row.want, "max_concurrent_agents: 7", "Body."} {
					if !strings.Contains(got, want) {
						t.Fatalf("trial %d: %q lost — %s did not serialize with the locked patcher (finished while it held the lock: %v)\n%s",
							i, want, row.name, writerFinishedEarly, got)
					}
				}
				if writerFinishedEarly {
					t.Fatalf("trial %d: %s completed while a locked patcher held the WORKFLOW.md lock", i, row.name)
				}
			}
		})
	}
}
