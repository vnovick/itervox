# Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                          itervox                             │
│                                                              │
│  ┌──────────┐   poll    ┌────────────────┐                   │
│  │ Tracker  │◄─────────│  Orchestrator  │                    │
│  │ (Linear/ │           │   event loop   │                   │
│  │  GitHub/ │           │  (1 goroutine  │                   │
│  │  memory) │           │   owns state)  │                   │
│  └──────────┘           └───────┬────────┘                   │
│                                 │ dispatch                   │
│                          ┌──────▼──────────┐                 │
│                          │     Workers     │ (N goroutines)  │
│                          │  agent.Runner   │                 │
│                          │  claude / codex │                 │
│                          │  / fake (tests) │                 │
│                          └─────────────────┘                 │
│                                                              │
│  ┌──────────┐  HTTP/SSE  ┌────────────────┐                  │
│  │  Web UI  │◄──────────│   HTTP server  │                   │
│  │ (React)  │           │   /api/v1/...  │                   │
│  └──────────┘           └────────────────┘                   │
│                                                              │
│  ┌──────────┐                                                │
│  │  TUI     │  bubbletea, reads Orchestrator.Snapshot()      │
│  └──────────┘                                                │
└──────────────────────────────────────────────────────────────┘
```

## Key design principles

### Single-goroutine event loop

`Orchestrator.Run()` owns all dispatch state (running, paused, retrying,
input-required) in a single goroutine. Every mutation flows through a buffered
`o.events` channel of `OrchestratorEvent` values. This eliminates locks on the
core state machine and makes state transitions easy to reason about.

Worker goroutines (one per running issue) send `EventWorkerExited` back through
the channel when done. HTTP handler goroutines send events such as
`EventResumeIssue` and `EventTerminatePaused`. Workers must never mutate
`State` directly.

### `cfgMu` field guards

A small subset of `config.Config` fields can be mutated at runtime by the web
dashboard. `Orchestrator.cfgMu` (`sync.RWMutex`) guards exactly these:

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

All other `cfg` fields are read-only after startup. The single source of truth
is the cfgMu list in the project root `CLAUDE.md`.

### Snapshot access

`Snapshot()` acquires `snapMu.RLock` and returns a copy of `lastSnap`. HTTP
handlers and the TUI consume the snapshot; they never read raw `State`. The
snapshot is rebuilt on the event loop after each state transition, then
broadcast to SSE subscribers via `OnStateChange`.

### Workspace isolation

Each issue gets a workspace under `~/.itervox/workspaces/<identifier>/` (or
`os.TempDir()/itervox_workspaces/...` if `$HOME` is unset). `workspace.Manager`
provisions directories; `workspace.Safety` enforces that agents cannot escape
to parent paths. `workspace.Worktree` and `workspace.Bare` support
git-worktree-based isolation, and `workspace.PR` / `workspace.Hooks` cover PR
branch and lifecycle hook execution.

### Per-run log isolation

Every daemon invocation generates a unique `AppSessionID` (16-byte
`crypto/rand` hex token), stamped on every `CompletedRun` and exposed as
`StateSnapshot.CurrentAppSessionID`. The Timeline page uses it to identify the
runs that belong to the current daemon session.

Within a run, each agent subprocess emits a `session_id`. The pipeline
(`formatBufLine` → `BufLogEntry.SessionID` → `IssueLogEntry.SessionID`)
preserves it so the Timeline's `extractSubagents` can filter the log down to a
single run when expanded.

### Input-required sentinel

WORKFLOW.md instructs the agent to emit the sentinel
`<!-- itervox:needs-input -->` on its own line when it needs human input.
`agent.IsSentinelInputRequired` (in `internal/agent/events.go`) detects it, and
`agent.FinalizeResult` (in `internal/agent/runner.go`) calls it to set
`TurnResult.InputRequired`. A 1-line wrapper `agent.IsContentInputRequired` is
kept as an alias for callers that don't go through `FinalizeResult`. The worker forwards
this to the orchestrator, which records the issue in
`State.InputRequiredIssues`. The dashboard's `ReviewQueueSection` surfaces
those issues so the user can supply guidance and resume. Codex backends also
set `InputRequired` directly when they emit `turn.failed` with a "human turn"
reason — both backends share the same downstream path.

### Configuration hot-reload

`workflow.Watch` (in `internal/workflow/watcher.go`) polls `WORKFLOW.md` once
per second using a content hash, so identical writes do not trigger reloads.
On a real change it invokes the supplied callback, which `cmd/itervox` wires
to a graceful orchestrator restart.

### Tracker abstraction

Linear, GitHub, and an in-memory backend all implement `tracker.Tracker`. The
orchestrator works exclusively with `domain.Issue` values, so the dispatch
logic is backend-agnostic.

## Request flow (web dashboard → agent dispatch)

```
Browser POST /api/v1/issues/:id/resume
  → server.Handler (HTTP goroutine)
  → orch.ResumeIssue()
  → o.events <- EventResumeIssue          (non-blocking send)
  → Orchestrator event loop receives
  → removes issue from PausedIdentifiers / InputRequiredIssues
  → next reconcile dispatches the issue
  → go runWorker(workerCtx, issue, ...)
  → agent.Runner.RunTurn() spawns claude or codex subprocess
  → streams parsed events (text, tool_use, session_id, ...) back
  → FinalizeResult applies the input-required sentinel check
  → EventWorkerExited sent to o.events
  → state updated, snapshot rebuilt
  → OnStateChange → SSE broadcast → React store patch
```

## Packages

| Package | Responsibility |
|---|---|
| `cmd/itervox` | Entry point; loads WORKFLOW.md, wires orchestrator, server, TUI, and `workflow.Watch` |
| `internal/orchestrator` | Single-goroutine event loop, dispatch, reconcile, state machine, reviewer, retries |
| `internal/agent` | `Runner` interface, claude and codex subprocess runners, stream parsing, sentinel detection, log tailer, fake runner for tests |
| `internal/tracker` | `Tracker` interface plus normalize helpers and an in-memory backend |
| `internal/tracker/linear` | Linear GraphQL client |
| `internal/tracker/github` | GitHub REST client |
| `internal/domain` | Shared value types (Issue, TurnResult, etc.) |
| `internal/config` | WORKFLOW.md config struct, validation, env-var resolution, defaults (incl. workspace root) |
| `internal/workflow` | WORKFLOW.md loader and content-hash file watcher |
| `internal/workspace` | Workspace provisioning, path safety, git worktree / bare-clone helpers, PR branches, lifecycle hooks |
| `internal/prompt` | Liquid template rendering for agent prompts |
| `internal/logbuffer` | Per-issue ring buffer for live log streaming |
| `internal/prdetector` | Detects PR URLs in agent output |
| `internal/app` | Cross-cutting business logic (e.g. `EnrichIssue`) |
| `internal/server` | chi-based HTTP API, SSE broadcaster, embedded web assets |
| `internal/statusui` | Bubbletea terminal UI; reads `Orchestrator.Snapshot()` |
| `internal/templates` | WORKFLOW.md scaffolding and human-input template |
| `internal/logging` | slog setup and shared logging helpers |
| `web/` | React 19 + Vite + TypeScript dashboard, embedded into the binary via `internal/server/embed.go` (`go:embed web/dist`) |
