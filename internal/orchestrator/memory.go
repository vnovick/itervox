package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/rules"
)

// Managed memory (#202). At dispatch startRun reads the memory files again
// (like the rules), selects the entries for the run's profile and issue
// within the budget, records their ids on the RunEntry (and so on the
// CompletedRun) and folds the block into the prompt digest. The worker puts
// the `## Memory` block after the `## Rules` block. Once the block has gone
// out in a turn's prompt the worker sends EventMemoryUsed; the event loop
// checks the run is still current and hands the lastUsed/useCount update to
// a tracked goroutine, which rewrites the files (atomicfs) off the loop.
// No State field is involved.

// EventMemoryUsed is sent by a worker once its run's memory block reached
// the agent (#202).
const EventMemoryUsed EventType = "MemoryUsed"

// MemoryUse is the payload of EventMemoryUsed.
type MemoryUse struct {
	Identifier string
	IDs        []string
	At         time.Time
}

// resolveMemoryFor selects the memory a run of profile on issue gets under
// cfg, within the memory budget left after the run's rules (set). Empty
// when memory is off. Shared by startRun and ComposeEvalPrompt, so an eval
// gets the memory a dispatched run would. cfg.Memory and cfg.WorkflowPath
// are read-only after startup.
func resolveMemoryFor(cfg *config.Config, profile string, issue domain.Issue, set rules.Set) (memory.Selection, []memory.FileError) {
	if cfg == nil || !cfg.Memory.Enabled {
		return memory.Selection{}, nil
	}
	desc := ""
	if issue.Description != nil {
		desc = *issue.Description
	}
	budget := cfg.Memory.MemoryBudget(memory.ApproxTokens(set.Block()))
	return memory.Resolve(cfg.WorkflowPath, memory.Target{
		Profile: profile, Title: issue.Title, Description: desc, Labels: issue.Labels,
	}, budget)
}

// resolveRunMemory is resolveMemoryFor for a dispatch; unreadable files are
// logged (the run still starts, without their entries).
func (o *Orchestrator) resolveRunMemory(entry *RunEntry, set rules.Set) memory.Selection {
	if o == nil || entry == nil {
		return memory.Selection{}
	}
	sel, errs := resolveMemoryFor(o.cfg, entry.ProfileName, entry.Issue, set)
	for _, e := range errs {
		slog.Warn("orchestrator: memory file ignored", "issue_identifier", entry.Issue.Identifier,
			"scope", e.Scope, "path", e.Path, "error", e.Message)
	}
	if sel.OverBudget {
		slog.Warn("orchestrator: pinned memory exceeds the memory budget",
			"issue_identifier", entry.Issue.Identifier, "tokens", sel.Tokens)
	}
	return sel
}

// runMemory returns the memory a worker's run was dispatched with, or, for a
// worker started without a run (tests that call runWorker directly), the
// selection made now.
func (o *Orchestrator) runMemory(ctx context.Context, profile string, issue domain.Issue, set rules.Set) memory.Selection {
	if h := runFrom(ctx); h != nil {
		return h.memory
	}
	sel, _ := resolveMemoryFor(o.cfg, profile, issue, set)
	return sel
}

// reportMemoryUsed tells the event loop that the run's memory block reached
// the agent. Worker only; blocks until the loop takes it or the run ends.
func (o *Orchestrator) reportMemoryUsed(ctx context.Context, identifier string, ids []string) {
	if len(ids) == 0 {
		return
	}
	select {
	case o.events <- OrchestratorEvent{
		Type:      EventMemoryUsed,
		RunID:     runIDFrom(ctx),
		MemoryUse: &MemoryUse{Identifier: identifier, IDs: slices.Clone(ids), At: time.Now()},
	}:
	case <-ctx.Done():
	}
}

// applyMemoryUsed handles EventMemoryUsed. Event loop only. A report from a
// run that is no longer running (reconcile stopped it) still counts: its
// prompt carried the block. The file update runs on a goroutine joined by
// Run (memoryWg), so a slow disk never stalls the loop.
func (o *Orchestrator) applyMemoryUsed(ev OrchestratorEvent) {
	u := ev.MemoryUse
	if u == nil || len(u.IDs) == 0 || o.cfg == nil {
		return
	}
	store := memory.Store{WorkflowPath: o.cfg.WorkflowPath}
	goSafe(&o.memoryWg, "memory-use", u.Identifier, func() {
		if err := store.RecordUse(u.IDs, u.At); err != nil {
			slog.Warn("orchestrator: memory usage not recorded", "issue_identifier", u.Identifier, "error", err)
			o.RecordFailure(FailureRecord{
				Kind: FailureKindPersist, Source: "memory", Identifier: u.Identifier,
				Message: fmt.Sprintf("memory usage not recorded: %v", err),
			})
		}
	}, nil)
}

// memoryPromptBlock is the `## Memory` block of a run, "" when it has none.
func memoryPromptBlock(sel memory.Selection) string { return sel.Block() }
