package profiles

import (
	"slices"
	"strings"
	"testing"
)

func TestLookupMergeBotReturnsEmbeddedContent(t *testing.T) {
	got := Lookup("merge-bot")
	if got == nil {
		t.Fatal("Lookup(merge-bot) returned nil; expected built-in profile")
	}
	if got.Name != "merge-bot" {
		t.Errorf("Name = %q, want %q", got.Name, "merge-bot")
	}
	if !strings.Contains(got.Soul, "merge-bot") {
		t.Errorf("Soul missing expected marker; got %q", got.Soul)
	}
	if !strings.Contains(got.Instructions, "gh pr list") {
		t.Errorf("Instructions missing expected marker; got %q", got.Instructions)
	}
	if got.DefaultCommand == "" {
		t.Error("DefaultCommand should be set for merge-bot")
	}
	if got.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", got.DefaultBackend, "claude")
	}
	wantActions := []string{"comment", "comment_pr", "merge_pr", "move_state"}
	if len(got.DefaultActions) != len(wantActions) {
		t.Errorf("DefaultActions len = %d, want %d", len(got.DefaultActions), len(wantActions))
	}
}

func TestLookupUnknownProfileReturnsNil(t *testing.T) {
	if Lookup("does-not-exist") != nil {
		t.Error("Lookup of unknown profile should return nil")
	}
	if Lookup("") != nil {
		t.Error("Lookup of empty name should return nil")
	}
}

func TestNamesIncludesMergeBot(t *testing.T) {
	names := Names()
	if !slices.Contains(names, "merge-bot") {
		t.Errorf("Names() = %v; expected to contain merge-bot", names)
	}
}

func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("merge-bot") {
		t.Error("IsBuiltin(merge-bot) should be true")
	}
	if IsBuiltin("does-not-exist") {
		t.Error("IsBuiltin(does-not-exist) should be false")
	}
}
