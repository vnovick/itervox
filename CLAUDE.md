# CLAUDE.md — itervox

## What this project is

**Itervox** is a long-running daemon (Go 1.25.9) that implements the
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

### `cfgMu` guards exactly these fields (and nothing else)

These `Orchestrator.cfg` fields can be mutated at runtime by HTTP handler goroutines
and must always be accessed under `cfgMu`:

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

The canonical allowlist is `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields`;
keep this section in sync with that test. All **other** `cfg` fields are read-only after startup — no lock needed for them.

### Snapshot access

`Snapshot()` acquires `snapMu.RLock` and returns a copy of `lastSnap`.
HTTP handlers call `Snapshot()` — they must never hold `cfgMu` while doing so.

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
- **Auth**: bearer-token middleware gated by `ITERVOX_API_TOKEN`. Auto-generated ephemeral token on non-loopback bind unless `server.allow_unauthenticated_lan: true`. All frontend HTTP goes through `authedFetch` in `web/src/auth/authedFetch.ts` — NEVER call `fetch()` or `new EventSource()` directly.
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
| `web/src/utils/timings.ts` | Shared timing constants (TOAST_DISMISS_MS, SSE_RECONNECT_BASE_MS, …) |
| `web/src/auth/authedFetch.ts` | `fetch()` wrapper — injects `Authorization: Bearer`, throws `UnauthorizedError` on 401 |
| `web/src/auth/authedEventStream.ts` | SSE wrapper over `@microsoft/fetch-event-source` — same header injection, exponential backoff, 401 → `FatalSSEError` |
| `web/src/auth/tokenStore.ts` | Token storage (sessionStorage default, localStorage opt-in via "Remember"), cross-tab `storage` event sync |
| `web/src/auth/authStore.ts` | Auth state machine: `unknown` / `serverDown` / `needsToken` / `authorized` |
| `web/src/auth/AuthGate.tsx` | Root wrapper — captures `?token=` from URL once, probes `/health` then `/state`, routes to app / login / error screen |
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

1. **Trace through `positiveIntField`** — `config.go` uses this helper to parse timeout fields. It rejects 0 and negative values, replacing them with the default. A zero value cannot reach `context.WithTimeout` at runtime.
2. **Check what the config default is** — look at `config.go:defaultConfig()`.

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

## Never do

- **Do not commit** 
- **Do not add `.env` files** — secrets are injected at runtime via env vars (`.itervox/.env` is gitignored and loaded by the daemon on startup)
- **Do not mock `orchestrator.State`** in tests that check state transitions — pass real State values
- **Do not call `patchSnapshot` from settings mutations** — they must call `refreshSnapshot()` to get the authoritative server state
- **Do not call `fetch()` or `new EventSource()` directly in `web/src`** — use `authedFetch` from `web/src/auth/authedFetch.ts` and `openAuthedEventStream` from `web/src/auth/authedEventStream.ts`. The only exceptions are inside the auth module itself (`AuthGate` health/state probes and `TokenEntryScreen` token validation), which bootstrap before a token is stored.
- **Do not call `os.Exit()` directly in `cmd/itervox/`** — use `fatalExit(code)` from `cmd/itervox/exit.go`. It restores the terminal to a sane mode (`stty sane`) before exiting, which is required for any code path that may run after `go statusui.Run` puts the terminal into raw mode. The CI guard `make no-os-exit` (run by `make verify`) fails the build if a new `os.Exit()` appears outside `exit.go`. Tests and the `exit.go` file itself are the only exceptions.
