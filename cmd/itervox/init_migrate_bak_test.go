package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMigrateWorkflowToSchema2RefusesToOverwriteStaleBackupWithoutForce —
// todolist4 A.3 named test. Stale .bak files must NOT be silently destroyed.
func TestMigrateWorkflowToSchema2RefusesToOverwriteStaleBackupWithoutForce(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\nagent:\n  profiles:\n    p:\n      command: claude\n      prompt: legacy\n---\n# prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bakPath := wf + ".bak"
	if err := os.WriteFile(bakPath, []byte("stale backup; precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := migrateWorkflowToSchema2(wf, false, time.Now())
	if err == nil {
		t.Fatal("expected refusal without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error message should explain stale .bak; got %q", err.Error())
	}

	// The original stale .bak content must be preserved (NOT overwritten).
	data, _ := os.ReadFile(bakPath)
	if string(data) != "stale backup; precious" {
		t.Errorf("stale .bak content was overwritten; got %q", string(data))
	}
}
