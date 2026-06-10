# Automation Queue

Itervox v0.2.0 keeps automation trigger attempts that cannot start immediately in a bounded orchestrator-owned queue. The queue is real runtime state, not a dashboard-only label.

## When Entries Queue

An automation enters the queue when its trigger fires but normal dispatch cannot start a worker for a retryable reason:

- no global worker slot is available.
- the per-state worker cap is full.
- the issue is already running or claimed.
- the issue is waiting for input or a pending input resume.
- the issue is blocked by unresolved dependencies.

Terminal, paused, deleted, malformed, or invalid-profile cases are dropped instead of queued.

## Ordering And Coalescing

Queue entries drain oldest-first by their original queue time. Cron triggers coalesce by automation ID and issue, so a cron rule firing every minute keeps one row and updates `lastFiredAt` / `attemptCount` rather than creating unbounded duplicates.

One-shot triggers preserve the trigger payload in the queue key:

- `pr_opened` keeps the PR URL.
- `input_required` keeps the question/comment key.
- `run_failed` keeps the retry attempt.
- `rate_limited` keeps the failed profile/backend.
- `blockers_resolved` keeps the dependency audit transition version.

## Drain Rules

The queue drains when worker capacity and eligibility allow. Itervox attempts a drain on each orchestrator tick and after events that can free work, such as worker exit, resume/provide-input, discard, or dependency audit updates.

Before starting a queued run, Itervox re-checks the current issue state. If the issue became terminal or otherwise non-retryable while waiting, the entry is removed.

## Queue Cap And Backpressure

`agent.max_automation_queue_length` caps the number of queued automation entries. The default is `100`; `0` or negative values fall back to the default. The queue is never unlimited.

When the queue reaches the cap:

- `automationQueueBackpressure.saturated` becomes true.
- `pausedProducers` becomes true.
- new cron and polled automation intake pauses.
- one-shot triggers that cannot be accepted increment rejection counters and record the latest rejection reason.
- existing queue entries continue draining as capacity opens.

Producers resume only after the queue drains below the low-water mark, currently 80% of the cap.

## Dashboard Surfaces

The Dashboard Live Ops strip shows the queue length and turns into a red queue-full alert while producers are paused. A non-empty queue is normal pressure; a saturated queue means new automation intake is paused.

The Automation Queue panel shows queued, blocked, dispatching, and ready entries. Use the local search box to filter by automation ID, trigger type, issue identifier, profile/backend, reason, or blocker metadata. Clicking details opens the left panel with trigger payload, automation policy, profile permissions, dependency audit state, worker capacity, and activity path.

Retry, review, and pending-resume/input queues use the same local search pattern. The review queue starts collapsed; when expanded it shows up to five visible rows with an internal scrollbar.

## Persistence

When daemon log/state persistence is configured, the automation queue is stored beside the other runtime state files as `automation_queue.json`. Entries are loaded before the first drain on restart.

Prompt instructions may be present in persisted queue entries because they preserve the original trigger semantics. Keep the runtime state directory local-only.

### v2 envelope (todolist4 A.2)

As of v0.2.0 the queue write is wrapped in a versioned envelope:

```json
{
  "schema_version": 2,
  "daemon_instance_id": "itervox-<pid>-<bootns>",
  "payload": { "Entries": {...}, "Order": [...], "Backpressure": {...} },
  "payload_sha256": "<hex>"
}
```

The reader peeks `schema_version` via `IsQueueEnvelopeShape`. Legacy v1
raw payload files (no envelope) are still read via fallback. Mismatched
schema version or payload SHA-256 moves the file to
`automation_queue.json.quarantine` and the queue starts empty rather than
silently consuming corrupt state. A mismatched `daemon_instance_id` is
logged as a warning but still loads.

## Backpressure structured rejection fields (todolist4 P2-2)

`AutomationQueueBackpressure` exposes the most recent rejection in two
parallel forms: the legacy colon-joined `LastRejectedReason` (preserved
for back-compat with the dashboard) and the structured fields
`LastRejectedAutomationID`, `LastRejectedTrigger`,
`LastRejectedIdentifier`. New code should prefer the structured fields;
the legacy single string remains accurate for back-compat clients.

## Terminal-state pruning audit trail (codex-B2)

When `pruneTerminalRuntimeLedgers` removes runtime entries for an issue
that reached a terminal tracker state, the janitor appends an
`IssueStatusChange` row with `source: janitor, reason: "issue_terminal"`
so the per-issue status timeline explains the disappearance. Absent-issue
pruning (codex-B9) similarly emits `reason: "absent_from_tracker"`.

## Boundaries

Queue rows do not mutate Linear or GitHub state. Tracker mutations only happen through explicit dashboard actions or through an automation profile with an allowed daemon action such as `move_state`.
