# AGENTS.md — itervox

> This file provides context for AI coding agents (Codex, Claude Code, Cursor, Gemini CLI, OpenCode, etc.) working on this repo.
> For human contributor docs see CONTRIBUTING.md.

## Project overview

Itervox is a Go 1.25.9 daemon that polls Linear/GitHub Issues, spawns Claude Code or
Codex subagents per issue, and serves a React web dashboard + Bubbletea TUI.
Config is a single `WORKFLOW.md` file (YAML front matter + Liquid template).

## Before making any change

1. **Read CLAUDE.md** — it contains architecture invariants, false-positive patterns for
   static analysis, and conventions that override defaults.
2. **Read the matching rule bundle under `.claude/skills/<name>/SKILL.md`** for the area you
   are editing (see the table below). These bundles are plain markdown and tool-agnostic —
   the directory name is historical. They are not optional reading.
3. **Run tests** to establish a baseline: `go test -race ./cmd/... ./internal/...` and `cd web && pnpm test:coverage`.
4. **Check the gap doc** (`planning/gaps_todo_030526.md`) for known open items before adding new
   ones — it may already be tracked.

## Rule bundles (read the matching one before editing)

| When you are editing… | Read this file first |
|---|---|
| `internal/orchestrator/**/*.go`, adding concurrent code, or mutable `cfg.*` fields | `.claude/skills/orchestrator-invariants/SKILL.md` |
| `web/src/**/*.{ts,tsx}` that makes HTTP or SSE calls (outside `web/src/auth/`) | `.claude/skills/authed-transport/SKILL.md` |
| `web/src/components/**` or `web/src/pages/**` — creating/editing React components | `.claude/skills/react-component-discipline/SKILL.md` |
| Adding a new `.go` file, growing one past ~400 lines, introducing a Go helper | `.claude/skills/go-package-hygiene/SKILL.md` |
| `internal/config/config.go` struct fields, evolving the `WORKFLOW.md` schema | `.claude/skills/config-field-checklist/SKILL.md` |
| Before editing any exported symbol / HTTP route / SSE event / Zod schema | `.claude/skills/change-impact-review/SKILL.md` |
| When the impact review surfaces a BREAKING change (hard stop) | `.claude/skills/breaking-change-gate/SKILL.md` |
| `go.mod`, `Makefile`, Go toolchain bumps, or govulncheck stdlib findings | `.claude/skills/go-toolchain-sync/SKILL.md` |
| Before claiming complete, before committing, before opening a PR | `.claude/skills/verify-before-done/SKILL.md` |

Each bundle is a focused checklist of enforced rules and verification steps for its area.
Reading the bundle before editing prevents the entire class of bugs it was written to catch.

## Commands (developer-facing workflows)

| Command | Use it for |
|---|---|
| `/interview` (`.claude/commands/interview.md`) | Start of a feature or refactor with unclear scope — 8 structured questions that surface design intent and verification criteria before any code |
| `/brainstorm` (`.claude/commands/brainstorm.md`) | Design decision with multiple reasonable approaches — spawns 3 subagents with forced orthogonal positions (Minimalist, Architect, Pragmatist), produces a tradeoffs table and decision document |

## Build commands

```bash
# Go
go build ./cmd/... ./internal/...
go test -race ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
golangci-lint run ./cmd/... ./internal/...

# Frontend
cd web
pnpm install --frozen-lockfile
pnpm test:coverage # vitest + coverage gate
pnpm build         # production bundle
pnpm exec tsc --noEmit -p tsconfig.app.json   # type-check only

# Combined
make verify        # fmt + vet + lint + go tests + web coverage/build/spelling + size/no-os.Exit guards
make build         # web build → go binary
```

## Repository layout

```
cmd/itervox/        CLI entry — wires all packages; main.go + main_test.go
internal/
  agent/             Claude/Codex subprocess runners (stream-json + JSONL protocols)
  app/               Business logic (EnrichIssue)
  config/            Typed config, defaults, $VAR resolution, validation
  domain/            Shared types: Issue, BlockerRef, BufLogEntry
  logbuffer/         Ring buffer for per-issue log streaming
  orchestrator/      Single-goroutine state machine (split into multiple files)
    orchestrator.go  Struct, New, Load, config setters/getters
    event_loop.go    Main select loop (Run), tick handling
    worker.go        Per-issue worker goroutine lifecycle
    snapshot.go      Snapshot construction and overlay
    dispatch.go      Eligibility checks, slot calculation
    reconcile.go     Stall/state reconciliation helpers
    retry.go         Retry queue scheduling
    reviewer.go      AI review dispatch
    issue_control.go Cancel/resume/discard/reanalyze actions
    ssh_host.go      SSH host selection (least-loaded)
    logging.go       Structured log formatting (BufLogEntry)
    state.go         OrchestratorEvent types and RunEntry
  prdetector/        PR URL detection via `gh pr list`
  prompt/            Liquid template rendering
  server/            HTTP API (chi router) — REST + SSE
  statusui/          Bubbletea TUI model and golden-file tests
  templates/         WORKFLOW.md scaffolding templates (Linear, GitHub)
  tracker/           Tracker interface + Linear GraphQL + GitHub REST adapters
  workflow/          WORKFLOW.md parser and file watcher
  workspace/         Per-issue worktree lifecycle (directory + git worktree modes)
web/                 React 19 / Vite frontend
testdata/            WORKFLOW.md fixtures
planning/            Gap analysis, design docs, roadmap
```

## Architecture constraints

### Orchestrator event loop — single goroutine

The orchestrator `Run()` loop is the ONLY place that mutates `State`. Workers
communicate via `o.events chan OrchestratorEvent`. Never write to state from a
worker goroutine — send an event instead.

### cfgMu scope

`cfgMu` protects only these `cfg` fields (mutable at runtime via HTTP):
- `cfg.Agent.MaxConcurrentAgents`, `cfg.Agent.MaxRetries`
- `cfg.Agent.MaxSwitchesPerIssuePerWindow`, `cfg.Agent.SwitchWindowHours`, `cfg.Agent.SwitchRevertHours`
- `cfg.Agent.RateLimitErrorPatterns`, `cfg.Agent.Profiles`
- `cfg.Agent.SSHHosts`, `cfg.Agent.SSHHostDescriptions`, `cfg.Agent.DispatchStrategy`, `cfg.Agent.InlineInput`
- `cfg.Agent.ReviewerProfile`, `cfg.Agent.AutoReview`
- `cfg.Tracker.ActiveStates`, `cfg.Tracker.TerminalStates`, `cfg.Tracker.CompletionState`, `cfg.Tracker.FailedState`
- `cfg.Automations`
- `cfg.Workspace.AutoClearWorkspace`

The canonical allowlist is `internal/orchestrator/cfg_mu_audit_test.go::AllowedMutableCfgFields`;
keep docs and any new runtime setter in sync with that test.

All other `cfg` fields are **read-only after startup** — no lock needed.

### Config value validation

`positiveIntField` in `config.go` rejects zero and negative values, replacing them
with defaults. Timeout fields (`TurnTimeoutMs`, `ReadTimeoutMs`, etc.) can never be
0 at runtime — do not flag `context.WithTimeout(ctx, 0)` as reachable.

### Package import order (no circular deps)

```
domain ─┬── tracker, prompt, logbuffer, prdetector
        │
workflow ── config ── workspace
        │
agent (imports domain, config)
        │
orchestrator (imports agent, config, domain, logbuffer, prdetector,
              prompt, tracker, workspace)
        │
app (imports domain, tracker) ── server (imports domain, config)
        │
cmd/itervox (wires everything)
```

## Testing conventions

- Always run `go test -race ./cmd/... ./internal/...` — the race detector catches real bugs here, and the explicit package list avoids traversing `web/node_modules`
- TUI tests use `charmbracelet/x/exp/teatest` (`model_teatest_test.go`) + catwalk
  golden files. Regenerate golden files with `make tui-golden` after intentional
  render changes.
- Integration tests (real API calls) are gated behind a build tag — not run by default.
- Frontend tests use Vitest + Testing Library.

## Common pitfalls

- **Toast API**: `addToast(message: string, variant?)` — first arg is a string.
  Passing an object silently renders `[object Object]`.
- **Settings mutations** must call `refreshSnapshot()`, NOT `patchSnapshot()`.
- **SSE hooks**: always use `useToastStore.getState()` / `useItervoxStore.getState()`
  inside effects — never call hooks conditionally.
- **Map copy**: use `maps.Copy(dst, src)` not manual for-range loops.
- **Clamp pattern**: `max(1, min(n, 50))` not if-chains (Go 1.21+).

## Open architectural items (from active gap planning)

The current gap backlog lives in `planning/gaps_todo_030526.md`. Historical
gap notes, including `gaps_300326.md`, are archived under `planning/archive/`.

Key unresolved items:
- T-6: Codex session log identity — single file instead of per-subagent files
- T-7: Reviewer backend parity — does not honor backend hints like worker path does
- T-9: Extract `orchestratorAdapter` from main.go to `internal/app`
- T-10: Replace 5s sublog polling with SSE push
- T-11: DRY `ParseSessionLogs`/`ParseSessionLogsMulti` duplication

See `planning/gaps_todo_030526.md` for the current task list with priorities and phases.

Before adding new items, spawn a verification agent to confirm the
issue is real (read full call chain, check for upstream validation, verify file
exists). See the "Gap analysis — avoiding false positives" section of CLAUDE.md.
