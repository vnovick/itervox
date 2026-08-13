# Configuration Reference

Itervox is configured via a single `WORKFLOW.md` file in your project root
(or wherever you point `--workflow`). The file contains a YAML front matter
block followed by a Liquid-templated agent prompt.

**Note:** `server.port` defaults to `8090` — a fixed port, so the dashboard URL is stable across daemon restarts **and** config reloads (the socket stays bound while WORKFLOW.md reloads; only changing `server.host`/`server.port` rebinds it). The bound URL is written to `.itervox/dashboard_url` (read by Vite's dev proxy) and surfaced in `HEARTBEAT.md` + the startup banner. To run several daemons on one machine, give each repo a distinct explicit `server.port`, or set `server.port: 0` to let the OS pick a free port per daemon. If the port is already taken, startup fails loudly naming the holding process — Itervox never silently shifts to a neighbouring port. Previous v0.1.x behaviour (`port omitted → no HTTP server`) is removed; if you genuinely want to run without the dashboard, kill the dashboard process or run `itervox` with a binary that omits the listener (currently not supported via config).

```markdown
---
itervox_schema_version: 2

tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
agent:
  command: claude
workspace:
  root: ~/.itervox/workspaces
server:
  port: 0  # 0 = OS picks a free port (recommended for multi-repo setups); pin a number for a stable URL
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
| `outbox` | bool | no | `true` | Enables the write-ahead outbox for tracker state transitions and comments: writes are persisted durably (`.itervox/outbox.json`) and flushed by an independent worker instead of being made synchronously from the orchestrator's completion/failed-state paths. Set `false` as a kill switch to restore the old synchronous behavior. Load-time only (no runtime setter). Pending/degraded entries are visible in the dashboard's Outbox panel and LiveOps tile, with per-entry Retry/Discard controls; an issue a human moves out of `active_states` while a write is still pending cannot auto-reconcile against that tracker change, so Discard is the operator remedy for a stuck entry |

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
| `max_automation_queue_length` | int | `100` | Maximum durable automation dispatch entries waiting for capacity or dependency resolution. `0`/negative values fall back to the default; the queue is never unlimited |
| `max_turns` | int | `20` | Maximum turns per issue before aborting |
| `turn_timeout_ms` | int | `3600000` | Hard wall-clock limit for the entire agent session (ms). `0` disables |
| `read_timeout_ms` | int | `30000` | Per-read timeout on subprocess stdout. Aborts if no bytes for this long |
| `stall_timeout_ms` | int | `300000` | Orchestrator-level inactivity timeout. `≤ 0` disables stall detection |
| `max_retry_backoff_ms` | int | `300000` | Exponential back-off cap between retries (10 s × 2^(n−1), capped here). `0`/negative values fall back to the default; use `max_retries` to control retry count |
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
| `reviewer_profiles` | []string | `[]` | Ordered list of reviewer profiles for multi-reviewer fan-out. When it has more than one entry, each reviewer runs **sequentially and independently** over the same issue and records a verdict; the combined result is decided by `review_quorum`. Empty falls back to `reviewer_profile`, so existing single-reviewer configs are unaffected. Sequential rather than concurrent because `State.Running` is keyed by issue — fan-out buys independence of judgement, not wall-clock |
| `review_quorum` | string | `"any_block"` | How multiple reviewer verdicts combine. One of `any_block` (default — a single blocking verdict blocks), `majority` (strictly more than half), or `unanimous` (every reviewer must block). The default is the strictest on purpose: adding a reviewer must never make it *easier* to ship. A reviewer that runs but records no parseable verdict counts as a **block**, so a crashing reviewer cannot shrink the quorum until the gate passes |
| `auto_review` | bool | `false` | When `true`, dispatches a reviewer worker after every successful worker completion |
| `reviewer_prompt` | string | Built-in default | **Deprecated** — prefer `reviewer_profile`. Liquid template used when no reviewer profile is set |
| `profiles` | map | `{}` | Named agent profiles — see below. Runtime-editable |
| `available_models` | map | discovered at init | Backend → model-option list used by the dashboard model picker. Populated by `itervox init` from the Anthropic / OpenAI APIs when keys are set (fallback: hardcoded defaults). Refresh after a new model release via `itervox models refresh` (CLI) or the dashboard Settings → Models → **Refresh** button (HTTP `POST /api/v1/settings/models/refresh`). |
| `pause_dispatch_when_any_in_state` | []string | `[]` | When ANY tracked issue is in one of these case-insensitive state names, no new dispatch begins. Use case: pause Todo dispatch while any issue is "In Review" so PRs queue/merge before the next start. Empty disables the guard. Load-time only (no runtime setter) |
| `merge_strategy` | string | `"squash"` | Default merge strategy for the daemon-backed `merge_pr` agent action. One of `squash`, `rebase`, `merge`. Per-request `strategy` field on the action body overrides per-call |
| `merge_block_labels` | []string | `["needs-human","migration","auth","feature-flag","breaking"]` | Case-insensitive PR labels that cause the `merge_pr` action to refuse the merge with reason `blocked_label:<label>`. Empty list disables the guard |
| `allow_unchecked_merge` | bool | `false` | When `false` (default), the `merge_pr` action refuses to merge a PR on a repo with zero required checks configured (reason `unarmed_gate:...`) instead of merging with no CI coverage. Set `true` to merge anyway; the daemon still logs a loud warning |
| `transport_error_patterns` | []string | `["stream disconnected","connection reset","i/o timeout"]` | Substrings (case-insensitive) that classify an agent-runner error as a transient transport failure rather than a generic failure. Increments `state.TransportFailureCount` when matched |
| `sort.prefer_high_outdegree` | bool | `false` | **Deprecated** — aliased to `dependencies.ordering: critical_path`. When `true` and `dependencies.ordering` was not explicitly set in the front matter, the daemon logs a one-time `slog.Warn` and behaves as if `dependencies.ordering: critical_path` were set (critical_path is already the default, so the net runtime effect is unchanged — only the tiebreaking got more precise). If `dependencies.ordering` **was** explicitly set, that value wins and the flag is a no-op. Prefer setting `dependencies.ordering` directly; this field will be removed in a future release |
| `deps_analyzer_profile` | string | `""` | Profile name used by the dashboard's "Analyze dependencies" sidecar. Empty disables the analyzer button |
| `deps_analyzer_timeout_ms` | int | `600000` | Wall-clock limit for one analyzer job end to end, across all chunks. `≤ 0` falls back to the default (matches the dashboard's 10-minute poll deadline) |
| `deps_analyzer_chunk_size` | int | `75` | Maximum issues sent to the agent in one analyzer turn. Larger backlogs are split into sequential chunks; relations spanning two chunks are not examined (the accepted blind spot — logged at analysis time). Raise this if you need full-graph fidelity over a larger backlog and can tolerate a longer/costlier turn. `≤ 0` falls back to the default |

### Agent profiles

Each entry under `profiles:` is a named role selectable per-issue from the
dashboard or the agent queue view. In schema `2`, profile text lives in
files under `.itervox/agents/<profile>/`; `WORKFLOW.md` points at those files.
Commands must not contain shell metacharacters (`;|&\`$()><`) — use a wrapper
script.

| Field | Description |
|---|---|
| `command` | CLI command for this profile (required) |
| `backend` | Explicit backend override (`claude` or `codex`); inferred from `command` when absent |
| `soul_file` | Path to this profile's `SOUL.md`, relative to `WORKFLOW.md` when not absolute. Holds identity, purpose, boundaries, and collaboration style. |
| `instructions_file` | Path to this profile's `INSTRUCTIONS.md`, relative to `WORKFLOW.md` when not absolute. Holds workflow rules, checklists, and done criteria. |
| `enabled` | Optional boolean. Disabled profiles stay in config but are hidden from normal selection and dispatch. |
| `allowed_actions` | Optional list of daemon-backed actions: `comment`, `comment_pr`, `create_issue`, `move_state`, `provide_input`. |
| `create_issue_state` | Required when `allowed_actions` includes `create_issue`; the tracker state/column for follow-up issues. |

`SOUL.md` is appended before `INSTRUCTIONS.md`, and both files support the same
Liquid bindings as the main workflow prompt. Automation `instructions` are
appended after the selected profile files. `agent.profiles.*.prompt` is legacy
input for `itervox init --update`; schema `2` rejects it at daemon startup.
The dashboard profile editor edits `SOUL.md` and `INSTRUCTIONS.md` separately
and writes those files before refreshing the snapshot.

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
      soul_file: .itervox/agents/fast/SOUL.md
      instructions_file: .itervox/agents/fast/INSTRUCTIONS.md
    thorough:
      command: claude --model claude-opus-4-6
      soul_file: .itervox/agents/thorough/SOUL.md
      instructions_file: .itervox/agents/thorough/INSTRUCTIONS.md
    code-reviewer:
      command: claude --model claude-opus-4-6
      soul_file: .itervox/agents/code-reviewer/SOUL.md
      instructions_file: .itervox/agents/code-reviewer/INSTRUCTIONS.md
      allowed_actions: [comment, move_state]
    codex-research:
      command: run-codex-wrapper --json
      backend: codex
      soul_file: .itervox/agents/codex-research/SOUL.md
      instructions_file: .itervox/agents/codex-research/INSTRUCTIONS.md
    input-responder:
      command: claude --model claude-sonnet-4-6
      soul_file: .itervox/agents/input-responder/SOUL.md
      instructions_file: .itervox/agents/input-responder/INSTRUCTIONS.md
      enabled: true
      allowed_actions: [comment, provide_input]
    qa:
      command: claude --model claude-sonnet-4-6
      soul_file: .itervox/agents/qa/SOUL.md
      instructions_file: .itervox/agents/qa/INSTRUCTIONS.md
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
- `pr_merged` — fires when a PR opened by an itervox-managed branch transitions to MERGED, either via the daemon-side `merge_pr` action or an externally-observed merge. Trigger context carries `pr_url`, `pr_number`, `merged_sha`, `base_ref`, `merged_at` (P1).
- `rate_limited` — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven. The per-issue switch cap limits or suppresses profile/backend switching; it is not the trigger condition.
- `blockers_resolved` — fires when dependency audit observes a previously blocked issue becoming unblocked.

Tracker event triggers (`tracker_comment_added`, `issue_entered_state`, and
`issue_moved_to_backlog`) are derived from the 15-second automation poll loop, not
webhooks. `tracker_comment_added` compares only the latest observed comment; if
multiple comments arrive between polls, the trigger sees the latest one.

When an automation trigger cannot start immediately for a retryable runtime
reason such as `no_slots`, `per_state_limit`, `already_running`,
`input_required`, `pending_input_resume`, or `blocked_by`, Itervox records a
durable automation queue entry instead of dropping the attempt. The queue is
capped by `agent.max_automation_queue_length`. Saturation pauses
recurring/cron/polled producer intake; one-shot and internal dispatch attempts
are rejected and counted for audit rather than paused. Existing queued entries
continue draining until the queue falls below the low-water mark.

| Field | Type | Description |
|---|---|---|
| `id` | string | Stable automation identifier |
| `enabled` | bool | Whether the automation is active |
| `profile` | string | Name of the agent profile to dispatch |
| `instructions` | string | Markdown/Liquid instruction overlay appended after the selected profile files |
| `trigger.type` | string | Trigger type |
| `trigger.cron` | string | Five-field cron expression for `cron` triggers |
| `trigger.timezone` | string | Optional IANA timezone name (`UTC`, `America/New_York`, …) for `cron` triggers. Blank = daemon timezone. Ignored by non-cron triggers. The Settings UI offers a typeahead dropdown |
| `trigger.state` | string | Required for `issue_entered_state`; the state that must be entered |
| `filter.match_mode` | string | How populated filters combine: `all` or `any` |
| `filter.states` | []string | Issue-state filter. For cron automations, leave empty to use backlog and active states |
| `filter.states_any` | []string | Alias for `filter.states`; recommended for `blockers_resolved` examples to make the source-state policy explicit |
| `filter.labels_any` | []string | Match issues with at least one listed label |
| `filter.identifier_regex` | string | Regex matched against issue identifiers like `ENG-42` |
| `filter.limit` | int | Maximum issues to queue from one cron tick or event poll batch |
| `filter.input_context_regex` | string | Only for `input_required`; matched against the blocked-agent question text |
| `filter.max_age_minutes` | int | Only for `input_required`; skips blocked entries older than this many minutes. `0`/absent means no age limit |
| `filter.body_contains` | []string | Only for `tracker_comment_added`. Case-insensitive substring list (OR-of-list). A comment body that contains none of the listed substrings short-circuits the dispatch before any agent runs. Empty = match all (P0-B) |
| `filter.body_regex` | string | Only for `tracker_comment_added`. Regex pattern applied to the comment body. AND-combines with `body_contains` when both are set (P0-B) |
| `policy.auto_resume` | bool | For `input_required`, allows the helper to resume the blocked run via `provide_input`. For `rate_limited`, accepted as a compatibility alias for `policy.auto_switch` |
| `policy.auto_switch` | bool | Required for `rate_limited` automatic profile/backend switching; allows immediate switching without a human approval step |
| `policy.switch_to_profile` | string | Required for `rate_limited`; profile to use for the switched run |
| `policy.switch_to_backend` | string | Optional `claude`/`codex` backend override for `rate_limited` switched runs |
| `policy.cooldown_minutes` | int | Optional cooldown for `rate_limited` rules on the same issue/profile tuple. Default is 30 when unset |
| `policy.move_to_state` | string | Optional for `blockers_resolved`; allows the helper profile to move matching unblocked issues to this state when the profile includes `move_state` |

When `switch_to_backend` is set, the target profile command must be compatible
with that backend. Prefer a dedicated Codex profile such as `command: codex` /
`backend: codex`, or a backend-aware wrapper command.

### Dependency readiness and blockers

Itervox exposes tracker blockers to the prompt and dashboard, and normal issue
dispatch skips `Todo` issues whose blockers are still non-terminal. That is the
deterministic blocker behavior shipped in v0.2.0.

Automation rules can opt into a deterministic `blockers_resolved` trigger. Core
dependency audit detects when a previously blocked issue has no unresolved
blockers left; tracker mutation still happens only through an enabled automation
whose selected profile is allowed to use `move_state`.

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

  - id: unblock-backlog-to-todo
    enabled: true
    trigger:
      type: blockers_resolved
    profile: pm
    instructions: |
      All tracked blockers for this backlog issue are terminal.
      Move only backlog/Backlog issues to Todo.
      Do not move review, in-review, PR-open, or merged issues.
    filter:
      states_any: ["backlog", "Backlog"]
    policy:
      move_to_state: "Todo"
```

For more detailed guides, including trigger semantics, queue behavior,
dependency audit, and dashboard surfaces, see:

- `docs/automation-queue.md`
- `docs/dependency-management.md`
- `docs/dashboard-deps.md`
- `docs/status-history.md`
- `site/src/content/docs/guides/automations.mdx`

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

## `dependencies`

Settings for the unified dependency graph — how LLM-inferred (non-tracker) blocker
edges factor into automation dispatch gating. See the deps-analyzer agent pass
(`agent.deps_analyzer_profile`) for how inferred edges are produced.

| Field | Type | Default | Description |
|---|---|---|---|
| `inferred_gating` | bool | `true` | Soft-gate kill-switch: when `true`, inferred (non-tracker) dependency edges can hold automation dispatch for their target issue, same as tracker-declared blockers. Set `false` to make inferred edges display-only |
| `confidence_threshold` | float | `0.7` | Minimum analyzer confidence score (`0.0`-`1.0`) an inferred edge must meet to gate dispatch; edges below the threshold are surfaced on the dashboard but never gate. Out-of-range values fall back to the default |
| `staleness_hours` | int | `168` | How long an inferred edge is trusted before it is considered stale and stops gating; non-positive values fall back to the default |
| `ordering` | string | `"critical_path"` | Dispatch ordering strategy for eligible issues. One of `critical_path` (default), `critical_path_strict`, or `simple`. See [Ordering modes](#ordering-modes) below. An unrecognized value falls back to the default with a `slog.Warn` |
| `escalate_blocked_after_hours` | int | `48` | How long an issue may sit blocked before it becomes eligible for the `blockers_resolved`/attention automation surface (`state.DependencyAttention`, kind `stale_blocker`). **`0` is a meaningful, explicit value that disables the escalation** — it is not treated as "absent"; only a negative value falls back to the default (with a `slog.Warn`) |
| `auto_analyze` | bool | `true` | Kill switch for scheduled incremental dependency analysis. When `true`, the daemon periodically re-runs the deps-analyzer pass in the background (fingerprint-scoped to changed issues) without an operator clicking "Analyze dependencies". Set `false` to make analysis strictly manual (dashboard button, API, or CLI) |
| `auto_analyze_min_interval_minutes` | int | `60` | Minimum gap between consecutive scheduled analysis passes. Parsed via `positiveIntField`; non-positive values fall back to the default — there is no meaningful zero here (the analyzer must not run every tick) |
| `auto_analyze_debounce_minutes` | int | `5` | Delay after a dispatch-affecting change before a scheduled analysis pass starts, so analysis waits for state to settle instead of racing an in-flight dispatch. Parsed via `positiveIntField`; non-positive values fall back to the default |

```yaml
dependencies:
  inferred_gating: true
  confidence_threshold: 0.7
  staleness_hours: 168
  ordering: critical_path
  escalate_blocked_after_hours: 48
  auto_analyze: true
  auto_analyze_min_interval_minutes: 60
  auto_analyze_debounce_minutes: 5
```

### Ordering modes

All three modes share the same final tiebreakers (`created_at` ascending with
nil last, then identifier ascending). They differ only in what they consider
before reaching them:

| Mode | Comparison order | Use when |
|---|---|---|
| `critical_path` (default) | priority band → fan-out → chain length | You want operator-set priority to stay authoritative. |
| `critical_path_strict` | fan-out → chain length → priority band | You want throughput across the dependency graph to outrank the priority field. |
| `simple` | priority band only (no graph awareness) | You want the legacy pre-graph behavior. |

"Fan-out" is `TransitiveDependents`: how many issues are transitively unblocked
by finishing this one. "Chain length" is `LongestChain`, the longest downstream
path in edges. Both are computed per tick over the SCC condensation of the
dependency graph, so a cycle collapses to one node and cannot skew the counts.

The distinction that matters between the two critical-path modes is that
**`critical_path` only applies graph leverage as a tiebreaker within a single
priority band.** If your issues carry consistently distinct priorities, the
graph metrics never get consulted and the mode behaves like `simple`. Choose
`critical_path_strict` if you want a blocker that gates a dozen issues to
dispatch ahead of an unrelated urgent leaf.

The tradeoff is real and runs the other way too: `critical_path_strict`
deliberately overrides an explicit operator signal. An issue marked urgent for
a reason outside the graph — a customer escalation, a deadline — will wait
behind a high-fan-out lower-priority blocker. Prefer the default unless you are
specifically optimizing fleet throughput.

Both graph-aware modes degrade to exactly `simple`'s ordering when the issue
set has no dependency edges, so enabling either on a project without blockers
changes nothing.

---

## `workspace`

| Field | Type | Default | Description |
|---|---|---|---|
| `root` | string | `~/.itervox/workspaces` | Root directory for per-issue workspaces. Supports `~` and `$ENV_VAR` |
| `auto_clear` | bool | `false` | Delete the workspace directory **only when the issue reaches a terminal tracker state** — `completion_state` after success, or `failed_state` after retries are exhausted. The workspace persists across retries, input-required pauses, stalls, and pipeline mid-states so chained profiles can share `.itervox/handoff/` files on the same branch. Logs live in a separate dir and are preserved. Runtime-editable. **Compatible with `agent.auto_review`** — the clear is deferred until after the reviewer also completes. **Breaking change in v0.2.0**: previous semantics cleared after every successful run |
| `worktree` | bool | `false` | Enable git-worktree mode: per-issue worktrees inside `root` instead of plain directories. Requires a git repo at `root` |
| `clone_url` | string | `""` | Remote URL used to initialise the bare clone when `worktree: true` and `root` is empty |
| `base_branch` | string | `"main"` | Branch worktrees are created from |

### Agent handoff (`.itervox/handoff/`)

Each worker run can leave a Markdown deliverable at `.itervox/handoff/<ISO8601-timestamp>_<profile-name>.md` on the issue's worktree branch. Before dispatching the next worker for the same issue, the orchestrator reads every `.md` file in that directory in chronological order (the ISO8601 filename prefix sorts lexicographically), applies a token budget (default 30 KB — oldest dropped first with a `[earlier handoffs truncated]` marker), and inlines the result into the agent's prompt as a `## Prior Agent Handoffs` block. A `## Run Context` block follows with two values the agent uses to write its own deliverable:

- `run.timestamp` — the ISO8601 dispatch timestamp (filename-safe form)
- `run.handoff_path` — the canonical destination for this run's handoff file

When a worker exits with `TerminalFailed` or `TerminalStalled`, the orchestrator renames the most recent matching `<timestamp>_<profile>.md` to `<timestamp>_<profile>.partial.md` so subsequent agents can distinguish a crash-mid-deliverable from a clean handoff. `TerminalInputRequired` does not mark partial — the agent intentionally paused.

The directory is committable: `itervox init` and `itervox init --update` patch the root `.gitignore` to whitelist `!.itervox/handoff/**` alongside `!.itervox/agents/**`. Commit the pipeline trail into PRs so reviewers can read the chain.

See the [Agent Handoff guide](https://itervox.dev/guides/agent-handoff/) for a worked example.

---

## `hooks`

Lifecycle scripts run via `bash -lc` inside each workspace. `after_create` and
`before_run` are fatal on non-zero exit; `after_run` and `before_remove`
failures are logged and ignored by default.

To make `after_run` a per-unit completion gate, set
`hooks.after_run_required: true`: a worker whose final `after_run` hook exits
non-zero fails the unit instead of completing it, so "done" requires the
operator's gate (e.g. `make test`) to pass — not just the agent's clean exit.

| Field | Type | Default | Description |
|---|---|---|---|
| `timeout_ms` | int | `60000` | Per-hook execution timeout (ms) |
| `after_create` | string | `""` | Shell script run once, right after the workspace directory is created |
| `before_run` | string | `""` | Shell script run before every agent turn |
| `after_run` | string | `""` | Shell script run after every agent turn |
| `after_run_required` | bool | `false` | When `true`, a failing final `after_run` hook blocks the unit from completing (per-unit gate) |
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
| `port` | int | `8090` | HTTP listen port. `0` = OS picks a free port (for running several daemons at once). If the configured port is in use, startup fails loudly naming the holder — no silent shifting |
| `allow_unauthenticated` | bool | `false` | By default Itervox requires bearer-token auth on every request, on every bind — including loopback (`127.0.0.1`) — and auto-generates an ephemeral `ITERVOX_API_TOKEN` if none is set. Set this flag to `true` to disable that gate entirely — **only** for trusted, fully local setups where the daemon is physically unreachable from anyone else. Has an effect on every bind, not just non-loopback ones: a loopback daemon behind a tunnel or reverse proxy is just as exposed as one bound to `0.0.0.0`, which is why the gate is no longer bind-address-scoped. Renamed from `allow_unauthenticated_lan`; the old key still parses and works identically, but logs a deprecation warning at startup — new configs should use `allow_unauthenticated`. |

### Authentication

Every bind — including loopback — requires `Authorization: Bearer <token>` on every HTTP request and on the SSE stream, unless `server.allow_unauthenticated: true` is set. Itervox reads the token from the `ITERVOX_API_TOKEN` environment variable; if unset, the daemon generates a random ephemeral token at startup, installs it, and logs the tokened dashboard URL to stderr once. The dashboard prompts for the token on first load and persists it (session-only by default, or via "Remember" checkbox in `localStorage`).

The `GET /api/v1/health` endpoint is auth-exempt so external probes (load balancers, uptime monitors) can verify the daemon is up. It is the only auth-exempt route.

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

## `.itervox` project files

`itervox init` creates `.itervox/.gitignore`, `.itervox/.env`, and starter
profile files. Commit `.itervox/agents/**`: those files are project agent
definitions. Do not commit `.itervox/.env`, `.itervox/HEARTBEAT.md`, logs,
runtime queue files, or other generated daemon state.

When a daemon starts, it writes `.itervox/HEARTBEAT.md` atomically with the
current workflow path, schema version, dashboard URL, tracker/project,
capacity, automation queue pressure, dependency audit summary, input-required
count, retry count, and last notable error. Agents can read it when they need
current daemon state; it is generated runtime state, not prompt text.
