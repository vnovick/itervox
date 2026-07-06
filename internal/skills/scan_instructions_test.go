package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanInstructions_RootLevel(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "# project claude")
	writeFile(t, filepath.Join(proj, "AGENTS.md"), "# project agents")

	docs, err := scanInstructions(proj, "")
	if err != nil {
		t.Fatalf("scanInstructions: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	var foundClaude, foundAgents bool
	for _, d := range docs {
		switch d.Name {
		case "CLAUDE.md":
			if d.Provider != "claude" || d.Scope != "project" {
				t.Errorf("unexpected CLAUDE.md fields: %+v", d)
			}
			foundClaude = true
		case "AGENTS.md":
			if d.Provider != "codex" || d.Scope != "project" {
				t.Errorf("unexpected AGENTS.md fields: %+v", d)
			}
			foundAgents = true
		}
	}
	if !foundClaude || !foundAgents {
		t.Errorf("missing expected docs: claude=%v agents=%v", foundClaude, foundAgents)
	}
}

func TestScanInstructions_NestedClaudeMd(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "root")
	writeFile(t, filepath.Join(proj, "internal", "CLAUDE.md"), "internal")
	writeFile(t, filepath.Join(proj, "internal", "agent", "CLAUDE.md"), "agent")

	docs, err := scanInstructions(proj, "")
	if err != nil {
		t.Fatalf("scanInstructions: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 nested CLAUDE.md docs, got %d", len(docs))
	}
}

func TestScanInstructions_NodeModulesSkipped(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "root")
	writeFile(t, filepath.Join(proj, "node_modules", "some-pkg", "CLAUDE.md"), "should be skipped")

	docs, err := scanInstructions(proj, "")
	if err != nil {
		t.Fatalf("scanInstructions: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected only root CLAUDE.md, got %d: %+v", len(docs), docs)
	}
}

func TestScanInstructions_UserClaudeMd(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	writeFile(t, filepath.Join(user, ".claude", "CLAUDE.md"), "user-level")

	docs, err := scanInstructions("", user)
	if err != nil {
		t.Fatalf("scanInstructions: %v", err)
	}
	if len(docs) != 1 || docs[0].Scope != "user" {
		t.Fatalf("expected 1 user-scope doc, got %+v", docs)
	}
}

func TestScanInstructions_SymlinkDeduped(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	real := filepath.Join(proj, "CLAUDE.md")
	writeFile(t, real, "content")

	// Create a directory with a symlink back to the same target.
	subDir := filepath.Join(proj, "alias")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(subDir, "CLAUDE.md")
	if err := os.Symlink(real, link); err != nil {
		// Some sandboxed file systems disallow symlinks — skip rather than fail.
		t.Skipf("symlink not supported here: %v", err)
	}

	docs, err := scanInstructions(proj, "")
	if err != nil {
		t.Fatalf("scanInstructions: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected dedup to leave 1 doc, got %d: %+v", len(docs), docs)
	}
}

func TestScanInstructions_MissingDirsReturnsNilNil(t *testing.T) {
	t.Parallel()
	docs, err := scanInstructions("", "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if docs != nil {
		t.Errorf("expected nil docs, got %v", docs)
	}
}
