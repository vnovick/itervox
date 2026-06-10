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
- `cfg.Agent.DepsAnalyzerProfile`
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

## v0.2.0 Automation Queue And Dependency Surfaces

Automation dispatch keeps retryable runtime failures in event-loop-owned queue
state. If an automation trigger cannot start because there are no slots, a
per-state cap is full, the issue is already running/claimed, input is pending,
or blockers are unresolved, the event loop records an `AutomationQueueEntry`
instead of dropping the trigger attempt. Queue entries drain when worker
capacity and eligibility allow.

The queue is capped by `agent.max_automation_queue_length` (default `100`).
When the cap is reached, `AutomationQueueBackpressure` marks producers as
paused. New automation trigger intake stops, rejected one-shot attempts are
counted for audit, and existing queued work continues draining until the queue
falls below the low-water mark.

Dependency audit normalizes `issue.BlockedBy` into observable `blocked`,
`unknown`, and `unblocked` states. Linear blockers come from native relations;
GitHub blockers come from issue-body `blocked by #123` references. Unknown
blocker state remains unresolved. A PR merge does not unblock dependents until
the tracker later reports the blocking issue as terminal/closed/done.

Core dependency audit is read-only. It can emit a `blockers_resolved`
automation event when all blockers become terminal, but it does not mutate
Linear or GitHub state directly. Tracker state changes remain explicit
dashboard actions or opt-in automations with profile permissions.

The dashboard Deps tab is a display-only React Flow graph of issue dependency
relationships. Edge direction is blocker -> blocked issue. Nodes show tracker,
running, queued, terminal, blocked, unblocked, and unknown badges. Clicking a
node reuses the existing issue-detail panel path.

The full editable Trigger -> Automation -> Profile -> Worker canvas remains a
v0.2.1+ concern; the v0.2.0 Deps graph is only an issue dependency
visualization.

## v0.2.0 Track B — Schema 2, file-backed profiles, and HEARTBEAT.md

### Schema 2 startup validation

Itervox runs on **schema 2** WORKFLOW.md files exclusively.
`internal/config/validate.go` requires `itervox_schema_version: 2` at the top
of every `WORKFLOW.md`. The daemon emits `MissingWorkflowSchemaMessage` and
refuses to start when the marker is absent or set to an unsupported version.
Migration is non-destructive: `itervox init --update --workflow WORKFLOW.md`
extracts inline `agent.profiles.<name>.prompt` content into per-profile
`INSTRUCTIONS.md` files, generates starter `SOUL.md` files, writes a
`WORKFLOW.md.bak`, patches the root `.gitignore` so `.itervox/agents/**` is
committable, and stamps `itervox_schema_version: 2` on the migrated file.

### Built-in profile registry

In addition to operator-authored profiles on disk, Itervox ships built-in
profiles embedded into the binary via `internal/profiles/registry.go`. The
first built-in is **`merge-bot`** at `internal/profiles/builtin/merge-bot/`.
Operators reference a built-in by name (`agent.profiles.merge-bot: {}` in
`WORKFLOW.md`) and the loader resolves SOUL/INSTRUCTIONS content from the
embedded `embed.FS`. File-on-disk content always wins — copying
`.itervox/agents/merge-bot/SOUL.md` to disk lets the operator customise
tone while still getting the embedded defaults for unset files. The
default `command`, `backend`, and `allowed_actions` for built-ins live in
`builtinDefaults` and apply only when the operator does not override them.
`itervox init` and `itervox init --update` scaffold built-in files to
disk; `itervox doctor` enumerates the shipped built-in profile list.

### SOUL.md / INSTRUCTIONS.md prompt assembly

Profile content lives in `.itervox/agents/<name>/` and is referenced from
`WORKFLOW.md` via `agent.profiles.<name>.soul_file` and `instructions_file`.

Assembly order at dispatch time (from `internal/orchestrator/worker.go`):

1. **Per-issue Liquid template** — rendered with `domain.Issue` fields by
   `internal/prompt`. The strict-variables engine rejects undefined references.
2. **`## Prior Agent Handoffs`** block (if any) — file-backed agent handoff
   content read from `<workspace>/.itervox/handoff/*.md` in chronological
   order, budget-truncated. See **Agent handoff** below.
3. **`## Run Context`** block — `run.timestamp` and `run.handoff_path` for
   this dispatch. The agent uses these to write its own deliverable.
4. **SOUL.md** — compact identity ("who you are"), appended via
   `renderProfilePromptBlocks`.
5. **INSTRUCTIONS.md** — operational rules, checklists, including the
   "Handoff Protocol" section that points at `run.handoff_path`.
6. Optional appends: automation instructions, action context, sub-agent
   roster, first-turn PR context.

The concatenated result is passed to the agent subprocess. Inline `prompt:`
fields are rejected at config load; this is enforced by validation tests, not
silent fallback.

### Agent handoff (`.itervox/handoff/`)

`internal/orchestrator/handoff.go` implements file-backed handoff for chained
profiles. Each worker run writes a Markdown deliverable to
`.itervox/handoff/<ISO8601-timestamp>_<profile-name>.md` on the issue's
worktree branch; subsequent workers see all prior deliverables prerendered
into their prompt.

**Key functions:**

- `handoffRunTimestamp(t time.Time)` returns an ISO8601 timestamp with `:`
  replaced by `-` so the value is filename-safe and lexicographically equal
  to chronological order. The orchestrator generates one timestamp per worker
  dispatch and exposes it as `run.timestamp`.
- `handoffPathFor(timestamp, profileName)` builds the canonical handoff path.
  Profile names are slugified (spaces → hyphens); empty names fall back to
  `agent`.
- `buildHandoffContextBlock(wsPath, budget)` reads every `.md` file in
  `<wsPath>/.itervox/handoff/`, sorts by filename (chronological), applies the
  budget (default 30 KB; oldest dropped first with a truncation marker), and
  returns a `## Prior Agent Handoffs` Markdown block. Files with the
  `.partial.md` suffix are included and explicitly marked.
- `buildRunContextBlock(timestamp, handoffPath)` builds the `## Run Context`
  block agents read to know where to write.
- `markHandoffPartial(wsPath, handoffRelPath)` renames a specific in-flight
  file to `<basename>.partial.md`. No-op when the file does not exist.
- `markLatestHandoffPartial(wsPath, profileName)` finds the most recent
  matching `<*>_<profile>.md` by mtime and renames it. Filesystem-driven so
  the orchestrator does not need to remember each worker's run timestamp.

**Partial rename hooks** live in `event_loop.go`. The orchestrator's
`markFailedHandoffPartial` runs on the `TerminalFailed` branch (excluding
`context.Canceled` orchestrator-initiated stops). `markStalledHandoffPartial`
runs on the `TerminalStalled` branch. `TerminalSucceeded` and
`TerminalInputRequired` do not rename — success is a clean handoff,
input-required is a pause that will resume.

**Workspace clear under handoff:** the orchestrator now treats workspace
cleanup as terminal-state-only. `cfg.Workspace.AutoClearWorkspace` fires when:

- A worker exits `TerminalSucceeded` AND no auto-review is queued. If
  `cfg.Agent.AutoReview` + `cfg.Agent.ReviewerProfile` are set, the clear is
  deferred to the reviewer's own `TerminalSucceeded` handler.
- A worker exits `TerminalFailed`, retries are exhausted, no rate-limited
  recovery is queued, and the issue moves to `cfg.Tracker.FailedState`.

Workspace is preserved across retries, input-required pauses, stalls, and any
mid-pipeline state transitions so handoff files accumulate across the chain.

### IssueStatusHistory ledger

`internal/orchestrator/status_history.go` keeps a per-issue ledger of state
transitions sourced from tracker observation, dashboard actions, worker
lifecycle moves, automation moves, and system cleanup. The per-issue slice is
capped at `maxIssueStatusHistory = 50`. The outer map is bounded by an
event-loop janitor (`pruneIssueStatusHistory`) that drops entries whose
identifier is absent from the candidate set AND whose most-recent change is
older than `issueStatusHistoryRetention` (default 7 days). Live issues are
preserved regardless of age. Janitor runs at the tail of every `onTick`.

The ledger is session-local in v0.2.0. Cross-restart persistence is gated on
the Track B queue-persistence proposal (`planning/v0.2.0/todolist4.md` A.2).

### v2 envelope queue persistence (todolist4 A.2)

Automation queue writes are wrapped in a versioned envelope:

```
{
  "schema_version": 2,
  "daemon_instance_id": "itervox-<pid>-<bootns>",
  "payload": { ... automationQueueStateDisk ... },
  "payload_sha256": "<hex>"
}
```

`EncodeQueueEnvelope` / `DecodeQueueEnvelope` (`internal/orchestrator/queue_persistence_envelope.go`)
own the serialisation. The reader peeks the top-level `schema_version` via
`IsQueueEnvelopeShape` — when absent, the file is treated as a legacy v1 raw
payload for back-compat. Mismatched `schema_version`, mismatched `payload_sha256`,
or a corrupt envelope move the file to `<path>.quarantine` instead of being
silently consumed. A mismatched `daemon_instance_id` is logged as a warning
but still loads, since the persistence file is local to a single repo and
not synchronised across processes.

### `pr_merged` automation trigger (P1)

Sibling of `pr_opened`. Compiled into `compiledAutomationSet.prMerged` by
`cmd/itervox/automations.go::compileAutomations` and installed via
`Orchestrator.SetPRMergedAutomations`. The daemon-side `merge_pr` action's
success path emits the trigger through `orchestratorAdapter.EmitPRMerged →
Orchestrator.DispatchPRMergedAutomations`. Per-(issue, PR URL, automation ID)
dedup ledger on `State.PRMergedDispatched`; `AutomationDispatchesPRMergedTotal`
and `AutomationDroppedPRMergedDedupTotal` counters surface dispatch
telemetry.

### `itervox doctor` (P0-D / P0-G)

A preflight subcommand that runs WORKFLOW.md schema validation + dispatch
checks without starting the daemon and reports binary-resolution drift
between the running daemon's binary (`os.Executable()`) and whatever
`which itervox` resolves on the daemon's PATH. The report also lists the
shipped built-in profile names (`profiles.Names()`) and surfaces any
existing `.itervox/STARTUP_ERROR.md`. Exits non-zero on schema failure,
binary drift, version mismatch, or a present startup-error marker.

On startup config-load failure the daemon writes
`.itervox/STARTUP_ERROR.md` (timestamp, workflow path, YAML/schema diagnostic,
suggested fix) before exiting via `fatalExit(1)`, then clears the file on
the next healthy boot. Operators running the daemon under nohup / launchd
get a durable record of what broke.

### `cmd/itervox/heartbeat.go` — `.itervox/HEARTBEAT.md`

A human-readable daemon liveness file. Written on startup and refreshed after
state changes at a bounded interval (default 15 s) via
`atomicfs.WriteFile(path, content, 0o644)`. The file records:

- active workflow path and `itervox_schema_version`
- dashboard URL and tracker/project
- agent capacity (running / max)
- automation queue pressure (length / max, paused producers, recent rejects)
- dependency audit summary (blocked / unknown / unblocked counts)
- input-required count and retry count
- last notable error

The file is gitignored as transient runtime state. Do NOT commit. Operators
typically wire it into `tail -f` or a monitoring dashboard for ops visibility
without needing to scrape `/api/v1/state`.

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
| `internal/templates/scaffold` | Registry of `--template` presets for `itervox init` (`minimal`, `full`, `rate-limit-fallback`, `pr-review`, `daily-qa`) |
| `internal/logging` | slog setup and shared logging helpers |
| `internal/profiles` | Built-in profile registry: `embed.FS` of `builtin/<name>/{SOUL,INSTRUCTIONS}.md` plus per-profile `Default{Command,Backend,Actions}` |
| `internal/depsanalysis` | Dependency analyzer sidecar — async job that runs an analyzer profile over the snapshot graph and caches results |
| `internal/evals` | Profile evaluation suite (`Scenario`, `Recording`, `Judge`, `Report`) wired into `make evals-fast`; first merge-bot fixtures live under `internal/evals/fixtures/merge-bot/` |
| `internal/agentactions` | Short-lived per-run bearer grant store for the `/api/v1/agent-actions/*` routes |
| `internal/atomicfs` | Tmp-file + fsync + rename helpers; used for every WORKFLOW.md mutation, scaffold write, pidfile, and queue persistence file |
| `internal/automationconfig` / `internal/automationdef` | Helper packages for automation YAML parsing and rule definitions |
| `internal/schedule` | Cron parsing for the `cron` automation trigger and the legacy `schedules:` block |
| `internal/skills` | Capability inventory scanner for the Settings → Skills surface |
| `web/` | React 19 + Vite + TypeScript dashboard, embedded into the binary via `internal/server/embed.go` (`go:embed web/dist`) |
