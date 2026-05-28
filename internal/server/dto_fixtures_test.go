package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOptionalTimeFieldsOmitWhenAbsent locks the v0.2.0 audit P1-5 contract:
// every `*time.Time,omitempty` field must be dropped from the JSON output
// when its pointer is nil, not emitted as "0001-01-01T00:00:00Z". A future
// regression that swaps the pointer back to a value type would re-introduce
// the year-0001 leak and fail here.
//
// Required time fields (QueuedAt / FiredAt — no omitempty) intentionally
// fall outside this contract: a missing required value emitting year-0001
// is a separate concern, surfaced by validation at the consumer.
func TestOptionalTimeFieldsOmitWhenAbsent(t *testing.T) {
	cases := map[string][]string{
		"AutomationQueueRow":             {"lastFiredAt", "lastAttemptAt"},
		"AutomationQueueBackpressureRow": {"lastRejectedAt"},
		"DependencyAuditRow":             {"firstBlockedAt", "unblockedAt", "lastAuditedAt"},
	}
	values := map[string]any{
		"AutomationQueueRow":             AutomationQueueRow{},
		"AutomationQueueBackpressureRow": AutomationQueueBackpressureRow{},
		"DependencyAuditRow":             DependencyAuditRow{},
	}
	for name, optionalKeys := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(values[name])
			require.NoError(t, err)
			serialized := string(data)
			for _, key := range optionalKeys {
				assert.False(t, strings.Contains(serialized, `"`+key+`":`),
					"%s should omit %q on zero value (got %s)", name, key, serialized)
			}
		})
	}
}

// TestGenerateDTOFixturesForZodParity emits a canonical JSON fixture per
// snapshot DTO under web/src/types/fixtures/. The Vitest suite reads these
// fixtures and asserts each one parses against its Zod counterpart. The
// pairing closes the v0.2.0 audit P1-14 gap: a Go-side field change without
// the matching Zod update will surface here on the next `make verify`
// instead of silently breaking the dashboard SSE parse boundary at runtime.
//
// Fixtures are regenerated every run; a stale fixture on disk that no longer
// matches the current DTO shape would let drift slip through unnoticed.
func TestGenerateDTOFixturesForZodParity(t *testing.T) {
	dir := fixturesDir(t)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	at := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	tptr := func(t time.Time) *time.Time { return &t }

	fixtures := map[string]any{
		"AutomationQueueRow.json": AutomationQueueRow{
			ID:                "queue-id-1",
			AutomationID:      "auto-impl",
			TriggerType:       "cron",
			Identifier:        "ENG-1",
			Title:             "Sample automation queued entry",
			IssueState:        "Todo",
			Profile:           "implementer",
			Backend:           "claude",
			Status:            "queued",
			Reason:            "queue_full",
			ReasonDetail:      "100/100 slots used",
			QueuedAt:          at,
			FiredAt:           at.Add(5 * time.Minute),
			LastFiredAt:       tptr(at.Add(4 * time.Minute)),
			LastAttemptAt:     tptr(at.Add(3 * time.Minute)),
			AttemptCount:      2,
			Cron:              "0 9 * * 1-5",
			Timezone:          "UTC",
			PRURL:             "https://github.com/example/repo/pull/42",
			InputContext:      "needs clarification on scope",
			ErrorMessage:      "tracker timeout",
			SwitchedToProfile: "implementer-fallback",
			SwitchedToBackend: "codex",
			MoveToState:       "Todo",
		},
		"AutomationQueueBackpressureRow.json": AutomationQueueBackpressureRow{
			Length:             5,
			MaxLength:          100,
			Saturated:          false,
			PausedProducers:    false,
			RejectedSinceBoot:  3,
			LastRejectedAt:     tptr(at),
			LastRejectedReason: "queue_full",
		},
		"DependencyAuditRow.json": DependencyAuditRow{
			Identifier:            "ENG-9",
			IssueState:            "Backlog",
			Status:                "blocked",
			Sources:               []string{"tracker_relation", "issue_text"},
			BlockedBy:             []BlockerRefRow{{ID: "blocker-1", Identifier: "ENG-5", State: "In Progress", URL: "https://linear.app/x/issue/ENG-5"}},
			UnresolvedBlockers:    []BlockerRefRow{{ID: "blocker-1", Identifier: "ENG-5", State: "In Progress"}},
			ResolvedBlockers:      []BlockerRefRow{{ID: "blocker-2", Identifier: "ENG-3", State: "Done"}},
			WasBlocked:            true,
			FirstBlockedAt:        tptr(at.Add(-2 * time.Hour)),
			UnblockedAt:           tptr(at.Add(-1 * time.Hour)),
			LastAuditedAt:         tptr(at),
			LastTransitionVersion: 7,
			LastTransitionReason:  "blockers_resolved",
		},
		"DependencyGraphNodeRow.json": DependencyGraphNodeRow{
			ID:         "node-1",
			Identifier: "ENG-1",
			Title:      "Dependent task",
			State:      "Todo",
			Status:     "blocked",
			Running:    false,
			Queued:     true,
			Terminal:   false,
			UpdatedAt:  at.Format(time.RFC3339),
			URL:        "https://linear.app/x/issue/ENG-1",
		},
		"DependencyGraphEdgeRow.json": DependencyGraphEdgeRow{
			ID:               "edge-1",
			SourceIdentifier: "ENG-5",
			TargetIdentifier: "ENG-1",
			SourceState:      "In Progress",
			TargetState:      "Todo",
			Resolved:         false,
			SourceKnown:      true,
		},
		"IssueStatusChangeRow.json": IssueStatusChangeRow{
			FromState:    "Todo",
			ToState:      "In Progress",
			Source:       "automation",
			AutomationID: "auto-impl",
			TriggerType:  "blockers_resolved",
			ProfileName:  "implementer",
			Backend:      "claude",
			WorkerHost:   "local",
			At:           at,
		},
	}

	for name, value := range fixtures {
		data, err := json.MarshalIndent(value, "", "  ")
		require.NoErrorf(t, err, "marshal %s", name)
		// Trailing newline keeps the file POSIX-friendly and diff-stable.
		data = append(data, '\n')
		path := filepath.Join(dir, name)
		require.NoErrorf(t, os.WriteFile(path, data, 0o644), "write %s", path)
	}
}

// fixturesDir resolves the canonical fixture directory relative to the
// repository root by walking up from this test file. Hard-coding "../../web"
// would break if the package is ever moved; runtime.Caller anchors to the
// actual file location.
func fixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// internal/server/dto_fixtures_test.go → repo root → web/src/types/fixtures
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(repoRoot, "web", "src", "types", "fixtures")
}
