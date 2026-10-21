# CLAUDE.md — itervox

## What this project is

**Itervox** is a long-running daemon (Go 1.26.9) that implements the
[OpenAI Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md).
It polls Linear, GitHub Issues or local Markdown issue files (`tracker.kind: local`), spawns Claude Code or Codex agents per issue, and
provides a live Kanban web dashboard (React/Vite) and a Bubbletea terminal UI.

Config lives in one `WORKFLOW.md` file per project (YAML front matter +
Liquid prompt template). Since #129 the `agent`, `polling`, `workspace` and
`server` sections can also come from JSONC settings layers
(`~/.itervox/settings.jsonc` < `.itervox/settings.jsonc` < `WORKFLOW.md`),
merged in `config.Load` (`internal/config/layers.go`); `itervox config show`
explains where each value came from. The binary is a single static Go
executable.

---

## Build and test

```bash
# Full build (web → Go binary)
make build

# All checks (mirrors CI): fmt + vet + lint + Go race tests + web coverage/build
make verify

# Go tests only (always run with -race)
go test -race ./cmd/... ./internal/...

# Single package
go test -race ./internal/orchestrator/...

# Orchestrator + tracker tests repeated under CPU saturation (#126; slow,
# not part of verify; CI runs it nightly and on the `ci:chaos` label)
make chaos   # CHAOS_COUNT / CHAOS_RUN / CHAOS_PACKAGES / CHAOS_CPU to narrow

# Frontend
cd web && pnpm install --frozen-lockfile && pnpm test:coverage
pnpm build   # production bundle

# Dev workflow: Go binary (in a project directory with WORKFLOW.md) + Vite
go build -o itervox ./cmd/itervox
cd web && pnpm dev   # HMR at localhost:5173, proxies /api/* to localhost:8090
```

Git hooks (lefthook) run `go vet`, `golangci-lint`, `tsc --noEmit`, ESLint,
Prettier on pre-commit; full test + build suites on pre-push.

Testing specifics not obvious from the commands above:
- **TUI tests** use `charmbracelet/x/exp/teatest` (e.g. `model_teatest_test.go`)
  with catwalk golden files. Regenerate golden files via `make tui-golden`
  after intentional render changes.
- **Integration tests** that hit a real tracker API are gated behind a build
  tag; they are NOT run by default with `go test ./...`.
- **Frontend tests** use Vitest + Testing Library. Coverage gates live in
  `web/vitest.config.ts`; `pnpm test:coverage` is the canonical entry point.

---

## Architecture — critical invariants

### Orchestrator is a single-goroutine state machine

All state mutations happen in ONE goroutine — the `select` loop inside `Run()`.
Workers communicate back via `o.events chan OrchestratorEvent`.

**Key rule: never mutate `State` from a worker goroutine.** Only send events.

```
  tick / HTTP / ctx.Done
          ↓
  ┌── orchestrator event loop (single goroutine) ──┐
  │   select { case ev := <-o.events: ...  }        │
  └────────────────────────────────────────────────┘
              ↑ OrchestratorEvent
  ┌── worker goroutine (one per issue) ──────────────┐
  │   runs claude/codex subprocess                   │
  │   sends EventWorkerExited on completion          │
  └──────────────────────────────────────────────────┘
```

### Runs have an ID and a lifecycle (#125)

Every dispatch calls `startRun` (`internal/orchestrator/run.go`), which gives the
Running entry a `RunID` (also its log `SessionID`) and puts the run handle in the
worker's context. Every `EventWorkerUpdate`/`EventWorkerExited` the worker sends,
and every synthetic exit reconcile or stall detection sends, carries that
`RunID`. The loop applies an event only to the run it names: an exit or update
from a superseded run (stopped, then the issue dispatched again) changes nothing.
`State.LatestRun` keeps a `RunRecord` of each issue's most recently dispatched
run after its Running entry is gone, so a late exit is still recognised as
superseded once its replacement has itself exited (and left a retry behind).
`RunEntry.Phase` (`dispatched → running → finishing → exited`) is event-loop
state. The worker reports `finishing` through its run handle (`markFinishing`)
before it moves the issue to `completion_state` and before it sends its exit;
reconcile and stall detection fold that in with `observeFinishing` and leave a
finishing run to its own exit. Do not key new run bookkeeping by issue ID alone.

The exit handler identifies the finishing run with `finishedRun`: the live
entry, else the run's record (#127). The record carries its kind, profile (a
reviewer's chain step), whether reconcile or stall detection stopped it
(`markRunStopped`; a stopped run's late success starts no review), and whether
its own exit was handled. A run that continues a reviewer carries the reviewer
itself: `RetryEntry.ReviewerProfile`, `PausedSessionInfo.Reviewer`, and
`Reviewer` on the input-required entries (persisted), which `dispatchRun` and
the input resume turn back into a reviewer `RunEntry`. A reviewer never writes
the operator's per-issue profile override (`issueProfiles`). Do not add a
per-issue side map to recover what a run was; put it on the run or on the
entry that continues it.

### `orchestrator.State` is a value type

`State` is a plain struct passed by value into reconcile/dispatch functions.
No mutex is needed for `State` fields — only the event loop writes them.

### Queue, dependency audit, and status ledgers are event-loop state

`AutomationQueue`, `AutomationQueueOrder`, `AutomationQueueBackpressure`,
`DependencyAudit`, `DispatchPressure`, and `IssueStatusHistory` are normal
`orchestrator.State` fields. They follow the same rule as `Running`, `RetryQueue`, and other state:
only the event loop mutates them.

- Automation producers send `EventDispatchAutomation`; the event loop decides
  whether to start immediately, enqueue, coalesce, drop, or record backpressure.
- Queue persistence is local runtime state under `.itervox/`; do not persist it
  through tracker comments or the dashboard store.
- Dependency audit is read-only with respect to Linear/GitHub. It can emit the
  `blockers_resolved` automation trigger when blockers transition to terminal,
  but tracker state moves require explicit automation policy plus a profile with
  `move_state`.
- Inferred (LLM-detected) edges soft-gate dispatch: a confidence threshold,
  a staleness window, and a per-issue operator override (`State.DepsOverrides`)
  all factor into `InferredDepEntry.Gating`; tracker-declared blockers
  (`DependencyAudit`) stay hard blocks regardless. The dependency graph is
  derived solely from `orchestrator.State.InferredDeps` (event-loop
  reconciled) plus `DependencyAudit` — `cmd/itervox` no longer reads the
  deps-analyzer sidecar for the dashboard graph.
- The dashboard Deps tab derives from snapshot dependency graph rows, not a
  parallel frontend dependency store. Its only mutation surface is the
  per-issue inferred-blockers override, which goes through the documented
  `POST`/`DELETE /api/v1/issues/{identifier}/deps-override` endpoints →
  `SetDepsOverride` → `EventSetDepsOverride` in the event loop; the tab never
  mutates dependency state directly.
- Issue status history is bounded runtime-session history unless future work
  explicitly adds restart durability.
- `RecentFailures` (CORE-046) is a bounded (100) event-loop ring. Off-loop
  producers (ledger writer, outbox flusher, recovered panics, the
  `/api/v1/client-errors` handler, a worker whose shared bare-clone fetch
  failed — #195, deduplicated in `internal/workspace` until a fetch
  succeeds) call `Orchestrator.RecordFailure`, a
  NON-blocking send of `EventFailureRecorded`; a dropped failure is counted,
  never re-sent. Messages come from classified errors only (never agent
  output or prompt text) and are redacted before truncation. Not persisted;
  `cmd/itervox` carries it across `WORKFLOW.md` reloads via
  `SeedRecentFailures`.
- `BackendHealth` (CORE-053) is the per-(backend, worker host) circuit
  breaker and `BackendLimitedHolds` the issues it refused (`backend_limited`).
  Both are event-loop state; the gate (`gateDispatch`) runs after the
  CORE-115 resolver in every admission path (dispatch, fireRetries, reviewer,
  automation, pending-input resume). Open breakers persist to
  `backend_health.json` via the ledger writer. `agent.backend_fallback` is
  load-time config, NOT in the cfgMu allowlist.
- `PendingReviews` (M4-close BH-M4-2) holds reviewer dispatches refused while
  draining. It is event-loop state persisted to `pending_reviews.json` (ledger
  writer) and re-dispatched by `resumePendingReviews` once admission reopens;
  the marker is cleared when any reviewer for that issue starts.
- `StackOnReviewState` (tick snapshot of `tracker.completion_state` when
  `dependencies.stacked_prs` is on) and `StackUnavailable` are event-loop
  state. An issue whose only unresolved blocker is in review is admitted
  (`reviewStackAdmits`) and stacked on the blocker's branch; a fresh worktree
  that could not be stacked exits `TerminalStackUnavailable` before any agent
  runs, and the event loop records the blocker so the issue waits for it.
  `pruneStackUnavailable` drops a record each tick once it no longer
  matches the candidate's blocker. Session-scoped, not persisted.
- `OutOfScope` (#182) marks running issues the scoped candidate poll no
  longer offers while they are still active (reassigned away, a matching
  label removed, moved out of the project). Derived by `observeOutOfScope`
  on each successful poll; the run finishes (never cancelled), and
  `fireRetries` releases a retry for a marked issue because its by-ID read is
  unscoped. Event-loop state, session-scoped, not persisted. The tracker
  scope itself (projects AND assignee AND labels) lives in the Linear
  client under its own `projectFilterMu`, not `cfg`:
  `tracker.assignee`/`team`/`labels` are load-time fields, and the dashboard
  scope card and TUI picker change the client's runtime scope
  (`SetScope`/`SetProjectFilter`), so there is no new `cfgMu` field.
- `CronCursors` (#176) is the per-cron-rule `filter.limit` rotation cursor
  (automation ID → last identifier served). A limited cron tick is handed to
  the loop whole (`EventDispatchCronBatch`); the loop selects, skips issues
  already running/queued for that rule, dispatches and advances the cursor.
  Persisted as `cron_cursors` in `automation_queue.json`.
- `DeferredStateMoves` (#177) holds itervox-originated state moves (an
  agent's `move_state`, offered via `DeferAgentStateMove` from the adapter)
  that would make reconciliation stop the issue's running worker. They are
  deferred until that run's exit, then applied off-loop through the write
  sink only while the tracker still shows the move's `FromState`. Event-loop
  state persisted to `deferred_state_moves.json` (ledger writer); while one is
  pending, dispatch and retries hold the issue (`state_move_pending`). A
  person's move (dashboard/tracker) is never deferred and still stops the
  worker.
- `LessonCorrections` / `LessonProposals` (#203) are the lesson detector's
  per-issue ledgers: the human corrections since the issue's last successful
  worker run (bounded, redacted) and the rate-limit stamps. Event-loop state,
  session-scoped, not persisted; a proposal that wrote nothing new gives its
  stamp back through `EventLessonDrafted`.
- `PendingRestacks` holds dependents whose blockers landed while they were
  running: the restack is never run under an agent, so it is deferred to the
  first dependency audit that finds the issue idle (dropped if it is blocked
  again). Event-loop state, session-scoped, not persisted.
- `SessionMessages` (#178) holds the operator messages sent from the Sessions
  panel, per issue identifier (bounded, 20). `SendSessionMessage` (HTTP
  goroutine) only classifies the target against `Snapshot()` and sends
  `EventSessionMessage`; the loop records the message and hands it to the run
  through `runHandle.steer` (non-blocking). The worker reports deliveries with
  `EventSessionMessageDelivered` — live (written into a local Claude turn's
  stream-json stdin, `agent.live_steering`) or on resume (the next turn's
  prompt, `--resume` / `exec resume`) — and the run's exit marks whatever it
  did not deliver `dropped`. Session-scoped, not persisted. Invariant:
  itervox writes only to agent processes it started (no TTY injection, no
  Accessibility/AppleScript automation) — with ONE narrow exception,
  `internal/tmuxctl`, which types into a CLI in a tmux pane through tmux's
  paste buffer and key events (hand-started sessions only with
  `sessions.tmux_control`, plus the tmux sessions itervox started). Enforced
  by `internal/agent/no_foreign_writes_test.go::TestNoWritesToProcessesItervoxDidNotStart`:
  no other package may run `tmux` or name its write commands, and tmuxctl
  runs nothing but a literal `tmux`.
- `ExternalSessions` (#178 step 3) is event-loop state.
  Discovery of sessions started by hand (`internal/extsessions`, read-only:
  transcript files, `/proc` or `ps`/`lsof`) runs on its own goroutine
  (`startExternalSessionDiscovery`, joined via `extSessionsWg`) and only sends
  `EventExternalSessionsDiscovered`; the loop drops every session itervox
  owns. These sessions are read-only: status `running` (with the liveness
  reason) or `exited`, transcript served, messages refused (`not_owned`) —
  unless matched to a tmux pane (by process ancestry + cwd, in the discovery
  pass, off the loop) with `sessions.tmux_control` on. Session-scoped, not
  persisted.
- `TmuxSessions` (#178 New session) are the tmux sessions itervox started,
  event-loop state: HTTP goroutines run tmux (cmd/itervox/sessions_tmux.go)
  and only send `EventTmuxSessionReserve` (cap 8, reply channel),
  `EventTmuxSessionStarted` and `EventTmuxSessionEnded`; each discovery pass
  (`EventExternalSessionsDiscovered`, carrying `ExternalScan` with the pane
  list) attaches panes and drops sessions whose tmux session is gone. Not
  counted toward `max_concurrent_agents`. Carried across `WORKFLOW.md`
  reloads by cmd/itervox (`SeedTmuxSessions`), not persisted. `TestExternalSessionInspectionIsReadOnly`
  keeps `internal/extsessions` free of writes, signals and commands other
  than `ps`/`lsof`.
- `ManagedPRs`, `PREventDispatched` and `PREventBaselines` (#173) back the
  PR-event triggers (`pr_check_completed`, `pr_synchronized`,
  `pr_review_submitted`, `pr_mergeable_changed`). A PR is *managed* only once
  an itervox run's exit recorded its URL for that issue; `internal/prwatch`
  (webhook handler and poller, off the loop) only sends
  `EventDispatchAutomation`, and `claimPREventDedup` in the event loop decides
  managed-ness, staleness and the once-per-event dedup. Persisted to
  `pr_events.json` by the ledger writer; pruned when the issue turns terminal.
- Run provenance (#186) on `RunEntry` / `CompletedRun`: `PromptDigest` and
  `PromptParts` are computed once in `startRun` (SHA-256 of SOUL.md +
  INSTRUCTIONS.md + the resolved rules + the memory selection (#202, only
  when non-empty) + the prompt template; hashes only,
  never prompt text), and delivered steering messages are appended to the
  running entry's `Steers` by the `EventSessionMessageDelivered` handler
  (redacted, ≤ 20 × 1 KiB; snapshots deep-copy the slice). Both reach
  `CompletedRun` (with `RunID`) only through `recordHistory` in the event
  loop; the stall exit built by `ReconcileStalls` copies them.
- `TrackerPoll`, `TrackerBreaker` and `ClaimChecks` (#187) are event-loop
  state for the off-loop tracker poll. A tick's tracker reads (running-state
  reconcile, candidate list, input-required reply checks) run on ONE
  goroutine at a time (`TrackerPoll.InFlight` latch, generation-checked,
  watchdog past deadline + 30 s) and come back as `EventTrackerPolled`;
  dispatch picks' #116 claim reads run off-loop too, each pick holding a slot
  in `ClaimChecks` until `EventClaimChecked`. The loop applies results with
  the state it has at apply time: reconcile acts only on runs whose `RunID`
  is unchanged since the read started, and an issue whose run ended during
  the poll (`TrackerPoll.EndedDuringPoll`) is not dispatched from that list.
  Reads still on the loop go through `loopReadCtx` (one 30 s deadline per
  iteration). `TrackerBreaker` opens after 3 polls in a row with a transport
  failure and skips tracker reads for a cooldown. Session-scoped, not
  persisted.
- `DispatchPressure` records, per tick, whether dispatch was *slot-bound*
  (no free slots with eligible work waiting) or *dependency-bound* (free
  slots that went unused because remaining candidates were blocked). It is
  observed once per tick after the dispatch loop via `observeDispatchPressure`
  and is session-scoped — deliberately not persisted, since it describes the
  running fleet's current configuration. Classification runs against a probe
  copy of `State` with the slot gate neutralized, because
  `ineligibleReasonShared` checks `AvailableSlots` *before* the blocker gates
  and would otherwise mask every dependency reason as `no_slots` on exactly
  the saturated ticks the metric exists to explain.

### `cfgMu` guards exactly these fields (and nothing else)

> **Canonical source for codex too** — AGENTS.md is a thin pointer that tells
> codex to read CLAUDE.md. If you add, remove, or rename a field here, mirror
> the change into `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields`
> in the same commit. No second copy in AGENTS.md to keep in sync.

These `Orchestrator.cfg` fields can be mutated at runtime by HTTP handler goroutines
and must always be accessed under `cfgMu`. **Keep this list alphabetically sorted by
full field path** so the test allowlist (`AllowedMutableCfgFields`) and this docs
section stay easy to diff.

- `cfg.Agent.AutoReview`
- `cfg.Agent.AvailableModels`
- `cfg.Agent.DepsAnalyzerProfile`
- `cfg.Agent.DispatchStrategy`
- `cfg.Agent.InlineInput`
- `cfg.Agent.MaxConcurrentAgents`
- `cfg.Agent.MaxRetries`
- `cfg.Agent.MaxSwitchesPerIssuePerWindow`
- `cfg.Agent.Profiles`
- `cfg.Agent.RateLimitErrorPatterns`
- `cfg.Agent.ReviewerProfile`
- `cfg.Agent.SSHHostDescriptions`
- `cfg.Agent.SSHHosts`
- `cfg.Agent.SwitchRevertHours`
- `cfg.Agent.SwitchWindowHours`
- `cfg.Automations`
- `cfg.Dependencies.AnalysisMode`
- `cfg.Tracker.ActiveStates`
- `cfg.Tracker.CompletionState`
- `cfg.Tracker.FailedState`
- `cfg.Tracker.TerminalStates`
- `cfg.Workspace.AutoClearWorkspace`

The canonical allowlist is `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields`;
keep this section in sync with that test. All **other** `cfg` fields are read-only after startup — no lock needed for them.

### Snapshot access

`Snapshot()` acquires `snapMu.RLock` and returns a copy of `lastSnap`.
HTTP handlers call `Snapshot()` — they must never hold `cfgMu` while doing so.

### Track B — file-backed profiles, schema 2, and HEARTBEAT.md

Profile content does NOT live in `WORKFLOW.md`. As of v0.2.0:

- **`itervox_schema_version: 2`** is mandatory at the top of every `WORKFLOW.md`.
  Startup validation in `internal/config/validate.go` emits `MissingWorkflowSchemaMessage`
  and the daemon hard-fails if the marker is absent or set to a different version.
- **Inline `agent.profiles.<name>.prompt` is rejected** by schema 2. Each profile
  must reference its identity and operating instructions via `soul_file` and
  `instructions_file`, typically `.itervox/agents/<name>/SOUL.md` and
  `.itervox/agents/<name>/INSTRUCTIONS.md`.
- **Prompt assembly order** at dispatch time: the per-issue Liquid prompt
  template (WORKFLOW.md body, or `agent.reviewer_prompt` for a reviewer) and
  the daemon's blocks (reply envelope, handoffs, run context), then the
  profile tail `profilePromptTail` shares with the eval composer: `## Rules`
  → `## Memory` (#202) → SOUL.md → INSTRUCTIONS.md → automation
  instructions / action notice / sub-agent roster. Concatenated and passed to
  the agent subprocess via stdin/argv depending on backend. SOUL is the
  compact identity ("who you are"); INSTRUCTIONS is the
  operational/behavioural rules. `TestPromptAssemblyOrderRulesMemorySoul`
  pins the tail order.
- **Rules (#157)** go in a `## Rules` block immediately ahead of the profile's
  SOUL.md/INSTRUCTIONS.md blocks (workers, reviewers, automation runs; the
  dependency analyzer gets it at the top of its prompt). `internal/rules` reads
  `~/.itervox/rules.json` < `.itervox/rules.json` < `.itervox/rules.local.json`
  from disk on every dispatch (`startRun` → `runHandle.rules`; NOT a cfg field,
  so nothing joins the cfgMu list), merges by id (a user `must` rule is
  locked), filters by project/profile/backend scope, and records hash + ids on
  `RunEntry`/`RunRecord`/`CompletedRun`. A template that prints `{{ rules }}`
  (or a field of it) suppresses the automatic block. JSONC goes through
  `config.ParseJSONC` / `config.PatchJSONC` / `config.WriteJSONCFile`, the same
  reader/writer as the settings layers. `.itervox/rules.json` is committable;
  `.itervox/rules.local.json` is gitignored by `itervox init` / `init --update`.
- **Memory (#202)** goes in a `## Memory` block after the rules and ahead of
  SOUL.md. `internal/memory` reads `~/.itervox/memory.json` (private;
  `ITERVOX_USER_MEMORY`) and `.itervox/memory.json` (project shared,
  committable: `!/memory.json` / `!.itervox/memory.json` carve-outs, checked
  by repo readiness) on every dispatch (`startRun` → `runHandle.memory`; NOT
  a cfg field). Pinned entries always, the rest ranked lexically against the
  issue within `memory.token_budget` and what the rules leave of
  `memory.prompt_share_tokens` (both load-time, NOT in the cfgMu list).
  Only `active` entries are used (`proposed` lessons wait for approval).
  The run records `MemoryIDs` on `RunEntry`/`CompletedRun`; the selection is
  a prompt-digest component (`PromptParts.Memory`, written only when
  non-empty). Usage (`lastUsed`/`useCount`) is updated ONLY via
  `EventMemoryUsed` (sent by the worker once the block reaches the agent),
  whose loop handler hands the file rewrite to a `goSafe` goroutine joined
  by `memoryWg`. Every save redacts secrets (`logging.RedactString`) and
  rejects entries over the size caps; writes go through `atomicfs` under the
  package's process-wide lock.
- **Lessons (#203)** are `lesson` memory entries Itervox proposes itself
  (`status: proposed`, never used until approved). Detection is event-loop
  only: `EventProvideInput` / a tracker reply (`applyReplyChecks`) →
  `observeInputReply`, `EventSessionMessageDelivered` → `observeSteers`, and a
  worker's `TerminalSucceeded` after a failed/stalled run → `observeRunSucceeded`.
  The loop records corrections in `State.LessonCorrections`, applies the
  per-issue rate limit in `State.LessonProposals`, resolves the drafting
  profile (`lessonDraftPlan`, cfgMu read on the loop) and hands drafting plus
  the proposal write to a `goSafe` goroutine joined by `lessonWg`, which
  reports back with `EventLessonDrafted`. Drafting goes through `o.runner`
  with the sandbox mode, an empty temp dir and a hard token/time budget,
  falling back to `memory.TemplateDraft`. Tests must use fake runners
  (`gateRunner`, `blockedRunner`); never a real CLI. `memory.lessons.*` is
  load-time config, NOT in the cfgMu list. Rejections live in
  `.itervox/lessons-rejected.json` (normalized words + fingerprint).
- **Git policy.** `.itervox/agents/**` is committable — `itervox init` patches the
  root `.gitignore` so the files are not swept under a broad `.itervox/` ignore.
  Conversely, `.itervox/HEARTBEAT.md`, `.itervox/.env`, runtime queue files, and
  daemon logs MUST remain gitignored as transient runtime state.
- **Migration entry point** is `itervox init --update --workflow WORKFLOW.md`.
  It writes a `WORKFLOW.md.bak` next to the workflow, extracts each profile's
  inline `prompt:` into `INSTRUCTIONS.md`, generates a starter `SOUL.md`, patches
  the root `.gitignore`, and stamps `itervox_schema_version: 2`.
- **`.itervox/HEARTBEAT.md`** is the daemon liveness file. Written on startup by
  `cmd/itervox/heartbeat.go` and refreshed after state changes at a bounded
  interval (15s by default). Atomic write via `atomicfs.WriteFile`. The dashboard
  URL, schema version, workflow path, capacity, queue pressure, dependency audit
  summary, input-required count, and last notable error live here. Never commit.
- **`.itervox/handoff/<ISO8601>_<role>.md` is the agent pipeline handoff dir.**
  Each worker run writes a Markdown deliverable at this path on the issue's
  worktree branch; the orchestrator pre-renders all prior handoffs (sorted
  chronologically by filename prefix, budget-truncated oldest-first) into every
  subsequent worker's prompt as a `## Prior Agent Handoffs` block. Liquid bindings
  `{{ run.timestamp }}` and `{{ run.handoff_path }}` expose the current run's
  metadata to the agent. The directory is committable via a `.gitignore`
  carve-out (`!.itervox/handoff/`, `!.itervox/handoff/**`, added alongside the
  agents carve-out by `itervox init` and `itervox init --update`). Non-success
  worker exits (`TerminalFailed`, `TerminalStalled`) rename the in-flight file
  to `<basename>.partial.md` so partials are visible to the next agent without
  being mistaken for completed work; `TerminalInputRequired` does not rename
  (the agent will resume).
- **`.itervox/issues/*.md` is the local tracker's data (#85)** — committable
  like `agents/` (`itervox init` adds the `!.itervox/issues/` carve-out). The
  directory is the source of truth: `internal/tracker/local` rescans it on
  every call, serves a malformed file's last good version and never writes
  to it until it parses.

When editing migration or schema-validation code, expect tests under
`cmd/itervox/init_migrate.go`/`init.go` and `internal/config/validate*.go` to
gate the change.

---

## Package dependency order (no circular deps)

```
domain ─────┬── tracker (interface + adapters: linear, github, local, memory;
            │            tracker/store is the local store, tracker/webhook the
            │            signed Linear/GitHub webhook receiver that refreshes
            │            it by issue — imports metrics, not store; #131)
            │     ├── tracker/store (local tracker store, #113: wraps a Linear
            │     │   or GitHub adapter; imports tracker, domain, atomicfs)
            │     └── tracker/localsync (#118: two-way sync of kind: local
            │         files with Linear or GitHub, off the event loop;
            │         imports tracker, tracker/local, outbox, atomicfs)
            ├── prompt (Liquid template rendering)
            ├── logbuffer (per-issue ring buffer)
            └── prdetector (PR URL detection)

extsessions (stdlib + logging — discovery, transcripts and read-only
            liveness of agent sessions started by hand, #178; imported by
            orchestrator and cmd/itervox)

tmuxctl (stdlib only — the tmux CLI: list/capture panes, paste text, an
         allowlist of keys, new/kill session; the ONLY package that writes to
         processes itervox did not start; imported by cmd/itervox)

procgroup (stdlib only — own process group + group SIGKILL re-sent until
           ESRCH; the one shared kill path for agent runners and hooks)

metrics (stdlib only — process-wide counters + hand-written Prometheus text
         exposition; a leaf imported by tracker, orchestrator, server,
         logbuffer and depsanalysis. It imports NO internal package: the
         View is injected by cmd/itervox from Snapshot(), never cfgMu)

logging (stdlib only — RedactingHandler / RedactString; imported by agent,
         orchestrator and cmd/itervox)

memory (managed memory files, selection and the `## Memory` block — imports
        atomicfs, config, logging; imported by orchestrator, server and
        cmd/itervox)

gitexec (stdlib only — the ONLY way to run `git`: scrubs GIT_DIR/GIT_WORK_TREE
         & co. and requires a dir; audited by TestNoRawGitExecOutsideGitexec)

workflow ──── config ──── workspace (hooks — also imports procgroup)

rules (layered JSONC rules, #157 — imports config only; used by orchestrator,
       server and cmd/itervox)

agent (claude/codex subprocess runners — imports domain, config, procgroup,
       logging)

orchestrator (single-goroutine state machine — imports agent, agentactions,
              atomicfs, config, depsanalysis, domain, extsessions, logbuffer, logging,
              memory, metrics, outbox, prdetector, procgroup, prompt, rules,
              tracker, workspace)

prwatch (GitHub PR events for managed PRs: webhook handler + conditional-
         request poller — imports metrics, tracker; used by cmd/itervox)

app (EnrichIssue business logic — imports domain, tracker)

server (HTTP API — imports domain, config, memory, rules)

statusui (Bubbletea TUI — imports domain)

templates (WORKFLOW.md scaffolding)

cmd/itervox (wires everything)
```

---

## Frontend architecture

- **Vite + React 19 + TypeScript + TailwindCSS**
- **State**: Zustand (`itervoxStore` for snapshot, `toastStore` for notifications, `uiStore` for view mode/filters, `tokenStore`/`authStore` for auth)
- **Server state**: TanStack Query (issues, logs — `staleTime: 10_000`)
- **Real-time**: SSE via `@microsoft/fetch-event-source` (NOT native `EventSource`) — needed so the connection can carry an `Authorization: Bearer` header. Single seam is `web/src/auth/authedEventStream.ts`, consumed by `useItervoxSSE`, `useLogStream`, and the per-issue log-stream in `queries/logs.ts`.
- **Auth**: bearer-token middleware gated by `ITERVOX_API_TOKEN`. Auto-generated ephemeral token on **every** bind — including loopback — unless `server.allow_unauthenticated: true` (renamed from `server.allow_unauthenticated_lan`, which still parses as a deprecated alias). All frontend HTTP goes through `authedFetch` in `web/src/auth/authedFetch.ts` — NEVER call `fetch()` or `new EventSource()` directly.
- **Routing**: React Router v7 (file-based lazy pages)
- **DnD**: dnd-kit (`PointerSensor` + `KeyboardSensor` registered on all boards)
- **Schema validation**: Zod at SSE parse boundary and query results

### Key files

| File | Purpose |
|---|---|
| `web/src/store/itervoxStore.ts` | SSE snapshot, `patchSnapshot`, `refreshSnapshot` |
| `web/src/store/toastStore.ts` | Toast queue with auto-dismiss timers |
| `web/src/queries/issues.ts` | All issue mutations with optimistic updates + rollback |
| `web/src/queries/logs.ts` | Log fetch + sublog queries |
| `web/src/queries/projects.ts` | Project list query |
| `web/src/hooks/useItervoxSSE.ts` | SSE connection with exponential backoff |
| `web/src/hooks/useSettingsActions.ts` | Settings mutations — PUT/POST/DELETE with toast error surface |
| `web/src/store/uiStore.ts` | View mode, search, filters, accordion expansion |
| `web/src/types/schemas.ts` | Canonical Zod schemas (source of truth) |
| `web/src/pages/Dashboard/components/LiveOpsStrip.tsx` | Dashboard status strip over live capacity, queue pressure, dependency audit, retry/input, SSH, and automation activity |
| `web/src/pages/Dashboard/components/AutomationQueueList.tsx` | Durable automation queue list and local search surface |
| `web/src/pages/Dashboard/components/AutomationQueueDetailPanel.tsx` | Queue entry detail panel for trigger/profile/permission/dependency context |
| `web/src/pages/Dashboard/components/DepsGraph.tsx` | Read-only React Flow dependency graph over blocker -> blocked issue rows |
| `web/src/pages/Sessions/index.tsx` | Sessions panel (#178): one tab per agent session (input-required first, sessions started by hand last and read-only), transcript from the issue log stream or the CLI transcript (`SessionTranscript.tsx`), composer (provide-input reply or `POST /api/v1/sessions/{id}/message`); tmux: live screen + keys (`TmuxSessionView.tsx`), New session (`NewSessionForm.tsx`), tmux-missing banner/composer warning (`TmuxNotice.tsx`) |
| `web/src/components/itervox/IssueStatusChanges.tsx` | Per-issue status-change timeline rendered inside issue detail |
| `web/src/components/itervox/QueueSearchInput.tsx` | Shared local search input for queue-like dashboard panels |
| `web/src/pages/Rules/index.tsx` | Rules editor (#157): a tab per rules layer, form and JSON views, effective-rules preview; queries in `web/src/queries/rules.ts` |
| `web/src/utils/timings.ts` | Shared timing constants (TOAST_DISMISS_MS, SSE_RECONNECT_BASE_MS, …) |
| `web/src/auth/authedFetch.ts` | `fetch()` wrapper — injects `Authorization: Bearer`, throws `UnauthorizedError` on 401 |
| `web/src/auth/authedEventStream.ts` | SSE wrapper over `@microsoft/fetch-event-source` — same header injection, exponential backoff, 401 → `FatalSSEError` |
| `web/src/auth/tokenStore.ts` | Token storage (sessionStorage default, localStorage opt-in via "Remember"), cross-tab `storage` event sync |
| `web/src/auth/authStore.ts` | Auth state machine: `unknown` / `serverDown` / `needsToken` / `authorized` |
| `web/src/auth/AuthGate.tsx` | Root wrapper — captures `?token=` from URL once, probes `/api/v1/health` then `/api/v1/state`, routes to app / login / error screen |
| `web/src/auth/UnauthorizedError.ts` | Typed error used by TanStack Query retry guards to skip retrying auth failures |

### Toast API

```ts
// Correct — addToast(message: string, variant?: 'error'|'success'|'info')
useToastStore.getState().addToast('Something failed', 'error');

// WRONG — do not pass an object; TypeScript accepts it but displays [object Object]
useToastStore.getState().addToast({ message: 'x', type: 'error' }); // ❌
```

---

## Known dead code (do not flag as bugs)

*Nothing is currently gated here.*

**Reviewer fan-out was ungated in #58.** `ReviewerProfileChain` previously
truncated its result to one entry, which made `AdvanceReviewChain`,
`ReadReviewVerdict`, `reviewVerdictRelPath`, `State.ReviewChainIndex`,
`State.ReviewOutcomes`, and `agent.review_quorum` statically unreachable. The
truncation is gone and every one of those now has a live read site, so they must
NOT be treated as dead code.

The two lifecycle defects that justified the gate are fixed and pinned:

1. **The chain never advanced.** The `TerminalSucceeded` handler identified a
   reviewer only via `state.Running`, which `ReconcileTrackerStates` deletes
   first when `tracker.completion_state` moves the issue terminal. It now
   reads the finishing run's own record (`State.LatestRun`, #127), which
   reconciliation does not delete.
2. **The workspace was cleared mid-chain.** The auto-clear decision keyed off
   `runStartsAReview` ("would a FRESH review start?"), which is false
   for a reviewer's own exit. It now also consults `reviewChainInFlight`, so the
   worktree survives until the quorum closes.

Both are covered by `TestMultiReviewerFanOutRunsEveryReviewerWithoutLooping`,
which runs the exact configuration that reproduced them and asserts one worker
plus both reviewers (no re-dispatch loop) and exactly one workspace clear.
`TestReplacedReviewerAdvancesTheChainOnce` pins the record-based identity: a
reviewer whose Running entry reconcile deleted still advances the chain, and a
late copy of its exit after the next reviewer replaced it does not.

---

## Gap analysis — avoiding false positives

When running static analysis or spawning gap-analysis subagents, enforce these
verification steps before flagging any issue:

### Data-race claims

Before claiming a field is accessed without a lock:

1. **Identify all write sites** — grep for every assignment to that field across the entire codebase.
2. **Check if a runtime setter exists** — for `cfg.*` fields, check if an HTTP handler calls a setter. If no setter exists, there is no concurrent writer and no race.
3. **Verify the field is in the `cfgMu` guard list** — only the fields listed in the cfgMu section above need locking. Other fields are read-only after startup.

### Context/timeout claims

Before claiming `context.WithTimeout(ctx, 0)` causes immediate cancellation:

1. **Check the field-specific parser** — timeout fields are mixed:
   - `agent.turn_timeout_ms`: `intField`; `<= 0` reaches runtime and intentionally disables the hard turn timeout because agent runners call `context.WithTimeout` only when the value is `> 0`.
   - `agent.read_timeout_ms`: `positiveIntField`; `<= 0` is replaced with the 30s default and cannot reach the read-deadline path from config.
   - `agent.stall_timeout_ms`: `intField`; `<= 0` reaches runtime and disables `ReconcileStalls`.
   - `hooks.timeout_ms`: `intField` followed by an explicit fallback; `<= 0` becomes the 60s hook default during config load.
2. **Check what the config default is** — look at `config.go:defaultConfig()` / `LoadFromFrontMatter` before flagging timeout behavior.

### File-existence claims

Before claiming a file has a bug:

1. **Verify the file exists** — `ls web/src/pages/<PageName>/` before assuming a lazy-imported route has no implementation.
2. **Check all lazy imports** — `App.tsx` has lazy imports for routes; confirm file existence with `ls` before flagging.

### "Already snapshotted" parameter claims

Before claiming a function uses live config instead of a snapshot:

1. **Read the function body, not just the signature** — a parameter named `cfg *config.Config` may exist but the function may still use `state.MaxConcurrentAgents` (the snapshot) internally. Check what variable is actually read at the decision point.

### Accessibility / already-fixed claims

Before claiming a component is missing an accessibility attribute:

1. **Read the actual file** — do not assume based on a description. Check the rendered JSX.

---

## Conventions

### Go

- `go test -race ./cmd/... ./internal/...` must pass — use `-race` in all test commands. The explicit package list is intentional so Go tooling does not walk `web/node_modules` after `pnpm install`.
- Package-level errors use `fmt.Errorf("package: ...")` with lowercase messages
- `errors.New` for static strings; `fmt.Errorf` with `%w` for wrapping
- `maps.Copy` (Go 1.21+) for map duplication — not manual `for k, v := range` loops
- `max(a, b)` / `min(a, b)` built-ins (Go 1.21+) — not if-chains for clamping
- `slog` for structured logging — not `log.Printf`
- Test files in `*_test.go` — always same package for whitebox tests

### TypeScript / React

- Components in `web/src/components/itervox/` — reusable
- Pages in `web/src/pages/<Name>/index.tsx` — route-level
- Mutation hooks in `web/src/queries/issues.ts` with optimistic updates + rollback
- Always use `useToastStore.getState()` inside effects/callbacks (not hook calls)
- `EMPTY_*` stable references as module-level constants — not `useMemo(() => [], [])` for empty arrays that depend on the full snapshot
- Keys on list items: use composite semantic key, not array index

---

## Verification before completion — MANDATORY

> **Canonical source for every agent that touches this repo, codex included.**
> AGENTS.md is a thin pointer that directs codex to read CLAUDE.md first; this
> section does not need to be mirrored anywhere. If you edit it, that edit is
> immediately authoritative for both Claude Code and codex.

You MUST NOT mark anything as "done", "shipped", "fixed", "ready",
"addressed", "implemented", "covered", or "resolved" from inference.
**Inference is forbidden as evidence**, including:

- Recognising related code, infrastructure, function names, or patterns
- Reading a passing top-level test suite (`make verify`, `go test ./...`)
- Recalling that you (or another agent) wrote the code earlier in the session
- Trusting another agent's, tool's, or user's claim of completion
- "I see the code that should do it"
- "The feature appears complete"
- "Mostly there"

These are CLAIMS. A claim is not verification. Treat every claim as
UNMET until proven by one of the evidence forms below.

### Evidence forms that count

Each form requires a runnable command whose output you can quote. "I
checked" without a quoted command and its output is not evidence.

1. **Named-test execution.** Run the exact test name from the
   acceptance criterion:
   ```bash
   go test -race -run '^TestFooBar$' ./internal/orchestrator/... -v
   ```
   The output MUST contain `--- PASS: TestFooBar`. If the output is
   `ok ... [no tests to run]`, the test does not exist — the criterion
   is UNMET regardless of how complete the surrounding code looks. A
   substring-match run (`-run TestFoo`) does NOT prove the named test
   exists; always anchor with `^...$`.

2. **Symbol-presence grep.** For acceptance criteria that name a
   specific counter, field, function, route, or constant:
   ```bash
   grep -rn 'automation_drops_self_reentry_total\b' internal/
   grep -rn 'PauseDispatchWhenAnyInState\b' internal/config/
   ```
   Zero hits = UNMET. A "similar" symbol is not the named one — do not
   substitute. Symbol presence proves declaration only; for behaviour
   criteria, also grep the call site / read site / increment site.

3. **Runtime output.** Run the actual command/action and quote the
   relevant output line. For e.g. an `itervox doctor` acceptance, run
   it and quote the line in the output that satisfies the criterion.

4. **Endpoint / file shape.** For HTTP routes, hit the endpoint
   (`curl ... | jq`) and quote the response shape. For files, read the
   path and quote the lines that match the criterion.

### Evidence forms explicitly REJECTED

- "I checked" without a quoted command and its output
- "All tests pass" when the acceptance names a specific test
- "The code is there" without citing file:line that matches the
  criterion verbatim
- "The user said it works" — treat as claim, verify independently
- A green `make verify` — proves "nothing regressed", not "the named
  criterion is satisfied"
- Memory of having implemented it — implementations can drift; the
  current tree is the only source of truth
- Presence of a sibling / related symbol — not the named one

### Mandatory completion annotation

For every item you mark done, write a `Verified by:` annotation
immediately below it, with the exact command and a one-line excerpt of
the relevant output:

```
- [x] Add automation_drops_self_reentry_total counter
  Verified by: grep -rn 'automation_drops_self_reentry_total\b' internal/
              → 4 hits: state.go:312, snapshot.go:88, dispatch.go:156, automation_test.go:412
              go test -run '^TestSelfReentryCounter$' ./internal/orchestrator/... -v
              → --- PASS: TestSelfReentryCounter (0.02s)
```

If you cannot produce a `Verified by:` annotation for an item, the item
is not done. Two possible reasons:

1. The acceptance is too vague to verify — escalate to clarify what
   would count as evidence before marking anything.
2. The work isn't actually finished — leave it open.

### Sampling — strict

For lists of 5+ items, sampling is permitted ONLY under all of these
conditions:

- The sample is the highest-risk subset (justify the choice in writing).
- The sample size is recorded (e.g. "sampled 3 of 9: items 4, 8, 9").
- **Unsampled items remain explicitly "claimed, unverified" — never
  silently promoted.**
- A 100% pass rate on a 30% sample does NOT promote the other 70%.
  They stay unverified until individually checked.

### "Looks done but isn't" — common evasions to refuse

- **Symbol exists, behaviour missing.** Function with the right name
  that no live caller reaches = UNMET. Grep the call site.
- **One sub-criterion of N.** An acceptance with multiple bullets is
  done when ALL bullets are verified, not when one is.
- **Test exists, body is a skip.** `t.Skip("TODO")` does NOT count as
  a passing test. Read the test body before quoting the PASS line.
- **Counter declared, never incremented.** `var fooCounter int` with
  no `fooCounter++` site is UNMET. Grep the increment.
- **Config field declared, never read.** Struct field with no read
  site is UNMET. Grep the read.
- **UI component imported, never rendered.** Presence in an imports
  block does not equal "visible to the operator". Grep the JSX usage.
- **Server route registered, handler is a 501.** Verify the handler's
  success path actually performs the work.
- **Logged-but-not-acted-on event.** An event written to a log file is
  not the same as an event consumed by a downstream system.

### When the user (or another agent) says "X is done"

Treat as a claim, not a fact. Run the acceptance check. If it passes,
confirm with the evidence. If it fails, surface the specific gap with
the exact command + output that proves the gap (don't just say "I
disagree"). The user is debugging the same uncertainty; corroborating
evidence is what they need.

### Failure mode

If something is marked done without a `Verified by:` annotation and is
later discovered unmet, the correct response is: revert the
done-marking, reopen the criterion, and write what was assumed vs. what
was true. This is the contract — it preserves the signal that an OPEN
item is genuinely open and a DONE item is genuinely done. A done-list
contaminated by inferred-completions is worse than no done-list at all,
because it silently consumes attention that should have caught the gap.

This rule is operationalised by the `verify-before-done` skill —
invoke it on every "are we done / is this ready / can I merge"
question. The skill enforces the same evidence rules; this section is
the canonical text.

---

## Never do

- **Do not commit** 
- **Do not add `.env` files** — secrets are injected at runtime via env vars (`.itervox/.env` is gitignored and loaded by the daemon on startup)
- **Do not mock `orchestrator.State`** in tests that check state transitions — pass real State values
- **Do not call `patchSnapshot` from settings mutations** — they must call `refreshSnapshot()` to get the authoritative server state
- **Do not call `fetch()` or `new EventSource()` directly in `web/src`** — use `authedFetch` from `web/src/auth/authedFetch.ts` and `openAuthedEventStream` from `web/src/auth/authedEventStream.ts`. The only exceptions are inside the auth module itself (`AuthGate` health/state probes and `TokenEntryScreen` token validation), which bootstrap before a token is stored.
- **Do not call `os.Exit()` directly in `cmd/itervox/`** — use `fatalExit(code)` from `cmd/itervox/exit.go`. It restores the terminal to a sane mode (`stty sane`) before exiting, which is required for any code path that may run after `go statusui.Run` puts the terminal into raw mode. The CI guard `make no-os-exit` (run by `make verify`) fails the build if a new `os.Exit()` appears outside `exit.go`. Tests and the `exit.go` file itself are the only exceptions.
- **Do not declare anything done by inference.** See "Verification before completion — MANDATORY" above. Every named test, counter, field, CLI subcommand, route, and UI surface in an acceptance section MUST be grepped or run, and the result MUST appear in a `Verified by:` annotation. Marking an item done without that annotation is a contract violation, not a style preference. The common evasions enumerated above (symbol-exists-but-callless, one-sub-criterion-of-N, test-body-is-skip, counter-declared-never-incremented, component-imported-never-rendered, route-registered-handler-is-501) are explicitly refused.
- **Do not move agent guidance into AGENTS.md if it is not codex-specific.** AGENTS.md is a thin pointer at CLAUDE.md plus a small "Codex-specific notes" section. Architecture invariants, the `cfgMu` allowlist, verification rules, conventions, false-positive patterns, the Never-do list — all of those live ONLY in CLAUDE.md and AGENTS.md tells codex to read them there. If you find yourself duplicating a CLAUDE.md section into AGENTS.md, stop: either the content belongs in CLAUDE.md (where it already is) or it's genuinely codex-specific (in which case it shouldn't be in CLAUDE.md). Picking only one home is the contract.
