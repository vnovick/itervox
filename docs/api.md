# Itervox — REST and SSE API Reference

Base URL: `http://localhost:8090/api/v1`

Non-streaming endpoints return JSON. Streaming endpoints use Server-Sent Events
(`text/event-stream`).

---

## Authentication

### Dashboard / API bearer auth

By default, on every bind — including loopback (`127.0.0.1`, `localhost`,
`::1`) — Itervox secures all `/api/v1/*` routes except `/api/v1/health` and
`/api/v1/ready` with bearer-token auth (and `/metrics`, when enabled, with the
same token). Bind address is not treated as a security boundary: a
loopback daemon behind a tunnel or reverse proxy is exactly as reachable as
one bound to `0.0.0.0`.

- If `ITERVOX_API_TOKEN` is set, that value is required.
- If `ITERVOX_API_TOKEN` is unset and `server.allow_unauthenticated` is
  `false` (the default), Itervox auto-generates an ephemeral token at
  startup and prints the tokened dashboard URL to stderr once — only when
  stderr is a terminal or `ITERVOX_PRINT_TOKEN=1` (`--no-print-token`
  always suppresses it). Otherwise the token is written to
  `<logs-dir>/api-token` (mode 0600) and the log carries only its path and
  a `sha256:` fingerprint.
- `server.allow_unauthenticated: true` disables auth entirely, on every
  bind. Renamed from `server.allow_unauthenticated_lan`, which still parses
  as a deprecated alias.
- With `server.allow_unauthenticated: true`, state-changing requests (`POST`,
  `PUT`, `PATCH`, `DELETE`) from another origin are refused with `403`
  `cross_origin_forbidden`: `Sec-Fetch-Site: cross-site`/`same-site`, or an
  `Origin` whose host and port differ from `Host` (scheme is not compared;
  `Origin: null` never matches). Requests with neither header (curl,
  scripts) and `GET`/SSE pass. Token mode has no origin check.
- With `server.allow_unauthenticated: true`, every route (API, SSE,
  dashboard) refuses a request whose `Host` is a DNS name other than
  `localhost`, `server.host` or one in `server.allowed_hosts` with `403`
  `host_not_allowed` (DNS-rebinding guard). IP addresses always pass;
  `GET /api/v1/health` and `GET /api/v1/ready` are exempt. Token mode has no
  Host check.
- `GET /api/v1/health` and `GET /api/v1/ready` are always auth-exempt — the
  only auth-exempt routes. `GET /metrics` is never auth-exempt.
- A settings write (`/settings/*`) that was already in flight when the
  daemon reloaded `WORKFLOW.md` is refused with `503 settings_reloading`
  and `Retry-After: 1`. Nothing was written; re-send it.

```bash
curl -H "Authorization: Bearer $ITERVOX_API_TOKEN" \
  http://localhost:8090/api/v1/state
```

### Agent-action bearer auth

The daemon-backed agent-action routes use a **separate** bearer token model.
They are authenticated with a short-lived per-run action grant, not the main
API token. These routes are:

- `POST /agent-actions/{identifier}/comment`
- `POST /agent-actions/{identifier}/comment_pr`
- `POST /agent-actions/{identifier}/merge_pr`
- `POST /agent-actions/{identifier}/create-issue`
- `POST /agent-actions/{identifier}/move-state`
- `POST /agent-actions/{identifier}/provide-input`

Profiles opt into these capabilities with `allowed_actions` and
`create_issue_state` in `WORKFLOW.md`.

## Error format

JSON errors use the typed envelope below:

```json
{
  "error": {
    "code": "bad_request",
    "message": "message is required",
    "field": "message"
  }
}
```

- `field` is optional and is mainly used by settings/forms.
- The dashboard surfaces `message` in its error toasts. A `503` whose `code`
  is `orchestrator_busy`, `settings_reloading` or `deps_override_queue_full`
  means nothing was enqueued or written; the dashboard retries such a request
  once after `Retry-After`. Other `503`s are not retried.
- Some legacy streaming code paths still use plain text responses for transport
  failures before SSE framing starts.

---

## Health

### `GET /api/v1/health`

Auth-exempt liveness probe.

**Response** `200`

```json
{ "status": "ok" }
```

A static answer: it proves the HTTP server is up, not that the orchestrator
is working. Use it for liveness (restart-on-failure); use `/ready` for
readiness and alerting.

### `GET /api/v1/ready`

Auth-exempt readiness probe driven by the orchestrator's event loop. `200`
when ready, `503` when not; the body is the same either way and holds only
booleans and one timestamp:

```json
{
  "ready": true,
  "loop_fresh": true,
  "last_poll_ok": true,
  "config_invalid": false,
  "degraded": false,
  "tracker_rate_limited_until": null,
  "draining": false
}
```

| Field | Meaning |
|---|---|
| `ready` | `loop_fresh`, fewer than 3 consecutive failed tracker polls, and not `draining`. |
| `loop_fresh` | The event loop is alive. Idle, it must have finished an iteration within 3× `polling.interval_ms` (at least 30 s). Inside a tick or event — for example a slow tracker call — it stays fresh for up to 10 minutes (the tick budget). Within one poll interval of startup (at least 30 s) it is fresh before the first tick. |
| `last_poll_ok` | The most recent tick polled the tracker and the poll succeeded (`false` before the first poll and while polling is shed for budget). |
| `config_invalid` | The last `WORKFLOW.md` reload failed validation; the daemon keeps running on its last valid config. |
| `degraded` | A rate-limited poll, an open tracker rate-limit gate, a tick that skipped its poll to keep the tracker budget for writes, 1–2 consecutive failed polls, or `config_invalid`. Degraded is still ready. |
| `tracker_rate_limited_until` | RFC 3339 instant the tracker rate-limit gate lifts, `null` while it is closed. |
| `draining` | The daemon is draining — after `SIGTERM`/`SIGINT`, or before applying an operator edit to `WORKFLOW.md` — and admits no new work while in-flight agent turns finish. A draining daemon answers `503` so a load balancer stops routing to it; `loop_fresh` stays `true`. |

A rate-limited poll does not count as a failure: the tracker said when to
come back, so the probe reports `degraded` and stays ready. `config_invalid`
does not make the daemon unready either — taking it out of a load balancer
would hide the dashboard you fix the file from. Like `/health`, `/ready` is
exempt from the unauthenticated-mode Host guard: its body carries no error
text, identifiers or configuration.

---

## Metrics

### `GET /metrics`

Prometheus text exposition (`text/plain; version=0.0.4`). Off by default:
set `server.metrics.enabled: true`. Requires the same bearer token as
`/api/v1/*` (in `allow_unauthenticated` mode it is open like the rest of the
API and subject to the Host guard). Disabled, it answers `404`.

```bash
curl -H "Authorization: Bearer $ITERVOX_API_TOKEN" http://localhost:8090/metrics
```

| Family | Type | Labels | Meaning |
|---|---|---|---|
| `itervox_workers_running` | gauge | | Agent workers running now. |
| `itervox_workers_max` | gauge | | `agent.max_concurrent_agents` as of the last tick. |
| `itervox_retry_queue_length` | gauge | | Issues waiting to retry. |
| `itervox_automation_queue_length` | gauge | | Automation queue entries. |
| `itervox_input_required` | gauge | | Issues waiting for human input. |
| `itervox_paused_issues` | gauge | | Paused issues. |
| `itervox_worker_exits_total` | counter | `reason` | Worker exits (`succeeded`, `failed`, `stalled`, `input_required`, `canceled_by_reconciliation`). |
| `itervox_tracker_requests_total` | counter | `adapter`, `outcome` | Tracker HTTP calls after in-call retries; `outcome` is `ok`, `rate_limited` or `error` (transport failure). |
| `itervox_goroutine_panics_total` | counter | | Panics recovered on daemon goroutines (the site is in the log line). |
| `itervox_events_dropped_total` | counter | | Orchestrator events that could not be delivered (full channel or send timeout). |
| `itervox_client_errors_total` | counter | `kind`, `outcome` | Dashboard error reports on `POST /api/v1/client-errors`; `kind` is `render`, `error`, `unhandledrejection`, `schema` or `other`, `outcome` is `accepted`, `rate_limited` or `dropped`. |
| `itervox_persist_write_errors_total` | counter | | Failed runtime-ledger writes. Resets on a `WORKFLOW.md` reload. |
| `itervox_transport_failures_total` | counter | | Retry-exhausted runs classified as transport failures. Resets on reload. |
| `itervox_dispatch_ticks_total` | counter | `bound` | Poll ticks observed (`any`), and those that were slot-bound (`slot`) or dependency-bound (`dependency`). Resets on reload. |
| `itervox_dispatch_eligible_waiting` | gauge | | Eligible issues waiting for a slot on the last tick. |
| `itervox_dispatch_blocked_by_dependency` | gauge | | Candidates held by a dependency gate on the last tick. |
| `itervox_outbox_entries` | gauge | `state` | Outbox entries: `pending` (all), `degraded`, `rate_limited`. |
| `itervox_tracker_rate_limited_until_seconds` | gauge | `adapter` | Unix time the tracker rate-limit gate lifts; `0` while closed. |
| `itervox_tracker_poll_consecutive_failures` | gauge | | Consecutive failed (non-rate-limited) candidate polls. |
| `itervox_tracker_last_error_timestamp_seconds` | gauge | `kind` | Unix time of the last tracker failure (`outage` or `rate_limited`); absent when none. |
| `itervox_event_loop_last_idle_timestamp_seconds` | gauge | | Unix time the event loop last finished an iteration. |

No series carries an issue identifier. The process-wide counters survive a
`WORKFLOW.md` reload; the ones marked "resets on reload" belong to the
orchestrator generation and restart from zero, which `rate()` handles as a
counter reset. A scrape never waits on a settings save: it reads the
orchestrator snapshot, the outbox and the rate-limit gate only.

---

## Real-time streams

### `GET /events`

Full `StateSnapshot` SSE stream.

- Event shape: `data: <JSON StateSnapshot>\n\n`
- Initial snapshot is sent immediately.
- Named keepalive event `event: keepalive` with `data: {}` after 25 seconds
  of stream inactivity.

### `GET /issues/{identifier}/log-stream`

Per-issue in-memory log SSE stream.

- Event name: `log`
- Event shape: `id: <cursor>\nevent: log\ndata: <JSON IssueLogEntry>\n\n`
- Supports resume via `Last-Event-ID`
- If the underlying in-memory buffer is cleared, stale cursors replay from the
  beginning of the current buffer

### `GET /issues/{identifier}/sublog-stream`

Per-issue session/subagent log SSE stream.

- Event name: `sublog`
- Event shape: `id: <epoch>-<seq>\nevent: sublog\ndata: <JSON IssueLogEntry>\n\n`
  (CORE-153; before it the id was a bare `<seq>`). `seq` is the 1-based
  position in the issue's current set of session-log entries; `epoch`
  identifies that set (a hash of its first entry — `0` for an empty set), so
  it stays the same while lines are appended and changes when the session
  files are replaced or cleared.
- Supports resume via `Last-Event-ID`: an id of the current epoch within range
  resumes after it. Any other cursor — another epoch (replaced or cleared
  files), a pre-CORE-153 bare number, or a seq past the end — gets one
  `id: <epoch>-0\nevent: gap\ndata: {}\n\n` frame followed by a full replay.
  A set that changes while the stream is open is announced the same way.
- On mid-stream fetch failure the server emits:

```text
event: error
data: {"code":"fetch_failed","message":"..."}
```

### `GET /logs`

Global daemon-log SSE tail.

- Event name: `log`
- Initial connection sends the last ~16 KiB of the rotating daemon log file.
- Optional query param `identifier=<ISSUE-ID>` filters matching log lines.

---

## State

### `GET /state`

Returns the current orchestrator snapshot as JSON.

**Response** `200`: `StateSnapshot`

Useful top-level fields include:

- `running`, `history`, `retrying`, `paused`
- `inputRequired`
- `availableProfiles`, `profileDefs`
- `automations`
- `automationQueue`, `automationQueueBackpressure`
- `dependencyAudit`, `dependencyGraphNodes`, `dependencyGraphEdges`
- `sshHosts`, `dispatchStrategy`
- `autoClearWorkspace`, `inlineInput`
- `configInvalid`
- `lastTrackerError` — the most recent tracker failure, absent when none:
  `{ "at", "op": "poll" | "update_state", "kind": "outage" | "rate_limited",
  "message", "resetAt"?, "consecutiveFailures"? }`. A successful poll clears
  a poll failure; a failed failed-state move stays for up to an hour.
- `outboxEntries[].lastFailedAt` — when that outbox entry last failed to
  deliver (rate-limit deferrals do not count).
- `backendHealth` (CORE-055) — one row per agent backend, and per SSH host
  that has a breaker: `{ "backend", "host"?, "status": "healthy" | "warning"
  | "limited" | "probing", "kind"?: "quota" | "throttle", "limitType"?,
  "limitedUntil", "retryAt"?, "since"?, "probeIssue"?, "heldIssues",
  "reroutedIssues" }`. `limitedUntil` is always present and `null` unless the
  vendor published a reset time; `retryAt` is when the breaker half-opens
  (the reset, or the cooldown end). This is the AGENT backend, unrelated to
  `rateLimits` (tracker API budget) and `outboxEntries[].rateLimitedUntil`
  (tracker writes). Absent on older daemons.
- `autoSwitches` (CORE-055) — issues whose next run uses an automatic
  override: `{ "identifier", "source": "automation" | "backend_fallback" |
  "unknown", "fromBackend"?, "fromProfile"?, "toBackend"?, "toProfile"?,
  "reason"?, "switchedAt"? }`, sorted by identifier. It describes the next
  dispatch; the running session's backend stays on `running[].backend`.
  `/api/v1/issues` rows carry the same object as `autoSwitch`, and an issue
  held by an open breaker has `ineligibleReason: "backend_limited"`.
- `pauseReasons` — `{ "<identifier>": "<reason>" }` for currently paused
  issues: `user_cancelled`, `user_dismissed_input`, `retries_exhausted` or
  `transition_failed` (treat unknown values as opaque). An issue paused by an
  older daemon without a recorded reason is absent. Omitted when empty.
- `failureAcks` (CORE-175) — `[{ "identifier", "upTo" }]`, sorted by
  identifier: the operator's acknowledgements of worker failures (see
  `POST /issues/{identifier}/failures/ack`). Omitted when empty.
- `capabilities` — optional daemon features the dashboard may use; currently
  `["failure_ack"]`. Treat unknown entries as opaque.
- `totals` (CORE-091) — daemon-session cumulative, reset on restart:
  `{ "inputTokens", "outputTokens", "costUsdEstimated", "costCoverage":
  { "claudeRuns", "codexRuns" } }`. `costUsdEstimated` is the sum of Claude's
  client-side `total_cost_usd` estimates — counted per agent session, so a
  resumed session is not counted twice — and is `null` until a Claude run
  reports a cost. Codex reports no cost: `codexRuns > 0` means the estimate
  covers Claude runs only.
- `recentFailures` — always an array (possibly empty) on a current daemon:
  the last 100 operator-relevant failures, oldest recorded first. Each row is
  `{ "kind", "identifier"?, "source"?, "message", "occurredAt", "recordedAt",
  "count" }`. `kind` is `worker_failed`, `worker_stalled`, `tracker_poll`,
  `tracker_write`, `persist`, `outbox`, `panic`, `client` or `automation` (an
  automation that could not be dispatched, e.g. `pr_merged` after a merge) (treat unknown
  kinds as opaque). `message` is built from the classified error only (never
  prompt text or agent output), redacted, and capped at 1 KiB. Identical
  consecutive failures coalesce into one row with a `count`. `occurredAt` is
  when the failure happened (latest repeat), `recordedAt` when the daemon
  recorded it; an off-loop failure can be recorded after a newer one, so sort
  by `occurredAt` to display. Rate-limited polls and outbox rate-limit
  deferrals are not failures. The list survives a `WORKFLOW.md` reload but not
  a daemon restart.

Retry rows' `error` and the rest of an agent failure's text are passed
through the log redactor before they reach the snapshot: known key shapes,
`NAME=value` assignments whose upper-case name contains `SECRET`, `TOKEN`,
`PASSWORD`, `API_KEY`, `PRIVATE_KEY`, `ACCESS_KEY` or `CREDENTIAL`, the same
with a lower- or mixed-case name (`password=`, `api_key=`, `"apiKey":`,
`x-api-key:`), HTTP Basic credentials, URL passwords (also with an empty
user), and long random-looking tokens (also inside a path) appear as `***`.
Vendor request ids (`req_…`, `msg_…`), SRI hashes, git SHAs, UUIDs and
ordinary paths stay readable.

---

## Client error reports

### `POST /api/v1/client-errors`

The dashboard reports its own production failures here — error-boundary
crashes, uncaught `error` / `unhandledrejection` events, and snapshot schema
drift — so they reach the daemon log, `itervox_client_errors_total` and
`recentFailures` (kind `client`). Authenticated like the rest of `/api/v1`
(bearer token; in `allow_unauthenticated` mode the CSRF guard and Host guard
apply).

```json
{ "kind": "render", "message": "Cannot read properties of undefined", "route": "/settings", "stack": "..." }
```

- `kind`: `render`, `error`, `unhandledrejection` or `schema`; any other value
  is recorded as `other`. `message` is required.
- The body is capped at 8 KiB **including trailing bytes** and must be exactly
  one JSON object. The daemon redacts `message`, `route` and `stack` and
  bounds them (1 KiB, 256 B, 2 KiB) before logging.
- Responses: `202 {"accepted":true}`; `400 bad_request` (malformed, empty
  message, trailing data); `413 payload_too_large`; `429 rate_limited`
  (a daemon-wide budget of 20 reports, refilling one per 3 s, with
  `Retry-After`); `503 orchestrator_busy` when the event queue is full (the
  report is dropped, not queued).
- The dashboard itself dedupes identical `{kind, message, route}` reports for
  60 s, sends at most 10 per minute per tab, never reports its own failed
  POST, and sends nothing while it is on the token or server-down screen.

---

## Issues

### Listing and detail

| Method | Path | Response |
|---|---|---|
| `GET` | `/issues` | `TrackerIssue[]` |
| `GET` | `/issues/{identifier}` | `TrackerIssue` |

### Lifecycle and control

| Method | Path | Request body | Success response | Notes |
|---|---|---|---|---|
| `DELETE` | `/issues/{identifier}` | — | `{"cancelled":true,"identifier":"ENG-1"}` | Alias for cancel |
| `POST` | `/issues/{identifier}/cancel` | — | `{"cancelled":true,"identifier":"ENG-1"}` | `404 not_running` if not running |
| `POST` | `/issues/{identifier}/resume` | — | `{"resumed":true,"identifier":"ENG-1"}` | `404 not_paused` if not paused |
| `POST` | `/issues/{identifier}/reanalyze` | — | `{"queued":true,"identifier":"ENG-1"}` | `404 not_paused` if not paused |
| `POST` | `/issues/{identifier}/terminate` | — | `{"terminated":true,"identifier":"ENG-1"}` | `404 not_found` if not running or paused |
| `POST` | `/issues/{identifier}/ai-review` | — | `202 {"queued":true,"identifier":"ENG-1"}` | Reviewer dispatch |

While the daemon is draining (see `draining` under `/ready`), `resume`,
`reanalyze`, `ai-review` and `provide-input` are refused with
`409 {"code":"draining"}`: nothing was queued; re-send once the daemon is
back.

### Backend health

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/backend-health/clear` | `{"backend":"claude","host":""}` | `202 {"queued":true,"backend":"claude","host":""}` |

Closes one agent-backend circuit breaker (`backendHealth[]` on the
snapshot) when you know the backend is available again. `host` is empty for
local runs or the SSH worker host of a host-scoped breaker. The clear is
queued on the orchestrator's event loop: `202` means queued — watch
`/api/v1/state` or the SSE stream for the row to turn `healthy`. Issues held
with `backend_limited` on that breaker are re-evaluated on the next dispatch
pass. Errors: `400 bad_request` (`field: "backend"` unless `claude` or
`codex`; `field: "host"` for a malformed host), `503 event_queue_full`
(retry), `501 not_implemented`. Like every state-changing route it needs the
bearer token, or, in `server.allow_unauthenticated` mode, passes the
cross-origin guard. The dashboard offers the same action as **Clear** next to
a limited backend chip.

### Per-issue overrides and human-input flow

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/issues/{identifier}/profile` | `{"profile":"frontend"}` or `{"profile":""}` | `{"ok":true,"identifier":"ENG-1","profile":"frontend"}` |
| `POST` | `/issues/{identifier}/backend` | `{"backend":"claude"}` or `{"backend":""}` | `{"ok":true,"identifier":"ENG-1","backend":"claude"}` |
| `POST` | `/issues/{identifier}/provide-input` | `{"message":"..."}` | `{"ok":true}` |
| `POST` | `/issues/{identifier}/dismiss-input` | — | `{"ok":true}` |
| `POST` | `/issues/{identifier}/failures/ack` | `{"upTo":"2026-09-27T10:00:00Z"}` | `202 {"queued":true}` |
| `POST` | `/issues/{identifier}/comment` | `{"body":"..."}` | `202 {"queued":true,"identifier":"ENG-1"}` or `200 {"ok":true,"identifier":"ENG-1"}` |

`backend` returns `400 bad_request` for anything but `claude`, `codex` or `""`,
and `409 backend_pin_refused` (with the resolver's reason in `message`,
`field: "backend"`) when the issue's command runs the other backend — for
example a `codex` pin over a `claude ...` profile, which the dispatcher would
refuse. A refused pin is not stored. A pin applies from the next run and wins
over `agent.backend_fallback`.

`failures/ack` (CORE-175) acknowledges the issue's `worker_failed` /
`worker_stalled` rows in `recentFailures` that occurred at or before `upTo`
(RFC 3339), so the dashboard's attention inbox stops counting them; a newer
failure is not covered. `400 bad_request` for a missing or unparsable `upTo`,
`404 issue_not_found` when the issue has no such row, `503 orchestrator_busy`
(`Retry-After: 1`) when the event queue is full. The ack is applied by the
event loop (the latest `upTo` per issue wins), is allowed while the daemon
drains (it starts no work), and is not persisted: like `recentFailures`, it
does not survive a restart, and it is dropped once the issue's failures leave
the ring. Offered only when `capabilities` includes `failure_ack`.

`provide-input` / `dismiss-input` return `404 not_found` when the issue is not
currently in `input_required`. `provide-input` returns `409 inline_input_enabled`
when `agent.inline_input: true` — the tracker is the only reply channel in that
mode. The `409` is checked first: in inline mode the route refuses regardless
of the request body or the issue's state. The agent-action `provide-input` route
(`POST /agent-actions/{identifier}/provide-input`) is unaffected.

Operator comments are plain comments (no managed marker): they can fire
`tracker_comment_added` automations and are delivered through the write-ahead
outbox when it is enabled. To answer an input-required agent, use
`provide-input`. The body is required and capped at 10 KiB; an empty or
oversize body returns `400 bad_request` with `field: "body"`.

---

## Logs

### Snapshot endpoints

| Method | Path | Response |
|---|---|---|
| `GET` | `/issues/{identifier}/logs` | `IssueLogEntry[]` |
| `GET` | `/issues/{identifier}/sublogs` | `IssueLogEntry[]` |
| `GET` | `/logs/identifiers` | `string[]` |

### Clear endpoints

| Method | Path | Success response |
|---|---|---|
| `DELETE` | `/issues/{identifier}/logs` | `{"ok":true}` |
| `DELETE` | `/issues/{identifier}/sublogs` | `{"ok":true}` |
| `DELETE` | `/issues/{identifier}/sublogs/{sessionId}` | `{"ok":true}` |
| `DELETE` | `/logs` | `{"ok":true}` |

Notes:

- `/issues/{identifier}/logs` reads the in-memory orchestrator log buffer.
- `/issues/{identifier}/sublogs` reads persisted agent session logs and returns
  an empty array when no logs exist.
- `/logs` is an SSE stream of the daemon log file, not a JSON endpoint.

---

## Runtime settings

All settings endpoints persist back to `WORKFLOW.md` and apply the value in
memory. None of them reloads `WORKFLOW.md`, so saving a setting never touches
an in-flight agent turn — this now includes `/settings/tracker/states`, the
Linear project filter (`PUT /projects/filter`) and
`POST /settings/models/refresh`, which used to reload (CORE-160).

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/settings/workers` | `{"workers":5}` or `{"delta":1}` | `{"workers":5}` |
| `POST` | `/settings/inline-input` | `{"enabled":true}` | `{"ok":true}` |
| `POST` | `/settings/workspace/auto-clear` | `{"enabled":true}` | `{"ok":true,"autoClearWorkspace":true}` |
| `PUT` | `/settings/tracker/states` | `{"activeStates":[...],"terminalStates":[...],"completionState":"Done"}` | `{"ok":true}` |
| `PUT` | `/settings/tracker/failed-state` | `{"failedState":"Failed"}` (empty string = pause instead) | `{"ok":true,"failedState":"Failed"}` |
| `PUT` | `/settings/agent/max-retries` | `{"maxRetries":5}` | `{"ok":true,"maxRetries":5}` |
| `PUT` | `/settings/agent/max-switches-per-issue-per-window` | `{"maxSwitchesPerIssuePerWindow":2}` | `{"ok":true,"maxSwitchesPerIssuePerWindow":2}` |
| `PUT` | `/settings/agent/switch-window-hours` | `{"switchWindowHours":6}` | `{"ok":true,"switchWindowHours":6}` |
| `POST` | `/settings/ssh-hosts` | `{"host":"builder-1","description":"GPU box"}` | `{"ok":true}` |
| `DELETE` | `/settings/ssh-hosts/{host}` | — | `{"ok":true}` |
| `PUT` | `/settings/dispatch-strategy` | `{"strategy":"round-robin" \| "least-loaded"}` | `{"ok":true}` |
| `POST` | `/settings/deps-analysis-mode` | `{"mode":"auto" \| "manual"}` | `{"ok":true,"mode":"auto"}` |
| `DELETE` | `/workspaces` | — | `202 {"ok":true}`; `409 clear_in_progress` while an earlier clear is still running (CORE-114) |
| `POST` | `/refresh` | — | `202 {"queued":true,"queued_at":"..."}` |
| `POST` | `/automations/{id}/test` | `{"identifier":"ENG-42"}` (target issue identifier) | `{"ok":true}` |

---

## Skills Inventory

The skills endpoints expose the Settings -> Skills inventory and recommendation
surface. They use the same dashboard bearer token as the rest of `/api/v1`.

| Method | Path | Request body | Success response | Notes |
|---|---|---|---|---|
| `GET` | `/skills/inventory` | — | `Inventory` | `503 inventory_unavailable` before the first successful scan |
| `POST` | `/skills/scan` | — | `Inventory` | Forces a fresh filesystem scan |
| `GET` | `/skills/issues` | — | `InventoryIssue[]` | Static analyzer recommendations |
| `POST` | `/skills/fix` | `{"issueID":"UNUSED_PROFILE","fix": Fix}` | `{"status":"ok"}` | v0.2.0 only applies the non-destructive `UNUSED_PROFILE` `edit-yaml` fix; unsafe actions such as `remove-mcp` are rejected |
| `GET` | `/skills/analytics` | — | `AnalyticsSnapshot` | Includes `HasRuntimeEvidence`; `503 analytics_unavailable` only before analytics can be computed |
| `GET` | `/skills/analytics/recommendations` | — | `Recommendation[]` | Runtime-side recommendations; empty until `HasRuntimeEvidence` is true |

### Dependency analyzer sidecar

| Method | Path | Request body | Success response | Notes |
|---|---|---|---|---|
| `POST` | `/deps/analyze` | — | `{"jobID":"<uuid>","status":"queued"}` | Starts an async analyzer job over the snapshot's dependency graph. `409 draining` while the daemon drains for shutdown or reload (the analyzer runs an agent); the 60 s auto-analyze scheduler skips its ticks then too. `409 backend_limited` while the breaker of the analyzer profile's backend is open (CORE-173; the message names the backend and its reset), and the scheduler skips those ticks; a job queued before the breaker opened fails with the same message instead of starting |
| `GET`  | `/deps/analyze/{jobID}` | — | `{"jobID":"<uuid>","status":"running\|completed\|failed","result":{...}}` | Polled by the dashboard until status is terminal |
| `DELETE` | `/deps/analyze/{jobID}` | — | `204 No Content` | Cancels the running analyzer job. `404` when no running job matches the ID; `503` when the analyzer is not configured |

`Inventory` is a direct inventory/recommendation snapshot, not a complete
normalized capability graph in v0.2.0. The response includes `ScanTime`,
`Partial`/`ScanError` for best-effort scanner failures, and `Stale` when a
tracked core config or discovered inventory file changed or disappeared since the scan. `ORPHAN_MCP`
scans skill names, frontmatter descriptions, and skill bodies. Duplicate MCP
recommendations are advisory because the daemon does not rewrite user-owned MCP
settings files.

---

## Profiles, reviewer, models, and automations

### Profiles

| Method | Path | Request body | Success response |
|---|---|---|---|
| `GET` | `/settings/profiles` | — | `{"profiles": { "<name>": ProfileDef }}` |
| `PUT` | `/settings/profiles/{name}` | See body below | `{"ok":true}` |
| `DELETE` | `/settings/profiles/{name}` | — | `{"ok":true}` |

Profile update body:

```json
{
  "command": "codex --model gpt-5-codex",
  "soul": "# qa SOUL\n\nYou are the QA specialist for this repository.",
  "instructions": "# qa INSTRUCTIONS\n\nRun focused verification and report failures clearly.",
  "soulFile": ".itervox/agents/qa/SOUL.md",
  "instructionsFile": ".itervox/agents/qa/INSTRUCTIONS.md",
  "backend": "codex",
  "enabled": true,
  "allowedActions": ["comment", "provide_input"],
  "createIssueState": "Todo",
  "originalName": "old-name"
}
```

For schema `2` profiles, `soul` and `instructions` are written to the referenced
`SOUL.md` and `INSTRUCTIONS.md` files. `prompt` is retained as a compatibility
field derived from `instructions`; new clients should use the file-backed
fields.

### Reviewer and models

| Method | Path | Response |
|---|---|---|
| `GET` | `/settings/reviewer` | `{"profile":"reviewer","auto_review":true}` |
| `PUT` | `/settings/reviewer` | `{"ok":true}` |
| `GET` | `/settings/models` | `{ "<backend>": [ModelOption] }` |
| `POST` | `/settings/models/refresh` | `{"ok":true,"models":{ "<backend>": [ModelOption] }}` — body `{"backend":"claude" \| "codex" \| "all"}` (default `all`); rewrites `agent.available_models` in `WORKFLOW.md` and the live model list, without a reload. `503 settings_reloading` like the other settings writes |

`PUT /settings/reviewer` request body:

```json
{ "profile": "reviewer", "auto_review": true }
```

### Automations

| Method | Path | Request body | Success response |
|---|---|---|---|
| `PUT` | `/settings/automations` | `{"automations":[AutomationDef]}` | `{"ok":true}` |

There is no dedicated `GET /settings/automations`; the current list is exposed
via `GET /state` / `GET /events`.

Supported trigger types:

- `cron`
- `input_required`
- `tracker_comment_added`
- `issue_entered_state`
- `issue_moved_to_backlog`
- `run_failed`
- `pr_opened` — fires when a worker's PR is detected
- `rate_limited` — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven. `agent.max_switches_per_issue_per_window` and `agent.switch_window_hours` cap automated switching; reaching the cap suppresses switching rather than causing the trigger.
- `blockers_resolved` — fires when dependency audit observes a previously blocked issue becoming unblocked.

Tracker event triggers are poll-derived, not webhook-derived. The automation
loop runs every 15 seconds. `tracker_comment_added` compares only the latest
observed comment, so multiple comments between polls collapse to the latest
comment for trigger purposes.

Automation definitions preserve these optional policy/filter fields:

```json
{
  "id": "answer-stale-input",
  "enabled": true,
  "profile": "input-responder",
  "trigger": { "type": "input_required" },
  "filter": {
    "maxAgeMinutes": 30,
    "inputContextRegex": "tests|review"
  },
  "policy": {
    "autoResume": true
  }
}
```

```json
{
  "id": "rate-limit-switch",
  "enabled": true,
  "profile": "default",
  "trigger": { "type": "rate_limited" },
  "policy": {
    "autoResume": true,
    "switchToProfile": "fallback",
    "switchToBackend": "codex",
    "cooldownMinutes": 45
  }
}
```

`maxAgeMinutes` is only valid for `input_required` triggers.
`rate_limited` triggers require `policy.autoResume: true` for the automatic
profile/backend switch. `switchToProfile`, `switchToBackend`, and
`cooldownMinutes` are only valid for `rate_limited` triggers.
`blockers_resolved` may set `policy.moveToState`; the selected profile must
allow `move_state` before the daemon accepts that policy.

Validation failures return `400` with typed error codes such as:

- `duplicate_automation_id`
- `invalid_cron`
- `invalid_timezone`
- `invalid_regex`
- `invalid_trigger_type`
- `invalid_match_mode`
- `invalid_limit`

---

## Projects (Linear only)

These endpoints return `501 not_supported` for trackers without project support.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `GET` | `/projects` | — | `{"projects":[Project]}` |
| `GET` | `/projects/filter` | — | `{"filter":["alpha","beta"]}` or `{"filter":null}` |
| `PUT` | `/projects/filter` | `{"slugs":["alpha","beta"]}` or `{}` | `{"filter":[...],"ok":true}` |

Notes:

- A save that did not happen is never a `200`: a save refused by the
  `WORKFLOW.md` reload fence answers `503 settings_reloading` with
  `Retry-After: 1`, and a failed write `500 project_filter_not_saved`. In both
  cases the live filter is unchanged. When the front matter has no
  `project_slug` line, the save adds one at the end of the `tracker:` block; a
  front matter with no `tracker:` block is refused (M4-close D4).

- Empty array means “all issues”.
- Omitting `slugs` also serves all issues: like an empty array it comments
  `project_slug` out in `WORKFLOW.md`, and the live filter matches the file.
- A save applies to the tracker immediately and never reloads `WORKFLOW.md`
  (CORE-160). A save that raced a reload is not written (the next
  generation keeps the file's value); re-send it.

---

## Agent actions

These routes are intended for agent subprocesses that have been granted
daemon-backed permissions through profile `allowed_actions`.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/agent-actions/{identifier}/comment` | `{"body":"..."}` | `{"ok":true}` |
| `POST` | `/agent-actions/{identifier}/comment_pr` | `{"summary":"...","findings":[{"path":"...","line":42,"severity":"warning","body":"..."}]}` | `{"ok":true,"findings":N}` |
| `POST` | `/agent-actions/{identifier}/merge_pr` | `{"pr":42,"strategy":"squash"}` | `{"ok":true,"merge_commit":"<sha>","strategy":"squash"}` |
| `POST` | `/agent-actions/{identifier}/create-issue` | `{"title":"...","body":"..."}` | `{"ok":true,"issue":{...}}` |
| `POST` | `/agent-actions/{identifier}/move-state` | `{"state":"Todo"}` | `{"ok":true}` |
| `POST` | `/agent-actions/{identifier}/provide-input` | `{"message":"..."}` | `{"ok":true}` |

`merge_pr` runs the daemon-side gh-CLI sequence (`gh pr view` → `gh pr checks --required` → `gh pr merge`) with required-checks, block-label, and mergeable-state guards. `strategy` defaults to `squash` when omitted and must be one of `squash` / `rebase` / `merge`. Refusal returns `409 merge_blocked` with a structured reason (`blocked_label:<label>`, `checks_failed:<excerpt>`, `not_mergeable:<state>`). Idempotent: a second call with the same `(identifier, pr)` returns the previously-merged commit SHA with `already_merged: true`; a call made while another merge of the same `(identifier, pr)` is still running gets `409 merge_in_progress` and never runs a second `gh pr merge` — retry it to get the `already_merged` answer (CORE-161). A refused or failed merge does not block a later retry. Block-list labels default to `["needs-human","migration","auth","feature-flag","breaking"]` via `agent.merge_block_labels`. A successful merge fires `pr_merged` automations with the PR's url, base branch and head branch (`trigger.pr_url`, `trigger.pr_base_branch`, `trigger.pr_branch`) from the same `gh pr view --json …,url,baseRefName,headRefName`; an automation that cannot be dispatched does not change the `200` answer.

These routes require an `Authorization: Bearer <grant-token>` header carrying a
short-lived action grant for the specific issue and action.

When the daemon spawns a **local** agent subprocess for a profile that has any
`allowed_actions` configured, the following environment variables are injected
so the agent can call back into the daemon without operator-supplied secrets:

| Variable | Description |
|---|---|
| `ITERVOX_ACTION_TOKEN` | The short-lived per-run action grant. Pass as `Authorization: Bearer $ITERVOX_ACTION_TOKEN` on every `/agent-actions/...` call. |
| `ITERVOX_DAEMON_URL` | The base URL for the daemon (`http://127.0.0.1:<port>` by default). Build the action URL as `$ITERVOX_DAEMON_URL/api/v1/agent-actions/$ITERVOX_ISSUE_IDENTIFIER/<action>`. |
| `ITERVOX_ISSUE_IDENTIFIER` | The issue this run belongs to (e.g. `ENG-42`). |
| `ITERVOX_CREATE_ISSUE_STATE` | Set when `allowed_actions` includes `create_issue`; the tracker state for follow-up issues. Usually used as the `state` field in the create-issue body. |
| `ITERVOX_RUN_ID` | The orchestrator's run ID for this dispatch — useful for correlating logs. |

Profiles WITHOUT `allowed_actions` do not receive these env vars and the shim
PATH is unchanged. SSH remote workers also do not receive these env vars or the
local action shims in v0.2.0; the worker prompt warns that daemon-backed actions
are unavailable remotely.

Denials on these routes use codes such as:

- `unauthorized`
- `agent_action_denied`
- `not_supported`

---

## Environment contract for agent runs

Every agent turn itervox spawns — every backend, local or SSH — carries:

| Variable | Value | Meaning |
|---|---|---|
| `ITERVOX_AGENT` | `1` | This process is an itervox agent run, not an interactive session. |

Set on every turn. Presence is the signal; the value carries no meaning.

**This is not authentication.** Any process can set an environment variable.
`ITERVOX_AGENT` lets a repository owner express "I trust itervox runs"; it
proves nothing by itself. Branch protection on your remote is what actually
prevents a forced or protected-branch push.

`ITERVOX_AGENT` is distinct from the agent-action bridge variables documented
above (`ITERVOX_RUN_ID`, `ITERVOX_ACTION_TOKEN`, `ITERVOX_ISSUE_IDENTIFIER`,
`ITERVOX_DAEMON_URL`, `ITERVOX_CREATE_ISSUE_STATE`), which are set only when a
profile declares `allowed_actions` and the daemon action bridge is live for a
**local** worker. `ITERVOX_AGENT` has no such precondition — it is set
unconditionally, on every backend, including SSH remote workers. Do not use
the action-bridge variables to detect an itervox run; use `ITERVOX_AGENT`.

### Letting itervox commit in a repo that denies git writes

If your repository denies `git commit` / `git push`, itervox's workers are
blocked from the one thing they exist to do. Two different guards are in
play, and they behave differently:

- A `deny` **rule** in `.claude/settings.json` is already bypassed — itervox
  passes `--dangerously-skip-permissions` on every invocation.
- A `PreToolUse` **hook** returning `permissionDecision: "deny"` is **not**
  bypassed — hooks run regardless of permission mode. This is the case that
  actually blocks itervox, and the one the reference hook below handles.

Copy [`docs/examples/git-write-guard.example.mjs`](examples/git-write-guard.example.mjs)
into your own repository's `.claude/hooks/` and register it as a
`PreToolUse(Bash)` hook in your own `.claude/settings.json` to allow itervox
runs while still denying interactive `git commit` / `git push`, and still
refusing force-pushes and pushes to `main`/`master` in both cases.

---

## Core response shapes

### `ProfileDef`

```json
{
  "command": "claude",
  "prompt": "# reviewer INSTRUCTIONS\n\nReview the change.",
  "soul": "# reviewer SOUL\n\nYou are the code reviewer.",
  "instructions": "# reviewer INSTRUCTIONS\n\nReview the change.",
  "soulFile": ".itervox/agents/reviewer/SOUL.md",
  "instructionsFile": ".itervox/agents/reviewer/INSTRUCTIONS.md",
  "backend": "claude",
  "enabled": true,
  "allowedActions": ["comment", "move_state"],
  "createIssueState": "Todo"
}
```

`soulFile` and `instructionsFile` mirror the `WORKFLOW.md` profile references.
The dashboard profile editor uses `soul` and `instructions` as the authoritative
editable text for schema `2` profiles.

### `AutomationDef`

```json
{
  "id": "qa-ready",
  "enabled": true,
  "profile": "qa",
  "instructions": "Run the QA routine.",
  "trigger": {
    "type": "issue_entered_state",
    "state": "Ready for QA"
  },
  "filter": {
    "matchMode": "all",
    "states": ["Ready for QA"],
    "labelsAny": ["qa"],
    "identifierRegex": "^ENG-",
    "limit": 10,
    "inputContextRegex": "continue|branch"
  },
  "policy": {
    "autoResume": true
  }
}
```

### `TrackerIssue`

Returned by both `GET /issues` and `GET /issues/{identifier}`.

Important fields include:

- `orchestratorState`: `idle`, `running`, `retrying`, `paused`,
  `input_required`, `pending_input_resume`
- `agentProfile`, `agentBackend`
- `comments`, `labels`, `branchName`, `blockedBy`
- `blockedByDetails`: richer blocker metadata for UI/API clients. Each entry has
  `identifier` and may include `state` and `url`. `blockedBy` remains the
  compatibility string-array form.

### `IssueLogEntry`

```json
{
  "level": "INFO",
  "event": "action",
  "message": "Write — updated README.md",
  "tool": "Write",
  "detail": "{\"status\":\"completed\",\"exit_code\":0}",
  "time": "12:34:56",
  "sessionId": "abc123"
}
```
