# Skills Inventory & Capability Analytics

The Skills Inventory surfaces every Claude Code / Codex capability your project carries — skills, plugins, MCP servers, hooks, and instruction docs — alongside an analyzer that flags duplicates, bloat, runtime drift, and unused config. In v0.2.0 this is a direct inventory and recommendations surface; normalized capability graph fields are reserved for a later phase. Runtime evidence (session logs) tells you which capabilities are *actually loaded* at runtime vs. just configured.

The feature lives at **Settings → Skills Inventory**. No external dependencies; everything is read from the local filesystem and `~/.itervox/logs/`.

---

## What gets scanned

| Source | Path | Provider |
|---|---|---|
| Claude skills (project) | `<project>/.claude/skills/<name>/SKILL.md` | claude |
| Claude skills (user) | `~/.claude/skills/<name>/SKILL.md` | claude |
| Claude plugins | `<dir>/.claude/plugins/<name>/plugin.json` | claude |
| MCP servers | `.claude/settings.json::mcpServers`, `.mcp.json::mcpServers` | claude |
| Hooks | `.claude/settings.json::hooks` (flat or nested form) | claude |
| Instructions | `<project>/CLAUDE.md`, nested CLAUDE.md, `<project>/AGENTS.md`, `~/.claude/CLAUDE.md` | claude / codex |
| Codex skills | `~/.codex/skills/`, `~/.codex/skills/.system/`, `~/.codex/superpowers/skills/`, `~/.agents/skills/` | codex |
| Codex plugins | `~/.codex/plugins/<name>/plugin.json` | codex |
| Codex superpowers hooks | `~/.codex/superpowers/hooks/hooks.json` | codex |
| Marketplace provenance | `~/.agents/.skill-lock.json` | (overlay) |

**Token estimates** are heuristics: `len(file) / 4` for skill bodies, `len(command) × 2 / 4` for hook commands, `800 × server_count` for MCP tool schemas (refined to `400 × runtime_tools` once Phase-2 evidence arrives).

---

## Static analyzer (Phase 1)

Seven production rules ship in `internal/skills/analyze.go`:

| Issue ID | Severity | What it catches |
|---|---|---|
| `DUPLICATE_SKILL` | info | Same skill name in multiple scopes (project + user, etc.) |
| `DUPLICATE_MCP` | warn | Same MCP server `command`/`url` registered twice (advisory only) |
| `UNUSED_PROFILE` | info | Profile defined but absent from recently-active set (with Fix → set `enabled: false`) |
| `BLOATED_PROFILE` | warn | Inventory carries > 20 MCP servers OR > 15 skills |
| `LARGE_CONTEXT` | warn | Estimated profile cost > 50K tokens |
| `INSTRUCTION_SHADOWING` | info | Same filename in multiple scopes (project / user / system) |
| `ORPHAN_MCP` | info | Configured MCP server name never appears in any skill name, description, or body |

`STALE_SCHEDULE` remains in code as a reserved analyzer for future schedule
inventory sources, but v0.2.0 does not populate `Inventory.Schedules` from the
validated `WORKFLOW.md` automations config.

The remaining 7 design-draft rules (cross-runtime Jaccard, Levenshtein hook similarity, model mismatch, teams-mode capability overlap, missing-action, instruction-shadowing-via-similarity, missing-skill-ref) are tracked in `planning/deferred_290426.md` and land in a follow-up.

---

## Runtime analytics (Phase 2)

Set `CLAUDE_CODE_LOG_DIR` (or rely on the default `~/.itervox/logs/`) so every Claude Code session writes a JSONL log. The analyzer aggregates the most recent 25 sessions and produces:

- **Skill stats** — `RuntimeLoads` count + `LastSeenAt` for every configured skill.
- **Hook stats** — execution count per `(event, command)` tuple.
- **Profile costs** — per-profile token breakdown (instructions / skills / hooks / MCP / workflow).
- **Recommendations**:
  - `HIGH_COST_LOW_USAGE` (warn) — skill > 2K tokens but < 2 runtime loads.
  - `HOOK_STORM` (warn) — hook fired ≥ 50 times in the lookback window.
  - `CONFIGURED_NOT_LOADED` (info) — skill configured but never observed.
  - `LOADED_NOT_CONFIGURED` (info) — runtime-loaded skill not in static inventory.

Codex evidence is parsed from `~/.codex/history.jsonl` + `~/.codex/sessions/**` and merged into the same surface.

### Recommendations compound over time

Runtime analytics are **evidence-based, not prescriptive** — every recommendation is derived from JSONL session logs the daemon writes during real dispatches, not from your config. This has three consequences operators should plan around:

1. **A fresh install gives mostly static-analyzer output.** With zero sessions in `~/.itervox/logs/`, the runtime analyzer returns an empty snapshot. You will see Phase-1 recommendations (`DUPLICATE_SKILL`, `BLOATED_PROFILE`, `LARGE_CONTEXT`, etc.) but the more interesting Phase-2 signals (`HIGH_COST_LOW_USAGE`, `HOOK_STORM`, `CONFIGURED_NOT_LOADED`) need real runs to fire. **This is not a bug.** Re-check the dashboard after the daemon has handled a meaningful sample of issues.
2. **The lookback window is 25 sessions by default** (defined in `parseClaudeRuntime`). Sessions older than that age out of the analysis. As your daemon runs more issues, the window shifts forward and stale evidence is discarded — so a skill that was heavy six months ago but trimmed last week will correctly fall off the warn list once enough new sessions accumulate.
3. **The signal sharpens as variety grows.** A daemon that has run the same profile 25 times against the same kind of issue will correctly report which skills *that profile* loads. Multi-profile, multi-issue-type evidence catches `LOADED_NOT_CONFIGURED` (a skill the model used implicitly but isn't in the YAML) and `CONFIGURED_NOT_LOADED` (a skill that pays its token cost but never gets used). Run a representative cross-section of profiles before treating the recommendation list as final.

Operationally: leave the daemon running, accumulate evidence, then come back and act on the warnings — the longer the daemon has been running real work, the more confident the recommendations are.

---

## HTTP API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/skills/inventory` | Cached static inventory (503 before first scan) |
| `POST` | `/api/v1/skills/scan` | Force a re-scan; returns the fresh inventory |
| `GET` | `/api/v1/skills/issues` | Static analyzer output |
| `POST` | `/api/v1/skills/fix` | Apply a one-click Fix (`{ issueID, fix }`) |
| `GET` | `/api/v1/skills/analytics` | Runtime + cost projection |
| `GET` | `/api/v1/skills/analytics/recommendations` | Runtime-side recommendations |

All routes require the standard bearer-token authentication (`Authorization: Bearer <token>`).

---

## One-click fixes

The dashboard's Recommendations panel renders a "Fix" button per issue when the analyzer attaches a `Fix` descriptor. The infrastructure is wired end-to-end (Zod schema, mutation hook, REST endpoint, server handler, `cmd/itervox/skills_adapter.go::ApplyFix` dispatch), but **as of v0.2.0 only one rule populates a Fix descriptor that actually works** — the rest of the recommendations are advisory.

| Rule | Fix label | Status |
|---|---|---|
| `UNUSED_PROFILE` | **Disable profile** | ✅ Working. Non-destructive `edit-yaml` action; calls `UpsertProfile` with `Enabled: false`; queues a re-scan. Covered by backend and UI mutation tests. |
| `DUPLICATE_MCP` | (none) | Advisory only. The analyzer does not emit a Fix descriptor because `remove-mcp` is intentionally rejected until four safety guards land — flock + backup + structured-edit + per-call confirm. See `planning/deferred_290426.md::T-95`. |
| `DUPLICATE_SKILL`, `BLOATED_PROFILE`, `LARGE_CONTEXT`, `INSTRUCTION_SHADOWING`, `ORPHAN_MCP` | (none) | Advisory only — no Fix descriptor populated. The recommendation tells you what to do; the edit is yours. |
| All Phase-2 (runtime) recommendations | (none) | Advisory only — `HIGH_COST_LOW_USAGE`, `HOOK_STORM`, `CONFIGURED_NOT_LOADED`, `LOADED_NOT_CONFIGURED` ship without Fix descriptors, and the analytics-recommendations section in `SkillsCard` passes a no-op `onApplyFix` stub regardless. |

Destructive fixes always trigger a `window.confirm` before the request goes out — relevant once a second working fix lands.

This skew is intentional for v0.2.0: the file edits the surviving rules would need (rewriting `~/.claude/settings.json`, deleting skill directories, mass-disabling profiles) all involve user-owned files where automated mutation is risky. The v0.2.0 cut ships the safest fix (toggling a profile's `enabled` flag in our own `WORKFLOW.md`) and surfaces every other recommendation as guidance with no auto-apply path.

---

## Architecture

```
internal/skills/
├── types.go             # Inventory, Skill, Capability, etc.
├── scan.go              # Public Scan(projectDir, homeDir, opts)
├── scan_skills.go       # Claude .claude/skills walker
├── scan_plugins.go      # Claude .claude/plugins walker
├── scan_mcp.go          # MCP server scanner (settings.json + .mcp.json)
├── scan_hooks.go        # Claude hooks (flat + nested form)
├── scan_instructions.go # CLAUDE.md / AGENTS.md (recursive, capped)
├── scan_codex.go        # Codex 8-path scanner + .skill-lock provenance
├── scan_ssh.go          # Per-host SSH cache (TTL, last-good)
├── runtime_claude.go    # Session-log JSONL parser
├── runtime_codex.go     # ~/.codex/history.jsonl + sessions/** parser
├── context_budget.go    # Per-profile cost estimator
├── analyze.go           # Static analyzer (7 production rules + reserved schedule rule)
├── analytics.go         # BuildAnalytics(inv, runtime, profiles)
├── recommend.go         # Runtime-side recommendation engine
└── cache.go             # Cache + mtime-based Stale() check
```

The orchestrator adapter (`cmd/itervox/skills_adapter.go`) implements the `server.SkillsClient` interface and runs the first scan synchronously at daemon startup so the dashboard sees populated data on first request.

---

## When to use the inventory

- **Before adding a new skill / plugin / MCP server** — check the recommendations panel for `BLOATED_PROFILE` or `HIGH_COST_LOW_USAGE` to make sure you're not piling onto an already-overloaded profile.
- **After a few weeks of use** — the runtime-side `CONFIGURED_NOT_LOADED` recommendations point at dead config that's silently inflating your context cost.
- **When debugging an unexpected agent answer** — the inventory shows the full set of CLAUDE.md / instruction docs the agent actually had access to.
- **Before a production cutover** — the `LARGE_CONTEXT` warning is a fast sanity check that no profile is silently > 50K tokens.

---

## Trade-offs and limitations

- **Token counts are approximate** (labelled "estimated" in the UI). They're useful for ratio comparisons, not absolute claims.
- **Runtime evidence depends on log capture** — if `CLAUDE_CODE_LOG_DIR` is unset and `~/.itervox/logs/` is empty, the Analytics tab falls back to the static heuristic and surfaces the "no runtime evidence" hint.
- **The MCP duplicate fix is intentionally manual.** Editing user-controlled config (`~/.claude/settings.json`) from a daemon is reversible only with explicit guards (lockfile, backup copy, structured-edit). Until those guards land, the recommendation surfaces without a Fix button and the operator edits manually.
- **The analyzer is not a substitute for runtime testing.** It catches structural issues (duplicates, dead config, oversized context) but not semantic problems (skill-vs-skill conflicts, prompt regressions). Pair with the Lane-2 route-mocked Playwright suite for those.
