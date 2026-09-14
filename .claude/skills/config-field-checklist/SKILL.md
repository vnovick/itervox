---
name: config-field-checklist
description: Use when adding or renaming fields in any config struct in internal/config/config.go (ServerConfig, TrackerConfig, AgentConfig, WorkspaceConfig, PollingConfig, HooksConfig), or when the user says "add a config option", "new WORKFLOW.md field", or mentions evolving the WORKFLOW.md schema.
---

# Adding a new WORKFLOW.md config field

Itervox reads per-project configuration from a single `WORKFLOW.md` file (YAML front matter + Liquid prompt template). Adding a new field touches several places that are easy to forget. Work through every step below before opening a PR.

## 1. Parse in `config.Load()`

Add the field to the relevant struct in `internal/config/config.go` (`ServerConfig`, `TrackerConfig`, `AgentConfig`, `WorkspaceConfig`, `PollingConfig`, or `HooksConfig`), then parse it in the matching `nestedMap` block in `Load()` using the right helper:

- `strField(m, "key", "default")` — strings with a default
- `boolField(m, "key", false)` — booleans
- `toInt(m["key"], default)` — integers (rejects zero / negative where appropriate via `positiveIntField`)

Pick a sensible default. Empty string, `false`, or `0` is usually wrong — prefer an explicit safe value. Pattern to mirror (see `internal/config/config.go` around line 324):

```go
cfg.Server.AllowUnauthenticatedLAN = boolField(srv, "allow_unauthenticated", false)
```

(The Go field name `AllowUnauthenticatedLAN` predates a #48 rename of the
YAML key to `allow_unauthenticated`; the field name itself was kept to
minimize blast radius. The real parse site also accepts the old
`allow_unauthenticated_lan` key as a deprecated alias with a `slog.Warn` —
see `internal/config/config.go` around the `server` block for the full
alias-handling pattern if your new field needs the same treatment.)

A struct field without a corresponding parse line silently stays at its zero value forever — the Go compiler will not warn you.

## 2. Decide lock discipline

Two cases, and only two:

- **Read-only after startup** (the common case). The field is set once by `config.Load()` and read everywhere without synchronization. No lock, no `CLAUDE.md` update.
- **Runtime-mutable via HTTP**. The field can be changed at runtime by an API handler. You must:
  1. Add the field to the `cfgMu` guard list in `CLAUDE.md` (the section that enumerates exactly which `cfg.*` fields require the lock).
  2. Wrap every read in `o.cfgMu.RLock()` / `RUnlock()` and every write in `o.cfgMu.Lock()` / `Unlock()`.
  3. Add a handler under `internal/server/` that performs the mutation.
  4. Audit `Snapshot()` — does the new field need to surface to HTTP clients so the dashboard reflects the change?

If you mark a field as runtime-mutable but forget to add it to the guard list, you create a data race the race detector may not always catch.

## 3. Add a roundtrip test in `config_test.go`

Add a table-driven case in `internal/config/config_test.go` that:

- Parses a YAML fixture containing the new field and asserts the resulting `Config` struct has the expected value.
- Parses a fixture where the field is **absent** and asserts the default kicks in.

This catches typos in the YAML key name — something the Go compiler cannot. Without this test, a typo ships unnoticed.

Run with the race detector:

```
go test -race ./internal/config/...
```

## 4. Document in two places

- **Public docs**: `site/src/content/docs/configuration.mdx`. Add a row to the relevant field table with the YAML key, type, default, and a one-sentence description of when to use it. This is the user-facing source of truth.
- **Inline Go doc comment** on the struct field. `golangci-lint` enforces comments on exported fields. Describe the **behavior**, not the type — `// Maximum number of concurrent agent subprocesses; zero means unlimited.` is useful, `// MaxAgents is an int.` is not.

## 5. Update `itervox init` scaffolding (when appropriate)

If a typical new-project user would need to set this field, add it to the templates under `internal/templates/` so `itervox init` writes it into the generated `WORKFLOW.md`. Experimental or power-user fields should stay out of the default template to keep the scaffold readable.

Updating `configuration.mdx` but forgetting `internal/templates/` is a common miss — new projects will not see the field even though the docs claim it exists.

## Verification

Before opening a PR, run:

- `go test -race ./internal/config/...` — confirms parsing and defaults.
- `make verify` — confirms lint, vet, full test suite, and frontend build still pass.
- Manual smoke test: create a throwaway `WORKFLOW.md` with the new field set to a non-default value and run `go run ./cmd/itervox` from that directory. Confirm the daemon loads cleanly and the field actually takes effect.

## Common mistakes

- Adding the struct field but forgetting the `strField` / `boolField` / `toInt` parse line — field silently stays at zero value forever.
- Documenting the field in the guide but skipping the roundtrip test — a typo in the YAML key name ships unnoticed.
- Marking a field as runtime-mutable but forgetting to add it to the `cfgMu` guard list — latent data race.
- Updating `configuration.mdx` but forgetting `internal/templates/` — `itervox init` does not emit the field.
- Choosing a zero-value default when a safer explicit value is more appropriate.
