package app

import (
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// AutoSwitchRowFor renders identifier's automatic override and provenance
// (CORE-055). Source is "unknown" for an override persisted before
// provenance was recorded; the target then comes from the override maps.
func AutoSwitchRowFor(s orchestrator.State, ident string) server.AutoSwitchRow {
	row := server.AutoSwitchRow{Identifier: ident, Source: "unknown",
		ToBackend: s.IssueBackends[ident], ToProfile: s.IssueProfiles[ident]}
	if rec, ok := s.AutoSwitchInfo[ident]; ok {
		row.Source = rec.Source
		row.FromBackend, row.FromProfile, row.Reason = rec.FromBackend, rec.FromProfile, rec.Reason
		if rec.ToBackend != "" {
			row.ToBackend = rec.ToBackend
		}
		if rec.ToProfile != "" {
			row.ToProfile = rec.ToProfile
		}
	}
	at := s.AutoSwitchedAt[ident]
	if at.IsZero() {
		at = s.AutoSwitchInfo[ident].SwitchedAt
	}
	if !at.IsZero() {
		row.SwitchedAt = &at
	}
	return row
}
