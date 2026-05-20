<div align="center">

# ITER//VOX

### Your codebase's journey, narrated by agents

Orchestrate AI agents across issues, profiles, and machines. Full visibility from dispatch to PR.

[![Go CI](https://github.com/vnovick/itervox/actions/workflows/ci-go.yml/badge.svg)](https://github.com/vnovick/itervox/actions/workflows/ci-go.yml)
[![Web CI](https://github.com/vnovick/itervox/actions/workflows/ci-web.yml/badge.svg)](https://github.com/vnovick/itervox/actions/workflows/ci-web.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/vnovick/itervox)](https://goreportcard.com/report/github.com/vnovick/itervox)
[![Latest Release](https://img.shields.io/github/v/release/vnovick/itervox)](https://github.com/vnovick/itervox/releases/latest)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
[![Discord](https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white)](https://discord.gg/ATU5n3yZNX)

**[itervox.dev](https://itervox.dev)** · **[Docs](https://itervox.dev/getting-started/)**
<img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/dashboard-overview.png" width="900" alt="Itervox dashboard — live overview of running agent sessions" />

</div>

---

Itervox is a long-running Go daemon that polls Linear or GitHub Issues, spawns Claude Code or Codex agents per issue, and gives you a live web dashboard and Bubbletea TUI while they work. One `WORKFLOW.md` per project, one static binary, no runtime. It's a full Go implementation of the [OpenAI Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md) — formerly known as "Symphony Go".

## Install

```bash
# Homebrew (macOS / Linux)
brew tap vnovick/tap && brew install itervox

# Or: go install
go install github.com/vnovick/itervox/cmd/itervox@latest
```

Pre-built binaries for macOS and Linux are available on the [latest release](https://github.com/vnovick/itervox/releases/latest).

### Authenticating the agent CLI

Itervox shells out to `claude` or `codex` and does **not** manage agent credentials itself. Before running Itervox, authenticate the agent CLI once on every machine where it will execute (including any SSH worker hosts):

```bash
claude login    # for Claude Code
codex login     # for Codex
```

For headless / CI environments, follow the upstream [Claude Code](https://docs.anthropic.com/en/docs/claude-code) or [Codex](https://github.com/openai/codex) docs — the auth flow is owned by those tools, not Itervox. The same applies to Claude Code on Bedrock or Vertex: set the env vars the upstream CLI expects and Itervox will pass them through to the subprocess.

---

## Why Itervox

You already have the agents. Now orchestrate them. Coding agents are powerful — but running them manually, one issue at a time, doesn't scale. Itervox turns them into a fleet.

| Manual Claude Code / Codex | With Itervox |
|---|---|
| Open terminal, pick an issue | Issues appear in your tracker |
| Create branch, `cd` into repo | Agents spawn in parallel per issue |
| Run `claude` with a prompt | Each agent gets an isolated worktree |
| Wait for the agent, review, open PR | PRs submitted, issues transitioned |
| Repeat, one at a time | You review and merge — that's it |

Pluggable agent backends — Claude Code and Codex are supported today; new backends require a small Go integration. OpenCode and Gemini CLI are on the roadmap.

---

## Built for autonomous agents at scale

- **Concurrent agents** — run up to N agents in parallel with per-state concurrency limits. Scale from 1 to 50+ without config changes.
- **Retry queue** — failed agents auto-retry with exponential backoff (10s, 20s, 40s… capped at 5 min).
- **Pause & resume** — free up a slot; resume later via `--resume` and continue the same session from exactly where it stopped.
- **Input required** — agents can request human input. The preferred contract is the explicit `<!-- itervox:needs-input -->` marker, but Itervox also has a best-effort English-oriented fallback for successful final messages that end in a real blocking decision or confirmation prompt. The question is posted as a tracker comment; reply from Linear/GitHub or the dashboard to resume.
- **Automations** — trigger helper runs from cron schedules, blocked-input events, tracker comments, issue-state transitions, backlog moves, or failed runs. Reuse normal profiles, filters, and permissions instead of inventing a separate workflow engine.
- **Auto-clear workspaces** — delete cloned workspaces after successful completion. Disk stays clean, logs are preserved.
- **Project filters** — filter issues by Linear project when working across multiple repos.
- **Stall detection** — no output inside the stall window? Worker is killed and retried automatically.
- **Auto-pause on open PR** — an existing open PR is detected and the agent pauses to prevent duplicate work.
- **Per-issue profile overrides** — route different issue types through different profiles, models, and machines.
- **API auth** — protect the local HTTP server with a shared token.

---

## Web dashboard

A real-time dashboard with everything you need to watch, steer, and debug a fleet of agents.

<table>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/kanban-board.png" width="440" alt="Kanban board" /><br/>
      <sub><b>Kanban</b> — drag-to-move issues across tracker states</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/agent-board.png" width="440" alt="Agent board" /><br/>
      <sub><b>Agent Board</b> — assign work by profile</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/host-pool-with-ssh.png" width="440" alt="Host pool with SSH workers" /><br/>
      <sub><b>Host Pool</b> — SSH workers with live load bars</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/timeline-multiple-runs.png" width="440" alt="Timeline" /><br/>
      <sub><b>Timeline</b> — parallel runs with token usage</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/timeline-expanded-with-logs.png" width="440" alt="Timeline expanded with logs" /><br/>
      <sub><b>Timeline drill-down</b> — session-isolated logs</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/logs-page.png" width="440" alt="Logs page" /><br/>
      <sub><b>Logs</b> — filter by issue, level, and source</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/issue-detail-slide.png" width="440" alt="Issue detail slide-over" /><br/>
      <sub><b>Issue Detail</b> — full context in a slide-over panel</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/input-required-issue-detail.png" width="440" alt="Input required state" /><br/>
      <sub><b>Input Required</b> — respond inline when an agent asks</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/dashboard-running-sessions-expanded.png" width="440" alt="Running sessions expanded" /><br/>
      <sub><b>Running sessions</b> — inline log streaming</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/narrative-feed.png" width="440" alt="Narrative feed" /><br/>
      <sub><b>Narrative feed</b> — human-readable timeline of fleet events</sub>
    </td>
  </tr>
</table>

### Hot-reconfigurable settings

Every setting below is live-editable from the dashboard. No daemon restart, no WORKFLOW.md hand-edit, no lost runs. Changes persist back to `WORKFLOW.md` automatically.

<table>
  <tr>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-agent-profiles.png" width="440" alt="Agent profiles" /><br/><sub>Agent profiles</sub></td>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-tracker-states.png" width="440" alt="Tracker states" /><br/><sub>Tracker states</sub></td>
  </tr>
  <tr>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-ssh-hosts-dispatch.png" width="440" alt="SSH hosts and dispatch strategy" /><br/><sub>SSH hosts + dispatch strategy</sub></td>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-add-worker-host-modal.png" width="440" alt="Add worker host modal" /><br/><sub>Add worker host</sub></td>
  </tr>
  <tr>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-max-concurrent-agents.png" width="440" alt="Max concurrent agents" /><br/><sub>Capacity slider</sub></td>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-workspace-auto-clear.png" width="440" alt="Auto-clear workspaces" /><br/><sub>Auto-clear workspaces</sub></td>
  </tr>
  <tr>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-project-filter.png" width="440" alt="Project filter" /><br/><sub>Project filter</sub></td>
    <td align="center"><img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/settings-code-review-agent.png" width="440" alt="Code review agent profile" /><br/><sub>Code review agent</sub></td>
  </tr>
</table>

### View from your phone, anywhere

The dashboard runs on your machine. Reach it from your phone over LAN, SSH tunnel, Tailscale, ngrok, or self-hosted Piko. Full guide: **[itervox.dev/guides/remote-access/](https://itervox.dev/guides/remote-access/)**.

<div align="center">
  <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/mobile-dashboard.png" width="280" alt="Itervox dashboard on mobile" />
</div>

---

## Or stay in the terminal

A full-featured Bubbletea TUI with the same real-time data as the web dashboard.

<table>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-issues-logs-timeline.png" width="440" alt="TUI issues + logs + timeline" /><br/>
      <sub><b>Issues · Logs · Timeline</b> — the default three-panel view</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-session-details.png" width="440" alt="TUI session details" /><br/>
      <sub><b>Session details</b> — press <code>d</code> for tools and metadata</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-gantt-timeline.png" width="440" alt="TUI Gantt timeline" /><br/>
      <sub><b>Gantt timeline</b> — parallel runs at a glance</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-history.png" width="440" alt="TUI history tab" /><br/>
      <sub><b>History</b> — press <code>h</code> for previous runs</sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-backlog.png" width="440" alt="TUI backlog" /><br/>
      <sub><b>Backlog</b> — press <code>b</code>, <code>enter</code> to dispatch</sub>
    </td>
    <td align="center">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-backlog-issue-details.png" width="440" alt="TUI backlog issue details" /><br/>
      <sub><b>Backlog issue details</b></sub>
    </td>
  </tr>
  <tr>
    <td align="center" colspan="2">
      <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/tui-project-picker.png" width="440" alt="TUI project picker" /><br/>
      <sub><b>Project picker</b> — press <code>p</code> to switch projects</sub>
    </td>
  </tr>
</table>

---

## Agent profiles

Define named agent profiles with their own command, backend, and Liquid-templated role prompt. Reference issue data like `{{ issue.identifier }}` and `{{ issue.title }}` directly in the role description. Different issue types get different profiles — a senior reviewer for security work, a fast haiku model for typo fixes, a Codex long-horizon runner for research.

**Rate-limit reassignment use case:** when a provider rate-limits one profile mid-run, Itervox retries the issue on a different profile — your fleet keeps moving instead of grinding to a halt.

---

## Human in the Loop

Autonomous, not unsupervised. Agents do the work. You stay in control at every checkpoint.

- **AI Code Review.** Configure a `reviewer_profile` and optionally `auto_review` to dispatch a second worker for PR review. `agent.auto_review` and `workspace.auto_clear` are mutually exclusive today because the reviewer needs the workspace to remain available after the main worker exits.
- **Agent asks for help.** When an agent needs input it pauses and posts a comment directly on the Linear or GitHub issue. Explicit `<!-- itervox:needs-input -->` remains the recommended way for prompts and skills to signal this. Itervox also catches common plain-English blocking questions like final choice or confirmation requests with a deterministic fallback detector, but that fallback is heuristic and English-oriented. If your prompts or skills use non-English wording or unusual phrasing, emit the explicit marker. Reply from the dashboard or from your tracker — the agent picks up your response and resumes automatically in the same session.
- **Automation helpers stay sandboxed by profile permissions.** If an automation should comment, move state, create follow-up issues, or auto-resume a blocked run, enable only the required `allowed_actions` on that profile. The daemon issues short-lived action grants per run instead of handing the agent your dashboard API token.
- **You merge the PR.** Agents submit PRs and post a session summary as a comment — they never merge. PR links are auto-commented on the tracker issue.

---

## Integrations

| Tracker | What you get |
|---|---|
| **Linear** | GraphQL API, automatic state transitions, project filtering, branch name hints |
| **GitHub Issues** | Issues + PRs with label-based routing, auto PR detection, PR link comments |

Full setup guides: **[Linear](https://itervox.dev/guides/linear-setup/)** · **[GitHub Issues](https://itervox.dev/guides/github-issues/)**.

---

## Remote access & bearer-token auth

When Itervox binds to any non-loopback address (`0.0.0.0`, a LAN IP), the dashboard and REST API require an `Authorization: Bearer <token>` header on every request. Set `ITERVOX_API_TOKEN` in the environment, or Itervox generates an ephemeral token at startup and logs it once. The dashboard captures the token from the URL query (`?token=…`) on first load and persists it via `sessionStorage` (or `localStorage` with the Remember checkbox). `GET /health` is auth-exempt for load-balancer probes. For trusted-LAN air-gapped setups, set `server.allow_unauthenticated_lan: true` to bypass auth entirely.

Full guide: **[itervox.dev/guides/remote-access/](https://itervox.dev/guides/remote-access/)**.

---

## SSH remote workers & Fleet Logs

Set `agent.ssh_hosts` in `WORKFLOW.md` and every agent turn runs on a remote machine over SSH. Round-robin dispatch with automatic failover to the next host on connection failure. Combine with NFS, `rsync` hooks, or per-host `git clone` to provision workspaces. Works with Docker-on-remote via `docker exec` inside a `before_run` hook.

Fleet Logs capture the full subagent tree — parent plus every spawned sub-agent, every tool call — via `CLAUDE_CODE_LOG_DIR`. Works identically whether the agent ran locally or across the SSH fleet.

```yaml
# WORKFLOW.md (SSH section)
agent:
  ssh_hosts:
    - build-worker-1.internal
    - build-worker-2.internal:2222
  dispatch_strategy: round-robin  # or: least-loaded
```

Full reference: **[itervox.dev/configuration/](https://itervox.dev/configuration/)** (`ssh_hosts`, `dispatch_strategy`) and per-profile examples in **[itervox.dev/guides/agent-profiles/](https://itervox.dev/guides/agent-profiles/)**.

---

## Lifecycle hooks

Shell scripts run at lifecycle events inside each workspace, via `bash -lc`.

| Hook | Runs | On failure |
|---|---|---|
| `after_create` | Once, right after the workspace directory is created | Fatal — aborts the run attempt |
| `before_run` | Once per worker invocation, before the first agent turn of that attempt | Fatal — aborts the run attempt |
| `after_run` | After each completed agent turn | Logged and ignored |
| `before_remove` | Before the workspace is removed (`auto_clear` or manual clear/remove) | Logged and ignored |

`before_run` is intentionally per-attempt, not per-turn, so setup hooks like `git reset --hard` do not wipe agent progress between turns. On an in-place `input_required` resume, Itervox skips `before_run`; if the workspace had to be recreated first, the normal setup hooks run again.

```yaml
hooks:
  after_create: |
    git clone git@github.com:org/repo.git .
    pnpm install --frozen-lockfile
  before_run: |
    git fetch origin
    git checkout main
    git reset --hard origin/main
  after_run: |
    git status --short
  before_remove: |
    tar -czf ../workspace-backup.tgz .
  timeout_ms: 120000
```

---

## Quick Start

```bash
# 1. Install
brew tap vnovick/tap && brew install itervox

# 2. Scaffold a WORKFLOW.md from your repo metadata
cd path/to/your/project
itervox init --tracker linear      # or: --tracker github

# 3. Store credentials (auto-loaded, gitignored)
mkdir -p .itervox
cat > .itervox/.env <<'EOF'
LINEAR_API_KEY=lin_api_...
# GITHUB_TOKEN=ghp_...
EOF

# 4. Run it
itervox
open http://127.0.0.1:8090
```

<div align="center">
  <img src="https://raw.githubusercontent.com/vnovick/itervox/main/site/public/screenshots/cli-init-output.png" width="720" alt="itervox init terminal output" />
</div>

### Common commands

| Command | Description |
|---|---|
| `itervox` | Start the orchestrator (reads `WORKFLOW.md` in the current directory) |
| `itervox init --tracker <linear\|github>` | Scaffold a `WORKFLOW.md` from your repo metadata |
| `itervox clear [IDENTIFIER…]` | Remove workspace directories (all, or specific issues) |
| `itervox --version` | Print version, commit, and build date |
| `itervox help` | Show all commands and run-mode flags |

Full CLI reference: **[itervox.dev/cli/](https://itervox.dev/cli/)**.

---

## WORKFLOW.md at a glance

One file per project. YAML front matter plus a Liquid prompt template.

```markdown
---
tracker:
  kind: linear                     # or: github
  api_key: $LINEAR_API_KEY
  project_slug: your-project-slug
  active_states: ["Todo", "In Progress"]
  completion_state: "In Review"

agent:
  command: claude --model claude-opus-4-6
  max_concurrent_agents: 5
  max_turns: 20
  profiles:
    code-reviewer:
      command: claude --model claude-opus-4-6
      prompt: "You are a senior code reviewer. Focus on correctness and test coverage."

workspace:
  root: ~/.itervox/workspaces
  auto_clear: true

server:
  port: 8090
---

You are working on {{ issue.identifier }} — {{ issue.title }}.

{{ issue.description }}

Implement the change, run tests, and open a PR.
```

The prompt template has access to `issue.*` fields (`identifier`, `title`, `description`, `state`, `priority`, `labels`, `blocked_by`, …) and the `attempt` counter on retries.

Full field reference: **[itervox.dev/configuration/](https://itervox.dev/configuration/)**.

`workspace.auto_clear` and `agent.auto_review` cannot be enabled together. Auto-review runs after the main worker finishes and needs that workspace and branch state to still exist.

---

## Architecture

Itervox is a single static Go binary that runs a continuous orchestration loop.

```
  WORKFLOW.md ──▶ Orchestrator (single-goroutine state machine)
                    │
                    ├─ every poll_interval_ms:
                    │    reconcile running sessions · fetch candidate issues
                    │    render Liquid prompt · spawn claude / codex subprocess
                    │
                    └─ per-issue worker goroutines stream stream-json events
                       back into the event loop. No locks on the hot path.
```

**Key design decisions:**

- **Single-goroutine state machine.** All orchestrator state mutations happen in one goroutine. Workers communicate back via a buffered event channel — no locks on the hot path.
- **New subprocess per turn.** Claude Code uses `--resume <session-id>` for continuity instead of a persistent app-server process. Conversation history lives server-side.
- **Isolated workspaces.** Each issue gets its own directory under `workspace.root`. Path containment is enforced via `filepath.EvalSymlinks` before every launch — symlink escapes are rejected at runtime.
- **Live config reload.** `WORKFLOW.md` is watched with a 1-second content-hash poller. Config changes hot-reload without restarting in-flight sessions.
- **Embedded dashboard.** The Vite/React bundle is embedded into the Go binary. No sidecar process, no Docker.

### Security posture

Itervox is designed for **high-trust local environments**:

- Claude runs with `--dangerously-skip-permissions`. Only run against code you trust.
- API tokens are never logged. Use `$ENV_VAR` references in config; values are resolved at runtime.
- The HTTP server binds `127.0.0.1` by default. Optional shared-token API auth for remote access.
- Hook scripts run with full shell access in the workspace directory. Treat them as trusted code.
- SSH workers use standard host key verification via `~/.ssh/known_hosts`.

### Platform support

| Surface | macOS | Linux | Windows |
|---|---|---|---|
| Orchestrator + web dashboard | Supported | Supported | Unsupported (use WSL2) |
| TUI | Supported | Supported | Unsupported (use WSL2) |
| Local agent execution | Supported | Supported | Unsupported (use WSL2) |
| SSH worker hosts | Supported | Supported | Unsupported (use WSL2) |

Windows is **unsupported**: Itervox relies on a POSIX shell for hooks and uses worktree paths that do not translate cleanly to Windows. Use WSL2, or run Itervox against Linux/macOS workers.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the dev loop, the `make` targets, the web dashboard HMR workflow, the QA lanes, and the pre-commit hook policy. The project uses `make verify` as the main CI-equivalent gate (fmt, vet, lint, Go tests with `-race`, web coverage/build, `web-spelling`, `size-budget`, and `no-os-exit`), with `make qa-current` covering the current-functionality Playwright baseline. Before tagging a release, run `make release-check` after committing release changes; it adds `govulncheck`, `goreleaser check`, and the GoReleaser hook dirty-worktree guard.

Bug reports, feature requests, and PRs are welcome. Join the [Discord](https://discord.gg/ATU5n3yZNX) to show your `WORKFLOW.md`, ask questions, or propose ideas.

---

## Sponsor

If Itervox saves you time, please consider sponsoring development: **[github.com/sponsors/vnovick](https://github.com/sponsors/vnovick)**. Sponsorships fund new agent backends, tracker integrations, and operator tooling.

---

## License

Apache 2.0. See [LICENSE](LICENSE).
