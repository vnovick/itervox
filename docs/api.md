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
  terminal or with `ITERVOX_PRINT_TOKEN=1`; nothing is written to `<logs-dir>/api-token` (a stale
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

### Token scopes (#214)

Every authenticated route requires a **scope**. Scopes are ordered — each
includes everything below it:

| Scope | Allows |
|---|---|
| `viewer` | `GET` routes and the SSE streams only. Secret values are redacted: profile and agent commands have inline env-assignment values, URL credentials and known key shapes masked (`snapshot.profileDefs`, `GET /settings/profiles`), and `GET /settings/layers` withholds each layer's raw file text (`raw: ""`, `redacted: true`). |
| `operator` | adds dispatch and issue control (cancel, resume, terminate, state, profile/backend overrides), steering messages, input replies, approvals, `POST /dispatch/pause` and `/resume`, memory edits, log clearing, outbox retries. Redaction as for viewer. |
| `admin` | adds settings, token management, tmux control (start, keys, kill), workspace deletion, the automations configuration and the rules editor. Sees secrets as written. |

Where a token comes from decides its scope:

- The **bootstrap token** — `ITERVOX_API_TOKEN`, the auto-generated
  ephemeral token, or `ITERVOX_SUPERVISOR_TOKEN` (the Mac app) — is always
  `admin`. Nothing about it changed.
- **Stored tokens** live in `.itervox/tokens.json` next to `WORKFLOW.md`
  (gitignored, mode 0600) as SHA-256 hashes only, each with an `id`,
  `name`, `scope`, `created`, `lastUsed` and optional `expires`. Their
  secrets start with `itx_`. Manage them with `itervox token
  create|list|revoke|rotate` (works with or without a running daemon) or
  Settings → Access. A running daemon notices a change made by the CLI
  within one second (it checks the file's identity and mtime at most every
  200 ms) and one made through the API at once — no restart. Expired tokens
  are refused. An unreadable or invalid `tokens.json` makes the daemon
  refuse every stored token (the bootstrap token keeps working) and is never
  overwritten.
- With `server.allow_unauthenticated: true` (and no supervisor token) every
  caller is `admin`, as before; stored tokens are not consulted and the
  token-management routes answer `503 tokens_unavailable`. The startup
  warning says so.

A request below the route's scope gets `403` with the scope it needed in
both the body and the `X-Itervox-Required-Scope` header:

```json
{"error":{"code":"insufficient_scope","message":"this action needs the admin scope; your token has operator","required_scope":"admin","scope":"operator"}}
```

The route table below is the one declaration the middleware enforces
(`internal/server/route_scopes.go`); a route missing from it is refused
(`403 route_scope_undeclared`), and `TestEveryRouteDeclaresAScope` walks the
router so a new route without a declaration fails CI. Every token create,
revoke and rotate is logged (`apitokens: token created|revoked|rotated`,
with id, name, scope and actor — never the secret).

#### `GET /api/v1/whoami` (viewer)

The caller's principal: `{"scope":"operator","source":"token","tokenId":"tok_…","name":"ci"}`.
`source` is `bootstrap`, `token` or `unauthenticated`. `GET /state` and every
`/events` frame also carry it as `callerScope`.

#### Token management (admin)

| Route | Body / answer |
|---|---|
| `GET /api/v1/tokens` | `{"tokens":[{"id","name","scope","created","lastUsed?","expires?","expired?"}]}` — never a hash or secret |
| `POST /api/v1/tokens` | body `{"name":"ci","scope":"viewer","expires":"90d"}` (`expires`: empty for never, `90d`, `12h`, `2027-01-31` or RFC 3339) → `201 {"token":{…},"secret":"itx_…"}`, `Cache-Control: no-store`. The secret is shown once. |
| `DELETE /api/v1/tokens/{id}` | `{"revoked":{…}}`; `404` for an unknown id |
| `POST /api/v1/tokens/{id}/rotate` | `{"token":{…},"secret":"itx_…"}` — same id, name, scope and expiry; the old secret stops working at once |

#### Route scopes

| Method | Route | Scope |
|---|---|---|
| `GET` | `/api/v1/deps/analyze/{jobId}` | viewer |
| `GET` | `/api/v1/events` | viewer |
| `GET` | `/api/v1/feed` | viewer |
| `GET` | `/api/v1/history` | viewer |
| `GET` | `/api/v1/history/prompt-versions` | viewer |
| `GET` | `/api/v1/issues` | viewer |
| `GET` | `/api/v1/issues/{identifier}` | viewer |
| `GET` | `/api/v1/issues/{identifier}/context` | viewer |
| `GET` | `/api/v1/issues/{identifier}/decisions` | viewer (redacted, bounded decision logs, #218) |
| `GET` | `/api/v1/issues/{identifier}/diff` | viewer (`?file=` hunks: operator) |
| `GET` | `/api/v1/issues/{identifier}/log-stream` | viewer |
| `GET` | `/api/v1/issues/{identifier}/logs` | viewer |
| `GET` | `/api/v1/issues/{identifier}/sublog-stream` | viewer |
| `GET` | `/api/v1/issues/{identifier}/sublogs` | viewer |
| `GET` | `/api/v1/logs` | viewer |
| `GET` | `/api/v1/logs/identifiers` | viewer |
| `GET` | `/api/v1/memory` | viewer |
| `GET` | `/api/v1/memory/recall` | viewer |
| `GET` | `/api/v1/playbooks` | viewer (#218) |
| `GET` | `/api/v1/projects` | viewer |
| `GET` | `/api/v1/projects/filter` | viewer |
| `GET` | `/api/v1/repo/evals` | viewer |
| `GET` | `/api/v1/repo/hotspots` | viewer |
| `GET` | `/api/v1/repo/readiness` | viewer |
| `GET` | `/api/v1/rules` | viewer |
| `GET` | `/api/v1/rules/preview` | viewer |
| `GET` | `/api/v1/sessions` | viewer |
| `GET` | `/api/v1/sessions/{id}/transcript` | viewer |
| `GET` | `/api/v1/settings/layers` | viewer (raw files and secrets redacted below admin) |
| `GET` | `/api/v1/settings/models` | viewer |
| `GET` | `/api/v1/settings/profiles` | viewer (commands redacted below admin) |
| `GET` | `/api/v1/settings/reviewer` | viewer |
| `GET` | `/api/v1/settings/schema` | viewer |
| `GET` | `/api/v1/skills/analytics` | viewer |
| `GET` | `/api/v1/skills/analytics/recommendations` | viewer |
| `GET` | `/api/v1/skills/inventory` | viewer |
| `GET` | `/api/v1/skills/issues` | viewer |
| `GET` | `/api/v1/skills/profile-backends` | viewer (Operator: run the fleet.) |
| `GET` | `/api/v1/state` | viewer |
| `GET` | `/api/v1/steering/phrases` | viewer (#218) |
| `GET` | `/api/v1/summary` | viewer |
| `GET` | `/api/v1/tracker/labels` | viewer |
| `GET` | `/api/v1/tracker/scope` | viewer |
| `GET` | `/api/v1/tracker/teams` | viewer |
| `GET` | `/api/v1/tracker/users` | viewer |
| `GET` | `/api/v1/whoami` | viewer |
| `GET` | `/metrics` | viewer |
| `POST` | `/api/v1/backend-health/clear` | operator |
| `POST` | `/api/v1/client-errors` | operator |
| `POST` | `/api/v1/deps/analyze` | operator |
| `DELETE` | `/api/v1/deps/analyze/{jobId}` | operator |
| `POST` | `/api/v1/dispatch/pause` | operator |
| `POST` | `/api/v1/dispatch/resume` | operator |
| `DELETE` | `/api/v1/issues/{identifier}` | operator |
| `POST` | `/api/v1/issues/{identifier}/ai-review` | operator |
| `POST` | `/api/v1/issues/{identifier}/backend` | operator |
| `POST` | `/api/v1/issues/{identifier}/cancel` | operator |
| `POST` | `/api/v1/issues/{identifier}/comment` | operator |
| `DELETE` | `/api/v1/issues/{identifier}/deps-override` | operator |
| `POST` | `/api/v1/issues/{identifier}/deps-override` | operator |
| `POST` | `/api/v1/issues/{identifier}/dismiss-input` | operator |
| `POST` | `/api/v1/issues/{identifier}/failures/ack` | operator |
| `DELETE` | `/api/v1/issues/{identifier}/logs` | operator |
| `POST` | `/api/v1/issues/{identifier}/pr-review` | operator (one GitHub review, never merges, #238) |
| `POST` | `/api/v1/issues/{identifier}/playbook` | operator (per-issue playbook override, #218) |
| `POST` | `/api/v1/issues/{identifier}/review-comments` | operator (to the running agent or the PR, #238) |
| `POST` | `/api/v1/issues/{identifier}/profile` | operator |
| `POST` | `/api/v1/issues/{identifier}/provide-input` | operator |
| `POST` | `/api/v1/issues/{identifier}/reanalyze` | operator |
| `POST` | `/api/v1/issues/{identifier}/resume` | operator |
| `PATCH` | `/api/v1/issues/{identifier}/state` | operator |
| `DELETE` | `/api/v1/issues/{identifier}/sublogs` | operator |
| `DELETE` | `/api/v1/issues/{identifier}/sublogs/{sessionId}` | operator |
| `POST` | `/api/v1/issues/{identifier}/terminate` | operator |
| `DELETE` | `/api/v1/logs` | operator |
| `POST` | `/api/v1/memory` | operator |
| `DELETE` | `/api/v1/memory/{id}` | operator |
| `PUT` | `/api/v1/memory/{id}` | operator |
| `POST` | `/api/v1/memory/{id}/approve` | operator |
| `POST` | `/api/v1/memory/{id}/pin` | operator |
| `POST` | `/api/v1/memory/{id}/promote` | operator |
| `POST` | `/api/v1/memory/{id}/reject` | operator |
| `POST` | `/api/v1/memory/{id}/enforcement/accept` | operator (creates an Enforce: issue, #217) |
| `POST` | `/api/v1/memory/{id}/enforcement/dismiss` | operator |
| `DELETE` | `/api/v1/outbox/{id}` | operator |
| `POST` | `/api/v1/outbox/{id}/retry` | operator |
| `POST` | `/api/v1/refresh` | operator |
| `POST` | `/api/v1/repo/readiness/verify-skill-issue` | operator (creates a tracker issue, #218) |
| `POST` | `/api/v1/sessions/{id}/message` | operator (steering) |
| `GET` | `/api/v1/sessions/{id}/screen` | operator (a live terminal pane) |
| `POST` | `/api/v1/skills/check-refs` | operator (read-only, but a POST) |
| `POST` | `/api/v1/skills/scan` | operator |
| `POST` | `/api/v1/tracker/filter/preview` | operator (read-only, but a POST) |
| `POST` | `/api/v1/automations/{id}/test` | admin |
| `PUT` | `/api/v1/projects/filter` | admin |
| `POST` | `/api/v1/memory/{id}/promote-to-rule` | admin (writes a rules layer, #217) |
| `PUT` | `/api/v1/rules/{layer}` | admin |
| `POST` | `/api/v1/sessions` | admin (starts a tmux session) |
| `DELETE` | `/api/v1/sessions/{id}` | admin |
| `POST` | `/api/v1/sessions/{id}/keys` | admin |
| `PUT` | `/api/v1/settings/agent/max-retries` | admin |
| `PUT` | `/api/v1/settings/agent/max-switches-per-issue-per-window` | admin |
| `PUT` | `/api/v1/settings/agent/switch-window-hours` | admin |
| `PUT` | `/api/v1/settings/automations` | admin |
| `POST` | `/api/v1/settings/deps-analysis-mode` | admin |
| `PUT` | `/api/v1/settings/dispatch-strategy` | admin |
| `POST` | `/api/v1/settings/inline-input` | admin |
| `PUT` | `/api/v1/settings/layers/{layer}` | admin |
| `POST` | `/api/v1/settings/models/refresh` | admin |
| `DELETE` | `/api/v1/settings/profiles/{name}` | admin |
| `PUT` | `/api/v1/settings/profiles/{name}` | admin |
| `PUT` | `/api/v1/settings/reviewer` | admin |
| `POST` | `/api/v1/settings/ssh-hosts` | admin |
| `DELETE` | `/api/v1/settings/ssh-hosts/{host}` | admin |
| `PUT` | `/api/v1/settings/tracker/failed-state` | admin |
| `PUT` | `/api/v1/settings/tracker/states` | admin |
| `POST` | `/api/v1/settings/workers` | admin |
| `POST` | `/api/v1/settings/workspace/auto-clear` | admin |
| `POST` | `/api/v1/skills/fix` | admin (writes skill files) |
| `GET` | `/api/v1/tokens` | admin |
| `POST` | `/api/v1/tokens` | admin |
| `DELETE` | `/api/v1/tokens/{id}` | admin |
| `POST` | `/api/v1/tokens/{id}/rotate` | admin |
| `PUT` | `/api/v1/tracker/scope` | admin |
| `DELETE` | `/api/v1/workspaces` | admin |
| `*` | `/*` | none (public) (the embedded SPA shell and its assets) |
| `GET` | `/api/v1/health` | none (public) |
| `GET` | `/api/v1/ready` | none (public) |
| `POST` | `/api/v1/webhooks/{tracker}` | none (public) |
| `POST` | `/api/v1/agent-actions/{identifier}/comment` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/comment_pr` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/create-issue` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/guardrail/approvals` | per-run agent grant |
| `GET` | `/api/v1/agent-actions/{identifier}/guardrail/approvals/{id}` | per-run agent grant (Viewer: reads and streams.) |
| `POST` | `/api/v1/agent-actions/{identifier}/guardrail/events` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/merge_pr` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/move-state` | per-run agent grant |
| `POST` | `/api/v1/agent-actions/{identifier}/provide-input` | per-run agent grant |

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
- `POST /agent-actions/{identifier}/guardrail/events` (#204)
- `POST /agent-actions/{identifier}/guardrail/approvals` (#204)
- `GET /agent-actions/{identifier}/guardrail/approvals/{id}` (#204)

Profiles opt into these capabilities with `allowed_actions` and
`create_issue_state` in `WORKFLOW.md`. The guardrail routes take a separate
per-run guardrail grant that only the guardrail shims and hooks hold (see
[Guardrail endpoints](#guardrail-endpoints-204)).

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
| `hub_member` | bool | `true` when the daemon takes part in the machine's hub (#228); `false` for `--no-hub`, `hub.enabled: false`, and daemons older than v0.2.9 |
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
`{url}api/v1/` takes `Authorization: Bearer <token>`. A supervisor that
polls should send `GET {url}api/v1/summary` with `If-None-Match` and read
`/api/v1/state` only when it answers `200` (see [`GET /summary`](#get-summary)).

**Reading the registry directly.** A supervisor may read
`~/.itervox/daemons/*.json` itself instead of running `status --json`, as
the Mac app does since #212, by applying the same liveness rule (and may
delete the records that fail it). It must fall back to `status --json` for a
record whose `schema_version` it does not know, and it still needs the
command now and then for daemons found by process scan. The shared fixture
`cmd/itervox/testdata/daemon_registry_parity.json` pins the rows both
produce.

**The hub.** The registry directory also holds `hub.lock` (see
[Hub](#hub)): the address of the machine's hub, which serves every
`hub_member` daemon at one URL. It is not a `*.json` record; a registry
reader skips it.

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
- State changes are coalesced to at most four snapshots per second (#210):
  the first change after a quiet period is sent at once, and changes inside
  the 250 ms window are folded into one snapshot sent when it ends, so the
  latest state always arrives and no change waits longer than 250 ms. Every
  client of one change receives the same encoded snapshot.
- Named keepalive event `event: keepalive` with `data: {}` after 25 seconds
  of stream inactivity.
- Named event `event: feed` with `data: {"seq":N}` (#213) on connect and
  after every snapshot whose `feedSeq` is new: the newest
  [event feed](#event-feed) seq. A client that keeps a cursor reads
  `GET /api/v1/feed?since=<cursor>` when it moves. It rides the same
  coalesced delivery as the snapshot.

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
- `dependencyAudit`, `dependencyGraphNodes`, `dependencyGraphEdges` —
  `dependencyGraphNodes` lists only issues that are the source or target of
  at least one edge (#210); an audited issue with no blocker and no
  dependent is in `dependencyAudit` only
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
- `running[]` rows carry `title` (the issue's title), `profile` (the run's
  resolved profile, omitted for the default) and `costUsdEstimated` (the run's
  estimated cost as its backend reports it; omitted until reported, always for
  Codex) (#240). `lastEvent` is redacted like every log surface.
- `inputRequired[]` rows carry `guardrailApproval: true` for a
  [guardrail approval](/guides/guardrails/) request (#240): `approve` (or
  `itervox approve <issue>`) runs the held command once, any other reply
  rejects it.
- `proposedLessons` (#203) — how many proposed lessons wait in the Memory
  page's review queue (the Memory nav item's count); omitted when none.
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
  `["failure_ack", "dispatch_pause"]`. Treat unknown entries as opaque.
- `dispatchPaused` (#213) — `null` while dispatch runs; while all dispatch is
  paused, `{ "reason"?, "until"?, "pausedAt" }` (see
  [Dispatch pause](#dispatch-pause)).
- `feedSeq` (#213) — the newest [event feed](#event-feed) seq.
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

### `GET /summary`

A few hundred bytes for a supervisor that polls (the Mac app, #212): the
counts the menu bar shows, with an `ETag`, so an unchanged daemon answers a
conditional request with `304 Not Modified` and no body. Behind the bearer
token like `/state`, and built from the same snapshot.

**Response** `200`:

```json
{
  "etag": "5d41402abc4b2a76b9719d911017c592",
  "version": "0.2.8",
  "projectName": "web-app",
  "capabilities": ["dispatch_pause", "failure_ack"],
  "counts": {
    "running": 2,
    "retrying": 0,
    "paused": 1,
    "inputRequired": 1,
    "pendingInputResume": 0,
    "recentFailures": 3
  },
  "paused": true,
  "pauseReason": "deploy freeze",
  "pausedUntil": "2026-10-22T12:00:00Z",
  "generatedAt": "2026-10-22T10:00:00Z"
}
```

- `counts.running`, `counts.retrying` and `counts.paused` are `counts` of
  `GET /state` (`paused` counts paused issues); `counts.inputRequired` and
  `counts.pendingInputResume` split `inputRequired[]` by `state`;
  `counts.recentFailures` is the length of `recentFailures[]`.
- `paused` is the daemon-wide [dispatch pause](#dispatch-pause) (#213):
  `true` while nothing new starts. `pauseReason` and `pausedUntil` describe
  it and are omitted when unset; the pause, its reason and its until are
  part of the ETag digest.
- `version` is the daemon's build version (`dev` for a local build).
- `capabilities` (#228) are the optional features the daemon serves, as in
  `GET /state`; omitted when there are none. The hub lists them per project.
- The `ETag` header is `"<etag>"`. The tag is a digest of every field except
  `generatedAt`, plus each input-required entry (identifier, session, state,
  question, queued time) and each recent failure (kind, identifier, source,
  message, time, count), so a new question or failure changes it even when
  the counts do not. Responses carry `Cache-Control: no-cache`.

Send the tag back as `If-None-Match: "<etag>"` (a list, `W/` tags and `*`
are accepted): while it is current the answer is `304` with the `ETag`
header and no body. The Mac app reads `GET /state` only after a `200` from
this endpoint, or every time from a daemon older than it (`404`).

---

## Event feed

### `GET /api/v1/feed`

A bounded, append-only feed of notable events with a cursor (#213), for a
client that was away — a closed Mac app window, a phone, a relay — to find
out what happened. The path is `/feed` so it does not clash with the
`/events` SSE stream; a document that says `/api/v1/events?since=` means this
endpoint.

**Query:** `since` (default `0`): return the events after this seq.
`limit` (1–1000, default 200). Anything else is `400`.

**Response** `200` (`Cache-Control: no-store`):

```json
{
  "events": [
    {"seq": 41, "ts": "2026-10-22T10:00:00Z", "kind": "run_started", "identifier": "ENG-12",
     "payload": {"runId": "r-8f2c", "runKind": "worker", "profile": "coder", "backend": "claude"}},
    {"seq": 42, "ts": "2026-10-22T10:07:13Z", "kind": "run_finished", "identifier": "ENG-12",
     "payload": {"runId": "r-8f2c", "runKind": "worker", "profile": "coder", "backend": "claude", "outcome": "succeeded"}},
    {"seq": 43, "ts": "2026-10-22T10:07:14Z", "kind": "pr_opened", "identifier": "ENG-12",
     "payload": {"prUrl": "https://github.com/acme/web/pull/88", "runId": "r-8f2c"}}
  ],
  "nextSeq": 43,
  "headSeq": 43,
  "oldestSeq": 1,
  "truncated": false,
  "hasMore": false
}
```

- `seq` increases by one per event and never repeats, also across restarts.
  Only the daemon's event loop assigns it.
- `nextSeq` is the cursor for the next read: the last event returned, or
  your `since` when nothing was newer. `hasMore` means `limit` cut the page.
- `truncated: true` means events after your cursor have left the window
  (or your cursor is ahead of `headSeq`, e.g. the feed file was removed):
  resynchronize from `GET /state`, then continue from `headSeq`.
- The window is the newest 5000 events, none older than 7 days. It persists
  to `feed.jsonl` in the daemon log directory, so a restart keeps it.

| `kind` | When | Payload |
|---|---|---|
| `run_started` | A worker, reviewer or automation run starts | `runId`, `runKind`, `profile`, `backend` |
| `run_finished` | A run succeeded, was cancelled (`outcome: "cancelled"`) or stopped by reconciliation | run fields, `outcome` |
| `run_failed` | A run failed, stalled, was rate limited, … | run fields, `outcome` (the terminal reason) |
| `input_required` | An agent asked a question, or a guardrail approval waits (`kind: "guardrail_approval"`) | run fields |
| `input_answered` | A question was answered (`via`: `dashboard`, `slack`, `tracker`, `agent`) or dismissed (`via: "dismiss"`) | `via`, `outcome` |
| `review_verdict` | A reviewer finished | `profile`, `verdict`, `outcome` (`passed` / `blocked`, once the review is complete) |
| `pr_opened` | A run recorded a PR itervox did not manage yet | `prUrl`, `runId` |
| `pr_merged` | The `merge_pr` action merged a PR | `prUrl` |
| `guardrail_blocked` | A shim or hook refused an operation, or an approval was rejected | `kind`, `via` |
| `guardrail_approved` | A guardrail approval was granted | `via` |
| `lesson_proposed` | A lesson was proposed (or auto-approved) | `outcome`, `memoryId` |
| `dispatch_paused` | All dispatch was paused | `reason`, `until` |
| `dispatch_resumed` | Dispatch resumed (`via`: `operator` or `expired`) | `via` |
| `review_comment_posted` | An operator's line comment went to the running agent (`via: "agent"`) or the PR (`via: "github"`) (#238) | `via`, `runId` or `prUrl` |
| `pr_review_submitted_by_operator` | An operator submitted a PR review from the dashboard (#238) | `prUrl`, `verdict` (`approve`, `request_changes`, `comment`), `count` |

Payloads are small and structured. They never contain agent output, prompt
text, a question or reply, failure text or a guardrail command line; the
pause reason is redacted before it is stored.

---

## Hub

The machine's hub (#228; guide: [The itervox hub](../site/src/content/docs/guides/hub.mdx)) serves these routes on its
own listener, `hub.bind:hub.port` (`http://127.0.0.1:8089` by default; the
bound address is in `~/.itervox/daemons/hub.lock`). Every route except
`GET /hub/api/v1/health` and the dashboard's static files needs a **hub
token** (`Authorization: Bearer <token>`) from the hub's own store,
`~/.itervox/hub/tokens.json` (`itervox token create --hub`). Project tokens
are not hub tokens. There is no unauthenticated mode. Errors use the
daemons' envelope, `{"error":{"code","message"}}`.

| Route | Scope | |
|---|---|---|
| `GET /hub/api/v1/health` | none | `{"status":"ok","hub":true}` |
| `GET /hub/api/v1/whoami` | viewer | `{"principal":{"scope","tokenId","name"},"hub":true}` |
| `GET /hub/api/v1/projects` | viewer | the members |
| `GET /hub/api/v1/feed?since=&limit=` | viewer | the merged event feed |
| `GET /hub/api/v1/events` | viewer | SSE: `feed` and `projects` announcements, desktop `notification` frames |
| `GET /hub/api/v1/notifications` | admin | the notification settings, kinds, channels and projects |
| `PUT /hub/api/v1/notifications` | admin | replace the notification settings |
| `POST /hub/api/v1/notifications/test` | operator | send a test notification through one channel |
| `GET /hub/api/v1/push/vapid-public-key` | viewer | `{"publicKey"}`: the hub's VAPID application server key (#231) |
| `POST /hub/api/v1/push/subscribe` | operator | subscribe this browser to Web Push under a device name |
| `GET /hub/api/v1/push/devices` | operator | the push devices (name and push service only) |
| `DELETE /hub/api/v1/push/devices/{id}` | operator | stop pushing to a device |
| `POST /hub/api/v1/pairing` | admin | a one-time phone pairing code (10 minutes) |
| `POST /hub/api/v1/pairing/exchange` | none (the code) | trade a pairing code for the device's operator token |
| `GET /hub/api/v1/devices` | admin | the paired devices |
| `DELETE /hub/api/v1/devices/{id}` | admin | revoke a paired device's token and its pushes |
| `* /hub/p/{project}/api/v1/...` | the member route's | the member's API through the proxy |
| `GET /hub/`, `/hub/m`, `/hub/pair`, `/hub/p/{project}/...`, `/assets/...`, `/manifest.webmanifest`, `/sw.js`, `/icons/...` | none | the dashboard, its phone layout and the installable web app |

### `GET /hub/api/v1/projects`

```json
{
  "hub": { "version": "0.2.9", "pid": 4182, "url": "http://127.0.0.1:8089/hub/", "startedAt": "2026-10-22T09:00:00Z", "headSeq": 1204 },
  "projects": [
    {
      "name": "web",
      "workflow": "/Users/you/code/web/WORKFLOW.md",
      "projectDir": "/Users/you/code/web",
      "pid": 4190,
      "url": "http://127.0.0.1:8091/",
      "version": "0.2.9",
      "startedAt": "2026-10-22T09:01:00Z",
      "health": "ok",
      "proxied": true,
      "capabilities": ["dispatch_pause", "failure_ack", "run_context"],
      "summary": { "etag": "…", "version": "0.2.9", "counts": { "running": 2, "inputRequired": 1 }, "paused": false },
      "checkedAt": "2026-10-22T10:00:00Z"
    }
  ]
}
```

- `name` is unique: the project directory's name, lowercased, with a short
  hash of the `WORKFLOW.md` path appended when two members share it. It is
  the `{project}` of the proxy and the dashboard path.
- `health` is `ok`, `unreachable`, `unauthorized` (the member refused the
  hub's token) or `error`, with `error` saying why.
- `proxied` is false (with `proxyError`) for a member running with
  `server.allow_unauthenticated` (never proxied: the hub could not cap a
  caller's scope on it) or one whose route scope table the hub could not
  read (an itervox older than v0.2.9).
- `summary` is the member's own `GET /api/v1/summary`, passed through;
  `capabilities` comes from it.

Members are the live daemon registry records with `hub_member: true` (see
the Supervisor contract), refreshed on every registry change and every 10 s.

### `GET /hub/api/v1/feed`

The same shape and cursor rules as a member's [`GET /api/v1/feed`](#get-apiv1feed),
over every member's feed merged into one sequence:

```json
{
  "events": [
    { "seq": 1203, "project": "web", "workflow": "/Users/you/code/web/WORKFLOW.md", "memberSeq": 88,
      "ts": "2026-10-22T10:00:00Z", "kind": "input_required", "identifier": "ENG-12", "payload": { "runId": "…" } }
  ],
  "nextSeq": 1203, "headSeq": 1204, "oldestSeq": 205, "truncated": false, "hasMore": true
}
```

`seq` is the hub's sequence number: it orders the merged feed and keeps
growing across hub restarts and hand-overs. `memberSeq` is the event's
`seq` in its project's own feed, and `payload` is the member's redacted
payload as is. The hub keeps the newest 5000 events in memory and a cursor
per member in `~/.itervox/hub/hub_feed_cursors.json` (`0600`), from which
the next hub catches up: nothing is missed or repeated. A project the hub
has never seen starts with the events of its current daemon process.
`limit` is 1–1000 (default 100); `since` must be a non-negative integer.

### `GET /hub/api/v1/events`

Server-sent events. On connect and on every change: `event: projects`
(`{"count":N}`, the member count) and `event: feed` (`{"headSeq":N}`, the
merged feed's newest `seq`); `: ping` comments every 25 s. Read the events
themselves with `GET /hub/api/v1/feed?since=`.

#229: every notification the settings route to the desktop channel is sent
as `event: notification` with `id: <n>` and the notification as data:

```
event: notification
id: 12
data: {"id":12,"channel":"desktop","kind":"guardrail_approval","urgent":true,"project":"web","identifier":"ENG-12","title":"ENG-12 needs approval","body":"web","url":"/hub/p/web/?issue=ENG-12","seq":1203,"ts":"2026-10-22T10:00:00Z"}
```

`kind` is a notification kind, `digest` (with `items`, the batched
notifications) or `test`. Titles are built from the kind, identifier and
enum payload fields only, never agent output. The stream starts after
`?notify_since=<id>` or the `Last-Event-ID` header, else at the newest
notification (nothing old is replayed); an id above the hub's newest (the
hub restarted) replays the up to 100 it kept. In `GET /hub/api/v1/feed`,
each event that notifies carries `notifyKind` and `channels`, the channels
the settings route it to at read time.

### `GET` / `PUT /hub/api/v1/notifications`

Admin. The hub's notification settings
(`~/.itervox/hub/notifications.json`, `0600`, schema
[`notifications.schema.json`](https://itervox.dev/schema/notifications.schema.json);
guide: [Notifications](../site/src/content/docs/guides/hub.mdx#notifications)):

```json
{
  "settings": {
    "$schema": "https://itervox.dev/schema/notifications.schema.json",
    "version": 1,
    "digest_interval_minutes": 15,
    "channels": { "push": { "enabled": true }, "slack": { "enabled": true }, "desktop": { "enabled": true } },
    "defaults": { "input_required": ["push", "slack", "desktop"], "guardrail_approval": ["push", "slack", "desktop"],
                  "run_failed": ["push", "slack", "desktop"], "review_verdict": [], "pr_merged": [],
                  "lesson_proposed": [], "dispatch_paused": [] },
    "projects": { "web": { "routes": { "pr_merged": ["desktop"] }, "snooze_until": "2026-10-23T08:00:00Z" } }
  },
  "kinds": [ { "name": "input_required", "urgent": true }, { "name": "run_failed", "urgent": false } ],
  "channels": [ { "name": "push", "available": false }, { "name": "slack", "available": false }, { "name": "desktop", "available": true } ],
  "projects": ["api", "web"]
}
```

`PUT` takes the `settings` document (unknown fields, kinds and channels are
400 `invalid_settings`; `digest_interval_minutes` is 1–1440), saves it and
applies it at once; it answers like `GET`. A project's `routes` override
`defaults` per kind (an empty list turns a kind off); `snooze_until`
suppresses the project's notifications until then. `available` is false for
a channel not set up on this hub.

### `POST /hub/api/v1/notifications/test`

Operator. Body `{"channel": "push" | "slack" | "desktop"}`: sends one test
notification through that channel, whatever the routes and snoozes say.
`200 {"sent":true,"channel":"desktop"}`; 400 `unknown_channel`; 409
`channel_unavailable` when the channel is not set up; 502 `send_failed`.

### Phones: Web Push and pairing (#231)

Guide: Your phone (site guides).

**`POST /hub/api/v1/push/subscribe`** (operator) takes a device name and the
browser's `PushSubscription.toJSON()`:

```json
{ "name": "Pixel", "subscription": { "endpoint": "https://fcm.googleapis.com/fcm/send/…",
  "keys": { "p256dh": "<base64url P-256 key>", "auth": "<base64url 16 bytes>" } } }
```

201 `{"device":{"id","name","service","tokenId","created"}}`. The endpoint
must be https on a known push service (Google, Mozilla, Apple, Microsoft;
400 `invalid_subscription` otherwise); re-subscribing the same endpoint
updates that device. The device is bound to the caller's hub token: revoking
the token removes it. At most 50 devices (409 `too_many_devices`). The list
(`GET /hub/api/v1/push/devices`) never returns an endpoint or key.

Every notification the notification settings
route to the `push` channel is sent to every device, encrypted per RFC 8291
(`Content-Encoding: aes128gcm`) and signed with the hub's VAPID key
(`~/.itervox/hub/push_vapid.pem`, `0600`, created on first use). The
decrypted payload is exactly
`{"kind","project","issue","title","url"}`: a notification kind (or
`digest`, `test`), a redacted title of at most 80 characters and a
hub-relative link (`/hub/p/{project}/m/{issue}`, else `/hub/m`). A device the
push service answers 404 or 410 for is removed.

**`POST /hub/api/v1/pairing`** (admin) answers 201
`{"code","expiresAt","path"}`: a 160-bit one-time code, valid for 10
minutes, and the link path `/hub/pair#code=<code>`. At most 20 codes are
pending (429 `too_many_pairings`). **`POST /hub/api/v1/pairing/exchange`**
takes `{"code","name"}` without a token and answers 201
`{"token","device":{"id","name","created"}}`: a new operator hub token named
`device: <name>`, shown once. A wrong, used or expired code is 400
`invalid_code`; after 10 wrong codes in a minute the exchange answers 429
`too_many_attempts` until the minute is over. **`GET /hub/api/v1/devices`**
(admin) lists the paired devices (`id`, `name`, `created`, `lastUsed`,
`pushDevices`); **`DELETE /hub/api/v1/devices/{id}`** revokes one (204; 404
for a token that is not a paired device).

**CORS.** `server.allowed_origins` of the hosting daemon applies to every
hub route (and to that daemon's own API): a listed origin gets
`Access-Control-Allow-Origin` for itself and its preflights answered; any
other cross-origin preflight is refused with 403 `origin_not_allowed`.

### The proxy: `/hub/p/{project}/api/v1/...`

Every method. The hub, in order:

1. answers 404 `project_not_found` for an unknown project, and 400
   `bad_path` for a path that is not a plain `/api/v1/...` path (encoded
   slashes, empty, `.` or `..` segments);
2. answers 503 `member_unavailable` while it has no route scope table for
   the member, and 403 `member_unauthenticated` for a member running with
   `server.allow_unauthenticated`;
3. looks the method and path up in the member's
   [`GET /api/v1/route-scopes`](#get-apiv1route-scopes) table (a path
   several patterns match takes the most restrictive scope): 404
   `route_not_found` when the member declares none; 403 `not_proxied` for an
   agent-action or guardrail grant route, a tracker webhook, or an unknown
   access class; public `GET` probes need viewer;
4. answers 403 `insufficient_scope` (with `X-Itervox-Required-Scope`) when
   the hub token's scope is below the route's, and 403
   `admin_route_not_proxied` for a member admin route (the hub never holds
   an admin token on a member);
5. drops the caller's `Authorization`, `Cookie`, `Origin` and `Referer`,
   sets `Authorization: Bearer <the hub's member token>` (the `hub-viewer`
   token for a viewer hub token, `hub-operator` otherwise, in the member's
   own store), and forwards the request with its query.

Responses stream through unbuffered, so SSE routes (`/events`,
`/issues/{identifier}/log-stream`, …) work. A member `401` (it refused the
hub's token) becomes `502` with `X-Itervox-Hub-Error: member_auth_failed`,
never a `401` that would sign the dashboard out; the hub then obtains a new
member token. An unreachable member is `502 member_unreachable`.

### `GET /api/v1/route-scopes`

On every daemon (viewer, #228): the daemon's whole route scope table, which
the hub's proxy checks a hub token against.

```json
{ "routes": { "GET /api/v1/state": "viewer", "POST /api/v1/dispatch/pause": "operator",
              "GET /api/v1/tokens": "admin", "GET /api/v1/health": "public",
              "POST /api/v1/agent-actions/{identifier}/comment": "agent_grant" } }
```

Keys are `METHOD pattern` with the full route pattern (`{name}` matches one
path segment; `*` is a route registered for every method). Values are
`viewer`, `operator`, `admin`, `public` or `agent_grant`.

### `hub.lock`

`~/.itervox/daemons/hub.lock` is the hub election's lock (a kernel `flock`,
released when the hub's process ends) and, while a hub holds it, its
address as JSON (never a token):

```json
{ "schema_version": 1, "pid": 4182, "url": "http://127.0.0.1:8089/hub/", "host": "127.0.0.1", "port": 8089,
  "version": "0.2.9", "workflow": "/Users/you/code/api/WORKFLOW.md", "started_at": "2026-10-22T09:00:00Z" }
```

The file is never deleted; a hub that stops empties it. A reader must check
that the lock is held (a shared non-blocking `flock` that fails), or ask the
`url`'s `/hub/api/v1/health`, before trusting the content. `itervox hub
--json` prints `{"hub": <this object or null>, "members": [...]}`.

---

## Dispatch pause

### `POST /api/v1/dispatch/pause`

Pauses ALL dispatch (#213). Running agents continue; nothing new starts:
tick dispatch, retries, reviewers, automations and pending-input resumes
are held — never dropped — until the pause ends. Retries stay due without
spending an attempt, reviews wait as pending reviews, automations queue
with reason `dispatch_paused`, answered questions wait to resume. Idle
issues show the ineligible reason `dispatch_paused`.

**Body** (optional): `{"reason": "deploy freeze", "until": "2026-10-22T12:00:00Z"}`.
`reason` is at most 500 bytes; `until` is RFC 3339 and must be in the
future (`400` otherwise). Without `until` the pause lasts until resumed. A
second pause while paused updates the reason and until.

**Response** `200`: the pause the event loop applied —
`{"dispatchPaused": {"reason": "deploy freeze", "until": "2026-10-22T12:00:00Z", "pausedAt": "2026-10-22T10:00:00Z"}}`.
`503` when the event loop did not answer.

The pause persists to `dispatch_paused.json` in the daemon log directory,
so a restart or a `WORKFLOW.md` reload stays paused; a pause whose `until`
passed while the daemon was down ends on its first tick. `GET /state`
shows it as `dispatchPaused` (`null` while dispatch runs) and advertises
the `dispatch_pause` capability.

### `POST /api/v1/dispatch/resume`

Ends the pause (a no-op when none is active). **Response** `200`:
`{"dispatchPaused": null}`. Held automations start at once, and the next
tick dispatches the rest as slots allow.

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
  each rule's id, severity and text), the run's memory selection (#202;
  sorted by id, each entry's id, type and text; left out entirely when the
  run had no memory, so such runs keep their earlier digest) and the
  prompt template (the
  `WORKFLOW.md` body, or `agent.reviewer_prompt` for a reviewer run that has
  one), computed when the run is dispatched. Two runs with the same digest
  were given the same prompt inputs. Treat it as an opaque identifier; the
  dashboard shows its first 12 characters.
- `memoryIds` — the ids of the memory entries the run's prompt carried
  (#202), in block order; omitted when it had none.
- `promptParts` — `{ "soul"?, "instructions"?, "rules"?, "memory"?, "template"? }`:
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
    { "id": "gitignore_carveouts", "title": ".gitignore keeps .itervox/agents, .itervox/handoff, .itervox/playbooks and .itervox/memory.json committable", "status": "warn",
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
| `gitignore_carveouts` | the root `.gitignore` keeps `.itervox/agents/**`, `.itervox/handoff/**`, `.itervox/playbooks/**` and `.itervox/memory.json` committable | `warn`: it ignores `.itervox/` without `!.itervox/`, `!.itervox/agents/`, `!.itervox/agents/**`, `!.itervox/handoff/`, `!.itervox/handoff/**`, `!.itervox/playbooks/`, `!.itervox/playbooks/**`, `!.itervox/memory.json` |
| `instruction_docs` | `CLAUDE.md` (Claude profiles) and `AGENTS.md` (Codex profiles) at the repository root | `warn`: a backend in use has no instruction file |
| `verify_skill` | a project verification skill or script (#218): `.claude/skills/<…verif…>/SKILL.md`, `scripts/verify*`, a `verify` Makefile target or a `verify` script in `package.json` | `warn`: none of them; `POST /api/v1/repo/readiness/verify-skill-issue` creates an issue to write one |
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

### Run context and changes (#215)

What an issue's agent is doing right now, and what it has changed. Both
routes are `viewer` reads; a file's hunks need `operator` (see below).
The daemon advertises them with `"run_context"` in `capabilities` on the
snapshot.

#### `GET /issues/{identifier}/context`

The context of the issue's running run (`live: true`), or of its most
recent finished run while the daemon still keeps it (the 32 newest finished
runs, this session only), plus the worktree's changed files and handoffs.

```json
{
  "identifier": "ENG-12",
  "run": {
    "runId": "r-8f2c", "live": true, "kind": "worker", "profile": "coder", "backend": "claude",
    "startedAt": "2026-10-22T10:00:00Z", "updatedAt": "2026-10-22T10:04:31Z", "baseBranch": "main",
    "currentStep": "Wiring the diff endpoint into the router.",
    "lastToolCall": {"name": "Edit", "target": "internal/server/server.go", "at": "2026-10-22T10:04:31Z"},
    "toolCalls": 41, "turnCount": 2, "inputTokens": 120400, "outputTokens": 8200, "totalTokens": 128600,
    "pendingInput": "", "queuedSteers": 0,
    "steers": [{"text": "use gitexec", "at": "2026-10-22T10:02:00Z", "delivery": "live"}],
    "guardrailBlocks": 1, "lastGuardrailKind": "push_protected"
  },
  "port": 41000,
  "changedFiles": [{"path": "internal/server/changes.go", "status": "added", "additions": 120, "deletions": 0}],
  "handoffs": [{"title": "Plan the endpoints", "path": ".itervox/handoff/2026-10-22T09-58-00Z_planner.md"}]
}
```

- `port` is the issue workspace's `ITERVOX_PORT` (from
  `workspace.port_range`); omitted when the workspace has none.
- `currentStep` is the run's last assistant message, redacted (the log
  redaction: known key shapes, bearer tokens, `NAME_TOKEN=…` assignments,
  URL credentials), whitespace-collapsed and cut to 500 bytes.
- `lastToolCall` is the latest tool call — started or finished — by name
  and target (a file path, a command, a pattern, a URL), redacted and cut to
  300 bytes. **Tool output is never included**: the target is computed from
  the call's input with every output-bearing field (`output`, `stdout`,
  `stderr`, `result`, `content`, …) removed, and tool results are not read.
  `toolCalls` counts finished calls.
- `turnCount` and the token counts are the run's so far; `pendingInput` is
  the question the issue waits on (`pendingApproval: true` for a guardrail
  approval); `steers` are the newest delivered steering messages and
  `queuedSteers` those not yet delivered; `guardrailBlocks` counts the
  operations a guardrail refused in this run.
- `changedFiles` is the file list of the diff below (omitted with
  `changedFilesError` — `no_worktree`, `base_not_found`, `diff_timeout`,
  `diff_failed` — when it cannot be computed); `handoffs` lists the
  `.itervox/handoff/` files, oldest first, titled by their first heading.
- `run` is `null` when the issue has a worktree but no kept run. `404
  no_run_context` when it has neither.

The run context is updated by each worker at most once a second (plus once
at the end of every turn) through the event loop, and is not part of the
`/state` snapshot or the SSE frames: read it here.

#### `GET /issues/{identifier}/diff`

The worktree against the **merge base** of its `HEAD` with the base branch:
committed changes, uncommitted edits and untracked files. Works while the
issue runs and after it finished, as long as its worktree exists.

**Query:** `base` (optional): the branch to diff against (`main`,
`origin/develop`); a name only — no options, ranges or revision
expressions (`400 invalid_base`). Default: the run's pull-request base (the
blocker branch of a stacked worktree),
then `workspace.base_branch`, `agent.base_branch`, `origin/HEAD`,
`origin/main` and `main`, each tried as `origin/<name>` first. `file`
(optional): return that file's hunks instead of the list.

Without `file`:

```json
{
  "identifier": "ENG-12", "base": "origin/main", "mergeBase": "3f9c2e1d…",
  "files": [
    {"path": "internal/server/changes.go", "status": "added", "additions": 120, "deletions": 0},
    {"path": "web/public/logo.png", "status": "modified", "additions": 0, "deletions": 0, "binary": true}
  ],
  "truncated": false,
  "hunksAllowed": true
}
```

`status` is `added`, `modified`, `deleted`, `typechange`, `unmerged` or
`untracked`; renames show as a deletion and an addition. At most 2000 files
(`truncated: true` beyond). `hunksAllowed` tells the client whether its
token may ask for `?file=`.

With `file=<path>` (**operator**; a viewer gets `403 insufficient_scope`):

```json
{
  "identifier": "ENG-12", "base": "origin/main",
  "file": {"path": "internal/server/changes.go", "status": "added", "additions": 120, "deletions": 0},
  "patch": "diff --git a/internal/server/changes.go b/internal/server/changes.go\n…",
  "truncated": false, "bytes": 4810, "maxBytes": 262144
}
```

- `file` must be one of the listed paths (`404 file_not_changed`
  otherwise); no other path reaches git.
- The unified diff is cut at 256 KiB on a line boundary (`truncated: true`,
  `bytes` is the full size). A binary file has `binary: true` and an empty
  `patch`.
- Below `admin` the patch passes through the log redaction (whitespace and
  lines are kept); an admin sees it as written. An agent can commit a
  secret, which is why hunks are not a viewer read.

Errors: `404 no_worktree` (never dispatched, or the worktree was cleared),
`404 base_not_found`, `400 invalid_base`, `504 diff_timeout`, `503
not_supported` (a daemon built without the endpoint). Every git call runs
through `internal/gitexec` on the request's goroutine — never on the
orchestrator's event loop — under a 15-second timeout.

### Change review (#238)

Act on the diff above without leaving Itervox: comment on a line or range
for the running agent or for the pull request, and review the pull request.
Both routes are **operator**; reading the diff stays `viewer`. The daemon
advertises them with `"change_review"` in `capabilities` on the snapshot.

GitHub is reached with the credentials the PR-event watcher already uses
(`tracker.api_key` for a GitHub tracker on github.com, else `GH_TOKEN` /
`GITHUB_TOKEN` — `GH_ENTERPRISE_TOKEN` / `GITHUB_ENTERPRISE_TOKEN` for
another host — else `gh auth token`). An issue's pull request is the one its
runs recorded most recently. **Nothing here merges**: a review does not
record a reviewer verdict, change `agent.review_quorum`'s outcome or touch
the `merge_pr` gate (required checks, `agent.merge_block_labels`, the
strategy).

#### The PR source of `GET /issues/{identifier}/diff`

Once the issue has a GitHub pull request, the diff defaults to the PR's
files as GitHub compares them (`base...head`) — what a merge would apply.
`?source=worktree` keeps the live worktree diff (it can be ahead of the
pushed head while a run is live); `?source=pr` asks for the PR explicitly
(`404 no_pr` without one). Both lists and `?file=` hunks follow the same
scope and redaction rules as the worktree diff.

```json
{
  "identifier": "ENG-12", "base": "main", "mergeBase": "9a1c…", "headSha": "4be0…",
  "files": [{"path": "internal/server/changes.go", "status": "modified", "additions": 12, "deletions": 3}],
  "hunksAllowed": true,
  "source": "pr", "sources": ["pr", "worktree"],
  "prUrl": "https://github.com/acme/app/pull/7",
  "worktreePath": "/Users/me/itervox-workspaces/ENG-12"
}
```

- `source` is what was diffed; `sources` what `?source=` may ask for:
  `pr` once the issue has a PR, `worktree` while a run is live or when there
  is no PR.
- A PR file's `status` is `added`, `modified`, `deleted`, `renamed` or
  `copied`; GitHub lists at most 3000 files.
- When GitHub cannot be read and you did not ask for `?source=pr`, the diff
  falls back to the worktree with `prError` (`github_failed`,
  `pr_not_found`); an explicit `?source=pr` answers `502 github_failed`,
  `404 pr_not_found` or `504 github_timeout` instead.
- `worktreePath` is the worktree's directory on the daemon's host, for the
  dashboard's open-in-editor links.

#### `POST /issues/{identifier}/review-comments`

One line or range comment, sent to the running agent or posted on the PR.

```json
{
  "destination": "agent",
  "path": "internal/server/changes.go",
  "startLine": 12, "line": 14, "side": "RIGHT",
  "body": "This should return 404, not 500.",
  "quote": "+\twriteError(w, 500, …)"
}
```

- `line` is the last line of the range and `startLine` its first (omit it
  for one line). `side` / `startSide` are `RIGHT` (the new file, default) or
  `LEFT` (the base file, for deleted lines) — GitHub's review-comment model.
  `body` is at most 8 KiB.
- `destination: "agent"` queues a steering message for the issue's live run
  (the [Sessions](#sessions) steering path, #178) and answers `202 {"destination":
  "agent","runId":"…","message":{…}}`. The message names the file and lines,
  quotes `quote` (the selected diff lines, cut to fit the 10 KiB message
  limit) in a fenced block, then the comment:
  ````
  Review comment on internal/server/changes.go, lines 12-14:

  ```diff
  +	writeError(w, 500, …)
  ```

  This should return 404, not 500.
  ````
  `409 no_live_run` when the issue has no running agent; `409
  input_required` while it waits for an answer.
- `destination: "pr"` posts one review comment on the PR's head commit and
  answers `201 {"destination":"pr","prUrl":"…","id":101,"url":"…"}`. Line
  numbers must come from the PR's diff (GitHub refuses a line outside it:
  `422 github_rejected`, with GitHub's message). `409 no_pr`, `409
  pr_closed` (closed or merged), `404 pr_not_found` (the credentials cannot
  read the PR), `502 github_failed`.

#### `POST /issues/{identifier}/pr-review`

One GitHub review: an event and the batched line comments, submitted
together.

```json
{
  "event": "approve",
  "body": "Looks good; two nits.",
  "comments": [
    {"path": "a.go", "line": 6, "body": "nit: name"},
    {"path": "b.go", "startLine": 2, "line": 3, "side": "LEFT", "body": "why removed?"}
  ]
}
```

`event` is `approve`, `request_changes` or `comment`; the last two need a
`body` or at least one comment. At most 50 comments. `201`:

```json
{
  "prUrl": "https://github.com/acme/app/pull/7", "id": 202, "url": "https://github.com/acme/app/pull/7#pullrequestreview-202",
  "event": "approve", "comments": 2, "merged": false,
  "mergePolicy": {
    "mergesFromHere": false,
    "blockLabels": ["needs-human"],
    "quorum": {"blocked": true, "approvals": 1, "blocks": 1, "quorum": "any_block"}
  }
}
```

`mergePolicy` reports what still governs a merge: `blockLabels` are the
PR's labels that make `merge_pr` refuse, and `quorum` the agent reviewers'
outcome (`null` when none ran), which an operator review does not change.
GitHub refuses an approval from the account that opened the PR (`422
github_rejected`); errors are otherwise those of `review-comments`.

Every post records a feed event — `review_comment_posted` (`via`: `agent`
or `github`) or `pr_review_submitted_by_operator` (`verdict`, `count`) —
with the issue and PR only. Comment text is never logged or put on the
feed.

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

### Playbooks, decisions and steering (#218)

Fetched on demand; none of these are on the `/state` snapshot or the SSE
frames. See the [Playbooks guide](/guides/playbooks/).

#### `GET /api/v1/playbooks`

The playbooks in `agent.playbooks.dir` (viewer):

```json
{"enabled": true, "playbooks": [
  {"name": "bug-fix", "description": "Reproduce first, fix the root cause, prove the fix with a test.",
   "keywords": ["bug", "crash"], "requireEvidence": ["test"]}
]}
```

#### `POST /api/v1/issues/{identifier}/playbook`

Sets the issue's playbook (operator): `{"playbook":"bug-fix"}`, `{"playbook":"none"}`
(no playbook), or `{"playbook":""}` (automatic: label, then keyword
classifier). `202 {"identifier":"ENG-1","playbook":"bug-fix"}`; `400
unknown_playbook` for a name not on disk; `503` when the event queue is full.
The choice is event-loop state, kept across restarts in
`.itervox/playbook_overrides.json`, and applies from the issue's next run.
`GET /api/v1/issues` and `GET /api/v1/issues/{identifier}` report it as
`playbookOverride`, with `playbook` and `playbookSource` (`override`,
`label`, `keyword`) naming the playbook the next (or current) run gets.

#### `GET /api/v1/issues/{identifier}/decisions`

Every run's decision log (`.itervox/handoff/<timestamp>_<profile>.decisions.tsv`
in the issue's worktree), oldest run first (viewer). Fields are redacted and
cut to 500 bytes; at most 200 rows per run and 50 runs; a file over 64 KiB is
read up to that and flagged `truncated`; `skipped` counts lines without a
decision.

```json
{"identifier": "ENG-1", "runs": [
  {"path": ".itervox/handoff/2026-05-02T10-00-00.000Z_implementer.decisions.tsv",
   "runTimestamp": "2026-05-02T10-00-00.000Z", "role": "implementer",
   "decisions": [{"time": "2026-05-02T10:01:00Z", "at": "2026-05-02T10:01:00Z",
     "decision": "Diff against the merge base", "why": "main moves on", "alternative": "main's tip"}]}
]}
```

#### `GET /api/v1/steering/phrases`

The steering chips (`sessions.steering_phrases`, viewer):
`{"phrases":[{"label":"Prove it works","message":"Prove it works: …"}]}`.
A chip is sent like any message, with `POST /api/v1/sessions/{id}/message`.

#### `POST /api/v1/repo/readiness/verify-skill-issue`

**Generate verify skill** (operator): creates a tracker issue asking an agent
to write the project's verification skill, through `tracker.CreateIssue` in
the enforcement issue state (as an accepted enforcement suggestion, #217).
`201 {"identifier":"ENG-42","url":"…"}`; `502 issue_create_failed` (redacted
message) when the tracker refuses.

### Per-issue overrides and human-input flow

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/issues/{identifier}/profile` | `{"profile":"frontend"}` or `{"profile":""}` | `{"ok":true,"identifier":"ENG-1","profile":"frontend"}` |
| `POST` | `/issues/{identifier}/playbook` | `{"playbook":"bug-fix"}`, `{"playbook":"none"}` or `{"playbook":""}` | `202 {"identifier":"ENG-1","playbook":"bug-fix"}` |
| `POST` | `/issues/{identifier}/backend` | `{"backend":"claude"}` or `{"backend":""}` | `{"ok":true,"identifier":"ENG-1","backend":"claude"}` |
| `POST` | `/issues/{identifier}/provide-input` | `{"message":"...","fromAgent"?:true,"via"?:"slack"}` | `{"ok":true}` |
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

`fromAgent: true` (#216) marks a reply relayed for an agent — `itervox mcp`
sends it for its `reply_input` tool. Such a reply takes the agent-action path:
it answers an ordinary input request but never a guardrail approval (the
event loop ignores it for an approval entry, so the refusal holds even when
an approval appears after the caller's check), and the feed records it with
`via: "agent"`. Guardrail approvals stay human-only.

`via: "slack"` (#230) marks a human reply the hub's Slack app relays for an
allow-listed Slack user: it answers input requests and guardrail approvals
like a dashboard reply, is mirrored to the tracker the same way, and the
`input_answered`, `guardrail_approved` and `guardrail_blocked` feed events
record `via: "slack"`. `via` is optional and a closed enum: any other value,
or `via` together with `fromAgent`, is `400 bad_request` (`field: "via"`).

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

## Memory

The Memory page's endpoints (#202). Memory lives in two files the daemon
reads again for every run (`~/.itervox/memory.json`, private, and
`.itervox/memory.json`, project shared; see the configuration reference,
"Memory files"), so a change applies to the next run and reloads nothing.
Every write redacts secrets first; text over 2000 bytes (or a list over 32
items of 512 bytes) is refused.

| Method | Path | Request body | Success response | Errors |
|---|---|---|---|---|
| `GET` | `/memory` | — | `{"enabled","tokenBudget","promptShareTokens","schemaUrl","files":[{"scope":"private"\|"project","path","exists","writable","error"?}],"entries":[Entry],"lessons":{"enabled","drafter","scope","autoApprovePrivate","autoApproveProject","maxPerIssue","rateWindowHours","staleAfterRuns","rejected","enforceThreshold"}}` | — |
| `POST` | `/memory` | `{"text","type"?,"scope"?,"tags"?,"profiles"?,"pinned"?,"status"?,"source"?}` (`type` defaults to `fact`, `scope` to `private`, `status` to `active`; posting a deleted entry back, id and timestamps included, restores it) | `201` the entry | `400 memory_invalid`, `409 memory_conflict` (id in use), `413 memory_too_large` |
| `PUT` | `/memory/{id}` | any of `{"text","type","tags","profiles","pinned","status"}` (omitted fields stay) | the entry | `400 memory_invalid`, `404 memory_not_found`, `413 memory_too_large` |
| `DELETE` | `/memory/{id}` | — | the deleted entry | `404 memory_not_found` |
| `POST` | `/memory/{id}/pin` | `{"pinned":true\|false}` (empty body pins) | the entry | `404 memory_not_found` |
| `POST` | `/memory/{id}/promote` | — | `{"entry":Entry,"path":"<project file>","diff":"<git diff of it>","diffNote"?}` | `404 memory_not_found`, `409 memory_conflict` (already shared) |
| `POST` | `/memory/{id}/approve` | `{"scope"?:"private"\|"project","text"?}` (optional; `scope` defaults to the file the proposal is in, `text` replaces the lesson text: edit, then approve) | the entry, now `active` (moved to `scope`, `lesson.approvedAt` set) | `400 memory_invalid` (bad scope), `404 memory_not_found`, `409 memory_conflict` (not proposed), `413 memory_too_large` |
| `POST` | `/memory/{id}/reject` | — | the removed proposal; the lesson is remembered in `.itervox/lessons-rejected.json` and not proposed again | `404 memory_not_found`, `409 memory_conflict` (not proposed) |
| `POST` | `/memory/{id}/promote-to-rule` | `{"layer":"user"\|"project","severity"?:"must"\|"should","text"?}` (admin, #217; `severity` defaults to `should`, `text` to the lesson's) | `{"entry":Entry,"layer","path","rule":{"id":"lesson-<entry id>","severity","text","description","scope"?}}`; the lesson is now `archived` with `lesson.rule` pointing at the rule | `400 rules_invalid` (bad severity, empty text, or the layer file does not parse), `404 memory_not_found`, `404 unknown_layer` (not `user` or `project`, or the layer is off), `409 memory_conflict` (not an approved lesson, or a different rule already has that id), `409 rules_stale` |
| `POST` | `/memory/{id}/enforcement/accept` | — | `{"entry":Entry,"issue":{"identifier","title","url"?}}`: the `Enforce: <lesson>` issue created in the tracker (in `memory.lessons.enforce_issue_state`), carrying the suggestion's draft and the lesson's evidence; `lesson.enforcement.status` is now `accepted` (#217) | `404 memory_not_found`, `409 memory_conflict` (no pending suggestion), `502 enforcement_issue_failed` (the tracker refused) |
| `POST` | `/memory/{id}/enforcement/dismiss` | — | the entry, `lesson.enforcement.status: "dismissed"` (never suggested again for it) | `404 memory_not_found`, `409 memory_conflict` (no pending suggestion) |
| `GET` | `/memory/recall?q=&profile=&budget=` | — | `{"enabled","profile","budget","tokens","dropped","entries":[{"id","type","text","scope","tags"?,"pinned"?}],"overBudget"?,"unreadableFiles"?}` | `400 bad_request` (`q` missing or over 2000 bytes, `budget` outside 1–8000) |

An `Entry` is the file's entry (`id`, `type`: `preference`\|`fact`\|`decision`\|`lesson`,
`text`, `scope`, `profiles`?, `tags`?, `pinned`?, `status`: `active`\|`proposed`\|`archived`,
`source`?: `{"kind"?,"issue"?,"runIds"?,"links"?}`, `created`, `updated`, `lastUsed`?, `useCount`?,
`lesson`?: `{"trigger","situation"?,"wentWrong"?,"worked"?,"correction"?,"confidence"?,"evidence"?,"draftedBy"?,"approvedAt"?,"verification"?,"rule"?:{"layer","id","at"},"enforcement"?:{"kind":"lint"|"type"|"test"|"docs"|"rule","status":"suggested"|"accepted"|"dismissed","title"?,"draft","draftedBy"?,"suggestedAt","decidedAt"?,"issue"?,"issueUrl"?}}`)
plus `path` (the file that holds it), `runs` (the newest runs in history whose prompt
carried it: `[{"runId"?,"identifier","status","finishedAt"}]`, at most 20) and, for an approved
lesson that is not helping (#203), `notHelping`: `"unused"` (none of the last
`memory.lessons.stale_after_runs` runs used it), `"failing"` (most runs that used it failed)
or `"enforced"` (its enforcement suggestion was accepted, #217) with `notHelpingReason`, and for every
lesson `analytics`: `{"applied","runs","succeeded","failed"}` (how many runs had it in their prompt,
and the runs in history that used it by outcome; #217). `lessons` also carries `enforceThreshold`.
`lesson.trigger` is `input_reply`, `steering`, `failed_then_succeeded`, or since #217
`reviewer_ask`, `reverted_pr` or `recipe`. Lessons Itervox proposes arrive as `status: "proposed"` entries;
approve and reject change the snapshot's `proposedLessons`, which is pushed at once. A promote's `diff`
is `git diff` of the project file after the move, or the whole file when git does not track it
yet (`diffNote` says so); outside a git repository it is empty. A file that does not parse is
reported in `files[].error` and never rewritten (`400 memory_invalid`) until it is fixed.

`recall` (#216, viewer) is memory search for `itervox mcp`, ranked exactly like
run-time retrieval: it runs the dispatch selection (`memory.Resolve`) for a run of
`profile` (default `default`) on an issue titled `q` — the active entries in that
profile's scope, pinned first, then by lexical overlap (ties: more used, more
recently updated, id), added while they fit in `budget` tokens (default the
run-time memory budget before rules, `min(token_budget, prompt_share_tokens)`).
`dropped` counts the in-scope entries the budget left out; proposed and archived
entries are never returned. Text is redacted again on the way out.

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
  set).
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

`provide-input` never answers a guardrail approval request (#204): the
request stays open and the daemon logs the attempt.

### Guardrail endpoints (#204)

Called by the [guardrail](https://itervox.dev/guides/guardrails/) shims and
pre-push hook of a guarded run, with the run's guardrail grant
(`Authorization: Bearer <grant>`, action `guardrail`, scoped to the issue — or
the tmux session ID — and the run). None of them runs anything: the command an
agent waits to run stays on the agent's side, and only its redacted display
form reaches the daemon.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/agent-actions/{identifier}/guardrail/events` | `{"kind":"self_merge","tool":"gh","command":"gh pr merge 12"}` | `204` |
| `POST` | `/agent-actions/{identifier}/guardrail/approvals` | `{"key":"<sha256 of the exact command>","command":"vercel --prod"}` | `{"id":"…","status":"pending"}` |
| `GET` | `/agent-actions/{identifier}/guardrail/approvals/{id}` | — | `{"id":"…","status":"pending"\|"approved"\|"rejected"\|"unknown","message":"<reply>"}` |

`kind` is one of `branch`, `force_push`, `delete`, `tag`, `hook_bypass`,
`self_merge`, `approval_rejected`, `approval_unavailable` (anything else is
`400`); a report is logged, written to the issue's log, recorded in
`recentFailures` (kind `guardrail`, source = the kind) and counted in
`itervox_guardrail_blocks_total{kind}`. A new approval request shows as an
input-required entry of the running issue ("Agent wants to run: …") and is
posted to the tracker; a request with the same `key` as an open one joins it.
An `approved` status is returned to exactly one read, which spends it; later
reads answer `unknown`, as do requests of a run that has ended. Without an
action-grant store the routes answer `501`; a full event queue answers `503`.

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
