# Dependency Management

Itervox v0.2.0 treats issue dependencies as dispatch eligibility and audit data. It does not implement a full DAG scheduler and it does not move tracker state from the core audit path.

## Dependency Sources

Itervox normalizes tracker blocker data into `issue.BlockedBy`.

- Linear blockers come from native Linear issue relations (inverse relations of type `blocks`) **and from sub-issues**: a parent issue is blocked by each of its children until they reach a terminal state.
- GitHub blockers are text-derived from the issue body. Matched phrases: `blocked by`, `blocked on`, `depends on`, `depends upon`, `requires`, `waiting on`, `waiting for` — each followed by one or more `#123` references (comma/`and`/`&` lists supported, e.g. `depends on #3, #4 and #5` or `depends on #3 & #4`). The phrase may optionally be followed by a colon (`Depends on: #3`), and the reference list may continue across newlines onto bullet-shaped lines (`- #3`, `* #3`, optionally indented) — a blank line, or a non-bullet non-reference line, ends the list. A cross-repo reference (`owner/repo#N`) mid-list is skipped rather than ending the list, e.g. `depends on #3, foo/bar#4, #5` yields `#3` and `#5`. Only same-repo `#N` references ever become blocker edges — cross-repo references are recognized and skipped, never matched as blockers themselves. Casual phrasing such as "requires #5 to be reviewed" also matches — this is deliberate. Comments are not parsed for blocker relationships.

The dependency audit exposes source labels so operators can see where blocker data came from: `tracker_relation` (an explicit Linear "blocks" relation), `issue_text` (a GitHub `#N` body reference), and `sub_issue` (a Linear parent blocked by an incomplete child/sub-issue).

## Dispatch Eligibility

An issue with unresolved blockers is not eligible for normal dispatch. An automation entry blocked by dependencies remains queued with a `blocked_by` reason until every blocker is observed as terminal.

Unknown blocker state is treated as unresolved. A paused blocker is still unresolved; pausing only stops local work, it does not prove the blocker is terminal. The dependent issue remains blocked until the tracker adapter can prove the blocker is terminal. GitHub's missing/deleted referenced issue fallback maps that blocker to closed, matching the GitHub adapter behavior.

PR merge alone is not a deterministic unblock signal. A dependent issue becomes eligible only after the blocking issue itself is later observed by the tracker as terminal, closed, or done.

## Dependency Audit

The orchestrator stores one audit row per issue. Rows include:

- current issue state.
- audit status: `blocked`, `unblocked`, or `unknown`.
- all blockers, unresolved blockers, and resolved blockers.
- source labels.
- first blocked time and last audit time.
- unblock transition time, version, and reason.

When an issue transitions from blocked or unknown-with-blockers to unblocked, Itervox increments a transition version and records `blockers_resolved`.

The audit is not startup-only. On each poll and manual refresh, the event loop refreshes known audit rows, including rows for issues outside active states and outside `blockers_resolved` source filters. If tracker data changes and a blocker becomes terminal, the next refresh updates the audit row, Deps graph, automation queue eligibility, and any matching `blockers_resolved` transition.

## `blockers_resolved` Automation

Core dependency audit is read-only. It can emit an opt-in `blockers_resolved` automation, but tracker mutation still requires a configured automation and a profile with permission to move state.

Release-safe example:

```yaml
automations:
  - id: unblock-backlog-to-todo
    trigger:
      type: blockers_resolved
    filter:
      states_any: ["backlog", "Backlog"]
    profile: pm
    policy:
      move_to_state: "Todo"
    instructions: |
      All tracked blockers for this backlog issue are terminal.
      Move only backlog/Backlog issues to Todo.
      Do not move review, in-review, PR-open, or merged issues.
```

The default source-state filter is intentionally limited to `backlog` / `Backlog`. Review, in-review, PR-open, merged, and closed states are not included in the default release guidance. Projects that want different movement must configure their own automation deliberately.

The selected profile must include `move_state` in `allowed_actions` before it can transition the issue. Without that permission, the profile can comment/report readiness but cannot move tracker state through daemon actions.

## Execution Order

Itervox keeps dependency ordering simple:

- unresolved blockers make a dependent issue ineligible.
- when blockers resolve, the dependent issue rejoins normal dispatch ordering.
- normal dispatch still sorts by priority, created time, and identifier.
- automation queue drain preserves FIFO queue order after eligibility is satisfied.

There is no v0.2.0 DAG scheduler, no transitive priority promotion, and no PR-merge-based readiness inference.
