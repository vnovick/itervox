# Configuration Reference

Itervox is configured via a single `WORKFLOW.md` file in your project root
(or wherever you point `--workflow`). The file contains a YAML front matter
block followed by a Liquid-templated agent prompt.

**Note:** `server.port` is required for the dashboard. If omitted, no HTTP server is started.

```markdown
---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
agent:
  command: claude
workspace:
  root: ~/.itervox/workspaces
server:
  port: 8090
---

You are working on {{ issue.identifier }} — {{ issue.title }}.
{{ issue.description }}
```

The prompt template is re-rendered on every agent turn. It has access to
`issue.*` (identifier, title, description, state, priority, labels,
blocked_by, branch_name, …) and the `attempt` counter on retries.

Any string value of the form `$VAR_NAME` is substituted with the corresponding
environment variable at load time. Unset variables resolve to an empty string.

The canonical schema lives in `internal/config/config.go`. Runtime-editable
fields are also mutable via the dashboard Settings page and persist back to
`WORKFLOW.md` automatically.

---

## `tracker`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `kind` | string | yes | — | Tracker backend: `linear` or `github` |
| `api_key` | string | yes | — | API key. Use `$ENV_VAR` for env var substitution |
| `project_slug` | string | github: yes | `""` | GitHub: `owner/repo`. Linear: optional project slug filter |
| `endpoint` | string | no | Linear: `https://api.linear.app/graphql`; GitHub: provider default | Override the API endpoint |
| `active_states` | []string | no | `["Todo","In Progress"]` | Issue states considered ready to work |
| `terminal_states` | []string | no | `["Closed","Cancelled","Canceled","Duplicate","Done"]` | States treated as permanently done |
| `backlog_states` | []string | no | Linear: `["Backlog"]`, GitHub: `[]` | Always fetched; shown as leftmost Kanban column(s) |
| `working_state` | string | no | `"In Progress"` | State assigned when an agent starts. Empty string disables the transition |
| `completion_state` | string | no | `""` | State assigned on successful completion. When set, the issue leaves `active_states` so it is not re-dispatched |
| `failed_state` | string | no | `""` | State assigned when max retries are exhausted. When empty, failed issues are paused instead |

---

## `polling`

| Field | Type | Default | Description |
|---|---|---|---|
| `interval_ms` | int | `30000` | How often to poll the tracker for new issues (milliseconds) |

---

## `agent`

| Field | Type | Default | Description |
|---|---|---|---|
| `command` | string | `"claude"` | Agent CLI command (e.g. `claude`, `codex`, `/abs/path/to/wrapper`) |
| `backend` | string | `""` | Explicit backend override when `command` is a wrapper. One of `claude`, `codex`. Inferred from `command` when empty |
| `max_concurrent_agents` | int | `10` | Global cap on parallel agents |
| `max_concurrent_agents_by_state` | map[string]int | `{}` | Per-state concurrency cap (state keys lowercased), e.g. `{"in progress": 3}` |
| `max_turns` | int | `20` | Maximum turns per issue before aborting |
| `turn_timeout_ms` | int | `3600000` | Hard wall-clock limit for the entire agent session (ms). `0` disables |
| `read_timeout_ms` | int | `30000` | Per-read timeout on subprocess stdout. Aborts if no bytes for this long |
| `stall_timeout_ms` | int | `300000` | Orchestrator-level inactivity timeout. `≤ 0` disables stall detection |
| `max_retry_backoff_ms` | int | `300000` | Exponential back-off cap between retries (10 s × 2^(n−1), capped here). Set to `0` to disable retries |
| `max_retries` | int | `5` | Maximum retry attempts before moving to `failed_state`. `0` means unlimited |
| `base_branch` | string | `""` (auto-detect) | Remote base branch for PR diff enrichment (e.g. `origin/main`). Auto-detected via `git symbolic-ref` when empty |
| `inline_input` | bool | `false` | When `true`, agent input-required signals post as tracker comments instead of waiting in the dashboard UI |
| `rate_limit_error_patterns` | []string | `[]` | Custom substrings for detecting rate-limit errors in agent stderr. Empty falls back to built-in defaults (`rate_limit_exceeded`, `rate limit`, `429`, `quota`, `too many requests`). WORKFLOW.md only |
| `max_switches_per_issue_per_window` | int | `2` | Maximum times a `rate_limited` automation can switch an issue's profile/backend within `switch_window_hours`. `0` for unlimited. Runtime-editable |
| `switch_window_hours` | int | `6` | Rolling window (hours) for the `max_switches_per_issue_per_window` cap. Runtime-editable |
| `switch_revert_hours` | int | `0` | TTL (hours) after which an auto-applied profile/backend switch is reverted on the next poll cycle, returning the issue to its original profile and backend. `0` disables the revert. Operator-set overrides survive. WORKFLOW.md only |
| `ssh_hosts` | []string | `[]` | SSH worker hosts (`host` or `host:port`). Empty = run locally. Runtime-editable |
| `ssh_host_descriptions` | map[string]string | `{}` | Optional display labels for `ssh_hosts`, shown in the dashboard/TUI. Runtime-editable |
| `ssh_strict_host_checking` | string | `"accept-new"` | Default `StrictHostKeyChecking` mode for SSH worker connections. Valid: `accept-new` (TOFU — pin on first contact), `yes`, `no`, `ask`, `off`. Defaults to TOFU; rejects mismatched host keys on subsequent connections |
| `ssh_strict_host_by_host` | map[string]string | `{}` | Per-host override for `StrictHostKeyChecking`. Keys are host addresses, values use the same set as `ssh_strict_host_checking`. Useful for hardening production hosts (`yes`) or temporarily relaxing sandbox VMs (`no`) |
| `dispatch_strategy` | string | `"round-robin"` | Routing for SSH hosts. One of `round-robin`, `least-loaded`. Runtime-editable |
| `reviewer_profile` | string | `""` | Name of the profile used for AI code review. Required if `auto_review: true` |
| `auto_review` | bool | `false` | When `true`, dispatches a reviewer worker after every successful worker completion |
| `reviewer_prompt` | string | Built-in default | **Deprecated** — prefer `reviewer_profile`. Liquid template used when no reviewer profile is set |
| `profiles` | map | `{}` | Named agent profiles — see below. Runtime-editable |
| `available_models` | map | `{}` | Backend → model-option list used by the dashboard model picker |

### Agent profiles

Each entry under `profiles:` is a named role selectable per-issue from the
dashboard or the agent queue view. Profile names with empty `command` are
silently dropped at load time. Commands must not contain shell metacharacters
(`;|&\`$()><`) — use a wrapper script.

| Field | Description |
|---|---|
| `command` | CLI command for this profile (required) |
| `backend` | Explicit backend override (`claude` or `codex`); inferred from `command` when absent |
| `prompt` | Role description appended to the rendered template whenever this profile is selected for an issue (always — no mode flag). When more than one profile exists, a peer-roster line is also injected so the agent knows which other profiles it can delegate to by name. |
| `enabled` | Optional boolean. Disabled profiles stay in config but are hidden from normal selection and dispatch. |
| `allowed_actions` | Optional list of daemon-backed actions: `comment`, `comment_pr`, `create_issue`, `move_state`, `provide_input`. |
| `create_issue_state` | Required when `allowed_actions` includes `create_issue`; the tracker state/column for follow-up issues. |

`allowed_actions` do not grant shell or tracker access by themselves. They only
allow the daemon to mint short-lived per-run bearer grants for the corresponding
`/api/v1/agent-actions/*` routes.

```yaml
agent:
  reviewer_profile: code-reviewer
  auto_review: true
  profiles:
    fast:
      command: claude --model claude-haiku-4-5
      prompt: "Fix this quickly with minimal changes."
    thorough:
      command: claude --model claude-opus-4-6
    code-reviewer:
      command: claude --model claude-opus-4-6
      prompt: "You are a senior code reviewer. Focus on correctness and test coverage."
      allowed_actions: [comment, move_state]
    codex-research:
      command: run-codex-wrapper --json
      backend: codex
      prompt: "You are a long-horizon investigation agent."
    input-responder:
      command: claude --model claude-sonnet-4-6
      enabled: true
      allowed_actions: [comment, provide_input]
    qa:
      command: claude --model claude-sonnet-4-6
      allowed_actions: [comment, create_issue, move_state]
      create_issue_state: Todo
```

---

## `automations`

Automations dispatch a selected profile when a trigger fires, then add a small
instruction overlay on top of that profile.

Supported triggers:

- `cron`
- `input_required`
- `tracker_comment_added`
- `issue_entered_state`
- `issue_moved_to_backlog`
- `run_failed`
- `pr_opened` — fires when a worker's PR is detected (gap B)
- `rate_limited` — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven. The per-issue switch cap limits or suppresses profile/backend switching; it is not the trigger condition.

Tracker event triggers (`tracker_comment_added`, `issue_entered_state`, and
`issue_moved_to_backlog`) are derived from the 15-second automation poll loop, not
webhooks. `tracker_comment_added` compares only the latest observed comment; if
multiple comments arrive between polls, the trigger sees the latest one.

| Field | Type | Description |
|---|---|---|
| `id` | string | Stable automation identifier |
| `enabled` | bool | Whether the automation is active |
| `profile` | string | Name of the agent profile to dispatch |
| `instructions` | string | Markdown/Liquid instruction overlay appended after the selected profile prompt |
| `trigger.type` | string | Trigger type |
| `trigger.cron` | string | Five-field cron expression for `cron` triggers |
| `trigger.timezone` | string | Optional IANA timezone name (`UTC`, `America/New_York`, …) for `cron` triggers. Blank = daemon timezone. Ignored by non-cron triggers. The Settings UI offers a typeahead dropdown |
| `trigger.state` | string | Required for `issue_entered_state`; the state that must be entered |
| `filter.match_mode` | string | How populated filters combine: `all` or `any` |
| `filter.states` | []string | Issue-state filter. For cron automations, leave empty to use backlog and active states |
| `filter.labels_any` | []string | Match issues with at least one listed label |
| `filter.identifier_regex` | string | Regex matched against issue identifiers like `ENG-42` |
| `filter.limit` | int | Maximum issues to queue from one cron tick or event poll batch |
| `filter.input_context_regex` | string | Only for `input_required`; matched against the blocked-agent question text |
| `filter.max_age_minutes` | int | Only for `input_required`; skips blocked entries older than this many minutes. `0`/absent means no age limit |
| `policy.auto_resume` | bool | For `input_required`, allows the helper to resume the blocked run via `provide_input`. For `rate_limited`, accepted as a compatibility alias for `policy.auto_switch` |
| `policy.auto_switch` | bool | Required for `rate_limited` automatic profile/backend switching; allows immediate switching without a human approval step |
| `policy.switch_to_profile` | string | Required for `rate_limited`; profile to use for the switched run |
| `policy.switch_to_backend` | string | Optional `claude`/`codex` backend override for `rate_limited` switched runs |
| `policy.cooldown_minutes` | int | Optional cooldown for `rate_limited` rules on the same issue/profile tuple. Default is 30 when unset |

```yaml
automations:
  - id: qa-ready
    enabled: true
    trigger:
      type: issue_entered_state
      state: "Ready for QA"
    profile: qa
    instructions: |
      Run the QA routine for this issue.
      Comment the results.
      If any required check fails, move the issue to Todo.

  - id: pm-backlog-review
    enabled: true
    trigger:
      type: cron
      cron: "0 9 * * 1-5"
      timezone: "Asia/Jerusalem"
    profile: pm
    instructions: |
      Review backlog issues for missing clarity and acceptance criteria.
      Leave one concise comment summarising what is unclear.
    filter:
      states: ["Backlog"]
      limit: 20
```

For a more detailed guide, including trigger semantics, filter behavior, prompt
variables, and worked examples, see `site/src/content/docs/guides/automations.mdx`.

### Migrating from `schedules:` (deprecated)

The legacy `schedules:` block is still parsed and silently upgraded to
equivalent `cron` automations at startup — **but this fallback is deprecated
and will be removed in a future release**. Itervox logs a `slog.Warn` at
startup when a `schedules:` block is seen, with the count of upgraded entries.

To migrate: rewrite each `schedules:` entry as an `automations:` entry with
`trigger.type: cron` plus the same cron expression, timezone, profile, and
state filter. The legacy format has no `instructions:` block, so migrated
entries start with an empty prompt overlay and can optionally add instructions
at migration time.

---

## `workspace`

| Field | Type | Default | Description |
|---|---|---|---|
| `root` | string | `~/.itervox/workspaces` | Root directory for per-issue workspaces. Supports `~` and `$ENV_VAR` |
| `auto_clear` | bool | `false` | Delete the workspace directory after a task reaches the completion state. Logs are preserved separately. Runtime-editable |
| `worktree` | bool | `false` | Enable git-worktree mode: per-issue worktrees inside `root` instead of plain directories. Requires a git repo at `root` |
| `clone_url` | string | `""` | Remote URL used to initialise the bare clone when `worktree: true` and `root` is empty |
| `base_branch` | string | `"main"` | Branch worktrees are created from |

---

## `hooks`

Lifecycle scripts run via `bash -lc` inside each workspace. `after_create` and
`before_run` are fatal on non-zero exit; `after_run` and `before_remove`
failures are logged and ignored.

| Field | Type | Default | Description |
|---|---|---|---|
| `timeout_ms` | int | `60000` | Per-hook execution timeout (ms) |
| `after_create` | string | `""` | Shell script run once, right after the workspace directory is created |
| `before_run` | string | `""` | Shell script run before every agent turn |
| `after_run` | string | `""` | Shell script run after every agent turn |
| `before_remove` | string | `""` | Shell script run before the workspace is removed (auto-clear) |

```yaml
hooks:
  timeout_ms: 60000
  after_create: |
    git clone git@github.com:org/repo.git .
  before_run: |
    git fetch origin && git reset --hard origin/main
```

---

## `server`

| Field | Type | Default | Description |
|---|---|---|---|
| `host` | string | `"127.0.0.1"` | HTTP bind address. Change to `0.0.0.0` to expose to LAN |
| `port` | int | unset → no HTTP server | HTTP listen port. The scaffolded `WORKFLOW.md` defaults to `8090`; if the port is in use, Itervox tries up to 10 successors |
| `allow_unauthenticated_lan` | bool | `false` | When binding to a non-loopback address (e.g. `0.0.0.0`), Itervox requires bearer-token auth on every request and auto-generates an ephemeral `ITERVOX_API_TOKEN` if none is set. Set this flag to `true` to disable that gate — **only** for trusted-LAN air-gapped setups where the daemon is physically unreachable from the public internet. Has no effect on loopback binds. |

### Authentication

Any non-loopback bind requires `Authorization: Bearer <token>` on every HTTP request and on the SSE stream. Itervox reads the token from the `ITERVOX_API_TOKEN` environment variable; if unset, the daemon generates a random ephemeral token at startup and logs it once. The dashboard prompts for the token on first load and persists it (session-only by default, or via "Remember" checkbox in `localStorage`).

The `GET /health` endpoint is auth-exempt so external probes (load balancers, uptime monitors) can verify the daemon is up.

---

## Input-required sentinel

Agents request human input by emitting a literal sentinel token in their
output: `<!-- itervox:needs-input -->`. The orchestrator detects this and
either pauses for a dashboard reply (`agent.inline_input: false`, default) or
posts the question as a tracker comment (`inline_input: true`). The prompt
template that teaches agents how to emit the sentinel is appended
automatically — see `internal/templates/human_input.md`. The canonical
constant is `agent.InputRequiredSentinel` in `internal/agent/events.go`; the
contract is documented in `CONTRIBUTING.md` and `docs/architecture.md`.

---

## Environment variable substitution

Any field value of the form `$VAR_NAME` is replaced with `os.Getenv("VAR_NAME")`
at load time. Unset variables resolve to an empty string. Itervox also
auto-loads `.itervox/.env` and `.env` from the current working directory at
startup (existing env vars are never overwritten).

```yaml
tracker:
  api_key: $LINEAR_API_KEY
workspace:
  root: $ITERVOX_WORKSPACES
```
