package orchestrator

import (
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
)

// InferredDepEntry is one LLM-inferred dependency edge reconciled against the
// current candidate-issue set and the dependencies gating policy. Every edge
// from the sidecar produces an entry (even when it does not gate dispatch) so
// the dashboard can display the edge alongside the reason it is not gating.
type InferredDepEntry struct {
	Source     string
	Evidence   string
	Confidence float64
	InferredAt time.Time
	// Stale is true when the edge is older than the configured staleness
	// window (now.Sub(InferredAt) > window). Exactly-at-boundary is NOT stale.
	Stale bool
	// BelowThreshold is true when Confidence < the configured threshold.
	// Confidence equal to the threshold gates (it is not below).
	BelowThreshold bool
	// SourceKnown is true when Source matches the Identifier of one of the
	// candidate issues passed to ReconcileInferredDeps.
	SourceKnown bool
	// SourceTerminal is true when the matching candidate's tracker state is
	// terminal per isTerminalState. Only meaningful when SourceKnown is true.
	SourceTerminal bool
	// Overridden is true when an operator override exists for this edge's
	// target identifier (overrides map, keyed by target — State.DepsOverrides,
	// set via SetDepsOverride / EventSetDepsOverride).
	Overridden bool
	// Gating is true when this edge actually blocks dispatch of its target:
	// InferredGating is enabled and none of Stale, BelowThreshold, Overridden,
	// SourceTerminal hold, and SourceKnown is true.
	Gating bool
}

// resolveDependenciesConfig clamps a hand-constructed
// config.DependenciesConfig to sane bounds, mirroring the clamping that
// config.Load already performs at load time. This lets
// ReconcileInferredDeps behave correctly even when called with a *config.Config
// built directly in tests rather than loaded from WORKFLOW.md.
func resolveDependenciesConfig(cfg *config.Config) config.DependenciesConfig {
	var deps config.DependenciesConfig
	if cfg != nil {
		deps = cfg.Dependencies
	}
	if deps.ConfidenceThreshold < 0 || deps.ConfidenceThreshold > 1 {
		deps.ConfidenceThreshold = config.DefaultDependenciesConfidenceThreshold
	}
	if deps.StalenessHours <= 0 {
		deps.StalenessHours = config.DefaultDependenciesStalenessHours
	}
	return deps
}

// ReconcileInferredDeps is a pure function that reconciles the sidecar's
// inferred edges against the current candidate-issue set, the dependencies
// gating policy, and any operator overrides, producing one InferredDepEntry
// per edge (keyed by target identifier). It performs no I/O and mutates
// nothing — the event loop is the only caller and assigns the result onto
// State.InferredDeps.
//
// Edges whose Target is empty are dropped. Per-target slice order preserves
// the input edge order. overrides may be nil (e.g. in tests that don't care
// about operator overrides); a nil map behaves like an empty one and every
// entry's Overridden is false.
func ReconcileInferredDeps(edges []depsanalysis.InferredEdge, candidates []domain.Issue,
	overrides map[string]time.Time, cfg *config.Config, state State, now time.Time,
) map[string][]InferredDepEntry {
	result := make(map[string][]InferredDepEntry)
	if len(edges) == 0 {
		return result
	}

	deps := resolveDependenciesConfig(cfg)
	stalenessWindow := time.Duration(deps.StalenessHours) * time.Hour

	candidateStates := make(map[string]string, len(candidates))
	for _, c := range candidates {
		if c.Identifier != "" {
			candidateStates[c.Identifier] = c.State
		}
	}

	for _, edge := range edges {
		if edge.Target == "" {
			continue
		}

		entry := InferredDepEntry{
			Source:     edge.Source,
			Evidence:   edge.Evidence,
			Confidence: edge.Confidence,
			InferredAt: edge.InferredAt,
		}

		entry.BelowThreshold = edge.Confidence < deps.ConfidenceThreshold
		entry.Stale = now.Sub(edge.InferredAt) > stalenessWindow

		if candState, known := candidateStates[edge.Source]; known {
			entry.SourceKnown = true
			entry.SourceTerminal = isTerminalState(candState, state)
		}

		if overrides != nil {
			if _, ok := overrides[edge.Target]; ok {
				entry.Overridden = true
			}
		}

		entry.Gating = inferredGatingFor(entry, deps.InferredGating)

		result[edge.Target] = append(result[edge.Target], entry)
	}

	return result
}

// inferredGatingFor computes InferredDepEntry.Gating from the entry's other
// flags plus the current dependencies.InferredGating config toggle. Shared by
// ReconcileInferredDeps (full per-tick recompute) and the EventSetDepsOverride
// handler (same-tick recompute for a single target after an operator toggles
// State.DepsOverrides) so the two paths can never drift on what "gating"
// means. unified-dependency-graph Task 6.
func inferredGatingFor(entry InferredDepEntry, inferredGatingEnabled bool) bool {
	return inferredGatingEnabled &&
		!entry.BelowThreshold &&
		!entry.Stale &&
		!entry.Overridden &&
		entry.SourceKnown &&
		!entry.SourceTerminal
}
