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
- If `ITERVOX_SUPERVISOR_TOKEN` is set, that value is required instead,
  even with `server.allow_unauthenticated: true` (see below).
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

### Supervisor token (`ITERVOX_SUPERVISOR_TOKEN`)

A supervisor that starts the daemon — the macOS menu-bar app, a process
manager — passes its own token in `ITERVOX_SUPERVISOR_TOKEN`. The daemon
requires it as the bearer token exactly as it would a pinned
`ITERVOX_API_TOKEN`, with these differences:

- It wins over `ITERVOX_API_TOKEN` from the environment or `.itervox/.env`
  (a warning names the replacement, never a value), and over
  `server.allow_unauthenticated: true`: auth stays on.
- It is never printed: no tokenised dashboard URL on stderr, even on a
  terminal or with `ITERVOX_PRINT_TOKEN=1`; the terminal UI's dashboard URL
  carries no token; nothing is written to `<logs-dir>/api-token` (a stale
  file from an earlier generated-token start is removed). The value is also
  registered for exact-match log redaction, and it never reaches
  `HEARTBEAT.md`, `dashboard_url` or the daemon registry. The startup log
  line reads `dashboard URL (token withheld from logs)` with
  `token_source=ITERVOX_SUPERVISOR_TOKEN`.
- The daemon moves it into `ITERVOX_API_TOKEN` (which hooks and agents'
  `itervox action` calls inherit) and removes `ITERVOX_SUPERVISOR_TOKEN`
  from its environment.
- It must be 16–512 printable ASCII characters without spaces (surrounding
  whitespace is trimmed); anything else stops the process with exit code 2
  and an error that does not quote the value. `openssl rand -hex 32` is a
  good source.

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

## Supervisor contract

What a supervisor (the macOS menu-bar app in `desktop/`, #128) relies on to
find, start and talk to daemons. Stable within `schema_version` 1: fields may be added, but a
rename, removal or change of meaning bumps the version.

**Start.** Run the `itervox` binary with `--workflow <path>` and
`ITERVOX_SUPERVISOR_TOKEN=<token>` in its environment (see above). From
Finder or launchd the inherited PATH is minimal; the daemon resolves PATH
from the user's login shell itself (see
[Login-shell PATH](https://itervox.dev/cli/#login-shell-path)), so the
supervisor does not need to.

**Discover.** `itervox status --all --json` prints a JSON array (`[]` when
none) with one object per running daemon on the host, sorted by `workflow`,
then `pid`; `itervox status --json [--workflow PATH]` only the daemons of
that project. Exit code 0 unless the output cannot be written. Every object
has every key:

| Key | Type | Meaning |
|---|---|---|
| `schema_version` | int | `1` |
| `pid` | int | Daemon process id |
| `workflow` | string | Absolute path of its `WORKFLOW.md` |
| `project_dir` | string | Directory holding `WORKFLOW.md` (and `.itervox/`) |
| `url` | string | Dashboard/API base URL, e.g. `http://127.0.0.1:8090/` (same as `.itervox/dashboard_url`) |
| `host`, `port` | string, int | The bound address split out of `url` |
| `version`, `commit` | string | Build of the daemon binary (`dev` / `none` for a local build) |
| `started_at` | string | RFC 3339 UTC process start time |
| `token_required` | bool | Whether `/api/v1/*` needs `Authorization: Bearer` |
| `token_source` | string | `supervisor` (`ITERVOX_SUPERVISOR_TOKEN`), `env` (`ITERVOX_API_TOKEN`), `generated` (read it from `token_file`), `none` (`server.allow_unauthenticated: true`), `unknown` |
| `token_file` | string | `<logs-dir>/api-token` for a `generated` token, else `""` |
| `logs_dir` | string | The daemon's logs directory |
| `heartbeat` | string | Path of its `.itervox/HEARTBEAT.md` |
| `executable` | string | Path of the daemon binary |
| `path_source` | string | `login_shell` or `inherited` (see Login-shell PATH) |
| `source` | string | `registry`, or `process_scan` for a daemon started by a binary older than the registry |

A token is never part of the output. Unknown values are `""` (or `0`).

**Registry.** Each daemon writes `~/.itervox/daemons/<pid>.json` (directory
`0700`, file `0600`, the same object without `source`) once its socket is
bound, rewrites it on every `WORKFLOW.md` reload, and removes it on shutdown
together with `daemon.pid`, `dashboard_url` and `HEARTBEAT.md`.
`status --json` lists a record only while its pid is alive, the project's
`.itervox/daemon.pid` still names it and — for a pid file written with the
pid lock (every daemon since v0.2.1) — that lock is held, so a pid recycled
by an unrelated process after a `SIGKILL` is not mistaken for the daemon.
Records that fail the check are deleted (a daemon killed with `SIGKILL`
cannot remove its own). All path fields are absolute; a relative
`--logs-dir` is resolved against the daemon's working directory. `itervox demo` writes
no record. Daemons started by an older binary are found by process scan:
an `itervox` process whose working directory holds a `.itervox/daemon.pid`
naming it (and, for a lock-marked pid file, whose lock is held); for those `version`, `commit` and `started_at` are `""`,
`token_source` is `unknown` and `token_required` is `true`.

**Talk.** `GET {url}api/v1/health` needs no token; everything else under
`{url}api/v1/` takes `Authorization: Bearer <token>`.

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
| `loop_fresh` | The event loop is alive. Idle, it must have finished an iteration within 3× `polling.interval_ms` (at least 30 s). Inside a tick or event it stays fresh for up to 2 minutes (the tick budget): since #187 the poll's tracker reads run off the event loop and the tracker reads an iteration still makes share one 30 s deadline, so a healthy iteration takes milliseconds. Within one poll interval of startup (at least 30 s) it is fresh before the first tick. |
| `last_poll_ok` | The most recent tick polled the tracker and the poll succeeded (`false` before the first poll and while polling is shed for budget). |
| `config_invalid` | The last `WORKFLOW.md` reload failed validation; the daemon keeps running on its last valid config. |
| `degraded` | A rate-limited poll, an open tracker rate-limit gate, a tick that skipped its poll to keep the tracker budget for writes, an open tracker breaker (#187: three polls in a row hit a timeout, network error or 5xx), 1–2 consecutive failed polls, or `config_invalid`. Degraded is still ready. |
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
| `itervox_tracker_webhooks_total` | counter | `tracker`, `result` | Tracker webhook deliveries (`tracker.webhooks`); `result` is `accepted`, `ignored` (another repository, a pull request, a comment), `bad_signature`, `stale` (a Linear delivery outside the one-minute replay window) or `malformed`. |
| `itervox_tracker_store_pulls_total` | counter | `trigger` | List pulls of the tracker store: `cold` (first pull), `interval` (the periodic pull; with webhooks on, the safety net), `webhook` (a delivery whose by-ID read failed) or `view` (a state set read for the first time). |
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
- `trackerPoll` (#187) — the off-loop tracker poll, absent on older daemons:
  `{ "inFlight", "startedAt"?, "runningForMs"?, "lastFinishedAt"?,
  "lastDurationMs", "slowAfterMs", "slow", "degraded", "breakerOpenUntil"?,
  "consecutiveTransportFailures"? }`. The poll tick reads the tracker
  (running issues' states, the candidate list, input-required reply checks)
  on its own goroutine, at most one poll at a time, under a deadline of
  3 × `polling.interval_ms` (at least 30 s, at most 2 minutes); the event
  loop keeps handling worker exits and operator actions meanwhile.
  `inFlight`/`startedAt`/`runningForMs` describe the poll now running;
  `slow` is set when it (or, when idle, the last one) took longer than
  `slowAfterMs` (the interval plus half an interval, at least 1 s).
  `degraded` is set once three polls in a row hit a transport failure: the
  daemon then skips tracker reads until `breakerOpenUntil` (two poll
  intervals, 30 s to 5 minutes), then one poll probes the tracker; a probe
  that succeeds clears it.
- `syncStatus` (#117) — the tracker sync status, absent on older daemons:
  `{ "mode": "store" | "direct" | "local_sync", "lastPullAt"?, "lagMs"?, "lagWarnAfterMs",
  "stale", "syncIntervalMs", "cursorAt"?, "consecutiveFailures"?,
  "lastError"?: { "at", "kind", "message" }, "pendingWrites",
  "pendingUrgent", "pendingBatched", "pendingRetrying", "pendingDegraded",
  "pendingRateLimited", "oldestPendingAt"?, "nextPushAt"?, "pushIntervalMs" }`.
  `mode` is `store` when the local tracker store pulls in the background
  (`tracker.store.enabled`), `direct` when each poll reads the tracker, and
  `local_sync` (#118) when local issue files are synced with Linear or
  GitHub (`tracker.sync`): `lastPullAt` is then the sync's last good cycle,
  `syncIntervalMs` is `tracker.sync.interval_ms`, and the `pending*` counts
  include the sync's queued remote writes.
  `lastPullAt` is the last successful pull (store) or candidate poll
  (direct), absent until one succeeds; `lagMs` is now − `lastPullAt` when the
  snapshot was built (clients should recompute it on their own clock).
  `stale` is `lagMs > lagWarnAfterMs`, or "never pulled and the last pull
  failed". `lagWarnAfterMs` is three pull intervals and at least five
  minutes (`syncIntervalMs`: `tracker.store.sync_interval_ms`, the webhook
  safety net with `tracker.webhooks`, or `polling.interval_ms` in direct
  mode). `cursorAt` is the newest tracker update the store has seen.
  `lastError` is the newest failed pull while pulls keep failing (a good
  pull clears it), classified: `kind` is `rate_limited`, `auth`,
  `not_found`, `api_status`, `graphql`, `timeout`, `network`, `canceled` or
  `unknown` (treat unknown values as opaque), and `message` is a fixed
  summary, never the tracker's response. The `pending*` counts describe the
  write-ahead outbox and overlap: `pendingBatched` waits for
  `tracker.push_interval_ms` (`pushIntervalMs`, 0 with the outbox off),
  `pendingRetrying` failed at least once, `pendingDegraded` failed five
  times or more, `pendingRateLimited` waits for a tracker rate-limit reset.
  `nextPushAt` is when the next queued write is due (at or before
  `generatedAt` means now).
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
- An issue whose worktree branch is checked out in another worktree (#193)
  has `ineligibleReason: "branch_in_use"` until that worktree frees the
  branch; no retry is scheduled for it.
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

## Run history

History rows (`history` in `GET /state`, and the endpoints below) describe
finished runs, oldest first, from the same bounded ring (200 runs, persisted
to the daemon's history file). Each row may carry, besides the fields above
(#186):

- `runId` — the run's ID (the same ID as running rows and logs).
- `profileName` — the profile the run used.
- `promptDigest` — the run's **prompt version**: 64 hex characters of
  SHA-256 over a versioned, length-prefixed concatenation of the profile's
  `SOUL.md`, its `INSTRUCTIONS.md`, the resolved rule set (sorted by id;
  each rule's id, severity and text) and the prompt template (the
  `WORKFLOW.md` body, or `agent.reviewer_prompt` for a reviewer run that has
  one), computed when the run is dispatched. Two runs with the same digest
  were given the same prompt inputs. Treat it as an opaque identifier; the
  dashboard shows its first 12 characters.
- `promptParts` — `{ "soul"?, "instructions"?, "rules"?, "template"? }`:
  16-hex-character digests of each component (absent when the run had none),
  so two versions can be compared part by part. History stores only these
  hashes, never prompt text.
- `steers` — operator messages delivered to the run from the Sessions panel:
  `[{ "text", "at", "delivery": "live" | "resume" }]`, in delivery order.
  Text is redacted like the rest of run data and cut to 1 KiB; a run keeps
  its newest 20, and `steersDropped` counts older ones that were let go.
  Typing into a tmux pane is not recorded.

All of these are omitted on rows recorded before they existed.

### `GET /api/v1/history`

The run history: `{ "history": [HistoryRow] }`. `?prompt=<digest prefix>`
(1–64 hex characters, case-insensitive) keeps only runs whose
`promptDigest` starts with it; rows without a digest never match. An
invalid prefix is `400 invalid_prompt_digest` (`field: "prompt"`).

### `GET /api/v1/history/prompt-versions`

The history grouped by prompt version, most recently used version first:

```json
{
  "versions": [
    {
      "promptDigest": "3f9a…",
      "runs": 12,
      "statuses": { "succeeded": 9, "failed": 3 },
      "successRate": 0.75,
      "firstSeenAt": "2026-10-01T09:00:00Z",
      "lastSeenAt": "2026-10-18T17:30:00Z",
      "avgDurationMs": 184000,
      "profiles": ["implementer"]
    }
  ],
  "unversionedRuns": 4
}
```

`statuses` counts runs per history status, `successRate` is the share that
succeeded (0–1), `firstSeenAt` / `lastSeenAt` are the earliest and latest run
start, `avgDurationMs` is the mean run duration (#200), and `unversionedRuns`
counts rows recorded without a digest. The dashboard's Repo Analysis →
Prompt versions section reads this endpoint.

---

## Repo readiness

### `GET /api/v1/repo/readiness`

The repository-level `itervox doctor` checks (#200), for the dashboard's
**Repo Analysis → Agent readiness** section. The endpoint and `itervox doctor`
build their rows from the same check functions, so they report the same IDs
and statuses for the same repository (doctor prints them in its
`repo readiness:` section). The checks run on the request goroutine, never on
the orchestrator's event loop; the tracker probe has its own 8 s timeout and
is reported as `fail` when it does not answer in time.

```json
{
  "workflow": "/srv/app/WORKFLOW.md",
  "generatedAt": "2026-10-21T10:00:00Z",
  "checks": [
    { "id": "schema_version", "title": "WORKFLOW.md schema 2 marker", "status": "pass", "detail": "itervox_schema_version: 2" },
    { "id": "gitignore_carveouts", "title": ".gitignore keeps .itervox/agents and .itervox/handoff committable", "status": "warn",
      "detail": "the root .gitignore ignores .itervox/ without the carve-outs: !.itervox/agents/** …",
      "fix": "run `itervox init --update --workflow /srv/app/WORKFLOW.md` (adds the carve-outs), or add the lines yourself" }
  ]
}
```

`status` is `pass`, `warn` or `fail`; `fix` is the text doctor prints, set for
anything that is not a pass. Check IDs, in order:

| ID | Checks | Not a pass when |
|---|---|---|
| `schema_version` | `itervox_schema_version: 2` in WORKFLOW.md, and the workflow loads | `fail`: missing, older or unloadable |
| `gitignore_carveouts` | the root `.gitignore` keeps `.itervox/agents/**` and `.itervox/handoff/**` committable | `warn`: it ignores `.itervox/` without `!.itervox/`, `!.itervox/agents/`, `!.itervox/agents/**`, `!.itervox/handoff/`, `!.itervox/handoff/**` |
| `instruction_docs` | `CLAUDE.md` (Claude profiles) and `AGENTS.md` (Codex profiles) at the repository root | `warn`: a backend in use has no instruction file |
| `bare_fetch_refspec` | the shared bare clone's fetch (#195) | `fail`: the last fetch failed; `warn`: still on the pre-#195 refspec, or stale |
| `profile_refs` | skill and subagent references in profile SOUL.md / INSTRUCTIONS.md (#86) | `warn`: a reference does not resolve |
| `tracker_reachable` | one read of the tracker API (Linear `{ viewer }`, GitHub repository access) | `fail`: the read failed or timed out; `warn`: GitHub token without push access |

`503 readiness_unavailable` when the daemon was built without the checks.

## Repo evals

### `GET /api/v1/repo/evals`

The latest saved eval result per profile and scenario (#201), for the
dashboard's **Repo Analysis → Evals** section. `make evals` (recorded mode),
`make evals-live`, `make evals-record` and `itervox-evals` save each judged
report as JSON to `<project>/.itervox/evals/<ISO8601>.json` (the newest 20
kept; the directory carries its own `.gitignore`); the endpoint reads the
newest of those files on the request goroutine. It never starts an eval run.

```json
{
  "dir": "/srv/app/.itervox/evals",
  "reports": [
    { "file": "20261021T090000.000Z.json", "mode": "live", "finishedAt": "2026-10-21T09:00:00Z", "pass": 7, "total": 8 }
  ],
  "cases": [
    {
      "profile": "reviewer", "scenario": "rejects-and-moves-back", "pass": false,
      "failures": ["forbidden phrase present in comments: /ai-approved"],
      "promptDigest": "9f2c…", "digestSource": "live",
      "ranAt": "2026-10-21T09:00:00Z", "mode": "live", "file": "20261021T090000.000Z.json",
      "regression": true, "previousDigest": "41ab…", "previousAt": "2026-10-20T09:00:00Z",
      "results": 3
    }
  ]
}
```

- `failures` lists every failed assertion: missing required actions,
  forbidden actions, `forbidden_phrases` present, missing marker phrases.
  `error` is a live run that could not be judged as complete (backend failure,
  budget); `stale` a recording older than its sources.
- `promptDigest` is the prompt version the case ran against, computed with
  the same digest as run history (#186), so it matches **Prompt versions**.
  `digestSource` is `live` (computed by the live run from the workflow it ran)
  or `recording` (read from a live recording's `meta` line). Recorded mode
  replays a fixed transcript, so it has a digest only when the recording was
  made by a live run since #201; the hand-written fixtures have none.
- `regression` is set when the case fails on its digest and the newest result
  on the previous digest passed (`previousDigest`, `previousAt`). Results
  without a digest never mark one.
- `mode` is `recorded`, `live` or `record`; `results` counts the reports read
  that include the case. `error` at the top level means the directory could
  not be read; no directory is an empty list.

`503 evals_unavailable` when the daemon was built without the provider.

## Repo hotspots

### `GET /api/v1/repo/hotspots`

Files and directories ranked by how often agent work on them went wrong
(#201), for **Repo Analysis → Hotspots**. A run in the history window counts
when it `failed` or `stalled`, asked for input (`input_request`), was a
reviewer whose verdict was `block` (`change_request`), or opened a PR that
was later reverted on the base branch (`reverted`). Each such run is
attributed to the files its branch changed.

A background job in the daemon computes it, never on the orchestrator's event
loop: it reads run history and runs read-only git through `internal/gitexec`
(no fetch), one refresh at a time, at most 100 run lookups per refresh, each
git call under a 10 s timeout and the refresh under 2 minutes. A request
returns the latest result at once and starts a refresh only when that result
is older than 10 minutes (`refreshing: true` while it runs; the dashboard
polls until it lands). Each run's changed files are cached by run ID, so a
refresh runs git only for runs it has not attributed yet (an unattributed run
is retried after an hour).

```json
{
  "generatedAt": "2026-10-21T09:05:00Z",
  "refreshing": false,
  "repo": "/srv/ws/.bare",
  "baseRef": "refs/remotes/origin/main",
  "runsConsidered": 200, "runsWithSignals": 31, "runsAttributed": 27, "runsUnattributed": 4,
  "files": [
    {
      "path": "internal/orchestrator/event_loop.go",
      "failed": 3, "stalled": 1, "changeRequests": 2, "inputRequests": 0, "reverted": 1, "total": 7,
      "issues": ["ENG-12", "ENG-40"],
      "runs": [
        { "runId": "r-…", "identifier": "ENG-40", "status": "succeeded", "kind": "reviewer", "profile": "reviewer",
          "finishedAt": "2026-10-21T08:40:00Z", "signals": ["change_request"] }
      ]
    }
  ],
  "directories": [ { "path": "internal/orchestrator/", "…": "same shape" } ],
  "notes": ["4 run(s) that went wrong could not be attributed to files: …"]
}
```

Rows are sorted by `total` (a run with two signals counts twice), at most 50
per list, with at most 20 runs each, newest first; a directory counts a run
once however many of its files the run changed.

How the signals are read, and where they are approximations:

- **Repository.** The shared bare clone in worktree mode with
  `workspace.clone_url`, the workspace root in plain worktree mode, otherwise
  the directory holding `WORKFLOW.md`. Refs are as fresh as the daemon's (or
  your) last fetch. The base is `workspace.base_branch` on origin, else the
  local branch.
- **Changed files.** Run history now records each run's branch (the branch
  its exit reported, else the issue's worktree branch), its PR URL and a
  reviewer's verdict. While the branch exists and adds commits, the files are
  `git diff <base>...<branch>` (against the merge base) — the branch's
  current state, not exactly what that one run changed. Once the branch is
  merged or gone, they are the files of the commit on the base whose message
  names the PR (`(#N)` squash subject or `Merge pull request #N`), against
  its first parent. A run with neither is counted in `runsUnattributed`.
- **Change requests** count reviewer runs whose recorded verdict was `block`.
  Reviewer runs recorded before #201 carry no verdict and are not counted (a
  note says how many).
- **Reverted PRs** come from the newest 500 commits on the base: a revert
  subject `Revert "… (#N)"`, a GitHub revert branch `revert-N-…`, or the PR
  number in the subject of a commit a revert names (`This reverts commit <sha>`).
  The newest non-reviewer run with that PR URL gets the signal.
  Reverts outside that window, or of commits that name no PR, are missed.
- The window is the daemon's run history (the newest 200 runs).

`503 hotspots_unavailable` when the daemon was built without the job.

---

## Tracker webhooks

### `POST /api/v1/webhooks/{tracker}`

Receives Linear (`linear`) or GitHub (`github`) issue webhooks when `tracker.webhooks.enabled` is set; otherwise, and for the other tracker, `404`. No bearer token (the tracker cannot send one) and exempt from the unauthenticated-mode Host guard: instead every delivery must be signed with `tracker.webhooks.secret` (`Linear-Signature`, or GitHub's `X-Hub-Signature-256`), and a Linear delivery must be less than a minute old (`webhookTimestamp`). Answers `202` for an accepted or ignored delivery, `401` for a bad signature or a stale delivery, `400` for an unreadable payload, `413` over 1 MiB. A delivery only names issues: the tracker store re-reads them by ID and keeps the ones in scope. See [Tracker webhooks](/guides/tracker-webhooks/).

### `POST /api/v1/webhooks/pr-events`

Receives GitHub `check_run`, `status`, `pull_request` and `pull_request_review` deliveries for the PR-event automation triggers (#173) when `pr_events.webhook_secret` is set; otherwise `404`. Same exemptions as the tracker route (no bearer token, past the Host guard) and the same rule: every delivery must carry a valid `X-Hub-Signature-256` made with `pr_events.webhook_secret`. Answers `202` for an accepted or ignored delivery (a `ping`, another event or action, a pending status, or a PR no Itervox run recorded), `401` for a bad signature, `400` for an unreadable payload, `413` over 1 MiB. Accepted deliveries only become automation dispatches through the orchestrator's event loop, which dedups them per check, push, review and mergeable transition. Counted in `itervox_tracker_webhooks_total{tracker="pr-events"}`. See [PR events](/guides/automations/#pr-events-on-managed-pull-requests).

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
| `POST` | `/issues/{identifier}/ai-review` | — | `202 {"queued":true,"identifier":"ENG-1"}` | Reviewer dispatch: a fresh review by the configured reviewers (the whole `reviewer_profiles` chain, previous verdicts cleared) |

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

## Sessions

The Sessions panel (#178) — the dashboard's `/sessions` page and the Mac app's
Sessions window. A session is an agent CLI session, identified by the CLI's
own session ID; itervox writes only to sessions it started. Sessions started
by hand on the daemon's host are listed read-only with `owner: "external"`.

### `GET /api/v1/sessions`

The daemon's agent sessions: every running itervox run, every session
waiting for (or resuming from) a human reply, and the recent sessions started
by hand on the daemon's host. Sessions waiting for input come first, then
resuming ones, then running ones, oldest first within each; sessions started
by hand come last, most recently written first.

```json
{
  "generatedAt": "2026-10-15T09:00:00Z",
  "sessions": [
    {
      "id": "6f1c…",
      "owner": "itervox",
      "identifier": "ENG-2",
      "backend": "claude",
      "status": "input_required",
      "queuedAt": "2026-10-15T08:59:00Z",
      "inputContext": "Which database should I use?"
    },
    {
      "id": "0b9e…",
      "runId": "run-2a78…",
      "owner": "itervox",
      "identifier": "ENG-1",
      "title": "Fix the login redirect",
      "profile": "coder",
      "backend": "claude",
      "workerHost": "gpu-1",
      "status": "running",
      "startedAt": "2026-10-15T08:40:00Z",
      "turnCount": 2,
      "lastEvent": "Running the test suite",
      "steering": "resume",
      "messages": [
        {"id": "msg-…", "text": "Also update the docs.", "status": "delivered",
         "delivery": "resume", "queuedAt": "…", "deliveredAt": "…"}
      ]
    },
    {
      "id": "c41d…",
      "owner": "external",
      "identifier": "",
      "title": "Add a health check",
      "backend": "claude",
      "status": "running",
      "cwd": "/work/app",
      "startedAt": "2026-10-15T07:10:00Z",
      "updatedAt": "2026-10-15T08:58:00Z",
      "inUseReason": "a claude process (4242) is running in the session's directory; it may be this session"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `id` | The agent CLI's session ID (Claude Code session, Codex thread). Empty in the first seconds of a run, until the CLI reports it; `runId` addresses the session meanwhile. |
| `runId` | Itervox's run ID (running sessions only). |
| `owner` | `itervox` for this daemon's runs — only those take replies and messages; `external` for a session started by hand (read-only, no `identifier`). |
| `status` | `running`, `input_required` or `pending_input_resume`; for a session started by hand, `running` while its CLI may still be running (with `inUseReason`) and `exited` once it is not. |
| `cwd` | The directory of a session started by hand. |
| `updatedAt` | A session started by hand: its transcript's last write. |
| `inUseReason` | A session started by hand with `status: "running"`: why it counts as still running. |
| `kind` | `tmux` for a session itervox started in tmux (New session); `reviewer` for a reviewer run. |
| `tmuxPane` | The tmux pane the session runs in, `{"id": "%3", "label": "work:0.0"}`; `null` when none. |
| `controllable` | The panel may show its screen and type into it ([Sessions in tmux](#sessions-in-tmux)): a session itervox started in tmux, or a hand-started one in a matched pane with `sessions.tmux_control`, while tmux is installed. |
| `attachCommand` | A session itervox started in tmux: the command that attaches a terminal to it. |
| `startedAt` / `queuedAt` | When the run started / when it started waiting for input. |
| `inputContext` | The agent's question while it waits. |
| `steering` | Running sessions: `live` (a message is written into the running turn — local Claude with `agent.live_steering`) or `resume` (delivered as the prompt of the next turn, which resumes the session). |
| `messages` | Operator messages sent to the issue's sessions, newest last (at most 20): `status` `queued`, `delivered` (with `delivery` `live` or `resume`) or `dropped` (with `reason`). |

The response also carries `tmux` — `{"available": true, "version": "tmux 3.4",
"installHint": "brew install tmux", "control": false}`, re-detected every
discovery pass (`control` is `sessions.tmux_control`) — and `newSession` —
`{"projectRoot": "…", "folders": ["…"], "profiles": ["…"], "max": 8}`, what
New session offers (the project directory, then itervox's workspaces).

`503 not_configured` when the daemon was built without the sessions client.
Messages are runtime state: they do not survive a daemon restart. Which sessions started by hand are listed is set by
[`sessions:`](configuration.md#sessions) (default: the last 24 hours, inside
the project directory only).

### `POST /api/v1/sessions/{id}/message`

Queues an operator message for a running session. `{id}` is the session's
`id`, or its `runId` before the CLI reported an ID. For a controllable
session in tmux the text is pasted into its pane instead, then Enter is
pressed unless `submit` is `false` (`{"text": "…", "submit": false}`;
`message` and `text` are interchangeable), and the answer is `202` with
`"status": "delivered", "delivery": "tmux"`.

```json
{"message": "Also update the docs."}
```

`202 {"message":{"id":"msg-…","text":"…","status":"queued","queuedAt":"…"}}` —
the message is queued; its `status` in `GET /api/v1/sessions` moves to
`delivered` or `dropped`. A session waiting for input takes a reply through
`POST /issues/{identifier}/provide-input` instead.
A delivered message is also kept, redacted, in the run's history entry
(`steers`, see [Run history](#run-history)).

| Status | Code | When |
|---|---|---|
| `400` | `bad_request` | Missing, empty or over-10 KiB `message` (`field: "message"`) |
| `403` | `not_owned` | Itervox did not start that session and it is not controllable through tmux |
| `404` | `not_found` | No running session with that ID |
| `409` | `input_required` | The session is waiting for input: use `provide-input` |
| `409` | `tmux_unavailable` / `pane_gone` | A tmux session: see [Sessions in tmux](#sessions-in-tmux) |
| `503` | `orchestrator_busy` | Event queue full (`Retry-After: 1`) |

### `GET /api/v1/sessions/{id}/transcript`

A page of the transcript of a session started by hand, read from the CLI's
own transcript file. Query: `limit` (1–500,
default 100 entries) and `before` (the cursor from the previous page; omitted
= the newest entries). Entries are oldest first; each text is redacted with
the daemon's log redaction and abbreviated (`truncated: true`). Tool calls
appear by name (`kind: "tool"`), tool results abbreviated
(`kind: "tool_result"`); thinking, sub-agent sidechains and the CLI's own
context messages are left out.

```json
{
  "id": "c41d…",
  "backend": "claude",
  "entries": [
    {"role": "user", "kind": "text", "text": "Add a health check", "at": "2026-10-15T07:10:02Z"},
    {"role": "assistant", "kind": "tool", "text": "Edit", "at": "…"},
    {"role": "assistant", "kind": "text", "text": "Done: GET /healthz returns 200.", "at": "…"}
  ],
  "before": 73400320,
  "hasMore": true,
  "size": 74066206
}
```

`before` is present only when `hasMore`. `404 not_found` for a session that is
not listed (only a listed session's transcript can be read — the path never
comes from the client); `400 bad_request` for a bad `before` or `limit`.

### Sessions in tmux

Sessions running in a tmux pane (#178) — hand-started ones the daemon matched
to a pane, and the ones it started with `POST /api/v1/sessions` — can be read
and typed into through tmux. A hand-started session is controllable only with
[`sessions.tmux_control: true`](configuration.md#sessions); every write needs `tmux` on the
daemon's `PATH` and re-checks, at that moment, that the pane still exists and
still runs the session's process. Writes are audited (`sessions: tmux send`
with session, pane, kind and size — never the text) and counted in
`itervox_tmux_sends_total{kind}`.

Errors common to the tmux endpoints:

| Status | Code | When |
|---|---|---|
| `403` | `not_controllable` | Not running in a pane itervox may control (keys, screen) |
| `409` | `tmux_unavailable` | `tmux` is not installed on the daemon's host; `message` carries the install command |
| `409` | `pane_gone` | The pane is gone, or no longer runs this session |

#### `GET /api/v1/sessions/{id}/screen`

The pane's text: `capture-pane` of the last `lines` (1–2000, default 200)
lines of screen and scrollback, plain text without escape sequences.

```json
{"id": "itervox-3f9a1c", "pane": "%7", "label": "itervox-3f9a1c:0.0", "text": "╭─ Claude Code …", "lines": 200, "capturedAt": "2026-10-15T09:00:01Z"}
```

`404 not_found` for an unknown session; `400 bad_request` for a bad `lines`.

#### `POST /api/v1/sessions/{id}/keys`

Presses one key: `{"key": "Enter"}`. Only `Enter`, `Escape`, `Up`, `Down`,
`Left`, `Right`, `Tab`, `C-c` and the digits `1`–`9` are accepted
(`400 key_not_allowed` otherwise). `202 {"ok": true, "key": "Enter"}`.

#### `POST /api/v1/sessions`

Starts an interactive agent CLI in a new tmux session `itervox-<id>` (New
session):

```json
{"backend": "claude", "cwd": "/work/app", "profile": "coder", "prompt": "Fix the flaky login test."}
```

`backend` is `claude` or `codex`. `cwd` (default: the `WORKFLOW.md`
directory) must be the project directory, a directory inside it, or one of
itervox's workspaces. The CLI is the profile's command (else `agent.command`)
when its binary is that backend, else the backend's binary, run interactively
(no `-p`) with the profile's `permission_mode` — by default the confining one:
`--permission-mode acceptEdits` (Claude), `--sandbox workspace-write` (Codex) —
and `prompt` as its first prompt. The session is owned by itervox (`owner:
"itervox"`, `kind: "tmux"`), controllable whatever `sessions.tmux_control`
says, and does not count toward `max_concurrent_agents`.

`201 {"session":{…,"kind":"tmux","controllable":true,"attachCommand":"tmux attach -t itervox-3f9a1c"}}`.

| Status | Code | When |
|---|---|---|
| `400` | `bad_request` | Bad `backend`, a `cwd` outside the project and workspaces (`field: "cwd"`), an unknown `profile`, the CLI not on the daemon's `PATH` (`field: "backend"`), an over-10 KiB `prompt` |
| `409` | `tmux_unavailable` | `tmux` is not installed |
| `429` | `too_many_sessions` | 8 tmux sessions itervox started are running |

These sessions are runtime state carried across `WORKFLOW.md` reloads; after
a daemon restart they keep running in tmux but are listed as sessions started
by hand.

#### `DELETE /api/v1/sessions/{id}`

Kills a tmux session itervox started (`tmux kill-session`). `200 {"ok": true}`;
`403 not_owned` for any other session; `404 not_found` for an unknown id;
`409 tmux_unavailable` without tmux.

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

The skills endpoints expose the Repo Analysis -> Capabilities inventory and recommendation
surface. They use the same dashboard bearer token as the rest of `/api/v1`.

| Method | Path | Request body | Success response | Notes |
|---|---|---|---|---|
| `GET` | `/skills/inventory` | — | `Inventory` | `503 inventory_unavailable` before the first successful scan |
| `POST` | `/skills/scan` | — | `Inventory` | Forces a fresh filesystem scan |
| `GET` | `/skills/issues` | — | `InventoryIssue[]` | Static analyzer recommendations |
| `POST` | `/skills/fix` | `{"issueID":"UNUSED_PROFILE","fix": Fix}` | `{"status":"ok"}` | v0.2.0 only applies the non-destructive `UNUSED_PROFILE` `edit-yaml` fix; unsafe actions such as `remove-mcp` are rejected |
| `GET` | `/skills/analytics` | — | `AnalyticsSnapshot` | Includes `HasRuntimeEvidence`; `503 analytics_unavailable` only before analytics can be computed |
| `GET` | `/skills/analytics/recommendations` | — | `Recommendation[]` | Runtime-side recommendations; empty until `HasRuntimeEvidence` is true |
| `POST` | `/skills/check-refs` | `{"profile":"worker","command":"codex","backend":"codex","text":"…"}` | `{"backend":"codex","sshHosts":[],"refs":[{"kind":"skill","name":"tdd","from":9,"to":12,"status":"missing","severity":"warn","issueId":"MISSING_SKILL_REF","message":"…"}]}` | #123: runs the `itervox doctor` reference check over one draft prompt (SOUL.md or INSTRUCTIONS.md) for a profile with that command and backend. `backend` is the one the daemon resolves; each reference occurrence has `status` `ok`, `missing` or `outside_repo` (only with SSH hosts), and `from`/`to` are UTF-16 offsets into `text`. `503 inventory_unavailable` before the first scan; `413` over 256 KiB |
| `GET` | `/skills/profile-backends` | — | `{"default":"claude","profiles":{"reviewer":"codex"}}` | #123: the backend each configured profile runs on, resolved like doctor and the dispatcher do; `default` is the workflow's |

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
- `pr_merged` — fires when the daemon-side `merge_pr` action merges an itervox-managed PR
- `pr_check_completed`, `pr_synchronized`, `pr_review_submitted`, `pr_mergeable_changed` — fire on GitHub activity on a PR an Itervox run recorded; sourced from `POST /api/v1/webhooks/pr-events` or a poll of the open managed PRs (#173)
- `rate_limited` — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven. `agent.max_switches_per_issue_per_window` and `agent.switch_window_hours` cap automated switching; reaching the cap suppresses switching rather than causing the trigger.
- `blockers_resolved` — fires when dependency audit observes a previously blocked issue becoming unblocked.

Tracker event triggers are poll-derived, not webhook-derived. The event poll
runs every `polling.automation_interval_ms` (default 15 seconds) and fetches only
the states its rules need (see `polling.automation_interval_ms` in
[configuration.md](configuration.md)). `tracker_comment_added` compares only the latest
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
- A project filter save changes only the project part of the combined
  scope below; the assignee and labels stay.

### Combined tracker scope (#182, Linear only)

The scope is the AND of the projects, an assignee and a label filter (any
of the labels). `501 not_supported` for trackers without it.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `GET` | `/tracker/scope` | — | `{"scope":{"projects":["ENG"],"assignee":"me","labels":["agent"]}}` |
| `PUT` | `/tracker/scope` | `{"projects":[...],"assignee":"me","labels":[...]}` | `{"scope":{...},"ok":true}` |
| `GET` | `/tracker/labels` | — | `{"labels":["agent","bug"]}` |
| `GET` | `/tracker/users` | — | `{"users":[{"id":"…","name":"Ada","email":"ada@example.com"}]}` |

- Empty `projects` is every project, `""` `assignee` anyone (`me`, a user id
  or an email otherwise), empty `labels` no label filter. Any combination is
  allowed.
- The assignee is resolved before anything is written: one that matches no
  user the key can see answers `422 assignee_not_found` and nothing changes.
  The reload fence answers `503 settings_reloading`, a failed resolve or
  write `500 tracker_scope_not_saved`.
- The save writes `project_slug` (commented out for every project),
  `assignee` and `labels` in `WORKFLOW.md` (a cleared key is commented out),
  applies the scope in memory without a reload, and makes the tracker store
  pull again.
- The snapshot carries `trackerScope` (the header label, e.g.
  `ENG · assigned to me · label: agent`), `scopeAssignee`,
  `scopeAssigneeName` and `scopeLabels`; a running row whose issue left the
  scope mid-run has `outOfScope: true`.

### Tracker filter (#198, Linear and GitHub)

The tracker filter generalises the scope: a list of groups, conditions
ANDed inside a group, groups ORed (see the configuration reference, "Tracker
filter"). Keys are the `WORKFLOW.md` condition names: `projects`, `teams`,
`assignee`, `labels_any`, `labels_all`, `labels_none`, `priority`, `states`,
`cycle`, `milestone`. `501 not_supported` for trackers without one.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `GET` | `/tracker/scope` | — | `{"scope":{"projects":[],"assignee":"","labels":[],"filter":[{"labels_any":["agent"]}],"filterMode":true,"conditions":["assignee","labels_any",…]}}` |
| `PUT` | `/tracker/scope` | `{"filter":[{"projects":["ENG"],"labels_none":["blocked"]},{"assignee":"me"}]}` | `{"scope":{...},"ok":true}` |
| `POST` | `/tracker/filter/preview` | `{"filter":[...]}` | `{"count":12,"identifiers":["ENG-1","ENG-4",…]}` |
| `GET` | `/tracker/teams` | — | `{"teams":[{"id":"…","key":"ENG","name":"Engineering"}]}` (`[]` on GitHub) |

- `GET /tracker/scope` always returns `filter`: `tracker.filter`, or the
  legacy keys as one implicit group (`filterMode: false`), `[]` for every
  issue. `conditions` lists what the tracker supports (every condition on
  Linear; `assignee`, the label conditions, `states` and `milestone` on
  GitHub).
- `PUT` with `filter` validates it with the `WORKFLOW.md` parser (an
  unknown condition, an empty group, a bad priority or an unsupported
  condition: `422 invalid_filter`), resolves its assignees (`422
  assignee_not_found`), writes `tracker.filter` as one flow-style line,
  comments the legacy keys out (`project_slug` only on Linear) and applies
  the filter without a reload; the tracker store pulls again and the
  automation poll rebaselines. `[]` reads every issue. The reload fence
  answers `503 settings_reloading`. A legacy body (`projects` / `assignee` /
  `labels`, Linear only) comments `tracker.filter` out.
- `POST /tracker/filter/preview` is read-only: it counts the issues in the
  active states the filter matches, through the same compiled query the poll
  uses, and lists the first 10 identifiers. Nothing is saved.
- While a filter is in effect, `PUT /projects/filter` answers `409
  tracker_filter_in_effect` (project_slug and tracker.filter cannot both be
  set) and the TUI picker does not save.
- The snapshot's `trackerScope` is the filter's summary (e.g.
  `ENG · agent|ai · ¬blocked  OR  me · urgent`) and `filterConditions` the
  supported conditions (present when the tracker has a filter).

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
- `review` (`GET /issues/{identifier}` only): the issue's latest review, when it
  has one. `verdicts` lists each finished reviewer (`profile`, `verdict`
  `approve`/`block`, `reasons`, line `comments`, `recordedAt`); `outcome`
  (`blocked`, `approvals`, `blocks`, `quorum`) appears once every reviewer has
  run; `inProgress` is set while a multi-reviewer chain is still running.

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
