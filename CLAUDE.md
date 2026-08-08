# CLAUDE.md — itervox

## What this project is

**Itervox** is a long-running daemon (Go 1.25.11) that implements the
[OpenAI Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md).
It polls Linear or GitHub Issues, spawns Claude Code or Codex agents per issue, and
provides a live Kanban web dashboard (React/Vite) and a Bubbletea terminal UI.

Config lives entirely in one `WORKFLOW.md` file per project (YAML front matter +
Liquid prompt template). The binary is a single static Go executable.

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

### `orchestrator.State` is a value type

`State` is a plain struct passed by value into reconcile/dispatch functions.
No mutex is needed for `State` fields — only the event loop writes them.

### Queue, dependency audit, and status ledgers are event-loop state

`AutomationQueue`, `AutomationQueueOrder`, `AutomationQueueBackpressure`,
`DependencyAudit`, and `IssueStatusHistory` are normal `orchestrator.State`
fields. They follow the same rule as `Running`, `RetryQueue`, and other state:
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
- **Prompt assembly order** at dispatch time: SOUL.md → INSTRUCTIONS.md → the
  per-issue Liquid prompt template. Concatenated and passed to the agent
  subprocess via stdin/argv depending on backend. SOUL is the compact identity
  ("who you are"); INSTRUCTIONS is the operational/behavioural rules.
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

When editing migration or schema-validation code, expect tests under
`cmd/itervox/init_migrate.go`/`init.go` and `internal/config/validate*.go` to
gate the change.

---

## Package dependency order (no circular deps)

```
domain ─────┬── tracker (interface + adapters: linear, github, memory)
            ├── prompt (Liquid template rendering)
            ├── logbuffer (per-issue ring buffer)
            └── prdetector (PR URL detection)

workflow ──── config ──── workspace

agent (claude/codex subprocess runners — imports domain, config)

orchestrator (single-goroutine state machine — imports agent, config, domain,
              logbuffer, prdetector, prompt, tracker, workspace)

app (EnrichIssue business logic — imports domain, tracker)

server (HTTP API — imports domain, config)

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
| `web/src/components/itervox/IssueStatusChanges.tsx` | Per-issue status-change timeline rendered inside issue detail |
| `web/src/components/itervox/QueueSearchInput.tsx` | Shared local search input for queue-like dashboard panels |
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

*No known dead code at this time.*

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
