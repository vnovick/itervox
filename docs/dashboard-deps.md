# Dashboard Deps Tab

The Dashboard **Deps** tab visualizes issue blocker relationships from the dependency audit. It is a display-only React Flow graph.

## Reading The Graph

Edges point from blocker to blocked issue:

```text
ENG-10 -> ENG-42
```

This means `ENG-42` is blocked by `ENG-10`.

Nodes are included for both sides of every dependency edge, even when a blocker is not visible in the current board columns. Missing or unknown blocker state is shown explicitly instead of being treated as resolved.

## Badges

Node badges come from real snapshot data:

- tracker state: the current tracker state when known.
- `running`: the issue currently has a worker.
- `queued`: the issue has an automation queue entry.
- `terminal`: the issue state matches configured terminal states.
- `blocked`: dependency audit still has unresolved blockers.
- `unblocked`: all tracked blockers are terminal.
- `unknown`: the blocker state is missing or cannot be proven terminal.

Unknown state remains blocking. Operators should fix the tracker relation/reference or wait for the tracker adapter to fetch fresh blocker state.

## Issue Details

Clicking an issue node opens the same issue detail panel used by the board and list views. The graph does not implement a second issue-detail view.

## Boundaries

The Deps tab does not edit dependencies and does not move Linear/GitHub statuses. It also does not represent the future editable automation canvas. v0.2.0 shows issue blocker relationships only; the Trigger -> Automation -> Profile -> Worker workflow canvas is deferred to v0.2.1+.
