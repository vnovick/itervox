package orchestrator

import (
	"sort"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// DependencyAttentionEntry is one operator-facing dependency alert: either a
// cycle member (Kind "cycle") or an issue that has been blocked longer than
// the configured escalation window (Kind "stale_blocker"). Derived,
// event-loop-owned state — recomputed every tick from the tick graph, never
// persisted, no events. critical-path-ordering Task 4.
type DependencyAttentionEntry struct {
	Identifier   string
	Blockers     []string
	BlockedSince time.Time
	Kind         string // "cycle" | "stale_blocker"
}

// DeriveDependencyAttention is a pure function producing the sorted
// operator-attention surface for one tick.
//
// Cycle members (from cycles, itself the output of ExtractCycles over g)
// always escalate with Kind "cycle" — a cycle can never resolve without
// operator action, so the escalation window does not apply, and cycle
// membership wins when an identifier is both a cycle member and has a
// stale blocker (Blockers becomes the other cycle members, BlockedSince the
// cycle's DetectedAt).
//
// Remaining candidates are graph nodes (g.Nodes) with at least one
// unresolved blocker from either source:
//   - tracker: the audit row for id (looked up via a one-shot index over
//     state.DependencyAudit keyed by DependencyAuditEntry.Identifier, since
//     the map itself is keyed by dependencyAuditKey — issue.ID in
//     production, never the identifier).UnresolvedBlockers (non-empty) —
//     these count even when the blocker identifier is outside the
//     candidate set, because an out-of-set tracker blocker still holds
//     dispatch (design doc § Escalation);
//   - inferred: state.InferredDeps[id] entries with Gating == true.
//
// Blockers is the sorted, deduplicated union of unresolved tracker blocker
// identifiers and gating inferred Sources. BlockedSince is the earlier of
// the audit row's FirstBlockedAt (when non-zero and a tracker blocker
// exists) and the earliest gating edge's InferredAt (when an inferred
// blocker exists). When neither source supplies a non-zero timestamp (e.g.
// an Unknown-status audit row from a transient tracker outage, which never
// stamps FirstBlockedAt), there is no evidence of blocked duration and no
// stale_blocker entry is emitted for that issue at all — the next audit
// pass that stamps FirstBlockedAt starts the clock. Otherwise a
// stale_blocker entry is emitted only when the escalation window is
// positive (cfg.Dependencies.EscalateBlockedAfterHours hours; <= 0 means
// escalation is disabled — no stale_blocker entries at all, though cycle
// entries are unaffected) and now.Sub(BlockedSince) > window — exactly at
// the window boundary is NOT escalated.
//
// Output is sorted by Identifier for determinism.
func DeriveDependencyAttention(g TickGraph, cycles []DependencyCycle, state State, cfg *config.Config, now time.Time) []DependencyAttentionEntry {
	windowHours := 0
	if cfg != nil {
		windowHours = cfg.Dependencies.EscalateBlockedAfterHours
	}
	window := time.Duration(windowHours) * time.Hour

	byIdentifier := make(map[string]DependencyAttentionEntry)

	// state.DependencyAudit is keyed by dependencyAuditKey(issue), which
	// prefers issue.ID (dependency_audit.go) — for real trackers that is a
	// UUID (Linear) or a bare numeric string (GitHub), never the identifier
	// that TickGraph nodes (and this loop) key on. A direct
	// state.DependencyAudit[id] lookup below would therefore always miss in
	// production. Build a one-shot index of audit rows by their .Identifier
	// field instead — this does NOT change how the audit map itself is
	// keyed; other consumers of state.DependencyAudit are unaffected. Rows
	// with an empty .Identifier field (only ever hand-built test fixtures —
	// auditIssueDependencies always stamps it from issue.Identifier) index
	// under their own map key instead, so identifier-keyed fixtures built
	// directly against state.DependencyAudit keep resolving correctly.
	auditByIdentifier := make(map[string]*DependencyAuditEntry, len(state.DependencyAudit))
	for key, row := range state.DependencyAudit {
		if row == nil {
			continue
		}
		identifier := row.Identifier
		if identifier == "" {
			identifier = key
		}
		auditByIdentifier[identifier] = row
	}

	for _, cyc := range cycles {
		for _, member := range cyc.Members {
			others := make([]string, 0, len(cyc.Members)-1)
			for _, m := range cyc.Members {
				if m != member {
					others = append(others, m)
				}
			}
			byIdentifier[member] = DependencyAttentionEntry{
				Identifier:   member,
				Blockers:     others,
				BlockedSince: cyc.DetectedAt,
				Kind:         "cycle",
			}
		}
	}

	if windowHours > 0 {
		for id := range g.Nodes {
			if _, isCycle := byIdentifier[id]; isCycle {
				continue
			}

			blockerSet := make(map[string]struct{})
			var trackerSince, inferredSince time.Time
			var haveTracker, haveInferred bool

			if row, ok := auditByIdentifier[id]; ok && row != nil {
				for _, b := range row.UnresolvedBlockers {
					if b.Identifier == nil || *b.Identifier == "" {
						continue
					}
					blockerSet[*b.Identifier] = struct{}{}
					haveTracker = true
				}
				if haveTracker && !row.FirstBlockedAt.IsZero() {
					trackerSince = row.FirstBlockedAt
				}
			}

			for _, e := range state.InferredDeps[id] {
				if !e.Gating {
					continue
				}
				if e.Source != "" {
					blockerSet[e.Source] = struct{}{}
				}
				if !haveInferred || e.InferredAt.Before(inferredSince) {
					inferredSince = e.InferredAt
				}
				haveInferred = true
			}

			if len(blockerSet) == 0 {
				continue
			}

			var blockedSince time.Time
			switch {
			case !trackerSince.IsZero() && !inferredSince.IsZero():
				if trackerSince.Before(inferredSince) {
					blockedSince = trackerSince
				} else {
					blockedSince = inferredSince
				}
			case !trackerSince.IsZero():
				blockedSince = trackerSince
			case !inferredSince.IsZero():
				blockedSince = inferredSince
			}

			if blockedSince.IsZero() {
				// No evidence of duration: an Unknown-status audit row
				// (blocker State nil, e.g. a transient tracker outage)
				// leaves FirstBlockedAt unstamped, and there is no gating
				// inferred edge to supply an InferredAt either. Escalating
				// here would compare now against the zero time and always
				// exceed the window, producing a bogus stale_blocker entry
				// with BlockedSince 0001-01-01. Skip; the next audit pass
				// that stamps FirstBlockedAt starts the clock.
				continue
			}

			if now.Sub(blockedSince) <= window {
				continue
			}

			blockers := make([]string, 0, len(blockerSet))
			for b := range blockerSet {
				blockers = append(blockers, b)
			}
			sort.Strings(blockers)

			byIdentifier[id] = DependencyAttentionEntry{
				Identifier:   id,
				Blockers:     blockers,
				BlockedSince: blockedSince,
				Kind:         "stale_blocker",
			}
		}
	}

	out := make([]DependencyAttentionEntry, 0, len(byIdentifier))
	for _, e := range byIdentifier {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identifier < out[j].Identifier })
	return out
}
