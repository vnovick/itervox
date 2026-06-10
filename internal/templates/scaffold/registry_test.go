package scaffold

import (
	"slices"
	"testing"
)

func TestIsKnown_ShippedTemplates(t *testing.T) {
	for _, name := range []string{"minimal", "full", "rate-limit-fallback", "pr-review", "daily-qa"} {
		if !IsKnown(name) {
			t.Errorf("expected %q to be a known template", name)
		}
	}
}

func TestIsKnown_RejectsUnknown(t *testing.T) {
	if IsKnown("yolo") {
		t.Error("yolo must not be a known template")
	}
	if IsKnown("") {
		t.Error("empty string must not be a known template")
	}
}

func TestNames_ReturnsFreshCopy(t *testing.T) {
	a := Names()
	if !slices.Contains(a, "minimal") {
		t.Error("Names() must include the default 'minimal'")
	}
	a[0] = "tampered"
	b := Names()
	if b[0] == "tampered" {
		t.Error("Names() must return a fresh copy; mutating the result must not bleed")
	}
}
