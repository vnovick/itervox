package skills

import (
	"path/filepath"
	"sort"
	"testing"
)

const flatHooks = `{
  "hooks": {
    "PreToolUse": [
      { "command": "echo pre", "matcher": "Bash" }
    ],
    "Stop": [
      { "command": "echo stop" }
    ]
  }
}`

const nestedHooks = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Edit",
        "hooks": [
          { "type": "command", "command": "echo a" },
          { "type": "command", "command": "echo b" }
        ]
      }
    ]
  }
}`

const emptyHooksObj = `{ "hooks": {} }`

func TestScanHooks_FlatForm(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), flatHooks)

	hooks, err := scanHooks(proj, "")
	if err != nil {
		t.Fatalf("scanHooks: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Event < hooks[j].Event })
	if hooks[0].Event != "PreToolUse" || hooks[0].Matcher != "Bash" || hooks[0].Command != "echo pre" {
		t.Errorf("unexpected first hook: %+v", hooks[0])
	}
	if hooks[1].Event != "Stop" || hooks[1].Command != "echo stop" {
		t.Errorf("unexpected second hook: %+v", hooks[1])
	}
	for _, h := range hooks {
		if h.Provider != "claude" || h.Source != "project-settings" {
			t.Errorf("unexpected provider/source: %+v", h)
		}
	}
}

func TestScanHooks_NestedForm(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), nestedHooks)

	hooks, err := scanHooks(proj, "")
	if err != nil {
		t.Fatalf("scanHooks: %v", err)
	}
	// Two child commands under one nested entry.
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks (one per child command), got %d", len(hooks))
	}
	for _, h := range hooks {
		if h.Matcher != "Bash|Edit" {
			t.Errorf("matcher should propagate from parent, got %q", h.Matcher)
		}
		if h.Event != "PreToolUse" {
			t.Errorf("event should be PreToolUse, got %q", h.Event)
		}
	}
}

func TestScanHooks_EmptyHooksObject(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), emptyHooksObj)

	hooks, err := scanHooks(proj, "")
	if err != nil {
		t.Fatalf("scanHooks: %v", err)
	}
	if hooks != nil {
		t.Errorf("expected nil hooks for empty hooks object, got %v", hooks)
	}
}

func TestScanHooks_MissingFileReturnsNilNil(t *testing.T) {
	t.Parallel()
	hooks, err := scanHooks(t.TempDir(), "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if hooks != nil {
		t.Errorf("expected nil hooks, got %v", hooks)
	}
}

func TestScanHooks_OrderPreservedWithinEvent(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), nestedHooks)

	hooks, err := scanHooks(proj, "")
	if err != nil {
		t.Fatalf("scanHooks: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}
	if hooks[0].Command != "echo a" || hooks[1].Command != "echo b" {
		t.Errorf("hook order not preserved: got %+v", hooks)
	}
}
