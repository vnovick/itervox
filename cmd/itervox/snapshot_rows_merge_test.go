package main

import (
	"testing"

	"github.com/vnovick/itervox/internal/server"
)

// TestMergeDependencyGraphNodePreservesRunningFlagAcrossEmptyPrevID —
// todolist4 P3-1 named test. Guards the audit P3-1 fix at snapshot_rows.go:322
// — when prev.ID was empty the short-circuit `return next` discarded any
// Running flag previously OR'd into prev. The fix is the per-field merge;
// this test pins it.
func TestMergeDependencyGraphNodePreservesRunningFlagAcrossEmptyPrevID(t *testing.T) {
	// prev has Running=true but an empty ID (legitimate seeding case where
	// edge-walk set the flag before a node arrived with its identifier).
	prev := server.DependencyGraphNodeRow{Running: true}
	next := server.DependencyGraphNodeRow{ID: "issue-1", Identifier: "ENG-1", Title: "T"}

	got := mergeDependencyGraphNode(prev, next)
	if !got.Running {
		t.Error("Running flag must survive merge across empty prev ID")
	}
	if got.ID != "issue-1" {
		t.Errorf("merged ID = %q; want issue-1", got.ID)
	}
	if got.Identifier != "ENG-1" {
		t.Errorf("merged Identifier = %q; want ENG-1", got.Identifier)
	}
}
