package orchestrator

import (
	"context"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// IsRateLimitFailure reports whether an exhausted-retry terminal error
// looks like a vendor rate-limit / quota exhaustion. Match is
// case-insensitive substring against the built-in default pattern list.
// Use IsRateLimitFailureWithPatterns to pass a custom list (e.g. from
// `cfg.Agent.RateLimitErrorPatterns`).
func IsRateLimitFailure(errorMessage string) bool {
	return IsRateLimitFailureWithPatterns(errorMessage, nil)
}

// IsRateLimitFailureWithPatterns is the patterns-aware sibling of
// IsRateLimitFailure. Empty/nil patterns argument falls back to the
// built-in default list. Gap §5.1.
//
// CORE-030: the default list reads the message in two fields —
// agent.SplitFailureText separates the agent-reported failure from the
// CLI's own "stderr: " segment. The anchored patterns, clause rules and
// vendor phrases match either field (pre-stream auth/credit/usage failures
// print the vendor phrase only on stderr); a standalone 429 token matches
// only the agent-reported field. Operator-supplied patterns stay verbatim
// case-insensitive substrings over the whole message, i.e. both fields.
func IsRateLimitFailureWithPatterns(errorMessage string, patterns []string) bool {
	return IsRateLimitFailureWithPatternsMode(errorMessage, patterns, config.RateLimitPatternsModeReplace)
}

// GoroutinePanicCount returns the number of goroutine panics recovered so far.
func GoroutinePanicCount() int64 { return goroutinePanics.Load() }

// flushPersistence synchronously writes every pending ledger version.
func (o *Orchestrator) flushPersistence() {
	for i := range numLedgers {
		_ = o.ledger(i).flush()
	}
}

// ComputeGraphMetrics is a pure function computing GraphMetrics for g. It
// runs iterative Tarjan SCC (no recursion — candidate sets can be large),
// builds the SCC condensation DAG, then computes per-component transitive
// dependents and longest chain via a single reverse-topological pass so
// cycles cannot wedge the traversal.
//
// This is a single-consumer convenience wrapper around the shared SCC
// decomposition — it recomputes Tarjan on every call. Callers that also need
// ExtractCycles' output for the same TickGraph in the same tick should call
// ComputeTickGraphAnalysis instead, which computes the SCC pass once and
// feeds both consumers from it.
func ComputeGraphMetrics(g TickGraph) GraphMetrics {
	nodes := sortedNodes(g)
	adj := adjacency(g, nodes)
	scc := tarjanSCC(nodes, adj)
	return computeGraphMetrics(g, nodes, adj, scc)
}

// ExtractCycles is a pure function producing the sorted DependencyCycle list
// for g. DetectedAt is carried forward from prev when the exact sorted
// member set matches an entry there (so the alert timestamp is stable across
// ticks instead of re-stamping every tick); otherwise it is stamped now.
// Output is sorted by first member.
//
// This is a single-consumer convenience wrapper around the shared SCC
// decomposition — it recomputes Tarjan on every call. Callers that also need
// ComputeGraphMetrics' output for the same TickGraph in the same tick should
// call ComputeTickGraphAnalysis instead, which computes the SCC pass once
// and feeds both consumers from it.
func ExtractCycles(g TickGraph, prev []DependencyCycle, now time.Time) []DependencyCycle {
	nodes := sortedNodes(g)
	adj := adjacency(g, nodes)
	scc := tarjanSCC(nodes, adj)
	return extractCycles(g, nodes, adj, scc, prev, now)
}

func (o *Orchestrator) startAutomationRun(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	automation AutomationDispatch,
) bool {
	started, _ := o.startAutomationRunOrHold(ctx, state, issue, now, automation)
	return started
}

// dispatchMatchingRateLimitedAutomations is the rate-limited sibling of
// dispatchMatchingRunFailedAutomations. Called from event_loop.go when a
// terminal failure is classified as rate-limit-driven AND the operator has
// configured at least one rate_limited rule. The two helpers are separate
// so rate_limited recovery can take precedence when a switch is queued, while
// generic run_failed handling remains the fallback when no switch fires.
//
// Per-issue switch-cap and per-(issue, profile) cooldown are evaluated
// here so the rule never fires beyond what the operator authorised. Each
// matching rule emits an EventDispatchAutomation through the orchestrator's
// events channel; the existing event-loop handler then claims a slot and
// spawns the helper. When the rule has AutoResume + SwitchToProfile, the
// orchestrator additionally overrides state.IssueProfiles for the issue so
// the next dispatch picks up the new profile.
func (o *Orchestrator) dispatchMatchingRateLimitedAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	failedProfile string,
	failedBackend string,
	errorMessage string,
	attempt int,
	promptTokensTotal, completionTokensTotal int,
) int {
	return o.dispatchMatchingRateLimitedAutomationsNote(ctx, state, issue, now, failedProfile, failedBackend,
		errorMessage, attempt, promptTokensTotal, completionTokensTotal, nil)
}
