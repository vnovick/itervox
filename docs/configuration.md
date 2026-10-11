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
| `outbox` | bool | no | `true` | Enables the write-ahead outbox for tracker state transitions and comments: writes are persisted durably (`.itervox/outbox.json`) and flushed by an independent worker instead of being made synchronously from the orchestrator's completion/failed-state paths. Set `false` as a kill switch to restore the old synchronous behavior. Load-time only (no runtime setter). Pending/degraded entries are visible in the dashboard's Outbox panel and LiveOps tile, with per-entry Retry/Discard controls; an entry enqueued with no observed from-state baseline (currently only the issue-discard path) is exempt from supersede-reconciliation, so Discard is the operator remedy for a stuck entry. Comments carry an idempotency key, so a retry checks whether the earlier attempt landed before posting again (never a duplicate; an unanswerable lookup defers). A write rejected by a tracker rate limit waits for the published reset, shows a "rate limited until HH:MM" chip, and does not count toward the degraded badge. With `outbox: false`, input-required questions and replies are posted with a single direct attempt instead of being queued, so they are not durable and not ordered, and replies are matched to the most recent question by position. Operator comments posted from the dashboard (`POST /api/v1/issues/{id}/comment`) also go through the outbox when it is enabled; with `outbox: false` they are posted directly (`200`), and entries left from an earlier outbox-on run stay visible with Retry/Discard but are delivered only once the outbox is enabled again. If the outbox cannot store an input-required question (for example `.itervox/` is not writable), the question is posted directly once instead, without a key |

---

## `polling`

| Field | Type | Default | Description |
|---|---|---|---|
| `interval_ms` | int | `30000` | How often to poll the tracker for new issues (milliseconds) |
| `rate_limit_reserve_percent` | int | `10` | Share of the tracker's request budget held back for **writes**. When the remaining budget falls below it, Itervox stops spending requests on polling reads so state transitions, comments and the input-resume path can still land. Set `0` to disable shedding entirely. Separately, when the tracker actually rate-limits a request (Linear: HTTP 400 `RATELIMITED`; GitHub: 429/403), the published reset is recorded once for every caller, writes are admitted ahead of reads when it lifts (Linear requests are classified by GraphQL operation), calls fail fast when the reset is more than 60s away, and a recorded window is capped at 2 hours. |

---

## `agent`

| Field | Type | Default | Description |
|---|---|---|---|
| `command` | string | `"claude"` | Agent CLI command (e.g. `claude`, `codex`, `/abs/path/to/wrapper`) |
| `backend` | string | `""` | Explicit backend override when `command` is a wrapper. One of `claude`, `codex`. Inferred from `command` when empty. A backend that disagrees with a command whose binary is `claude` or `codex` (this field, a profile `backend`, a per-issue backend pin, or a `rate_limited` `switch_to_backend`) is refused: the command keeps its own backend and the daemon logs a warning, because the other runner cannot execute it |
| `max_concurrent_agents` | int | `10` | Global cap on parallel agents |
| `max_concurrent_agents_by_state` | map[string]int | `{}` | Per-state concurrency cap (state keys lowercased), e.g. `{"in progress": 3}` |
| `max_automation_queue_length` | int | `100` | Maximum durable automation dispatch entries waiting for capacity or dependency resolution. `0`/negative values fall back to the default; the queue is never unlimited |
| `max_turns` | int | `20` | Maximum turns per issue before aborting |
| `turn_timeout_ms` | int | `3600000` | Hard wall-clock limit for the entire agent session (ms). `0` disables |
| `read_timeout_ms` | int | `30000` | Per-read timeout on subprocess stdout. Aborts if no bytes for this long |
| `stall_timeout_ms` | int | `300000` | Orchestrator-level inactivity timeout. `≤ 0` disables stall detection |
| `max_retry_backoff_ms` | int | `300000` | Exponential back-off cap between retries (10 s × 2^(n−1), capped here). `0`/negative values fall back to the default; use `max_retries` to control retry count |
| `max_retries` | int | `5` | Maximum retry attempts before moving to `failed_state`. `0` means unlimited. When the failed turn carried a vendor limit signal, the retry waits for the vendor's reset time or retry delay (at most 6 hours) instead of the normal back-off, if that is longer |
| `base_branch` | string | `""` (auto-detect) | Remote base branch for PR diff enrichment (e.g. `origin/main`). Auto-detected via `git symbolic-ref` when empty |
| `pr_footer` | bool | `false` | Append a one-line "Shipped with Itervox" footer to each PR an agent run opens on its own branch (not PRs merely linked from the issue), once per PR (hidden `<!-- itervox:shipped -->` marker; added with `gh pr edit` after a successful worker run). README badge: `[![Shipped with Itervox](https://img.shields.io/badge/shipped%20with-Itervox-6f42c1)](https://github.com/vnovick/itervox)` |
| `inline_input` | bool | `false` | When an agent needs human input, its question is always posted as a comment on the tracker issue, and a comment on the issue resumes the agent — normally in the same session; after a daemon restart that had to rebuild the entry from tracker comments, a fresh session starts with the question and your reply as context. `false` (default): the dashboard also offers a reply box. `true`: the tracker is the only place to reply — the dashboard reply box is hidden and `POST /api/v1/issues/{id}/provide-input` returns `409 inline_input_enabled`. Automation replies (`itervox action provide-input`) are unaffected. A reply written before the agent's question has actually reached the tracker still counts: with the outbox enabled, any comment created after the question was queued resumes the agent. Runtime-editable from Settings → General. |
| `rate_limit_error_patterns` | []string | `[]` | Custom case-insensitive substrings for detecting rate-limit errors, matched verbatim against the whole failure text (agent-reported failure and CLI stderr). How they combine with the built-in defaults is set by `rate_limit_error_patterns_mode`. Empty falls back to the built-in defaults: `rate_limit_exceeded`, `rate limit`, `http 429`, `status: 429`, `(429)`, `429 too many requests`, `insufficient_quota`, `quota exceeded for`, `usage quota`, `exceeded your current quota`, `too many requests`, `out of extra usage`, `reached the limit for your current claude`, `out of credits`, `resets at`, `you hit your spend cap`, `usage limit`, `session limit`, `weekly limit`, `spend limit`, `spend cap`, `credit balance is too low`, `credits required`, `temporarily limiting requests`, plus the clause rule "`you've hit your` followed by limit wording in the same clause" and a standalone `429` token. The standalone `429` counts only in the agent-reported failure, never in CLI stderr; a bare `quota` never counts. WORKFLOW.md only |
| `rate_limit_error_patterns_mode` | string | `"replace"` | How a non-empty `rate_limit_error_patterns` combines with the built-in defaults: `replace` (default, the earlier behaviour) uses only your list, so a custom list silently turns every default off; `extend` checks your list **and** the defaults. An empty list means the defaults in either mode. Any other value fails config load. WORKFLOW.md only (CORE-100) |
| `max_switches_per_issue_per_window` | int | `2` | Maximum times a `rate_limited` automation can switch an issue's profile/backend within `switch_window_hours`. `0` for unlimited. Also counts `backend_fallback` switches of implementer runs. Runtime-editable. The switch history, cooldowns and cap-comment dedupe survive a daemon restart (stored with the auto-switch overrides in the daemon's `auto_switched.json`) |
| `switch_window_hours` | int | `6` | Rolling window (hours) for the `max_switches_per_issue_per_window` cap. Runtime-editable |
| `switch_revert_hours` | int | `0` | TTL (hours) after which an auto-applied profile/backend switch is reverted on the next poll cycle, returning the issue to its original profile and backend. `0` disables the revert. Operator-set overrides survive. WORKFLOW.md only |
| `backend_fallback` | map | absent | Declarative reroute when a backend hits its usage limit, with reset-time switch-back and a minimum dwell. See [Backend health and fallback](#backend-health-and-fallback-agentbackend_fallback). Read at load time; not runtime-editable |
| `ssh_hosts` | []string | `[]` | SSH worker hosts (`host` or `host:port`). Empty = run locally. Runtime-editable. Each worker needs bash 3.2+ (with process substitution: `/dev/fd` or a writable `TMPDIR`) with `sh`, `cat`, `printf`, `wc` and `sleep` (any SSH server: OpenSSH or Dropbear), and a login profile that does not read stdin. The prompt reaches the agent CLI on its stdin, never as an argument (`claude ... -p`, `codex exec ... -`), so no prompt size is refused on Linux workers (`MAX_ARG_STRLEN`); Claude Code caps piped input at 10 MB. Cancelling a turn (or stopping the daemon) stops the remote agent: when the ssh channel closes, the worker-side wrapper sends `SIGTERM` to the process group it created for the agent, and `SIGKILL` 2 s later; nothing outside that group is signalled, and a profile's `TMOUT` does not affect it |
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

### Backend health and fallback (`agent.backend_fallback`)

**Backend circuit breaker.** Itervox keeps one breaker per agent backend and
worker host (`claude`, `codex`, `claude@build-1`, ...): a limit seen on SSH
host A says nothing about local runs or host B, which may use other
credentials. A breaker opens when:

- a run stops on a usage limit (Claude `rate_limit_event` `rejected`, a
  result with API status 429/402, a Codex "You've hit your usage limit"):
  limited until the vendor's published reset, or for
  `default_cooldown_minutes` (15) when no reset is published; or
- Claude reports `api_retry` for `rate_limit`/`overloaded` three times within
  5 minutes on that backend and host: limited for 5 minutes (or the vendor's
  retry delay, when longer). Fewer retries only mark it `warning`.

A breaker never holds longer than its source can justify: a
`rate_limit_event` reset is capped at 5h15m for the `five_hour` window, 7 days
+ 1h for `seven_day*` windows and 24h for any other window; a reset read from
text (Codex, or a Claude result message) at 6h; an `api_retry` throttle at 1h;
an unknown reset uses `default_cooldown_minutes` (at most 1440). A further
reset is capped and logged, and a persisted value is capped again on load.
A zone-less text reset from an SSH worker (Codex prints the host's local time
with no zone) is treated as unknown, because the daemon cannot tell the
host's zone; the cooldown and one probe re-learn it. To close a breaker
early, use **Clear** on the dashboard chip or
`POST /api/v1/backend-health/clear` (see the API reference).

While a breaker is open, no issue is started on that backend and host.
Workers, retries, reviewers, automation runs and input-required resumes are
all checked. An issue that cannot run anywhere is held with the
`backend_limited` ineligible reason (shown as the issue's "why idle" reason),
never paused, and consumes no retry. Queued automations stay queued with
`backend_limited`. When the reset passes, the breaker half-opens: exactly
one issue is dispatched as a probe, and the others wait. A probe that
succeeds (or stops to ask a question) closes the breaker. A probe that hits
the limit again reopens it. Open breakers survive a restart
(`backend_health.json` in the daemon log directory, next to
`auto_switched.json`; runtime state, never commit it).

**Declarative fallback.** With `agent.backend_fallback`, a limited target is
rerouted instead of held:

```yaml
agent:
  backend_fallback:
    chain: [claude, codex]          # preference order
    profile_map:                    # counterpart profile per backend
      coder:    { codex: coder-codex }
      reviewer: { codex: reviewer-codex }
      default:  { codex: coder-codex }   # issues running agent.command
    on_unmapped: hold               # hold | backend_hint
    default_cooldown_minutes: 15    # when the vendor publishes no reset
    min_dwell_minutes: 30
    switch_back: at_reset           # at_reset | on_success | manual
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `true` when the block is present | `false` keeps the block but turns rerouting off. Without the block there is no rerouting, but the breaker still holds issues on a limited backend. The whole block may also be a boolean: `backend_fallback: false` is off, `true` is on with the defaults. Any other shape (`"off"`, a number, a list, `enabled: "false"`) fails config loading |
| `chain` | []string | `[claude, codex]` | Backends tried in order when the target is limited. Known, distinct backends only |
| `profile_map` | map | `{}` | `source_profile: {backend: target_profile}`. `default` is the key for issues that run `agent.command`. Every target must exist, be enabled and actually run that backend (its command's binary, else its `backend:` for a wrapper). The reverse direction is implied: a target profile maps back through its source row |
| `on_unmapped` | string | `hold` | For a profile with no mapping: `hold` (wait with `backend_limited`) or `backend_hint` (request the chain backend for the issue's own command; works only for wrapper commands — a `claude ...` command is never paired with codex) |
| `default_cooldown_minutes` | int | `15` | How long a breaker stays limited when the vendor published no reset time (1–1440). Also used by the breaker when the block is absent |
| `min_dwell_minutes` | int | `30` | Minimum time a switched issue stays on the fallback before it may switch back. It never blocks a further hop when the fallback itself becomes limited |
| `switch_back` | string | `at_reset` | `at_reset`: the override is cleared once the dwell has passed AND the original backend is no longer limited (its reset, or the cooldown, has passed); the next dispatch goes back through the breaker. `on_success`: cleared by the first successful run after the dwell. `manual`: never cleared automatically (pin a backend, or use `switch_revert_hours`) |

The block is read at load time: edit `WORKFLOW.md` to change it (the normal
reload applies it). There is no settings API for it.

A switched implementer issue keeps its new profile and backend for later
runs (a sticky override, shown on the card as `codex (auto)` and in the
snapshot's `autoSwitches`). Before, a success cleared the override, so the
next dispatch went back to the still-limited backend. Each fallback switch
counts against `max_switches_per_issue_per_window`. With the cap spent, the
issue is held rather than rerouted. Reviewer and automation runs are
rerouted per run and leave the issue's profile alone.

**Precedence** (fallback vs pins vs `rate_limited` rules):

1. An operator's per-issue backend pin (dashboard issue detail, or
   `POST /api/v1/issues/{identifier}/backend`) wins. A pinned issue is never
   rerouted: when its pinned backend is limited it is held with
   `backend_limited`. A pin is refused up front (`409 backend_pin_refused`) when
   the issue's command runs the other backend.
2. `agent.backend_fallback`, for profiles it maps (or every profile with
   `on_unmapped: backend_hint`). When such a run hits a usage limit, the
   fallback alone handles it: the issue is rerouted at once (no retry
   consumed) or held, and `rate_limited` automations are **not** evaluated
   for that exit.
3. `rate_limited` automations, for everything else: unmapped profiles, pinned
   issues, and failures classified from text at retry exhaustion. They keep
   their own cap, cooldown and switch target. A switch target on a limited
   backend is queued with `backend_limited` rather than started.

When both are configured, use `profile_map` for the common Claude ⇄ Codex
pairs and keep `rate_limited` rules for custom mappings or helper runs.

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
| `require_evidence` | Optional list of checks (e.g. `[test, lint, ci]`) a run must prove before the issue moves to `completion_state`: named checks as passing entries in `.itervox/evidence/<profile>.json` (`run.evidence_path`) stamped with the commit they ran on (later commits may only touch `.itervox/`, and no tracked file may have an uncommitted change), `ci` as all-passing PR checks. Without it the run ends input-required with a "Needs evidence" reason. Off by default; must be a list. See the site's [Done needs evidence](https://itervox.dev/configuration/#done-needs-evidence). |

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
- `pr_merged` — fires when a PR opened by an itervox-managed branch transitions to MERGED, either via the daemon-side `merge_pr` action or an externally-observed merge. Trigger context carries `pr_url`, `pr_number`, `merged_sha`, `base_ref`, `merged_at` (P1); in a prompt they bind as `trigger.pr_url`, `trigger.pr_base_branch` and `trigger.pr_branch` (the PR's head branch), which the `merge_pr` action fills from its `gh pr view` (CORE-108).
- `rate_limited` — fires when a worker run hits a vendor usage limit. When the agent reports a structured limit (Claude's `rate_limit_event` with status `rejected` or a result with API status 429/402; a Codex "You've hit your usage limit" error), it fires on the **first** failure, without consuming a retry, also with `max_retries: 0`. Otherwise it fires when the run exhausts its retries and Itervox classifies the terminal failure text as rate-limit-driven. The per-issue switch cap limits or suppresses profile/backend switching; it is not the trigger condition. When no rule dispatches a fallback (cap reached, cooldown, no match), the issue is retried after the vendor's reset time (at most 6 hours).
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
| `analysis_mode` | string | `"auto"` | How the LLM dependency analyzer is triggered. `auto`: on the scheduler's debounce/min-interval rules. `manual`: only via the Deps tab's Analyze button or `POST /api/v1/deps/analyze`. The blocker audit is unaffected. Runtime-editable from Settings → Dependencies. `auto_analyze` (bool) is a deprecated alias: `false` = `manual`. Changing the mode from the dashboard leaves the old `auto_analyze` line in `WORKFLOW.md`; delete it by hand to stop the "both set" warning on every reload |
| `stacked_prs` | bool | `false` | Branch an issue's worktree from its blocker's branch instead of `workspace.base_branch`, when the issue has exactly one live blocker that carries an identifier. Best-effort: several live blockers give no unambiguous base, and a blocker branch missing locally falls back to `base_branch` rather than failing the dispatch (except for the in-review admission below, which waits instead). The blocker's branch is the tracker's branch name when it has one (Linear), else `itervox/<identifier>`; on GitHub a blocker with the `completion_state` label counts as in review. A stacked run's pull request is pointed at the blocker's branch (`run.pr_base_branch` in the prompt, then `gh pr edit --base` after the run), and back to `base_branch` once the dependent is restacked there. An issue whose only unresolved blocker is in `tracker.completion_state` (in review) is dispatched without waiting for the merge, stacked on that blocker's branch; if that branch is not available locally, no agent runs and the issue waits for the blocker. See the site's [Stacked worktrees](https://itervox.dev/configuration/#stacked-worktrees) section |
| `auto_analyze_min_interval_minutes` | int | `60` | Minimum gap between consecutive scheduled analysis passes. Parsed via `positiveIntField`; non-positive values fall back to the default — there is no meaningful zero here (the analyzer must not run every tick) |
| `auto_analyze_debounce_minutes` | int | `5` | Delay after a dispatch-affecting change before a scheduled analysis pass starts, so analysis waits for state to settle instead of racing an in-flight dispatch. Parsed via `positiveIntField`; non-positive values fall back to the default |

```yaml
dependencies:
  inferred_gating: true
  confidence_threshold: 0.7
  staleness_hours: 168
  ordering: critical_path
  escalate_blocked_after_hours: 48
  analysis_mode: auto
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
| `root` | string | `~/.itervox/workspaces/<project>` | Where per-issue workspaces are created. Namespaced per project by default because a workspace directory is keyed by issue identifier alone, and identifiers are only unique within one tracker project (every GitHub repo has a `#1`). Set explicitly to opt out |
| `auto_clear` | bool | `false` | Delete the workspace directory **only when the issue reaches a terminal tracker state** — `completion_state` after success, or `failed_state` after retries are exhausted. The workspace persists across retries, input-required pauses, stalls, and pipeline mid-states so chained profiles can share `.itervox/handoff/` files on the same branch. Logs live in a separate dir and are preserved. Runtime-editable. **Compatible with `agent.auto_review`** — the clear is deferred until after the reviewer also completes. **Breaking change in v0.2.0**: previous semantics cleared after every successful run |
| `worktree` | bool | `false` | Enable git-worktree mode: per-issue worktrees inside `root` instead of plain directories. Requires a git repo at `root` |
| `clone_url` | string | `""` | Remote URL used to initialise the bare clone when `worktree: true` and `root` is empty |
| `base_branch` | string | `"main"` | Branch worktrees are created from |

### Agent handoff (`.itervox/handoff/`)

Each worker run can leave a Markdown deliverable at `.itervox/handoff/<ISO8601-timestamp>_<profile-name>.md` on the issue's worktree branch. Before dispatching the next worker for the same issue, the orchestrator reads every `.md` file in that directory in chronological order (the ISO8601 filename prefix sorts lexicographically), applies a token budget (default 30 KB — oldest dropped first with a `[earlier handoffs truncated]` marker), and inlines the result into the agent's prompt as a `## Prior Agent Handoffs` block. A `## Run Context` block follows with two values the agent uses to write its own deliverable:

- `run.timestamp` — the ISO8601 dispatch timestamp (filename-safe form)
- `run.handoff_path` — the canonical destination for this run's handoff file

Both are also Liquid bindings (`{{ run.timestamp }}`, `{{ run.handoff_path }}`) in the WORKFLOW.md body, profile `SOUL.md` / `INSTRUCTIONS.md` and automation `instructions`.

**Backend switch notice (CORE-101).** When Itervox moved the issue off another backend — a `rate_limited` automation's switch, or an `agent.backend_fallback` reroute — the run gets a `## Backend Switch Notice` block right after the Run Context and before any profile block (so an `instructions_file` cannot drop it). It names the previous backend and profile, the reason, the backend and profile of this run, and the previous backend's published limit reset, and tells the agent that the previous session is not resumed. The same values are Liquid bindings, empty strings on an ordinary run:

- `run.previous_backend`, `run.previous_profile` — what the issue ran on before the switch
- `run.switch_reason` — e.g. `rate_limited automation "switch-to-codex"` or `backend_fallback: claude limited until …`
- `run.limit_resets_at` — the previous backend's vendor-published reset (RFC 3339, UTC), empty when unknown

`run.pr_base_branch` is the branch a pull request from this run should target: the blocker's branch when the worktree is stacked on it (`dependencies.stacked_prs`), otherwise `workspace.base_branch`. A stacked run also gets a `## Stacked Branch` block after the Run Context.

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
| `host` | string | `"127.0.0.1"` | HTTP bind address. Change to `0.0.0.0` to expose to LAN. `ITERVOX_SERVER_HOST` overrides it (see "Bind overrides from the environment") |
| `port` | int | `8090` | HTTP listen port. `0` = OS picks a free port (for running several daemons at once). If the configured port is in use, startup fails loudly naming the holder — no silent shifting. `ITERVOX_SERVER_PORT`, then `PORT`, override it |
| `allow_unauthenticated` | bool | `false` | By default Itervox requires bearer-token auth on every request, on every bind — including loopback (`127.0.0.1`) — and auto-generates an ephemeral `ITERVOX_API_TOKEN` if none is set. Set this flag to `true` to disable that gate entirely — **only** for trusted, fully local setups where the daemon is physically unreachable from anyone else. Has an effect on every bind, not just non-loopback ones: a loopback daemon behind a tunnel or reverse proxy is just as exposed as one bound to `0.0.0.0`, which is why the gate is no longer bind-address-scoped. Renamed from `allow_unauthenticated_lan`; the old key still parses and works identically, but logs a deprecation warning at startup — new configs should use `allow_unauthenticated`. State-changing requests from another origin are still refused with 403 (see Authentication below). |
| `allowed_hosts` | list of strings | `[]` | Extra `Host` header names the dashboard answers when `allow_unauthenticated: true` (DNS-rebinding protection). Without a token every route refuses a request whose `Host` is a DNS name other than `localhost`, the configured `host`, or one listed here, with 403 `host_not_allowed`; IP addresses are always accepted. List the reverse-proxy, tunnel, Tailscale MagicDNS or container service hostname you browse to, without scheme (a `:port` is ignored). Entries are lower-cased and a Unicode name is converted to its punycode (`xn--`) form, which is what browsers send. An entry that could never match — a URL (`https://proxy.example`), a path, userinfo, or a wildcard (`*.ts.net`; wildcards are not supported, list each full name) — fails config load with an error naming it. Ignored in token mode. |
| `metrics.enabled` | bool | `false` | Serve Prometheus metrics at `GET /metrics` (text format) on the dashboard listener. Requires the same bearer token as `/api/v1/*`; off, `/metrics` answers 404. Families, labels and semantics: API reference, "Metrics". Read at startup. |

### Bind overrides from the environment

Containers need `0.0.0.0` and PaaS platforms (Cloud Run, Heroku, Render, …)
inject the port as `$PORT`, so the bind can come from the environment:

| Value | Precedence, highest first |
|---|---|
| host | `ITERVOX_SERVER_HOST` → `server.host` → `127.0.0.1` |
| port | `ITERVOX_SERVER_PORT` → `PORT` → `server.port` → `8090` |

- There is no daemon command-line flag for the host or port.
- `0` means "the OS picks a free port" in an env var as in `server.port`; an
  explicit `server.port: 0` still holds when no env var is set.
- A variable that is **set** but invalid — empty, not an integer, outside
  0–65535, a host with a scheme (`http://…`), a path, whitespace or a
  `host:port` pair — fails startup with an error naming the variable. It is
  never silently ignored. Only the **winning** port variable is validated: a
  junk `PORT` injected by a platform does not matter while
  `ITERVOX_SERVER_PORT` is set.
- An IPv6 literal, bare or bracketed (`::`, `::1`, `[::1]`), or a host name
  is accepted and binds (`[::]:8090`); the listen address and the dashboard
  URL are built with the IPv6 brackets.
- Beware a shell that exports `PORT` for another project: it moves the
  daemon's bind. The `HTTP server listening` log line names the winning
  source (`host_source`, `port_source`).
- The environment is fixed when the process starts: a `WORKFLOW.md` reload
  re-reads the same values, so changing the bind through the environment
  needs a restart.
- The host override feeds the same `server.host` everything else uses, so
  the unauthenticated-mode Host guard accepts the env host by name exactly as
  it would the YAML one, and the dashboard URL written to
  `.itervox/dashboard_url` uses the bound address. The cross-origin guard
  compares `Origin` with the request's `Host`, so it is unaffected.

### Authentication

Every bind — including loopback — requires `Authorization: Bearer <token>` on every HTTP request and on the SSE stream, unless `server.allow_unauthenticated: true` is set. Itervox reads the token from the `ITERVOX_API_TOKEN` environment variable; if unset, the daemon generates a random ephemeral token at startup, installs it, and logs the tokened dashboard URL to stderr once — only when stderr is a terminal or `ITERVOX_PRINT_TOKEN=1` (`--no-print-token` always suppresses it). Headless (systemd, containers, redirected stderr), an auto-generated token is written to `<logs-dir>/api-token` (mode 0600, rewritten on every start) instead, and the log line carries only its path and a `sha256:` fingerprint. The dashboard prompts for the token on first load and persists it (session-only by default, or via "Remember" checkbox in `localStorage`).

**Cross-origin guard in unauthenticated mode.** With `server.allow_unauthenticated: true` there is no bearer check, so Itervox instead refuses cross-origin state-changing requests, which stops a malicious web page in the operator's browser from driving the daemon. A `POST`, `PUT`, `PATCH` or `DELETE` to `/api/v1/*` gets `403` with code `cross_origin_forbidden` when the browser sends `Sec-Fetch-Site: cross-site` or `same-site`, or, for a browser that sends no `Sec-Fetch-Site`, an `Origin` whose host and port differ from the request's `Host` header (`Origin: null` never matches). Only host and port are compared, not the scheme, so a TLS-terminating proxy in front of a plain-HTTP daemon keeps working. Requests with neither header (curl, scripts, the TUI) pass, as do `GET` requests and the SSE streams. The Vite dev server (page on `:5173`, proxying to the daemon) passes on any browser that sends `Sec-Fetch-Site`, which all have since 2023. To call the API from a web page on another origin, run with a token instead: token mode has no origin check, because a browser never attaches the bearer token cross-site. The guard is not access control: anyone who can reach the port with curl still has full access.

**Host guard (DNS rebinding) in unauthenticated mode.** The cross-origin guard cannot stop DNS rebinding: a page on a hostname the attacker controls re-points that name at your daemon's address and then talks to it *same-origin*, so it could otherwise store a profile with an arbitrary `command`, rewrite settings, or read state and logs. With `server.allow_unauthenticated: true`, every route — API, SSE, the dashboard and static files — therefore requires the request's `Host` (port and IPv6 brackets stripped, case-insensitive) to be `localhost`, an IP address, the configured `server.host`, or a name in `server.allowed_hosts`; anything else gets 403 with code `host_not_allowed`. IP addresses always pass because a rebinding attack needs a DNS name — so browsing to `http://192.168.1.20:8090` with `host: 0.0.0.0`, container probes by pod IP, and SSH tunnels to `localhost` all keep working, as does the Vite dev proxy (it forwards `Host: localhost:<port>`). `GET /api/v1/health` is exempt because it returns a constant, and `GET /api/v1/ready` because it returns only booleans and the tracker's rate-limit reset and is probed by whatever name the platform uses. **If you reach an unauthenticated daemon through a reverse proxy, tunnel, MagicDNS or container service hostname, add that name to `server.allowed_hosts`.** Token mode has no Host check: a rebound page never holds the bearer token.

The `GET /api/v1/health` (static liveness) and `GET /api/v1/ready` (event-loop readiness) endpoints are auth-exempt so external probes (load balancers, uptime monitors, container health checks) can use them without a token. They are the only auth-exempt routes; `GET /metrics` (opt-in via `server.metrics.enabled`) always needs the token.

---

## Input-required sentinel

Agents request human input by emitting a literal sentinel token in their
output: `<!-- itervox:needs-input -->`. The orchestrator detects this and
always posts the question as a tracker comment; a comment on the issue
resumes the agent — except one written by the bot's own author (skipped as
self-reply detection). With the outbox enabled a reply that lands before the
question comment itself has reached the tracker still counts, matched by its
creation time; with `tracker.outbox: false` replies are matched by position
below the question only.
`agent.inline_input` only controls whether the dashboard
also offers a reply box — see the `inline_input` row above. The prompt
template that teaches agents how to emit the sentinel is appended
automatically — see `internal/templates/human_input.md`. The canonical
constant is `agent.InputRequiredSentinel` in `internal/agent/events.go`; the
contract is documented in `CONTRIBUTING.md` and `docs/architecture.md`.

The question comment carries at most 8 KiB of the agent's text (the end of
it, where the question normally is); the dashboard shows the full text. If
the tracker keeps rejecting the question (a locked or deleted issue, a token
without comment scope), the issue's later tracker writes wait behind it in the
outbox and the daemon logs one warning once the entry is degraded — retry or
discard it from the Outbox panel.

---

## Daemon lifecycle: shutdown, reload and logs

### Graceful drain on `SIGTERM` / `SIGINT`

The first `SIGTERM` or `SIGINT` starts a **drain**:

1. The daemon stops admitting work: no new dispatch, no retry, no
   pending-input resume, no automation start (automations are queued and
   persisted), no reviewer. `resume`, `reanalyze`, `ai-review` and
   `provide-input` answer `409 draining`.
2. `GET /api/v1/ready` answers `503` with `"draining": true`, so a load
   balancer stops routing to the daemon. The dashboard and API stay up.
3. In-flight agent turns keep running on an uncancelled context and finish
   normally; their tracker writes and handoffs happen as usual.
4. When the last turn finishes — or `--shutdown-grace` (default `30s`)
   expires, or a **second** `SIGTERM`/`SIGINT` arrives — the daemon cancels
   whatever is still running (the agent's process group is killed), waits for
   the orchestrator to record the exits, flush its ledgers and join its
   workers (at most ~30 s more), removes `daemon.pid` / `dashboard_url` /
   `HEARTBEAT.md`, and exits `0`. A third signal exits without waiting.

The log shows `shutdown: draining …`, then `shutdown: drain complete` /
`shutdown: drain grace expired, forcing stop` / `shutdown: received second
signal, forcing immediate stop`. Whether a turn cancelled at grace expiry
resumes its agent session after the restart is not verified for either CLI —
size `--shutdown-grace` to your longest normal turn. Under a service manager,
its stop timeout must exceed `--shutdown-grace` (for systemd:
`TimeoutStopSec`, with `KillMode=mixed` so only the daemon receives the
signal), and `itervox stop --grace` must exceed it too, or the drain is cut
short by a `SIGKILL`.

#### Agents left behind by a daemon that died hard

Each agent turn and each workspace hook runs in its own process group, which
the daemon kills when it cancels the turn. A daemon that dies without
cancelling — `kill -9`, the OOM killer, a crash — leaves those groups running
(the agent CLI, or at least the children it started, such as a test runner or
a dev server). The daemon therefore records every local agent and hook group
in `.itervox/run/agent-pgids.json` while it runs (runtime state; `itervox init`
gitignores `run/`), and the next daemon started for the same workflow reaps
them before it dispatches anything. A process-group number alone is never
trusted: the ledger records the group leader's start time (as the kernel
reports it through `ps`, so a later clock change does not matter) and command
line, and, every 10 s, each member's pid and start time. The next daemon sends
`SIGTERM`, then `SIGKILL` 2 s later, to the whole group only if the leader is
still that same process (same start time and command), and otherwise only to
the recorded members that are still the same processes. A recycled
process-group number is left alone; a descendant started less than 10 s
before the daemon died can be missed. The log line
`agent-pgids: previous daemon's process group` reports each entry with
`action=reaped`, `reaped_members`, `gone`, `skipped_identity` or
`skipped_self`.

SSH workers are **not** covered: the local process is the `ssh` client and the
agent's group lives on the worker host, where the remote wrapper stops it when
the ssh channel closes (see `agent.ssh_hosts`). Leftover children in a group
after a normal agent exit, while the daemon keeps running, are not reaped
either.

### What an edit to `WORKFLOW.md` does

Settings saved from the dashboard or TUI never reload: each is written as the
daemon's own write and applied in memory. An **operator edit** (your editor,
`git pull`, a deploy) is detected by the file watcher and applied with
**drain, then reload**: admission stops exactly as for a shutdown, in-flight
turns finish (bounded by the same `--shutdown-grace`; past it the remaining
turns are cancelled), and then the new generation loads the file and starts
dispatching again. `/ready` reports `draining` meanwhile. A signal during a
reload drain turns it into a shutdown. Edits made while draining are picked
up by the same reload.

Why not the alternatives: *reload-and-cancel* (the old behaviour) kills every
running turn on any edit, and whether a killed turn resumes is unverified;
*apply without cancelling* would need every load-time field
to be hot-swappable, while only the fields on the runtime-mutable list can be
changed safely under a running generation. Draining keeps the one-generation-
at-a-time invariant and loses no work; the cost is that an edit takes effect
only after the current turns end (or the grace expires), and no new work
starts in between. An edit that makes the file invalid still drains first,
then shows the "config invalid" banner and waits for a fix.

### Log format

`--log-format text|json` (or `ITERVOX_LOG_FORMAT`; the flag wins) selects
the format of the rotating log file **and**, when no terminal is attached
(systemd, containers, CI), of stderr — one JSON object per log record, for
platforms that collect stdout/stderr. With a terminal, stderr stays
human-readable text (and once the TUI starts, logs go to the file only). Both
sinks go through the same secret redaction (`Bearer` tokens, `lin_api_…`,
`ghp_…`, `sk-ant-…` are written as `***`). Lines printed before logging
starts or outside it (usage text, fatal start-up errors, the TUI's own
"not starting" notice) are plain text in either format.

### Log retention

The daemon log `<logs-dir>/itervox.log` rotates by size; rotated files are
compressed. Three environment variables tune it (CORE-114):

| Variable | Default | Meaning |
|---|---|---|
| `ITERVOX_LOG_MAX_SIZE_MB` | `10` | Rotate when the file reaches this many megabytes (1–10240) |
| `ITERVOX_LOG_MAX_BACKUPS` | `5` | Rotated files to keep, 0–1000 (`0` keeps all of them, subject to the age limit) |
| `ITERVOX_LOG_MAX_AGE_DAYS` | `0` | Delete rotated files older than this many days, 0–3650 (`0` = no age limit) |

A value that is not a whole number in its range stops the daemon at start
with an error naming the variable and the range (an unbounded size used to
overflow the log writer and make every log write fail). The startup line `logging to file` shows the policy in
effect. Per-issue logs under `<logs-dir>` have their own size cap and
rotation and are not affected.

### GitHub state labels: `itervox doctor --fix`

The GitHub tracker maps every state to an issue label: `active_states`,
`working_state`, `completion_state`, `terminal_states` (except `closed`,
GitHub's native state), `backlog_states` and `failed_state`. A state whose
label does not exist on the repository matches no issue, so nothing is
dispatched or moved and nothing reports why.

`itervox doctor` reads the repository's labels with `tracker.api_key` and
lists each missing one with the command that creates it:

```
ERROR: 2 state label(s) missing on owner/repo — issues in those states are never dispatched or moved. Create them with `itervox doctor --fix`, or:
  gh label create "in-review" --color "d93f0b" --repo owner/repo
  gh label create "backlog" --color "f9f9f9" --repo owner/repo
```

Missing labels make doctor exit `1`. If the API cannot be reached or rejects
the token, doctor prints `github labels: could not check …` and the exit code
is unaffected. When `tracker.api_key` is empty it prints `github labels: not
checked` instead: plain `doctor` does not load `.itervox/.env`, so keep the token
in the environment or run `itervox doctor --deploy`, which loads that file first.
Linear workflows skip the check.

`itervox doctor --fix` creates the missing labels after asking
`Create N label(s) on owner/repo …? [y/N]`. Any answer other than `y`/`yes`,
including no input at all, creates nothing. Pass `--yes` (or `-y`) to skip the
prompt in CI. The token needs write access to the repository.

### Deployment preflight: `itervox doctor --deploy`

`itervox doctor --deploy [--workflow PATH]` runs the normal doctor checks
plus non-mutating deployment probes and prints one
`[ok]` / `[warn]` / `[fail]` / `[skipped]` line each; it exits `1` if any
line is `[fail]`:

| Probe | How |
|---|---|
| `claude credentials` | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_AUTH_TOKEN` set (Bedrock/Vertex env accepted), else `~/.claude/.credentials.json` (or `$CLAUDE_CONFIG_DIR`) with its expiry; an expired access token with a refresh token is fine. On macOS the login lives in the Keychain, which doctor does not read (`[warn]`). |
| `codex credentials` | `CODEX_API_KEY` / `OPENAI_API_KEY` set, else the read-only `codex login status`. |
| per SSH host | With `agent.ssh_hosts`, one `[skipped]` line per host and backend: only this machine is probed. |
| `gh auth` | `gh auth status` (missing `gh` is a `[warn]`: PR detection and merges will not work). |
| `git push auth` | `git push --dry-run origin HEAD:refs/heads/itervox-doctor-probe` in the `WORKFLOW.md` directory, with prompts disabled. The dry run never creates the ref; it proves the credentials, not branch protection or receive hooks. |
| `tracker API` | Linear: one `{ viewer { id name } }` query. GitHub: `GET /repos/{owner}/{repo}`, reporting whether the token may push (write labels/states). Through the same client code as the daemon. |
| `daemon /ready` | `GET /api/v1/ready` on the URL in `.itervox/dashboard_url`; `[warn]` when no daemon runs here. |

Probes only backends the configuration uses, loads `.itervox/.env` first like
the daemon, runs each probe with a 10 s timeout, and prints one redacted line
of any CLI output — never raw tokens. It does not check `HEARTBEAT.md`: the
heartbeat is rewritten only when state changes, so its age is not a liveness
signal. Unknown `doctor` flags are now an error (exit `2`) instead of being
ignored.

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
