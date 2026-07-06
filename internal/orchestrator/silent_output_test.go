package orchestrator

import (
	"strings"
	"testing"
)

// V2-3 [SILENT] no-op convention. The suppression site in runWorker only
// blanks sessionComment before the PR/tracker CreateComment guards; it touches
// no log-buffer code, so the per-issue audit record is preserved by
// construction (the buffer is fed by the streaming path before this point).
func TestSilentSessionOutput(t *testing.T) {
	cases := []struct {
		name string
		all  []string
		want bool
	}{
		{
			name: "final block begins with SILENT suppresses",
			all:  []string{"working on it", "[SILENT] nothing to report"},
			want: true,
		},
		{
			name: "leading whitespace before SILENT still suppresses",
			all:  []string{"  \n\t[SILENT] scan clean"},
			want: true,
		},
		{
			name: "bare SILENT marker suppresses",
			all:  []string{"[SILENT]"},
			want: true,
		},
		{
			name: "trailing empty blocks are skipped",
			all:  []string{"real output", "[SILENT] done", "   ", ""},
			want: true,
		},
		{
			name: "mid-message SILENT does not suppress",
			all:  []string{"all done, marking [SILENT] for next time"},
			want: false,
		},
		{
			name: "SILENT only in an earlier block does not suppress",
			all:  []string{"[SILENT] first turn", "final turn has findings"},
			want: false,
		},
		{
			name: "match is case-sensitive",
			all:  []string{"[silent] lowercase is not the convention"},
			want: false,
		},
		{
			name: "no text blocks",
			all:  nil,
			want: false,
		},
		{
			name: "only empty blocks",
			all:  []string{"", "  "},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := silentSessionOutput(tc.all); got != tc.want {
				t.Fatalf("silentSessionOutput(%q) = %v, want %v", tc.all, got, tc.want)
			}
		})
	}
}

// sessionCommentForRun is the actual function runWorker calls (the single
// delivery seam for PR + tracker comments) — not a mirror of its logic.
func TestSessionCommentForRun_SilentFinalOutputSuppresses(t *testing.T) {
	got := sessionCommentForRun([]string{"scanned 14 issues", "[SILENT] no stale issues found"}, "ENG-1")
	if got != "" {
		t.Fatalf("silent final output must yield an empty session comment, got %q", got)
	}
}

func TestSessionCommentForRun_NormalOutputFormatsSummary(t *testing.T) {
	got := sessionCommentForRun([]string{"implemented the fix", "opened PR #7"}, "ENG-2")
	if got == "" {
		t.Fatal("non-silent run must yield a session comment")
	}
	for _, want := range []string{"ENG-2", "implemented the fix", "opened PR #7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session comment missing %q:\n%s", want, got)
		}
	}
}

func TestSessionCommentForRun_NoOutputYieldsEmpty(t *testing.T) {
	if got := sessionCommentForRun(nil, "ENG-3"); got != "" {
		t.Fatalf("empty run must yield no comment, got %q", got)
	}
}
