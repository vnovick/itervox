package main

// adapter_outbox.go — RetryOutboxEntry/DropOutboxEntry satisfy
// server.OrchestratorClient for the Outbox panel's per-entry Retry/Discard
// controls (write-ahead-outbox design, "Surfaces" / Task 4). Both call the
// Outbox handle directly rather than routing through the orchestrator's
// event loop (contrast with SetDepsOverride in adapter_settings.go, which
// must go through orch.SetDepsOverride because DepsOverrides is
// orchestrator.State — see CLAUDE.md's single-goroutine invariant). The
// Outbox has its own mutex and is safe to call from an HTTP handler
// goroutine directly.

// RetryOutboxEntry satisfies server.OrchestratorClient. Returns false when
// no entry with that id exists so the handler can answer 404.
func (a *orchestratorAdapter) RetryOutboxEntry(id string) bool {
	return a.ob.RetryNow(id)
}

// DropOutboxEntry satisfies server.OrchestratorClient. Outbox.Drop is
// documented as idempotent on an unknown id (Task 1) — this never reports
// failure, matching the interface's no-return-value contract.
func (a *orchestratorAdapter) DropOutboxEntry(id string) {
	a.ob.Drop(id, "operator_discard")
}
