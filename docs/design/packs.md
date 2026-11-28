# Packs: format and manifest (v1 draft)

**Status:** Proposed. Nothing here is implemented yet.
**Date:** 2026-11-28

A pack is a directory, or a zip of one, that installs a ready-made agent crew
into an Itervox project: profiles, playbooks, rules, skills, automations and
`verify` checks, plus a **permissions envelope** that the daemon enforces.
One pack runs on every backend Itervox drives: Claude Code, Codex, Cursor CLI
and OpenCode.

Packs are content, not code. Itervox runs nothing from a pack except its
`verify` checks, which run in the run's worktree (inside the run's sandbox when
the envelope requires `isolation: process`).

## 1. Goals and non-goals

Goals:

- One format for every backend. A pack written once runs under `claude`,
  `codex`, `cursor` and `opencode`; switching an issue's backend (by hand or by
  `agent.backend_fallback`) keeps the pack working.
- Safe to run unattended. The envelope bounds what a pack's agents may do, and
  a project cannot loosen it by accident.
- Reproducible. A team gets the same pack bytes (lock file), and every run
  records which pack and version it used.
- Import, not rewrite. Existing Claude Code plugins, Codex skills, Cursor rules
  and OpenCode agents can be turned into packs.

Non-goals for v1: a registry, signatures, git or daemon hooks shipped by a pack,
and backend-native subagents as the unit of a crew (see §6).

## 2. Layout

```
acme-secfix/
  itervox-pack.jsonc            # manifest (required)
  profiles/
    fixer/SOUL.md
    fixer/INSTRUCTIONS.md
    reviewer/SOUL.md
    reviewer/INSTRUCTIONS.md
  playbooks/dependency-cve.md   # same front matter as .itervox/playbooks
  rules.json                    # same schema as .itervox/rules.json
  skills/<skill>/SKILL.md       # Agent Skills folders (one copy for all backends)
  mcp.jsonc                     # optional, neutral MCP server definitions (§5.3)
  automations.jsonc             # optional automation rules
  evals/                        # optional, internal/evals scenario format
  README.md  LICENSE  CHANGELOG.md
```

Every file format is one Itervox already reads: profile files follow schema 2
(`soul_file` / `instructions_file`, no inline prompts), playbooks use the
`description` / `keywords` / `require_evidence` front matter, rules use the
`rules.json` schema, and JSONC goes through `config.ParseJSONC`.

## 3. Manifest: `itervox-pack.jsonc`

```jsonc
{
  "$schema": "https://itervox.dev/schema/pack.schema.json",
  "schema": 1,
  "name": "acme/secfix",            // publisher/name: lowercase [a-z0-9-]
  "version": "1.2.0",               // semver
  "title": "Security fixer",
  "description": "Fixes dependency CVEs and re-scans to prove it.",
  "authors": ["Acme <packs@acme.dev>"],
  "license": "Apache-2.0",          // SPDX id
  "itervox": ">=0.3.5",             // compatible daemon versions

  // Which backends the pack supports, and how (§5).
  "backends": {
    "claude":   "native",
    "codex":    "native",
    "cursor":   "native",
    "opencode": "native"
  },

  "provides": {
    "profiles": {
      "fixer": {
        "soul_file": "profiles/fixer/SOUL.md",
        "instructions_file": "profiles/fixer/INSTRUCTIONS.md",
        "backend": "claude",                      // default backend
        "models": {                               // per backend, optional
          "claude": "sonnet",
          "codex": "gpt-5-codex",
          "cursor": "auto",
          "opencode": "anthropic/claude-sonnet"
        },
        "args": { "codex": ["-c", "model_reasoning_effort=\"high\""] },
        "verify": [
          { "name": "rescan", "run": "osv-scanner scan --recursive ." },
          { "name": "tests",  "run": "make test", "timeout_ms": 600000 }
        ],
        "require_evidence": ["rescan", "tests"],
        "skills": ["sarif-reading"]               // subset of provides.skills
      },
      "reviewer": {
        "soul_file": "profiles/reviewer/SOUL.md",
        "instructions_file": "profiles/reviewer/INSTRUCTIONS.md"
      }
    },
    "reviewer_chain": ["reviewer"],
    "playbooks": ["playbooks/dependency-cve.md"],
    "rules": "rules.json",
    "skills": ["skills/sarif-reading"],
    "mcp": "mcp.jsonc",
    "automations": "automations.jsonc",
    "evals": "evals/"
  },

  "requires": {
    "tools": ["semgrep", "osv-scanner"],   // checked on PATH by `itervox doctor`
    "env": ["SNYK_TOKEN?"],                // names only, never values; ? = optional
    "tracker": ["github", "linear", "local"]
  },

  // Enforced ceiling (§4). Field names match the profile/guardrails config.
  "permissions": {
    "isolation": "process",
    "egress": "allowlist",
    "egress_allow": ["registry.npmjs.org", "pypi.org", "api.osv.dev"],
    "egress_defaults": true,
    "push": "daemon",
    "permission_mode": "sandbox",
    "lanes": { "human": [".github/**", "**/auth/**"] },
    "allowed_actions": ["comment", "move_state"]
  }
}
```

`verify` entries are the existing `VerifyCheck` shape (`name`, `run`,
`timeout_ms`). The `egress*` keys follow #349; they are provisional until it
merges.

## 4. Permissions envelope

A pack's `permissions` are a ceiling, not a suggestion.

- **Defaults.** A profile that comes from a pack takes the pack's isolation,
  egress, push mode, permission mode and lanes unless the project sets a
  stricter value.
- **Loosening is refused at load.** Project settings that would loosen the
  envelope (`isolation: none`, a host outside `egress_allow`, `push: agent`,
  `permission_mode: bypass`, removing a human-lane path) fail config load with
  an error naming the pack and the key. The only way through is
  `packs[].override_permissions: true` in the **local** layer
  (`.itervox/settings.local.jsonc`): a personal, visible decision that
  `itervox doctor` and the evidence report both show.
- **Backend-independent.** The envelope is enforced around the agent process
  (sandbox, egress proxy, guardrail shims, pre-push hook, `push: daemon`), so it
  holds on all four backends. Per-backend permission flags are derived from
  `permission_mode` by the existing runners (`internal/agent/permission.go`).
- **Install-time review.** `itervox pack install` prints the envelope, the
  exact `verify` commands (they run code in the worktree), the MCP servers and
  the required tools, and asks for confirmation; `--yes` is required without a
  terminal.
- **No hooks.** Packs cannot install git hooks, Itervox `hooks.*` or
  backend-native hooks. An imported plugin's hooks are listed in the import
  report and dropped.

## 5. Multiple backends

Most of a pack is backend-neutral already: Itervox assembles the prompt itself
(rules, memory, SOUL.md, INSTRUCTIONS.md, playbook) and hands it to whichever
runner the issue uses. Four things differ per backend, and the daemon, not the
pack author, handles each one.

### 5.1 Support levels

`backends.<name>` is `native` (every feature delivered natively), `inlined`
(skills delivered in the prompt, §5.2) or absent (unsupported).
`itervox pack validate` checks each claim against the delivery table below, and
`itervox doctor` warns when a profile from the pack is set to a backend the pack
does not list.

### 5.2 Skills: one copy, delivered per backend

Skills live once under `skills/<name>/SKILL.md`. At run time the worker
materialises the profile's skills into the worktree under a namespaced name
(`<pack>.<skill>`, e.g. `secfix.sarif-reading`) in two directories, which
between them cover all four backends:

| Directory | Read by |
|---|---|
| `.agents/skills/` | Codex, Cursor CLI, OpenCode |
| `.claude/skills/` | Claude Code, Cursor CLI, OpenCode |

The copies are written into the worktree's `info/exclude` so they never reach a
commit, and are removed when the run exits. A project's own skill with the same
name is never overwritten (the namespace prevents a clash; a collision is an
error).

`inlined` mode, for a backend or version without native skills, writes no
files: a `## Skills` block in the prompt lists each skill's name, description
and path inside the pack, and the agent reads the file when it needs it. The
block is a prompt-digest component, like the playbook.

### 5.3 MCP servers: written once, passed per backend

`mcp.jsonc` holds neutral definitions:

```jsonc
{ "servers": {
    "osv": { "command": "osv-mcp", "args": ["--stdio"], "env": ["OSV_TOKEN?"] }
} }
```

`env` lists names only; the daemon passes values from its own environment and
never writes a value into a file. Delivery per backend prefers command-line
flags, so nothing is written into the repository's own config:

| Backend | Delivery |
|---|---|
| Claude Code | `--mcp-config <generated JSON>` (`mcpServers` shape) |
| Codex | `-c mcp_servers.<name>.command=...` overrides (a project `.codex/config.toml` is ignored when the project is untrusted, so flags are the reliable route) |
| OpenCode | open question: no MCP flag found; candidates are a generated config passed via an environment variable, or an `opencode.json` merge the run removes on exit |
| Cursor CLI | no config-path flag; the run writes the servers into the worktree's `.cursor/mcp.json` (`mcpServers`, values as `${env:NAME}` interpolation, so no secret reaches the file), merged with and restored to any file the repo already has, and passes `--approve-mcps` (which approves every configured server, the repo's own included) |

A backend with no confirmed delivery route makes `validate` downgrade that
backend to "no MCP" and say so; a profile that requires an MCP server is then
refused on that backend rather than run without it.

### 5.4 Models and flags

`models.<backend>` and `args.<backend>` compose into the runner's command
(`--model` for Claude Code and Cursor CLI, `-m` for Codex, `--model
provider/model` for OpenCode). A project's own `command` for the profile still
wins.

## 6. Crews are Itervox profiles

Claude Code, Codex, Cursor CLI and OpenCode each have their own subagent format
(`.claude/agents/*.md`, `.codex/agents/*.toml`, `.cursor/agents/*.md`,
`.opencode/agents/*.md`) and they do not behave alike non-interactively. A
pack's crew is therefore expressed with what Itervox already runs on every
backend: profiles, `reviewer_chain` and playbooks. Backend-native subagents are
out of scope for v1; a later version may let a Claude-only profile carry them
(passed with `--agents <json>`).

## 7. Installing, locking and enabling

- **Location:** `.itervox/packs/<publisher>/<name>/<version>/`, committable
  (`itervox init` adds the `!.itervox/packs/` carve-out), so the whole team gets
  the same packs.
- **Lock file:** `.itervox/packs.lock.json` records, per pack, name, version,
  source (path, zip or URL) and the SHA-256 of every file. The daemon refuses a
  pack whose files no longer match the lock.
- **Enabling:** a new layered `packs` list section in the settings files:

  ```jsonc
  // .itervox/settings.jsonc
  "packs": [{ "name": "acme/secfix", "version": "^1.2", "profiles": ["fixer", "reviewer"] }]
  ```

  Pack content sits at the lowest precedence (pack < user < WORKFLOW.md <
  project < local), so a project can always override it, within the envelope.
  `itervox config show` reports which pack a value came from.
- **Namespacing:** profiles appear as `secfix.fixer`, playbooks as
  `secfix.dependency-cve`, rule ids are prefixed `secfix.`. A project item with
  the same full name always wins.

## 8. Provenance and outcomes

Each run records `pack: name@version` and the pack's lock hash on its
`RunEntry` / `CompletedRun`, and the pack becomes a prompt-digest component
(#186). The evidence report then shows exactly which pack ran, and outcome
stats can be broken down per pack **and per backend** ("merged / reverted on
Claude Code vs OpenCode").

## 9. Importing existing formats

`itervox pack import <zip|dir>` detects the source and writes the layout above
with a conservative envelope (`isolation: process`, `egress: allowlist` with
defaults only, `push: daemon`, `permission_mode: sandbox`) for the operator to
review:

| Source | Becomes |
|---|---|
| Claude Code plugin: `.claude-plugin/plugin.json` | manifest name, version, description, authors |
| `agents/*.md` (Claude Code, Cursor CLI, OpenCode) / `agents/*.toml` (Codex) | a profile each: body → `INSTRUCTIONS.md`, description → generated `SOUL.md`, model → `models.<backend>` |
| `skills/*/SKILL.md` (any) | `provides.skills` |
| `commands/*.md` (Claude Code, Cursor CLI, OpenCode) | playbooks |
| `.mcp.json` / `[mcp_servers]` / `opencode.json` `mcp` | `mcp.jsonc` (secret values dropped, names kept) |
| `.cursor/rules/*.mdc`, `AGENTS.md` | `rules.json` entries or INSTRUCTIONS.md text (operator chooses) |
| hooks of any kind | not imported; listed in the report |

## 10. Validation

- The manifest parses against the published JSON Schema.
- Paths are relative and stay inside the pack: no `..`, no absolute paths, no
  symlinks out; unzip refuses path traversal.
- Size caps: pack ≤ 10 MB, file ≤ 1 MB.
- Secrets: only `$VAR` names (`config.ValidateSettingValue`), never inline
  values, in the manifest, `mcp.jsonc` or automations.
- Profiles use `soul_file` / `instructions_file`; inline prompts are rejected.

## 11. CLI

`itervox pack new | validate | install | list | enable | disable | update |
remove | import`, plus `itervox doctor` checks for missing tools, lock
mismatches, a loosened envelope, unsupported backends and incompatible daemon
versions.

## 12. Rollout

1. **v1:** local directories and zips, import, lock file, envelope,
   provenance, skills delivery for all four backends, MCP for Claude Code,
   Codex and Cursor CLI.
2. **v2:** a registry (`itervox pack install acme/secfix`), Ed25519 signatures
   and a trust store; MCP for OpenCode once its route is
   confirmed.

## 13. Open questions

- OpenCode MCP delivery (§5.3).
- Cursor CLI: `--approve-mcps` approves all configured servers, not only the
  pack's. Decide whether a pack's MCP servers may run on Cursor when the repo
  has its own `.cursor/mcp.json`, or only when it has none.
- Pin each backend's file locations with a recorded fake-runner test, since
  these formats change often (checked 2026-11-28: Cursor docs, OpenCode and
  Codex source, Claude Code docs).
- Whether `packs` fits the layered-settings rules as a list section or needs a
  merge-by-name rule so a local file can override one pack's version.
- Whether materialised skills should go into the worktree or a per-run
  directory the backend is pointed at (only some backends accept one).
