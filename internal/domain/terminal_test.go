package domain

import "testing"

// TestIsTerminalState_TrimsAndRejectsEmpty (CORE-111) pins the shared
// contract: trimmed on both sides, case-insensitive, empty never terminal.
func TestIsTerminalState_TrimsAndRejectsEmpty(t *testing.T) {
	terminal := []string{"Done", " Cancelled ", ""}
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"Done", true},
		{"done", true},
		{"  DONE\t", true},
		{"Cancelled", true},
		{"In Progress", false},
		{"", false},
		{"   ", false},
	} {
		if got := IsTerminalState(tc.state, terminal); got != tc.want {
			t.Errorf("IsTerminalState(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
	if IsTerminalState("Done", nil) {
		t.Error("no terminal states: nothing is terminal")
	}
}
