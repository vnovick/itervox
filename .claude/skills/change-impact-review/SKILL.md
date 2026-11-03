---
name: change-impact-review
description: Use BEFORE editing any exported Go symbol (type, function, interface, struct field), any HTTP route in `internal/server/`, any SSE event shape, any Zod schema in `web/src/types/schemas.ts`, any WORKFLOW.md config field, or any public API surface. Produces a structured impact analysis listing affected call sites, schema parity risks, and regression surface before code changes begin.
---

# Change Impact Review

itervox has many cross-language boundaries (Go structs ↔ Zod schemas, HTTP routes ↔ typed fetch wrappers, `Snapshot()` output ↔ dashboard SSE consumers). A change that looks local often ripples across both halves of the codebase. This skill forces you to enumerate the blast radius **before** writing code.

## When to run

Run this review BEFORE editing:
- Any exported Go symbol (type, function, interface, struct field)
- Any HTTP route in `internal/server/`
- Any SSE event payload shape
- Any Zod schema in `web/src/types/schemas.ts`
- Any `WORKFLOW.md` config field
- Any field in `orchestrator.State` or `Snapshot()` output
- Any method on the `Tracker` interface

## When to SKIP

- Pure internal refactors with no boundary touched (renaming an unexported helper, splitting a file)
- Test-only changes
- Comment-only changes
- Changes inside a single function body that don't alter its signature

## The checklist

### 1. Identify the boundary type(s)

A single change may touch multiple — check all that apply:
- [ ] Exported Go symbol (type, field, function, interface)
- [ ] HTTP route (path, method, request body, response body)
- [ ] SSE event shape (data field, new event name, field rename)
- [ ] Zod schema in `web/src/types/schemas.ts`
- [ ] `WORKFLOW.md` config field (name, type, default, semantics)
- [ ] `orchestrator.State` field (added, removed, renamed)
- [ ] `Snapshot()` output field (consumed by dashboard via SSE)
- [ ] `Tracker` interface method (Linear / GitHub / memory adapters must stay in sync)

### 2. Enumerate every call site BEFORE editing

Use grep. Write the list down. The list is the proof you did the analysis — do not start editing without it.

- Exported Go symbol: `grep -rn "TheSymbol" --include="*.go"` across the whole repo
- HTTP route: grep `web/src/` for the literal route path AND any wrapping helper (e.g. `authedFetch('/api/v1/...')`)
- Zod schema: grep `web/src/` for the schema name AND for `z.infer<typeof SchemaName>` and any exported type alias
- `Snapshot()` field: grep both `internal/orchestrator/snapshot.go` (producer) AND `web/src/types/schemas.ts` + `web/src/store/itervoxStore.ts` (consumers)

### 3. Check Go ↔ TypeScript schema parity

itervox has two independent representations of many types: Go structs in `internal/server/` or `internal/domain/` and Zod schemas in `web/src/types/schemas.ts`. **When either side changes, the other must change in the same commit.** Common drift patterns:
- Adding a Go response field without updating Zod → silent parse failure, dashboard shows stale data
- Removing a Go field while Zod still requires it → runtime parse errors on every SSE tick
- Renaming on one side only → dashboard goes blank with no compile error

### 4. Tests that touch the changed surface

List every test file that needs updating:
- Go: `grep -rn "TheSymbol" --include="*_test.go"`
- TS: `grep -rn "schemaName" web/src/**/__tests__/`
- Terminal commands: `itervox watch` (`cmd/itervox/watch*.go`) decodes `/api/v1/state`, the feed and the run context; its tests are `cmd/itervox/watch_test.go`
- Tracker adapter tests under `internal/tracker/`

### 5. Orchestrator impact

If the change touches `internal/orchestrator/` or its shared types:
- Does it add a `cfg.*` field requiring `cfgMu` guard treatment? (see `.claude/skills/orchestrator-invariants/SKILL.md`)
- Does it add a `State` field that must only be written from the event loop?
- Does it change `Snapshot()` output, requiring a Zod schema update and a dashboard type update?

### 6. Docs touched

List every docs file referencing the changed surface:
- `site/src/content/docs/api-reference.mdx` for HTTP routes
- `site/src/content/docs/configuration.mdx` for `WORKFLOW.md` fields
- `CLAUDE.md` / `AGENTS.md` for architecture invariants
- `internal/templates/` scaffolding for new config fields that `itervox init` should emit

### 7. Produce an IMPACT REPORT before editing

```
## Change Impact Report
**What changes**: <one sentence>
**Boundary type(s)**: <from step 1>
**Call sites** (N): <list from step 2>
**Schema parity updates required**: <Go side, TS side, or both>
**Tests to update** (N): <list from step 4>
**Docs to update** (N): <list from step 6>
**Is this breaking?** <yes/no with one-sentence rationale>
```

### 8. If breaking, STOP

If the report surfaces a breaking change (removing/renaming an exported symbol, removing a Zod-required field, removing an HTTP route, changing semantics of an existing field), stop coding and invoke `.claude/skills/breaking-change-gate/SKILL.md`. This review is the trigger that feeds that gate.

## Common traps

- Editing a Go struct field and forgetting the matching Zod schema
- Adding an HTTP route without a typed fetch wrapper in `web/src/queries/`
- Renaming a `cfg.*` field without updating CLAUDE.md and the `strField` / `boolField` parse line
- Changing SSE event structure without re-running the dashboard and checking the Zod parse fallback path
