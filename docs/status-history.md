# Issue Status History

Issue detail panels include a bounded status-change timeline so operators can see how an issue moved through tracker and daemon states during the current daemon session.

## Recorded Sources

Status changes can come from:

- `tracker_observed`: Itervox observed a tracker state change during issue fetch/audit.
- `dashboard`: an operator changed state through the dashboard.
- `worker_lifecycle`: a worker completion, failure, or lifecycle transition moved the issue.
- `automation`: an automation-triggered run moved the issue.
- `janitor`: the runtime-ledger janitor swept the issue and recorded the cleanup (codex-B2 / B9). Janitor rows additionally carry a `Reason` tag:
  - `issue_terminal` — terminal-state pruning removed runtime ledger entries for an issue whose tracker state became terminal.
  - `absent_from_tracker` — absent-issue pruning removed runtime ledger entries for an issue no longer present in tracker polls (after a two-tick grace window).
- `system`: fallback source for internal daemon changes.

Automation rows include automation ID, trigger type, profile, backend, and worker host when those fields are available. Janitor rows include the `Reason` tag listed above; the row is a backend-only ledger entry today and is not yet propagated through the `IssueStatusChangeRow` DTO consumed by the dashboard. Operators can read it via the daemon log and forthcoming snapshot surface.

## Retention

v0.2.0 keeps the last 50 status changes per issue in memory. The status timeline is session-local; it is not a restart-durable audit log.

For durable history, continue relying on the tracker, PR history, and daemon logs. The timeline is a dashboard observability aid for the current daemon run.

## Dashboard Behavior

The timeline appears near the top of the issue detail slide, before the long description. Long state names and worker metadata wrap inside the panel so mobile layouts stay readable.

The timeline is read-only. It does not create controls that silently mutate tracker state.
