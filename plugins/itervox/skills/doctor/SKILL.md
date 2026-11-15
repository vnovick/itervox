---
name: doctor
description: 'Use when Itervox will not start, nothing is dispatched, runs fail, or the user asks "is my itervox setup ok", "run itervox doctor", "why is nothing running", "which agents have full permissions", "is the sandbox working", "is my local model server up", "where does this setting come from". Runs itervox doctor and itervox config show, reads every section (PATH and agent CLIs, watched files, validation, repo readiness, Isolation, Permissions, Model servers, How agents run) and fixes what they report. Never reads or prints a secret.'
---

# Diagnose and fix an Itervox setup

Sources: `cli.mdx` (`itervox doctor`, `itervox config`, `itervox secret`),
`guides/safety.mdx`, `guides/isolation.mdx`, `guides/local-models.mdx`,
`guides/repo-analysis.mdx`.

## Rules

- **Never read or print a secret.** No `cat`/`grep` of `.itervox/.env`, no
  printing `ITERVOX_API_TOKEN` or `<logs-dir>/api-token`, no
  `itervox token create` or `itervox token rotate` (they print a new secret).
  Check secrets with `itervox secret list`; a key the user must change is
  theirs to type: `itervox secret set KEY --replace` in their own terminal.
- **Permissions are the user's decision.** Report full-permission runs and
  the options; change `permission_mode` or `isolation` only when the user
  picks one.
- Fix one cause at a time and re-run `itervox doctor` after each.

## 1. Run doctor

```bash
itervox doctor                      # add --workflow PATH if WORKFLOW.md is elsewhere
```

Exit `0` clean, `1` a warning or failed check, `2` the check could not run.
Read the report top to bottom:

| Section / line | What it means | Fix |
|---|---|---|
| `PATH source:`, `agent CLI <name>:` | How the daemon resolves `PATH`; `WARNING: agent CLI … not found` | Install the CLI, give the profile's `command` an absolute path, or set `ITERVOX_LOGIN_SHELL_PATH` |
| `watched:` | Files a running daemon reloads on change | Informational |
| Schema / validation errors | e.g. a missing `itervox_schema_version: 2`, an inline profile `prompt:` | `itervox init --update --workflow WORKFLOW.md --dry-run`, then without `--dry-run` (writes `WORKFLOW.md.bak`) |
| Deprecated front-matter keys, conflicts with a settings file | A layered key still in `WORKFLOW.md`, or set in both | The same `itervox init --update` moves them to `.itervox/settings.jsonc` |
| Missing GitHub state labels | GitHub tracker only | `itervox doctor --fix` (asks `[y/N]`) |
| `.itervox/STARTUP_ERROR.md` | Why the last start failed | Fix the cause, then `itervox doctor --clear-startup-error` |
| `repo readiness:` `[warn]` / `[fail]` | Each line has a `fix:` | Apply that fix |
| `Isolation` | Sandbox mechanisms on this machine (bubblewrap and Landlock on Linux, sandbox-exec on macOS), the profiles with `isolation: process` | An `ERROR:` means a profile asks for process isolation and no mechanism works, so every run of it is refused: install one (for example bubblewrap) or set that profile's `isolation: none` knowingly |
| `Permissions` | `(default)` and each enabled profile: backend, permission mode, isolation; `WARNING:` for runs with full permissions | See step 3 |
| `Model servers` | Each `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` / `OPENAI_API_BASE` a profile's command sets, probed for 3 s; `WARNING:` when one does not answer | Start the server (colibri: `coli serve`) or fix the URL; see the `local-models` skill. A URL from a variable is listed, not checked |

The Permissions, Model servers and repo readiness findings do not change the
exit code on their own.

Extra checks, each on request:

- `itervox doctor --deploy`: agent credentials, `gh auth`, git push auth (dry
  run), the tracker API and the running daemon's `/api/v1/ready`.
- `itervox doctor --upgrade`: upgrade notes for workflows from v0.2.0 or
  earlier. It runs on its own (with `--deploy` or `--fix` it exits `2`).

## 2. See the effective settings

```bash
itervox config show            # --json for the settings only
```

Each value shows its layer: `default`, `user` (`~/.itervox/settings.jsonc`),
`workflow` (`WORKFLOW.md`), `project` (`.itervox/settings.jsonc`), `local`
(`.itervox/settings.local.jsonc`) or `environment`. The project and local
files win over `WORKFLOW.md`. A key no layer reads, or a wrong-typed value,
carries a note. Secrets are hidden.

Change a value with `itervox config set KEY VALUE` (`--local` for the
gitignored file, `--user` for `~/.itervox/settings.jsonc`) or remove it with
`itervox config unset KEY`. `set` covers `agent.*`, `polling.*`, `server.*`
and `workspace.*`; edit other sections in `.itervox/settings.jsonc`. A running
daemon reloads on the change.

## 3. Read "How agents run" (and Permissions)

`itervox config show` ends with **How agents run**: `agent.command` as
`(default)` and every enabled profile, with backend, permission mode and
isolation as dispatch resolves them. A run has **full permissions** when no
operating-system sandbox wraps it and its permission mode does not confine
its backend: `bypass` (the default), or any mode on Cursor or a custom backend.
Such an agent can read `~/.ssh`, `~/.aws`, the `gh` token and `.itervox/.env`,
and the network is open.

Offer the user, strongest first (`guides/safety.mdx`):

1. Run the daemon on a disposable machine or VM (the `deploy` skill).
2. `isolation: process` on the profile, or `agent.isolation: process` for all.
3. `permission_mode: sandbox` (Codex: writes only to the workspace; Claude:
   auto-accepts edits only; no effect on Cursor or custom backends).
4. Keep guardrails on, and branch protection plus required CI on the remote.

Profiles are in `WORKFLOW.md` under `agent.profiles.<name>`.

## 4. When the daemon runs but nothing happens

- `itervox status`: is a daemon running for this project?
- `.itervox/HEARTBEAT.md`: capacity, queue pressure, dependency audit,
  input-required count and the last notable error.
- `itervox logs <issue>`: the issue's run log (`-f` to follow).
- The issue must be in an active state, inside the tracker scope, and not
  blocked. The MCP tools `board_summary`, `explain_blockers` and
  `recent_failures` (the `operate` skill) say which.
- Tracker `401`: the key is wrong or revoked. The user sets a new one with
  `itervox secret set KEY --replace`, then restarts: `itervox stop` and start
  `itervox` again (`.env` is read at start).

## Done means

`itervox doctor` exits `0`, or every remaining line is a warning the user
chose to keep (for example a full-permissions profile they accept). Quote the
final doctor summary and any `How agents run` line you changed.
