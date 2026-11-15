---
name: automations
description: 'Use when the user wants Itervox to act on events or schedules, or to manage issue dependencies: "add an itervox automation", "run QA every morning", "answer input-required questions automatically", "switch backend when rate limited", "review every PR itervox opens", "move unblocked backlog issues to Todo", "why is this issue blocked", "stack dependent PRs". Covers automation triggers, filters, instructions and policy, the profile permissions they need, dependency sources and the dependency audit, inferred dependencies (itervox deps), and dependencies.stacked_prs.'
---

# Automations, dependencies and stacked PRs

Sources: `guides/automations.mdx`, `guides/automation-queue.mdx`,
`guides/dependency-management.mdx`, `guides/dashboard-deps.mdx`,
`guides/stacked-prs.mdx`, `configuration.mdx` (`dependencies`, stacked
worktrees).

## Automations

An automation runs one **profile** on an issue when its **trigger** fires,
with its **instructions** appended to the prompt and optional **filters**. It
is a normal worker run: same backend, workspace, logs, and the same
`agent.max_concurrent_agents` slots. When every slot is busy it waits in the
durable automation queue.

Where to edit: the dashboard's **Automations** page, or the `automations`
list in `.itervox/settings.jsonc` (a settings file sets the whole list).
Use only documented fields; unknown keys may be ignored silently.

```yaml
automations:
  - id: qa-ready
    enabled: true
    trigger:
      type: cron
      cron: "0 */2 * * *"
      timezone: "UTC"
    profile: qa
    instructions: |
      Run the QA routine for this issue and comment the results.
    filter:
      states: ["Ready for QA"]
      labels_any: ["qa"]
      match_mode: all
      limit: 10
```

### Triggers (`trigger.type`)

| Type | Fires when | Notes |
|---|---|---|
| `cron` | a 5-field cron schedule | `trigger.cron`, optional `trigger.timezone` (IANA); `filter.states` picks the states searched |
| `input_required` | a running agent asks for input | `filter.input_context_regex`, `policy.auto_resume` (the profile needs `provide_input`) |
| `tracker_comment_added` | the latest tracker comment changed | poll-derived; `filter.body_contains`, `filter.body_regex` |
| `issue_entered_state` | an issue enters a state | requires `trigger.state` |
| `issue_moved_to_backlog` | an issue enters a backlog state | |
| `run_failed` | a run fails permanently (retries exhausted) | summaries, escalation |
| `pr_opened` / `pr_merged` | a run opened a PR / the PR merged | use `pr_opened` **or** `agent.auto_review` for reviews, not both |
| `pr_check_completed`, `pr_synchronized`, `pr_review_submitted`, `pr_mergeable_changed` | events on a PR an itervox run opened | |
| `rate_limited` | a run exhausted retries on a usage limit | `policy.auto_switch`, `switch_to_profile`, `switch_to_backend`, `cooldown_minutes` |
| `blockers_resolved` | the dependency audit sees a blocked issue's last blocker resolve | `policy.move_to_state` (the profile needs `move_state`) |

### Filters (`filter.*`)

`states` (alias `states_any`), `labels_any`, `groups` (the tracker filter
syntax), `identifier_regex`, `input_context_regex`, `body_contains`,
`body_regex`, `limit`, and `match_mode` (`all`, the safe default, or `any`).

### Profile permissions

A profile acts on the tracker only through daemon actions listed in its
`allowed_actions` (in `WORKFLOW.md`, for example
`allowed_actions: [comment, move_state]`). An automation that moves state needs
`move_state`; one that answers input needs `provide_input`. Grant the fewest.

### Safe defaults

- Keep review/QA handoff states out of `tracker.active_states` unless normal
  workers should run there.
- A QA automation comments findings; CI stays the hard gate.
- Keep `agent.max_automation_queue_length` bounded.
- For `rate_limited`, the target profile's command must match
  `switch_to_backend` (for example `command: codex` with `backend: codex`).
  `itervox init --template rate-limit-fallback` writes a working example.

## Dependencies

Blockers hold dispatch: an issue with an unresolved blocker is not started,
and an unknown blocker state counts as unresolved. A merged PR does not
unblock a dependent; the blocker issue itself must be terminal.

| Tracker | Blocker source |
|---|---|
| Linear | native `blocks` relations, and open sub-issues of a parent |
| GitHub | phrases in the issue body: `blocked by`, `depends on`, `requires`, `waiting on`, … followed by `#N` (same repository; comments are not read) |
| Local | `blocked_by: [ITX-3]` in the issue file |

The dependency audit is read-only toward the tracker. It moves an issue only
through an opt-in `blockers_resolved` automation with `policy.move_to_state`
and a `move_state` profile. Keep that rule's filter to backlog states:

```yaml
automations:
  - id: unblock-backlog-to-todo
    enabled: true
    trigger:
      type: blockers_resolved
    profile: pm
    filter:
      states_any: ["backlog", "Backlog"]
    policy:
      move_to_state: "Todo"
```

**Inferred dependencies.** An LLM analyzer (`agent.deps_analyzer_profile`)
proposes extra edges. `dependencies.analysis_mode` is `auto` or `manual`;
`itervox deps analyze` runs one pass now (`--full` ignores the incremental
fingerprint) and `itervox deps list` prints the result. Inferred edges hold
dispatch only above `dependencies.confidence_threshold`; the dashboard's Deps
tab can override them per issue. Tracker blockers always hold.

**Why is X blocked?** Use the MCP tool `explain_blockers` with the identifier
(the `operate` skill), or the dashboard's Deps tab.

## Stacked pull requests

With `dependencies.stacked_prs: true`, an issue whose **only** live blocker is
in review (`tracker.completion_state`) starts at once on the blocker's branch,
and its PR targets that branch. When the blocker lands, Itervox rebases the
dependent onto `workspace.base_branch` and retargets its PR.

Turn it on in `.itervox/settings.jsonc` (it needs `workspace.worktree: true`):

```jsonc
{
  "dependencies": {
    "stacked_prs": true,
  },
}
```

It stacks only with exactly one live blocker that has an identifier and whose
branch exists in this checkout. A restack is deferred while the dependent is
running, skipped with uncommitted changes, and a conflict moves the issue to
input required. `workspace.auto_clear: true` in the legacy layout (no
`clone_url`) removes the blocker's branch and so turns stacking off in
practice.
