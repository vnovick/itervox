package orchestrator

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"

	"github.com/vnovick/itervox/internal/rules"
)

// resolveRunRules reads the rules layers (#157) and returns the set in scope
// for a run of profile on backend. It reads the files on every call, so an
// edit applies to the next run without a restart. cfg.WorkflowPath and
// cfg.Tracker.ProjectSlug are read-only after startup (not in the cfgMu
// list), so no lock is taken.
func (o *Orchestrator) resolveRunRules(profile, backend string) rules.Set {
	if o == nil || o.cfg == nil {
		return rules.Set{}
	}
	wf := o.cfg.WorkflowPath
	return rules.Resolve(wf, rules.Target{
		Projects: rules.ProjectNames(wf, o.cfg.Tracker.ProjectSlug),
		Profile:  profile,
		Backend:  cmp.Or(backend, "claude"), // "" runs claude (see runWorker)
	})
}

// runRules returns the rule set a worker's run was dispatched with, or, for a
// worker started without a run (tests that call runWorker directly), the set
// resolved now.
func (o *Orchestrator) runRules(ctx context.Context, profile, backend string) rules.Set {
	if h := runFrom(ctx); h != nil {
		return h.rules
	}
	return o.resolveRunRules(profile, backend)
}

// reportRuleProblems writes the rule set's conflicts and unreadable layers to
// the daemon log and the issue's log, once per run, so an operator sees why a
// rule they wrote is not in the prompt.
func (o *Orchestrator) reportRuleProblems(set rules.Set, identifier, runLogID string) {
	for _, c := range set.Conflicts {
		slog.Warn("worker: rules conflict", "issue_identifier", identifier, "rule", c.ID, "layer", c.Layer, "kept_layer", c.KeptLayer)
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLineWithSession("WARN", "rules: "+c.Message, runLogID))
		}
	}
	for _, e := range set.Errors {
		slog.Warn("worker: rules layer ignored", "issue_identifier", identifier, "layer", e.Layer, "path", e.Path, "error", e.Message)
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLineWithSession("WARN", fmt.Sprintf("rules: the %s layer was ignored: %s", e.Layer, e.Message), runLogID))
		}
	}
}

// rulesPromptBlock is the automatic `## Rules` block for a run: empty when the
// set is empty or when any of the run's templates prints `{{ rules }}` (or a
// field of it) and so places the rules itself.
func rulesPromptBlock(set rules.Set, templates ...string) string {
	if rules.TemplatePlacesRules(templates...) {
		return ""
	}
	return set.Block()
}
