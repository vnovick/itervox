---
name: setup
description: 'Use when the user wants to install Itervox or set it up in a repository: "set up itervox", "install itervox", "itervox init", "connect itervox to Linear", "use GitHub issues with itervox", "run itervox on local issue files", "add an agent profile". Installs the binary, runs itervox init (or quickstart) for Linear, GitHub or local issues, explains the SOUL.md / INSTRUCTIONS.md profiles, has the user store tracker and agent secrets in .itervox/.env with itervox secret set (never echoed), runs itervox doctor and starts the daemon.'
---

# Set up Itervox in a repository

Sources: `getting-started.mdx`, `cli.mdx` (`itervox init`, `itervox quickstart`,
`itervox secret`), `guides/local-issues.mdx`, `guides/github-issues.mdx`,
`guides/linear-setup.mdx`, `guides/agent-profiles.mdx` (all under
`site/src/content/docs/`, published at https://itervox.dev).

## Rules

1. **Never handle a secret.** Do not ask the user to paste a key into the chat,
   never read, `cat` or `grep` `.itervox/.env`, and never print a token. The
   user types secrets at the hidden prompt of `itervox secret set KEY` in their
   own terminal. Check them only with `itervox secret list`, which prints
   `set`, `placeholder` or `empty`, never a value. If a secret is pasted into
   the chat anyway, do not repeat it and tell the user to rotate it.
2. **Ask before writing over anything.** `itervox init` refuses to overwrite an
   existing `WORKFLOW.md` unless given `--force`; do not pass `--force` without
   the user's yes.
3. Run every command from the repository root (where `WORKFLOW.md` lives), or
   add `--workflow <path>`.

## 1. Install

Check first: `itervox --version`. If it is missing:

- macOS / Linux with Homebrew: `brew tap vnovick/tap && brew install itervox`
- Otherwise: the binary from https://github.com/vnovick/itervox/releases
  (Windows: `itervox_windows_amd64.zip`, plus Git for Windows; see
  `guides/windows.mdx`).

An agent CLI must be on `PATH`: `claude` (Claude Code) or `codex`. To look
around first, `itervox demo` opens a throwaway board in a temp directory with
no tracker, keys or agent CLI.

## 2. Pick the tracker

| Tracker | `--tracker` | Credential | Notes |
|---|---|---|---|
| Linear | `linear` | `LINEAR_API_KEY` | Scope with `tracker.project_slug`, `tracker.assignee`, `tracker.labels` (an AND) |
| GitHub Issues | `github` | `GITHUB_TOKEN` | States are labels; `gh` must be authenticated |
| Local Markdown files | `local` | none | Issues are `.itervox/issues/<ID>.md`; init writes `ITX-1.md` in Backlog |

## 3. Generate the workflow

One command, for the user to run in their own terminal (it asks before saving
a token or creating labels, then starts the daemon):

```bash
itervox quickstart --tracker github --runner claude
```

Or step by step (works from here):

```bash
itervox init --tracker <linear|github|local> --runner <claude|codex>
```

Other options (`cli.mdx`): a starter template (`minimal`, `full`,
`rate-limit-fallback`, `pr-review` or `daily-qa`), the two-backend
cross-review preset, and the agent-ready issue template
(`.github/ISSUE_TEMPLATE/agent-task.md` on GitHub; see the `issue`
skill):

```bash
itervox init --tracker github --template pr-review --issue-template
itervox init --tracker linear --runner codex --template minimal --preset cross-review
itervox init --tracker local --dir ./myrepo --output myrepo/WORKFLOW.md
```

What init writes:

- `WORKFLOW.md`: `itervox_schema_version: 2`, the profiles and the prompt
  template (its body).
- `.itervox/settings.jsonc`: the layered settings (`tracker`, `agent`,
  `workspace`, `server`, …). Change them with `itervox config set KEY VALUE`.
- `.itervox/agents/<profile>/SOUL.md` and `INSTRUCTIONS.md` for each profile.
- `.itervox/.env` with a placeholder for the tracker key (gitignored).

An existing older workflow: preview with
`itervox init --update --workflow WORKFLOW.md --dry-run`, then run it without
`--dry-run` (it writes `WORKFLOW.md.bak` first).

## 4. Profiles

A profile lives in `WORKFLOW.md` under `agent.profiles.<name>` with a
`command` (for example `claude` or `codex`), `soul_file` and
`instructions_file`. Schema 2 rejects an inline `prompt:`.

- `SOUL.md`: who the agent is, a few lines.
- `INSTRUCTIONS.md`: how it works: steps, checks, what not to touch.

The prompt is assembled as the `WORKFLOW.md` body, then rules and memory, then
SOUL.md, then INSTRUCTIONS.md. Commit `.itervox/agents/**`. Keep
`.itervox/.env`, `.itervox/HEARTBEAT.md`, logs and queue files ignored.

## 5. Secrets

Give the user the command for each missing key, to run in their own terminal:

```bash
itervox secret list
itervox secret set LINEAR_API_KEY      # Linear
itervox secret set GITHUB_TOKEN        # GitHub; or: gh auth token | itervox secret set GITHUB_TOKEN
```

`WORKFLOW.md` and the settings file refer to them as `$LINEAR_API_KEY` /
`$GITHUB_TOKEN`; never write a literal key there. The agent CLI normally uses
its own login (`claude` / `codex`); a headless machine needs
`ANTHROPIC_API_KEY` (or `CLAUDE_CODE_OAUTH_TOKEN`) or `OPENAI_API_KEY` set the
same way. A real value is kept unless `--replace` is given; the daemon reads
`.env` when it starts.

## 6. Check

- GitHub: create the state labels with `itervox doctor --fix` (it asks
  `[y/N]`; `--yes` only with the user's yes).
- Run `itervox doctor`. Exit `0` is clean. For anything else use the
  `doctor` skill.

## 7. Start

The daemon runs in the foreground: have the user start `itervox` in a
terminal in the repository (or keep `itervox quickstart`'s background daemon).
Then:

- `itervox status` lists it; `itervox watch` is a read-only live panel.
- The dashboard URL is in `.itervox/dashboard_url` (no token in it). The token
  is the tokenised URL printed on the daemon's terminal, `<logs-dir>/api-token`
  for a generated one, or the user's `ITERVOX_API_TOKEN`. Tell the user where
  it is; do not print it.
- `itervox stop` stops it.

Nothing runs until an issue is in an active state (default `Todo`,
`In Progress`; the `todo` label on GitHub).

## Done means

`itervox doctor` exits `0`, `itervox status` lists the daemon, and the
dashboard loads. Report those with the commands you ran.
