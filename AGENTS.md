# AGENTS.md — itervox
@CLAUDE.md

> **Read `CLAUDE.md` first. It is the canonical agent guide for this repo.**
>
> CLAUDE.md contains: project overview, build/test commands, architecture
> invariants (single-goroutine orchestrator, `cfgMu` allowlist, queue +
> dependency audit ownership, Track B file-backed profiles / schema 2 /
> HEARTBEAT.md / handoff pipeline), package dependency order, frontend
> architecture, the `Toast API` contract, the gap-analysis false-positive
> patterns, Go and TypeScript conventions, the `Never do` list, and — most
> importantly — the MANDATORY "Verification before completion" rule.
>
> All of that applies to codex (and any other agent) identically. This file
> contains ONLY the things that are codex-specific or that codex needs in
> addition to CLAUDE.md.

## Before making any change

1. **Read `CLAUDE.md` end-to-end.** No exceptions. The rules there are not a
   superset of what's here — they are the rules. This file does not repeat
   them.
2. **Read the matching rule bundle from the table below** for the area you are
   editing. The bundles live under `.claude/skills/<name>/SKILL.md` — the
   directory name is historical; they are plain markdown that codex can read
   like any other doc, not Claude-specific code.

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

Each bundle is a focused checklist of enforced rules and verification steps
for its area. Reading the bundle before editing prevents the entire class of
bugs it was written to catch.

## Codex-specific notes

- **Canonical-source precedence**: when CLAUDE.md and any rule in your
  training/memory disagree, CLAUDE.md wins. When CLAUDE.md and a skill bundle
  disagree on scope, the skill bundle wins for its area and CLAUDE.md wins
  for everything else.
- **Verification rule applies identically**: the "Verification before
  completion — MANDATORY" section in CLAUDE.md is not Claude-specific. Codex
  must produce the same `Verified by:` annotations, refuse the same evasions,
  and follow the same sampling rules. There are no codex carve-outs.
- **Tool-name equivalences**: CLAUDE.md references Claude Code tools (`Read`,
  `Edit`, `Write`, `Bash`, `TaskCreate`, `Skill`, etc.). Use whichever tool
  actually exists in your runtime — for codex this is typically `shell.exec`
  for everything. The semantics are the same: read before edit, prefer
  surgical edits over rewrites, run tests with race detector, mark tasks as
  completed only after `Verified by:` evidence.
- **Slash commands** (`/interview`, `/brainstorm`) are Claude Code-specific
  and not available to codex. The underlying intent — interview before code
  on vague specs, brainstorm before committing to an approach with multiple
  valid options — applies regardless. Spawn subagents or run the equivalent
  workflow manually if useful.

Before adding new follow-up items, spawn a verification agent to confirm the
issue is real (read full call chain, check for upstream validation, verify
file exists). See "Gap analysis — avoiding false positives" in CLAUDE.md.

## File-backed profile surfaces (read CLAUDE.md before editing)

Five concrete surfaces define how agents and the daemon interact with on-disk
profile state. CLAUDE.md is the canonical reference for each; read the
matching section there before changing behaviour:

- `itervox_schema_version` — schema-version marker required at the top of
  every `WORKFLOW.md`. Mismatch is a hard startup failure.
- `SOUL.md` — compact per-profile identity file under `.itervox/agents/<name>/`.
- `INSTRUCTIONS.md` — full per-profile operating rules, same directory.
- `HEARTBEAT.md` — daemon liveness file written under `.itervox/`. Transient
  runtime state; never committed.
- `init --update` — the migration subcommand that moves v0.1.x inline profile
  prompts into the file-backed layout and stamps the schema marker.
