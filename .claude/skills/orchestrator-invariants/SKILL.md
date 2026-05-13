---
name: orchestrator-invariants
description: Use when editing any file in internal/orchestrator/, when adding or modifying fields on structs in internal/config/config.go, or when discussing concurrency, state mutations, locking, goroutines, or race conditions in the itervox Go codebase. Enforces the single-goroutine state machine rule and the exact cfgMu guard list.
---

# Orchestrator Invariants

Itervox's orchestrator is a single-goroutine state machine. Violating these rules causes data races that the `-race` detector will (eventually) catch. Enforce them on every edit.

## 1. Single-goroutine state machine

- ALL mutations of `orchestrator.State` happen in ONE goroutine: the `select` loop inside `Run()` in `internal/orchestrator/event_loop.go`.
- Worker goroutines (one per issue, in `internal/orchestrator/worker.go`) MUST NOT touch `o.state` directly. They communicate results back via `o.events chan OrchestratorEvent`.
- Red flag: any assignment like `o.state.Running[id] = ...`, `o.state.Foo = ...`, or `state.X = ...` inside a function that runs on a worker goroutine. That is a race, period. Convert it to an `OrchestratorEvent` sent on `o.events`.
- `State` is a value type. No mutex guards its fields because only the event loop writes them. Do not add one.

## 2. `cfgMu` guards EXACTLY these fields — nothing else

These `Orchestrator.cfg` fields can be mutated at runtime by HTTP handler goroutines and MUST be accessed under `cfgMu`:

- `cfg.Agent.MaxConcurrentAgents`
- `cfg.Agent.MaxRetries`
- `cfg.Agent.MaxSwitchesPerIssuePerWindow`
- `cfg.Agent.SwitchWindowHours`
- `cfg.Agent.SwitchRevertHours`
- `cfg.Agent.RateLimitErrorPatterns`
- `cfg.Agent.Profiles`
- `cfg.Agent.SSHHosts`
- `cfg.Agent.SSHHostDescriptions`
- `cfg.Agent.DispatchStrategy`
- `cfg.Agent.ReviewerProfile`
- `cfg.Agent.AutoReview`
- `cfg.Agent.InlineInput`
- `cfg.Tracker.ActiveStates`
- `cfg.Tracker.TerminalStates`
- `cfg.Tracker.CompletionState`
- `cfg.Tracker.FailedState`
- `cfg.Workspace.AutoClearWorkspace`
- `cfg.Automations`

The canonical allowlist is `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields`. All OTHER `cfg.*` fields are read-only after startup. Do NOT add locks defensively to read-only fields. If a static-analysis pass flags one, verify there is a runtime setter (HTTP handler) before believing the claim — no setter means no race.

## 3. Adding a new runtime-mutable `cfg.*` field

If you make a `cfg.*` field mutable at runtime, you MUST do all three:

1. Add it to the cfgMu guard list in `CLAUDE.md`.
2. Ensure the HTTP handler that mutates it acquires `o.cfgMu.Lock()`.
3. Ensure every read of it acquires `o.cfgMu.RLock()`.

If you cannot do all three, leave the field read-only and configure it via `WORKFLOW.md` at startup instead.

## 4. Snapshot lock ordering

- `Snapshot()` acquires `snapMu.RLock()` and returns a copy of `lastSnap`.
- Lock order is `cfgMu` -> `snapMu`. Never the reverse.
- HTTP handlers MUST NOT hold `cfgMu` while calling `Snapshot()`. Release `cfgMu` first, then call `Snapshot()`.

## 5. Test doubles touched by worker goroutines

Test doubles whose methods run on worker goroutines (e.g., fake `RunTurn` implementations) must synchronize any field the test body also reads. Use `sync/atomic` (`atomic.Int64`, `atomic.Bool`) or a mutex.

Reference: `internal/orchestrator/retry_test.go`'s `alwaysFailRunner` uses `atomic.Int64` for `callCount`. Follow that pattern. If a field is touched by both `RunTurn` and the test goroutine, it MUST be synchronized.

## 6. Always verify with `-race`

- Run `go test -race ./internal/orchestrator/...` after any orchestrator change.
- For new concurrency code, stress with `go test -race -count=5 ./internal/orchestrator/...`. The race detector is non-deterministic; a single passing run is not proof.
- `make verify` is the full gate before pushing.

## Verification checklist for orchestrator edits

Before finalizing any change in `internal/orchestrator/`:

- [ ] No worker-goroutine code path writes to `o.state` or any `State` field.
- [ ] Any new `cfg.*` write site is in the cfgMu guard list AND wrapped in `o.cfgMu.Lock()`.
- [ ] Any new `cfg.*` read site for a guarded field is wrapped in `o.cfgMu.RLock()`.
- [ ] No code path holds `cfgMu` across a `Snapshot()` call.
- [ ] New test doubles synchronize fields shared with worker goroutines.
- [ ] `go test -race ./internal/orchestrator/...` passes.
