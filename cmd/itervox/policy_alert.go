package main

import (
	"slices"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// Policy changes while agents run (approval integrity). The orchestrator
// raises a change to a file that governs agents seen while runs are active
// (internal/orchestrator/policy_alert.go: Warn log, policy_changed feed
// event); this file connects it to the daemon around it:
//
//   - the config watcher (watchConfigFiles) reports every settled change of
//     WORKFLOW.md or a settings file to the running generation's
//     orchestrator before it asks for a reload (NotePolicyFilesChanged);
//   - the orchestrator's policy change hook publishes the latest change for
//     the dashboard's banner (StateSnapshot.PolicyChanged). It is process
//     state, like the config-invalid banner, so it outlives the reload the
//     change causes.
//
// `itervox watch` prints the policy_changed feed events.

// policyAlertPtr is the latest policy change seen while runs were active.
var policyAlertPtr atomic.Pointer[server.PolicyChangedStatus]

// liveOrch is the running generation's orchestrator, for the config
// watcher's reports; nil between generations.
var liveOrch atomic.Pointer[orchestrator.Orchestrator]

// publishPolicyAlert is the orchestrator's policy change hook: it stores the
// alert (called from the event loop; never blocks).
func publishPolicyAlert(a orchestrator.PolicyChangeAlert) {
	policyAlertPtr.Store(&server.PolicyChangedStatus{
		At: a.At, Source: a.Source, Files: slices.Clone(a.Files), RunIDs: slices.Clone(a.RunIDs),
		PossiblyAgent: a.PossiblyAgent,
	})
}

// loadPolicyAlert is the latest policy change for the snapshot, nil when
// none was seen.
func loadPolicyAlert() *server.PolicyChangedStatus { return policyAlertPtr.Load() }

// watchPolicyChanges installs the hook on orch (before Run) and makes it the
// orchestrator the config watcher reports to; the returned func forgets it
// when the generation ends.
func watchPolicyChanges(orch *orchestrator.Orchestrator) func() {
	orch.SetPolicyChangeHook(publishPolicyAlert)
	liveOrch.Store(orch)
	return func() { liveOrch.CompareAndSwap(orch, nil) }
}

// notePolicyFilesChanged tells the running generation that the config
// watcher saw these files change; it raises them when runs are active.
func notePolicyFilesChanged(changed []string) {
	if o := liveOrch.Load(); o != nil {
		o.NotePolicyFilesChanged(changed)
	}
}
