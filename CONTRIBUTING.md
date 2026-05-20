# Contributing to Itervox

Thank you for your interest in contributing. This document covers how to get the project running locally, how the codebase is structured, and the conventions used throughout.

---

## Development Setup

### Prerequisites

- Go 1.25.10 (matches `go.mod`; the `Makefile` pins `GOTOOLCHAIN=go1.25.10`)
- Node.js 20+ and `pnpm` (for the web dashboard)
- `git`
- [Lefthook](https://github.com/evilmartians/lefthook) (`brew install lefthook` or `go install github.com/evilmartians/lefthook@latest`)
- One of the following agent CLIs (only needed for end-to-end manual testing — unit tests do not shell out):
  - [Claude Code CLI](https://claude.ai/code), or
  - [Codex CLI](https://github.com/openai/codex) — Itervox supports both backends and selects one per profile in `WORKFLOW.md`

### Clone and build

```bash
git clone https://github.com/vnovick/itervox
cd itervox
lefthook install        # wires pre-commit and pre-push hooks
make build              # builds web/dist then compiles the Go binary
go test -race ./cmd/... ./internal/...
```

> **Always use `make build`** rather than `go build ./cmd/itervox` directly. The binary embeds `internal/server/web/dist` via `//go:embed`; if it is missing, the binary compiles but panics at runtime.

All tests pass without external dependencies. Tests that hit real APIs are gated behind a build tag (see [Integration Tests](#integration-tests) below).

### Frontend setup (web dashboard)

The web dashboard is a Vite app that proxies API calls to a running `itervox` daemon. **You must have the daemon running first** — Vite proxies `/api/*` to `http://127.0.0.1:8090` (see `web/vite.config.ts`).

**Terminal 1 — build and run the Go binary from a project that has a `WORKFLOW.md`:**

```bash
# In the itervox repo — build the binary with embedded web assets
make build

# In your project repo (must contain a WORKFLOW.md)
/path/to/itervox/itervox   # picks up WORKFLOW.md in the current directory
```

If you don't have a project lying around, scaffold a throwaway one:

```bash
mkdir /tmp/itervox-test && cd /tmp/itervox-test && git init -q
/path/to/itervox/itervox init --tracker linear --runner claude --force
/path/to/itervox/itervox    # daemon now serves localhost:8090
```

Make sure `server.port` is set to `8090` in that project's `WORKFLOW.md` (this matches the Vite proxy target):

```yaml
server:
  port: 8090
```

**Terminal 2 — start the Vite dev server (in this repo):**

```bash
cd web
pnpm install --frozen-lockfile
pnpm dev     # HMR at http://localhost:5173, proxies /api/* to 127.0.0.1:8090
```

> **First-time pnpm note:** pnpm requires explicit approval for packages that run install scripts. If `pnpm install` prompts you, run `pnpm approve-builds` once per machine.

`make dev` is a shortcut for the Vite step only — the Go binary must be running separately from a project directory.

> **Vite proxy port is hard-coded.** `web/vite.config.ts` proxies `/api/*` to `http://127.0.0.1:8090`. If your project's `WORKFLOW.md` sets `server.port` to anything other than `8090`, the dev proxy will silently fail with 502 errors. For dev work, keep `server.port: 8090`.

> **Build output path.** `pnpm build` writes to `internal/server/web/dist` (NOT `web/dist`) so the Go binary can `//go:embed` it. Running `git clean web/` will **not** clear the embed.

### Make commands

| Command | Description |
|---|---|
| `make all` | `build` + `verify` — full build and check suite |
| `make build` | Build web dashboard (`web-build`) then compile Go binary |
| `make verify` | `fmt` + `vet` + `lint-go` + `test` + `web-coverage` + `web-build` + `web-spelling` + `size-budget` + `no-os-exit` — mirrors CI |
| `make release-check` | Release preflight after committing release changes: `verify` + `govulncheck` + `goreleaser check` + GoReleaser hook dirty-worktree guard |
| `make dev` | Start the Vite dev server with HMR (run the daemon separately) |
| `make test` | `go test -race ./cmd/... ./internal/... -count=1` |
| `make coverage` | Run tests with coverage over `./cmd/... ./internal/...`; output `coverage.html` |
| `make benchmark` | `go test -bench=. -benchmem ./cmd/... ./internal/...` |
| `make tui-golden` | Regenerate catwalk golden files after intentional TUI render changes |
| `make size-budget` | Enforce LOC caps on the intentionally size-budgeted files |
| `make no-os-exit` | Guard against new `os.Exit()` calls outside `cmd/itervox/exit.go` |
| `make fmt` | `gofmt -l -w cmd internal` |
| `make vet` | `go vet ./cmd/... ./internal/...` |
| `make lint-go` | `golangci-lint run ./cmd/... ./internal/...` |
| `make web-build` | `pnpm build` in `web/` |
| `make web-test` | `pnpm test` in `web/` |
| `make web-coverage` | `pnpm test:coverage` in `web/` |
| `make web-spelling` | Guard against the legacy `Symphony` brand name leaking into user-visible TS/TSX strings |
| `make e2e` | Build the binary, then run the real-daemon Playwright smoke suite |
| `make qa-current-ui` | Run the route-mocked Playwright regression lane for the current UI |
| `make qa-daemon` | Alias for the real-daemon Playwright lane |
| `make qa-current` | `verify` + `qa-current-ui` + `qa-daemon` — full current-functionality baseline |
| `make clean` | Remove `itervox` binary and coverage files |

> **Note:** `make web-spelling` (also part of `make verify`) rejects any TypeScript/TSX string literal containing `Symphony` (the legacy project name). If your editor autocompletes the old name, the rule will fail your `pre-push` hook — search and replace before pushing.

> **QA baseline:** `make verify` remains the zero-extra-dependency contributor gate and includes the frontend coverage threshold used by Web CI. The browser QA lanes (`make qa-current-ui`, `make qa-daemon`, `make qa-current`) require a one-time Playwright browser install: `cd web && pnpm exec playwright install chromium`.

> **Release preflight:** `make release-check` is intentionally stricter than `make verify`. It requires `govulncheck` and GoReleaser to be installed, reruns the release hooks (`go mod tidy` and web build), then fails if tracked files or the index are dirty. Use it after the release-prep diff is committed, not as the everyday edit loop.

> **Go package scope:** repo-owned Go workflows intentionally use `./cmd/... ./internal/...`. A raw `go test ./...` can traverse generated Go fixtures under `web/node_modules` after `pnpm install`, so use `make test`, `make coverage`, or the explicit package list above.

---

## Project Structure

```
itervox/
├── cmd/itervox/          # CLI entry point — wires all packages together
│   └── main.go
├── internal/
│   ├── agent/            # claude/codex subprocess runners, stream-json protocol, sentinels
│   │   └── agenttest/    # FakeRunner test double (see Test doubles)
│   ├── app/              # EnrichIssue and other tracker-aware business logic
│   ├── config/           # Typed config, defaults, $VAR resolution, validation
│   ├── domain/           # Shared types: Issue, BlockerRef, etc.
│   ├── logbuffer/        # Per-issue ring buffer for live log streaming
│   ├── logging/          # slog setup and per-run log isolation
│   ├── orchestrator/     # Single-goroutine state machine, retry queue, reconciliation
│   ├── prdetector/       # Detects PR URLs in agent output
│   ├── prompt/           # Liquid template rendering
│   ├── server/           # HTTP API, SSE, embedded web/dist (chi router)
│   ├── statusui/         # Bubbletea terminal dashboard
│   ├── templates/        # WORKFLOW.md scaffolding + bundled prompt templates (e.g. human_input.md)
│   ├── tracker/          # Tracker interface + adapters
│   │   ├── tracker.go    # Tracker interface
│   │   ├── memory.go     # In-memory test adapter (MemoryTracker)
│   │   ├── linear/       # Linear GraphQL adapter
│   │   └── github/       # GitHub REST adapter
│   ├── workflow/         # WORKFLOW.md parser + content-hash file watcher
│   └── workspace/        # Per-issue workspace dirs, path safety, lifecycle hooks
├── web/                  # Vite + React 19 + TS dashboard
├── testdata/workflows/   # WORKFLOW.md fixtures used by config/workflow tests
└── docs/                 # Design spec, ADRs, and reference docs
```

### Protocol specification

The agent communication protocol is inspired by the [Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md). Refer to it when adding new agent backends or modifying the event stream format.

### Package dependency order

Packages only import packages above them in this list — there are no circular dependencies. This is the canonical ordering (mirrors `CLAUDE.md`):

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

templates (WORKFLOW.md scaffolding + bundled prompts)

cmd/itervox (wires everything)
```

---

## Architecture

### Concurrency model

The orchestrator is a **single-goroutine state machine** (see ADR 001). All state mutations go through one `select` loop in `Run()` — no mutexes are needed on the orchestrator's `State` struct.

Worker goroutines (one per running issue) send results back via a buffered `chan OrchestratorEvent`. The orchestrator processes these events in its main loop. **Never mutate orchestrator state from a worker goroutine.** Only send events.

```
                  ┌──────────────────────────────────┐
                  │        Orchestrator loop          │
                  │                                   │
  tick timer ───► │  select {                         │
  worker events ─►│    case <-tick.C:                 │
  ctx.Done ──────►│    case ev := <-o.events:         │
                  │    case <-ctx.Done():             │
                  │  }                                │
                  └──────────────────────────────────┘
                            ▲              │
                 OrchestratorEvent        spawn goroutine
                            │              ▼
                  ┌────────────────────────────────┐
                  │       Worker goroutine          │
                  │  (one per running issue)        │
                  │  runs claude/codex subprocess   │
                  │  sends EventWorkerExited        │
                  └────────────────────────────────┘
```

A small `cfgMu` mutex guards exactly the runtime-mutable subset of `Orchestrator.cfg`: agent concurrency/retry/rate-limit knobs, profiles, SSH hosts, dispatch strategy, reviewer settings, inline-input flag, tracker active/terminal/completion/failed states, automations, and `auto_clear_workspace`. Every other `cfg` field is read-only after startup. The authoritative allowlist is `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields` and is mirrored in `CLAUDE.md`.

### State is a value type

`orchestrator.State` is a plain struct, not a pointer. Reconcile and dispatch functions take a `State` and return a new `State`. This makes data flow explicit and tests trivial — pass in a state, assert on the state out. Do not mock `State` in tests; build a real value.

### Config reload

`workflow.Watch` polls `WORKFLOW.md` every 1 second using a content-hash stamp (mtime+size fast-path, SHA-256 fallback). When the file changes, `cmd/itervox/main.go` cancels the current run context, gracefully shuts down the orchestrator and HTTP server, then restarts with fresh config. In-flight agent sessions are not interrupted — they exit naturally and their results are discarded.

### Input-required sentinel contract

When an agent needs human clarification, it emits a sentinel token in its output. Itervox detects this and parks the issue in the `needs-input` state until a human responds via the dashboard.

- The literal token is defined in `internal/agent/events.go`:

  ```go
  const InputRequiredSentinel = "<!-- itervox:needs-input -->"
  ```

- Detection lives in `agent.IsSentinelInputRequired(text string) bool` (substring match, whitespace-tolerant). This is the function `agent.FinalizeResult` calls in `internal/agent/runner.go`.
- The bundled prompt template at `internal/templates/human_input.md` instructs agents how and when to emit it.

If you change the sentinel value, you **must** update both the constant and the template, and re-run `go test -race ./internal/agent/...` to refresh the parser tests.

---

## Testing

### Running tests

```bash
go test ./cmd/... ./internal/...                 # all repo-owned unit tests
go test ./internal/orchestrator/...              # a single package
go test -race ./cmd/... ./internal/...           # with race detector (required pre-PR)
go test -v ./internal/orchestrator/...           # verbose
```

### TUI tests (`internal/statusui`)

The status UI package uses two complementary testing strategies:

#### Pure-function and model-state tests

Whitebox unit tests cover pure helpers (`wrapText`, `fmtDuration`, `truncate`, `extractPRLink`, `buildNavItems`, …). Interactive model-state tests use [`charmbracelet/x/exp/teatest`](https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest) to drive the full `Update→View` pipeline:

```go
tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
tm.Send(tickMsg(time.Now()))
tm.Quit()
final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
assert.Len(t, final.sessions, 1)
```

#### Golden-file tests (catwalk)

Render-path tests use [`knz/catwalk`](https://github.com/knz/catwalk) — a data-driven framework that records `View()` output into golden files under `internal/statusui/testdata/` (`catwalk_details`, `catwalk_picker`, `catwalk_profile_picker`, `catwalk_split`). Each test file contains an input sequence followed by the expected rendered output.

**Regenerating golden files** after an intentional TUI change:

```bash
make tui-golden                               # preferred
# or directly:
go test ./internal/statusui/... -args -rewrite
```

Review the diff in `internal/statusui/testdata/` — it is the exact render change — and commit alongside your code change. The catwalk tests run in the `pre-push` hook, so unintentional renders fail before they reach CI.

### Frontend tests

```bash
cd web
pnpm test              # run once (Vitest)
pnpm test:watch        # watch mode
pnpm test:coverage     # with lcov report
```

Tests use **Vitest** + **@testing-library/react**. Test files live next to the code under `__tests__/` directories.

### Test doubles

The project ships two test doubles that avoid real subprocesses and real API calls:

| Double | Package | Path | Usage |
|---|---|---|---|
| `agenttest.FakeRunner` | `internal/agent/agenttest` | `internal/agent/agenttest/fake.go` | Replays scripted `StreamEvent`s without spawning `claude` or `codex` |
| `tracker.MemoryTracker` | `internal/tracker` | `internal/tracker/memory.go` | In-memory tracker with configurable issues and state |

Use these in orchestrator and integration tests. Never add a test that shells out to a real agent CLI or hits a live API without an `integration` build tag.

### Integration tests

Tests that require live credentials are gated behind the `integration` build tag:

```bash
LINEAR_API_KEY=lin_api_... go test -tags integration ./internal/tracker/linear/...
GITHUB_TOKEN=ghp_...       go test -tags integration ./internal/tracker/github/...
```

These are skipped (not silently passed) in CI without credentials.

### Linting

**Go:** `golangci-lint run ./cmd/... ./internal/...`

**Frontend:**

```bash
cd web
pnpm lint          # ESLint
pnpm format:check  # Prettier (check only)
pnpm format        # Prettier (write)
```

CI enforces both. PRs with lint errors are not merged.

### What to test

- **Happy path** — the normal flow works
- **Error path** — every `error` return has at least one test
- **Edge cases called out in the spec** — blockers, retry backoff cap, stall detection with `stall_timeout_ms=0`, etc.

For the orchestrator, prefer table-driven tests that build an initial `State`, call the function under test, and assert on the returned `State`.

---

## Frontend architecture

The dashboard is a Vite + React 19 + TypeScript + TailwindCSS app. A few patterns are non-negotiable for keeping the UI predictable:

- **Client state — Zustand.** `web/src/store/itervoxStore.ts` owns the SSE snapshot (`patchSnapshot`, `refreshSnapshot`); `toastStore.ts` and `uiStore.ts` own ephemeral UI state.
- **Server state — TanStack Query.** All issue mutations live in `web/src/queries/issues.ts` with optimistic updates and rollback. Settings mutations call `refreshSnapshot()` (never `patchSnapshot`).
- **Real-time — SSE.** `useItervoxSSE` reconnects with exponential backoff and validates payloads with Zod schemas from `web/src/types/schemas.ts`.
- **Toasts.** Always call `useToastStore.getState().addToast(message, variant)` — the message must be a string, never an object.

When adding a new page, drop a lazy route into `App.tsx` and a folder under `web/src/pages/<Name>/index.tsx`.

### Where to find things

| I want to… | Look in… |
|---|---|
| Add or modify a Kanban column | `web/src/components/itervox/BoardColumn.tsx`, `web/src/components/itervox/IssueCard.tsx` |
| Add a new dashboard page | `web/src/pages/<PageName>/index.tsx`, lazy-imported in `web/src/App.tsx` |
| Mutate an issue (with optimistic update + rollback) | `web/src/queries/issues.ts` |
| Read snapshot state (running, paused, retrying) | `web/src/store/itervoxStore.ts` |
| Show a toast | `useToastStore.getState().addToast(message, 'error' \| 'success' \| 'info')` — see `web/src/store/toastStore.ts` |
| Stream live logs for one issue | `web/src/hooks/useLogStream.ts` |
| Subscribe to the live SSE snapshot | `web/src/hooks/useItervoxSSE.ts` |
| Add a setting that round-trips to WORKFLOW.md | `web/src/hooks/useSettingsActions.ts` + a backend handler in `internal/server/` |
| Add a Zod schema for new server data | `web/src/types/schemas.ts` |
| Run a single Vitest file in watch mode | `cd web && pnpm test --watch <pattern>` |

---

## Code Conventions

### Error values

Return prefixed error strings for errors that callers may need to distinguish:

```go
return fmt.Errorf("linear_api_status: %d", resp.StatusCode)
```

The prefix (`linear_api_status`) is the stable error code. Use `errors.New` for static strings; use `fmt.Errorf("...: %w", err)` to wrap. Package-level errors use lowercase messages prefixed with the package name.

### Logging

Use `log/slog` throughout — never `log.Printf`. Always include structured fields rather than interpolating into the message:

```go
// good
slog.Warn("workspace hook failed", "hook", "before_run", "error", err, "issue_id", issue.ID)

// avoid
slog.Warn(fmt.Sprintf("before_run hook failed for %s: %v", issue.ID, err))
```

Never log API tokens or raw hook output beyond a safe truncation length.

### Idiomatic Go 1.25

- `maps.Copy` for map duplication — not manual `for k, v := range` loops
- `max(a, b)` / `min(a, b)` built-ins — not if-chains for clamping
- `slices` package helpers where applicable

### No globals

Avoid package-level mutable state. Configuration and dependencies flow through constructors. The orchestrator, server, and workspace manager all take their dependencies as constructor arguments.

### Comments

Add a doc comment to every exported type and function. A wrong comment is worse than no comment.

---

## Making Changes

### Before you start

- Open an issue to discuss significant changes before writing code.
- For bug fixes, a short description in the PR is sufficient.
- Check `planning/README.md` before adding backlog items or release tasks; it lists
  the active v0.2.0 plan, deferred items, future-version pass folders, and archived
  historical notes.

### Maintainer map

Use this map to route questions and reviews. Maintainers can overlap; pick the primary area when opening an issue or asking for review.

| Area | Primary reviewer |
|---|---|
| Orchestrator, worker lifecycle, retry/input-required flows | Core maintainer |
| Tracker integrations, prompt rendering, workflow parsing | Core maintainer |
| Web dashboard, Settings UI, frontend tests | Web maintainer |
| Release engineering, CI, Go toolchain, GoReleaser | Release maintainer |
| Documentation, examples, planning index | Docs maintainer |

When an issue crosses areas, name the highest-risk owner first and list the secondary area in the issue body.

### Label taxonomy

Use labels to make work pick-up and automation filters predictable:

| Label | Use for |
|---|---|
| `bug` | Reproducible incorrect behavior |
| `enhancement` | New user-facing capability |
| `docs` | README, site docs, examples, or planning text |
| `frontend` | React/Vite dashboard work |
| `backend` | Go daemon, server, orchestrator, tracker, or workspace work |
| `automation` | Automation triggers, filters, profiles, or dispatch behavior |
| `release` | Release-check, changelog, CI, versioning, GoReleaser |
| `security` | Auth, tokens, SSH, secret handling, permission boundaries |
| `good first issue` | Small, low-risk, well-scoped contribution |
| `needs-plan` | Requires an implementation plan before coding |
| `blocked` | Waiting on another issue, decision, upstream fix, or credential |

### Branching

Work on a feature branch off `main`:

```bash
git checkout -b feat/my-feature
```

### Commit style

The project uses **conventional commits** (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, optionally with a scope):

```
feat(tracker): add GitHub Issues adapter
fix(orchestrator): cap backoff at max_retry_backoff_ms
docs: add WORKFLOW.md reference to README
test(workspace): cover symlink escape rejection
chore: rename binary to itervox
```

### Pull request checklist

- [ ] `make verify` passes locally
- [ ] New behaviour is covered by tests
- [ ] No API tokens or secrets in the diff
- [ ] Exported symbols have doc comments
- [ ] PR description explains the **why**, not just the **what**

---

## Reporting Issues

Please include:

- itervox version (`itervox --version` or the commit hash)
- Tracker kind (`linear` or `github`)
- Agent backend (`claude` or `codex`)
- A minimal `WORKFLOW.md` that reproduces the problem (redact API keys)
- The full error output or relevant log lines

Open issues at: https://github.com/vnovick/itervox/issues

---

## Spec Conformance

Itervox is built on top of and conforms to the [Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md), the upstream OpenAI project that defines the agent communication protocol.
