# Itervox — REST and SSE API Reference

Base URL: `http://localhost:8090/api/v1`

Non-streaming endpoints return JSON. Streaming endpoints use Server-Sent Events
(`text/event-stream`).

---

## Authentication

### Dashboard / API bearer auth

When Itervox binds to a non-loopback address, it secures all `/api/v1/*`
routes except `/health` with bearer-token auth.

- If `ITERVOX_API_TOKEN` is set, that value is required.
- If `ITERVOX_API_TOKEN` is unset and `server.allow_unauthenticated_lan` is
  `false`, Itervox auto-generates an ephemeral token at startup.
- Loopback binds (`127.0.0.1`, `localhost`, `::1`) do not require auth.
- `GET /health` is always auth-exempt.

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
- `POST /agent-actions/{identifier}/create-issue`
- `POST /agent-actions/{identifier}/move-state`
- `POST /agent-actions/{identifier}/provide-input`

Profiles opt into these capabilities with `allowed_actions` and
`create_issue_state` in `WORKFLOW.md`.

---

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
- Some legacy streaming code paths still use plain text responses for transport
  failures before SSE framing starts.

---

## Health

### `GET /health`

Auth-exempt liveness probe.

**Response** `200`

```json
{ "status": "ok" }
```

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
- Event shape: `id: <cursor>\nevent: sublog\ndata: <JSON IssueLogEntry>\n\n`
- Supports resume via `Last-Event-ID`
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
| `PATCH` | `/issues/{identifier}/state` | `{"state":"In Review"}` | `{"ok":true,"identifier":"ENG-1","state":"In Review"}` | Triggers immediate refresh |

### Per-issue overrides and human-input flow

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/issues/{identifier}/profile` | `{"profile":"frontend"}` or `{"profile":""}` | `{"ok":true,"identifier":"ENG-1","profile":"frontend"}` |
| `POST` | `/issues/{identifier}/backend` | `{"backend":"claude"}` or `{"backend":""}` | `{"ok":true,"identifier":"ENG-1","backend":"claude"}` |
| `POST` | `/issues/{identifier}/provide-input` | `{"message":"..."}` | `{"ok":true}` |
| `POST` | `/issues/{identifier}/dismiss-input` | — | `{"ok":true}` |

`provide-input` / `dismiss-input` return `404 not_found` when the issue is not
currently in `input_required`.

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

All settings endpoints persist back to `WORKFLOW.md`.

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
| `DELETE` | `/workspaces` | — | `202 {"ok":true}` |
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

- Empty array means “all issues”.
- Omitting `slugs` resets to the `WORKFLOW.md` default.

---

## Agent actions

These routes are intended for agent subprocesses that have been granted
daemon-backed permissions through profile `allowed_actions`.

| Method | Path | Request body | Success response |
|---|---|---|---|
| `POST` | `/agent-actions/{identifier}/comment` | `{"body":"..."}` | `{"ok":true}` |
| `POST` | `/agent-actions/{identifier}/comment_pr` | `{"summary":"...","findings":[{"path":"...","line":42,"severity":"warning","body":"..."}]}` | `{"ok":true,"findings":N}` |
| `POST` | `/agent-actions/{identifier}/create-issue` | `{"title":"...","body":"..."}` | `{"ok":true,"issue":{...}}` |
| `POST` | `/agent-actions/{identifier}/move-state` | `{"state":"Todo"}` | `{"ok":true}` |
| `POST` | `/agent-actions/{identifier}/provide-input` | `{"message":"..."}` | `{"ok":true}` |

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
