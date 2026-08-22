# Changelog

All notable changes to Itervox are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [Unreleased]

---

## [0.2.1] — 2026-08-22

Dependency autonomy, a write-ahead outbox for tracker writes, and the security and ops fixes that accumulated alongside them. Itervox now only picks up issues that are not blocked, orders work by what unblocks the most downstream effort, detects dependency cycles, keeps its dependency analysis fresh on its own, and captures far more real dependencies from both trackers — while making roughly 10x fewer tracker requests on the hot paths.

### Upgrade notes

> **Read these five before upgrading — each can change which issues get worked on.**
>
> 1. **Issues that used to dispatch may now be held.** Parents with open sub-issues, and issues whose bodies match the widened blocker phrases, are now blocked. Every hold is explained in the dashboard.
> 2. **A Linear parent carrying a `blocks` relation on its own child now forms a visible 2-cycle** — both issues are held and an alert is raised. Remove the relation to resolve it.
> 3. **Blocker-unblock detection lags up to ~15 minutes** worst case (5-minute state cache plus the 10-minute refresh interval). Stale data always fails safe, i.e. still-blocked.
> 4. **The runtime project filter now narrows automations, the dashboard, and the TUI**, not just dispatch.
> 5. **Two default paths are now namespaced per project.** A project that sets
>    no `tracker.project_slug` previously shared `~/.itervox/logs` with every
>    other slugless project, and `workspace.root` defaulted to a single shared
>    `~/.itervox/workspaces` for everyone. Both now sit under a per-project
>    subdirectory. On upgrade, itervox starts from empty state in the new
>    locations: in-flight worktrees under the old shared root are not migrated,
>    so finish or discard running work before upgrading, or set
>    `workspace.root` explicitly to keep the old path. The old directories are
>    left in place and can be deleted once you are satisfied.
> 6. **`server.port` now defaults to a fixed 8090** instead of an ephemeral port. Running several daemons at once needs a distinct explicit `server.port` per repo, or `server.port: 0` to keep asking the OS for a free port.

### Added

- **Durable, async tracker writes via a write-ahead outbox.** Tracker state transitions and comments are now enqueued to `.itervox/outbox.json` and flushed by an independent worker instead of being written synchronously (with inline retries) from the orchestrator's completion/failed-state paths — a crash between "agent finished" and "tracker updated" no longer loses the transition, and a slow/rate-limited tracker call no longer blocks the event loop. Pending writes are overlaid onto the dashboard's issue state so a completed-but-unflushed issue is never re-dispatched, and are visible via a "Syncing" badge on the affected issue card, a LiveOps tile (pending/degraded counts), and a new Outbox panel with per-entry **Retry** and **Discard** controls (`POST /api/v1/outbox/{id}/retry`, `DELETE /api/v1/outbox/{id}`). Set `tracker.outbox: false` as a kill switch to restore the previous synchronous write behavior — see `docs/configuration.md`'s `tracker.outbox` row. **Known limitation:** an entry enqueued with no observed `from_state` baseline (currently only the issue-discard path) is exempt from supersede-reconciliation, so it retries until it lands or an operator discards it via the Outbox panel.
- **Critical-path dispatch ordering** (default on): priority band, then transitive dependents, then longest chain, via Tarjan SCC condensation. `dependencies.ordering: critical_path_strict` compares graph leverage ahead of the priority band; `dependencies.ordering: simple` restores the legacy order.
- **Inferred (LLM-detected) dependency edges soft-gate dispatch** — confidence >= 0.7, fresher than 168h, no per-issue operator override, and a known non-terminal source. Tracker-declared blockers remain hard blocks. Kill switch: `dependencies.inferred_gating: false`.
- **Dependency cycles are first-class alerts** (LiveOps tile, Deps-tab banner and red edges, heartbeat). Members stay blocked and are never auto-released. **Escalation** surfaces issues blocked longer than `dependencies.escalate_blocked_after_hours` (default 48).
- **The dependency analyzer is cancellable, timeout-bounded, chunked, and autonomous.** `DELETE /api/v1/deps/analyze/{jobId}` cancels a run; `agent.deps_analyzer_timeout_ms` (default 10 min) bounds it; `agent.deps_analyzer_chunk_size` (default 75) bounds the prompt. Change-driven incremental passes run on a 5-minute debounce with a 60-minute floor (`dependencies.auto_analyze`, default true).
- **Widened tracker dependency parsing.** GitHub now recognises `blocked by/on`, `depends on/upon`, `requires`, and `waiting on/for` plus `#N` lists as hard edges. Linear sub-issues gate their parent.
- **Per-issue inferred-blocker overrides** via `POST`/`DELETE /api/v1/issues/{identifier}/deps-override`, surfaced in the dashboard Deps tab.
- **Dispatch-pressure telemetry**: each tick records whether dispatch was slot-bound or dependency-bound, surfaced in the dashboard status strip.
- **JSON structured logging** via `--log-format=json`; stderr logging is no longer lost when the TUI does not start.
- **Tracker rate limits are now waited out, not fatal.** HTTP 429 responses are retried with a bounded, `Retry-After`-aware backoff (both RFC 9110 forms), capped at 4 retries and 60s per wait and abandoned on shutdown. GitHub signals rate limits with 403 rather than 429 for both its primary and secondary limits, so those are recognised too — by an exhausted `x-ratelimit-remaining` or a `retry-after` header, never by a bare 403, which stays a genuine authorization failure and is not retried. Previously a 429 failed the call outright, so the moment a tracker's budget ran out *every* operation failed — including the state transitions and comments that would have drained the queue and stopped itervox asking for more.
- **The dependency audit refreshes in batches on Linear.** Rows carrying an issue ID resolve in a single `FetchIssueStatesByIDs` request instead of one request each (GitHub has no batch endpoint, so it still fetches per issue); the audit reads only blockers, identity and state, all of which the batch query returns. This was the largest per-issue consumer of the tracker budget (~28% of Linear traffic in issue #42's incident). Identifier-only rows still fetch individually, and an ID absent from a batch response is confirmed by a single authoritative fetch before its row is retired.
- **Stacked worktrees** (`dependencies.stacked_prs`, default `false`). When an issue has exactly one live, identified blocker, its worktree is branched from that blocker's branch instead of `workspace.base_branch`, so the work starts from the blocker's commits rather than duplicating them. Opt-in and best-effort: several live blockers give no unambiguous base, and a blocker branch that does not exist locally falls back to `base_branch` rather than failing the dispatch. **The pull-request base is not yet set from the blocker** — until that lands (#60), a PR opened from a stacked branch still diffs against the default branch and therefore still shows the blocker's commits.
- **`itervox doctor` validates agent commands.** Every enabled profile's binary is checked against `PATH`, and doctor exits non-zero naming the profile when one is missing. This catches the common case of a tool installed under a version manager (nvm, asdf), whose bin directory is on `PATH` in an interactive shell but not in the environment the daemon was started from — previously visible only as a runtime "command not found" buried in the log.
- **Write priority under tracker rate limits** (`polling.rate_limit_reserve_percent`, default 10). Reads and writes drew on one request budget, and the polling loops scale with the number of stuck issues — so polling could exhaust the hourly budget early, after which every state transition, comment and dependency audit failed. Unsticking an issue requires a write, so reads starved the operations that would have drained the queue. Below the reserve, Itervox now sheds **polling** reads (the candidate poll and the input-required reply check) while still admitting writes and the input-resume fetch. Set `0` to disable; adapters that report no rate-limit counters are unaffected (the check fails open).
- **Pause reasons.** `itervox` now records WHY an issue is paused — `user_cancelled`, `user_dismissed_input`, `retries_exhausted`, or `transition_failed` — and persists them across restarts. Previously a completion-state write that failed was recorded identically to a deliberate user cancel, so an operator could not tell which pauses were safe to resume and a recoverable infrastructure failure sat waiting on a human who had no way to know.

### Changed (breaking)

- **Bearer-token auth is now on by default on every bind, including loopback (#48).** Previously the daemon only auto-generated `ITERVOX_API_TOKEN` and installed bearer-token middleware when `server.host` was a non-loopback address. That gate was the wrong signal: a loopback bind (`127.0.0.1`) sitting behind a reverse proxy, ngrok, Piko, or another tunnel is exactly as reachable from outside as a non-loopback bind, and the daemon has no way to detect that from inside the process. As of this release, the API requires a bearer token by default on **all** binds; unauthenticated local scripting must either read the token (from stderr, `.itervox/dashboard_url`'s sibling startup log line, or a pinned `ITERVOX_API_TOKEN` in `.itervox/.env`) or explicitly opt out via `server.allow_unauthenticated: true`.
- **`server.allow_unauthenticated_lan` renamed to `server.allow_unauthenticated`.** The flag is no longer LAN/non-loopback-scoped — setting it to `true` disables auth entirely on every bind. The old key still parses as a deprecated alias (with a startup `slog.Warn`); the new key wins if both are set in the same `WORKFLOW.md`. Update `WORKFLOW.md` and any templates/examples to use the new key.
- `GET /api/v1/health` remains the only auth-exempt route — unchanged behavior, but corrected in docs (README, `docs/configuration.md`) which previously and incorrectly described it as `GET /health`.
- **`server.port` defaults to a fixed 8090** instead of an ephemeral port — see upgrade note 5.

### Changed

- **The HTTP socket survives config reloads.** The listener binds once, above the reload loop, and is rebound only when the resolved address actually changes — so editing an unrelated setting no longer drops in-flight requests and SSE streams, and the dashboard URL stays stable. WORKFLOW.md edits debounce into a single reload.
- **Linear workflow-state UUIDs are cached**, so a state transition costs one request instead of two; `FetchIssuesByStates` honors the runtime project filter.
- **GitHub blocker states are cached for 5 minutes** — probe-measured at ~480 requests/hour against ~4,800 uncached at 40 refs.
- **The dependency audit's synchronous tracker fetches moved off the event loop** (batched, throttled, watchdogged) and gained a staleness TTL (`agent.dependency_audit_refresh_interval_ms`, default 10 min).
- **The input-required replay cache survives across ticks**, removing a per-entry re-fetch every 15 seconds.

### Fixed

- **Auto-review could spawn agents without bound.** With `tracker.completion_state` set, a successful worker moves its issue terminal; reconciliation then stops the run and removes it from the live set, so the run's own exit event arrived with no live entry. That was read as "a plain worker succeeded", which dispatched a reviewer, which was stopped the same way — a loop nothing broke while the issue stayed terminal. The eligibility check now fails closed on an unknown run. Measured before the fix at 23-30 agent runs in 1.5s against an expected 2.
- **The write-ahead outbox discarded its own completion transitions.** The completion path enqueues the session comment ahead of the state transition for the same issue, so the comment flushed first and bumped the tracker's `updated_at` without changing state — which reconciliation read as a human edit and dropped the transition, leaving the issue active and eligible for re-dispatch. Entries now record the state observed at enqueue time, and reconciliation requires an actual state change.
- **A malformed persisted outbox entry could wedge an issue's write queue silently.** Entries are validated on load, not only on enqueue, and an undeliverable entry now accrues attempts so it backs off and surfaces as degraded instead of retrying at full tick rate behind a healthy-looking badge.
- **Incremental dependency analysis eroded its own graph.** An inferred edge spanning a changed and an unchanged issue was dropped and could not be re-derived, because the unchanged endpoint never reached the analyzer. With auto-analysis on by default and no scheduled full pass, repeated runs degraded the graph and silently un-gated dispatch. Boundary-spanning edges now bring their unchanged endpoint into the pass.
- **A cosmetic `server.host` edit could kill the daemon.** The reload path compared the literal `host:port` string, so `127.0.0.1` to `localhost` looked like a move; the new listener bound before the old one closed, collided with itself, and exited via a path that skipped cleanup of `daemon.pid`, `dashboard_url`, and `HEARTBEAT.md` — leaving `itervox doctor` reporting a daemon that had died. Rebinds are now decided on the resolved address and release the old socket first.
- **Reviewer fan-out (`agent.reviewer_profiles`) is disabled in this release.** A config listing several reviewers runs only the first, with a startup warning, and a config setting only `reviewer_profiles` now starts instead of hard-failing. The chain did not advance past the first reviewer, and the workspace was cleared while a reviewer was still live. The machinery ships intact but gated.
- **One transient accept failure could wedge the HTTP listener for the life of the process.** The daemon's accept loop replaced `http.Server.Serve`'s, which backs off and retries temporary errors — without that, a single fd-exhaustion blip (`EMFILE`/`ENFILE`) ended accepts while the socket stayed bound, so the port still showed LISTEN, clients hung, and a config reload could not recover it. Temporary accept errors now retry on net/http's 5ms-doubling-to-1s schedule.
- **A config reload could briefly run two orchestrators against one outbox file.** `run()` waited for the HTTP server when the orchestrator exited first, but returned immediately in the opposite case — which is the one a reload actually takes, since shutting down the HTTP generation is a channel close while the orchestrator is still draining. The reload loop then opened a second `.itervox/outbox.json` handle, and two handles rewriting the whole file each persist can erase one another's durable entries. Both exit paths now wait, with a bounded grace and a warning if it is exceeded.
- **The input-required reply check no longer scales its request rate with the backlog.** It spent one tracker request per stuck issue per tick, uncapped and in randomized map order, so a 19-issue backlog cost roughly 2,280 requests/hour against Linear's 2,500/hour ceiling before any real work (issue #42). It now spends a fixed budget per tick, least-recently-checked first, so cost is constant in backlog size and every entry is still reached within a couple of minutes. The pending-resume path gained the same bound on its failing-fetch retries.
- **The "another daemon is already running" guard actually holds now.** Three gaps closed: it ran *after* the logs directory was created and the rotating handler installed, so a daemon about to be rejected had already written to the live daemon's log file; `pidFilePath` used `filepath.Abs` without resolving symlinks, so a symlinked checkout produced a different pid path and neither daemon saw the other; and the one-shot subcommands that write `.itervox/` state outside the daemon — `itervox init --update` and `itervox deps analyze` — bypassed the guard entirely and now warn that a live daemon owns the same state.
- **A bare clone is no longer reused for a different repository.** `EnsureBareClone` reused `<root>/.bare` on the mere presence of a `HEAD` file, never comparing `remote.origin.url` to the configured `clone_url` — so two projects sharing a workspace root had the second daemon silently operating on the first one's repository, branching from, committing to and pushing the wrong repo. It now verifies the remote (normalising scp-vs-https spellings) and refuses rather than re-cloning, since the existing clone may hold another daemon's live worktrees.
- **`itervox init` no longer scaffolds a colliding workspace root.** It wrote `root: ~/.itervox/workspaces/<repo-basename>`, and because that is explicit it bypassed the per-project default entirely: two checkouts named `api` under different owners shared a root, and every non-git directory shared `my-project`.
- **Skill analytics read this project's logs.** They pointed at the bare `~/.itervox/logs`, which the daemon no longer writes — returning nothing, or another project's leftovers.
- **Projects no longer share state through global directories.** Two collision surfaces, both keyed by values that are only unique *within* one project:
  - `~/.itervox/logs` was shared by every project without a `tracker.project_slug`, and that directory holds `automation_queue.json` — which carries the **dependency audit**. One project's audit rows were restored by another's daemon on every start. Observed live: a Linear project inherited ten `demo-id-*` rows written by an unrelated `kind: memory` run, then asked Linear about issues that had never existed there on every refresh cycle.
  - `workspace.root` defaulted to a single `~/.itervox/workspaces`, and a workspace directory is keyed by issue **identifier** alone. GitHub identifiers are the repo-local issue number, so *every* GitHub repo has a `#1` — two daemons would use the same directory for their respective issue #1, checking out different codebases over each other, and one project's `workspace.auto_clear` would delete another's live worktree mid-run.
- **Malformed issue IDs no longer fail a whole Linear batch.** `issues(filter: {id: {in: […]}})` validates the entire list and rejects the request if any element is not a UUID or `TEAM-123` identifier, so one stale row took down every healthy row beside it. Such ids are now filtered out before the query and confirmed individually instead.
- **A deleted or unknown Linear issue no longer retries forever.** Linear reports an ID it has never seen as a GraphQL error (`Entity not found: Issue`), not as a null result, so it was classified as a transient failure and the dependency-audit row was retained and retried every refresh cycle for the daemon's lifetime. It now maps to the not-found sentinel and the row retires. Only that specific message is treated as permanent — rate-limit and auth errors stay retryable, because the response to not-found is to delete the row.
- **`itervox init --update` creates `.itervox/.env`.** The update path returned after migrating the workflow schema and never reached the code that scaffolds the environment file, so a migrated project was left referencing `$LINEAR_API_KEY` with no file to define it in and the daemon hard-failed startup with `missing tracker.api_key`. The stub is written with `0600` permissions and never overwrites an existing file.
- **Hallucinated dependency edges no longer reach the sidecar.** Nothing validated that analyzer-emitted `source`/`target` identifiers corresponded to real issues, so an LLM-invented pair was written to `.itervox/dependencies.json` and reloaded verbatim on every start. Edges are now validated against the fetched issue set. Validation is against the whole fetch, not the analyzed chunk, so legitimate cross-chunk edges survive.
- **A fatal daemon exit no longer leaves stale liveness files.** `fatalExit` is `os.Exit`, which skips deferred cleanup — so a failed rebind or an invalid config left `daemon.pid`, `dashboard_url` and `HEARTBEAT.md` behind, which are exactly the files `itervox doctor` and `itervox status` read to decide a daemon is alive. The daemon died while leaving evidence it was running.
- **`itervox doctor` no longer warns on a healthy daemon.** Its port-collision check keyed off "`server.port` is set", but that field is always populated now that the port defaults to 8090 — so every daemon reported its own listening socket as a collision, with a warning and exit 1. It now compares the holder's PID against the recorded daemon.
- A transient empty tracker fetch can no longer wipe the inferred-dependency sidecar.
- Dependency-analyzer runs report the number of issues scanned, write logs to disk, and report progress.

---

## [0.2.0] — 2026-07-06

### Migration from v0.1.x

> **Schema 2 is mandatory.** `WORKFLOW.md` files now require an `itervox_schema_version: 2` marker at the top, and inline `agent.profiles.<name>.prompt` is no longer accepted. On startup the daemon hard-fails with a `MissingWorkflowSchemaMessage` pointer when either condition is unmet. To migrate an existing v0.1.x project:
>
> ```bash
> itervox init --update --workflow WORKFLOW.md
> ```
>
> The migrator writes a `WORKFLOW.md.bak` next to your workflow, extracts each profile's inline `prompt:` block into `.itervox/agents/<name>/INSTRUCTIONS.md`, generates a compact `.itervox/agents/<name>/SOUL.md` identity file, and ensures the git policy via a nested `.itervox/.gitignore` (always written; also self-healed on every daemon startup) plus root-`.gitignore` carve-outs when a root `.gitignore` broadly ignores `.itervox/` — so `.itervox/agents/**` and `.itervox/handoff/**` can be committed while runtime state stays ignored. Review the migrated `WORKFLOW.md` and the new agent files, then delete `WORKFLOW.md.bak`. See [Agent Profiles guide](https://itervox.dev/guides/agent-profiles/) for the file-backed profile reference.
>
> **If you set `workspace.auto_clear: true` on a v0.1.x project,** the migrator will print a one-line warning to remind you that v0.2.0 changes the semantics from "clear after every successful run" to "clear only when the issue reaches a terminal tracker state." For features that succeed on first attempt, behavior is unchanged. See the **Changed (breaking)** section below for the full rationale.

### Removed (breaking)

- **Inline `agent.profiles.<name>.prompt` is rejected by schema 2.** Each profile must now reference SOUL/INSTRUCTIONS files via `soul_file` and `instructions_file` (typically `.itervox/agents/<name>/SOUL.md` and `INSTRUCTIONS.md`). Use `itervox init --update --workflow WORKFLOW.md` to migrate; see the Migration section above.
- **`agent.agent_mode`** is gone. The previous values were `""` (solo), `"subagents"`, `"teams"`. Behavior reasoning:
  - `""` and `"subagents"` were aliases at runtime — the daemon never actually gated subagent dispatch.
  - `"teams"` injected a "your peer agents are X, Y, Z" roster into worker context; that injection now happens **unconditionally** when more than one profile exists.
  - Profile content (now `INSTRUCTIONS.md`) always injects when a profile is selected. Previously the legacy inline `prompt:` was suppressed unless `agent_mode` was non-empty — silent suppression that operators rarely intended.

  **Migration:** delete the `agent.agent_mode` field from your `WORKFLOW.md`. The daemon now hard-fails at startup with a clear pointer to this entry if the field is present (typo guard). The legacy alias `agent.enable_agent_teams` is similarly rejected.
- The **`POST /api/v1/settings/agent-mode`** HTTP endpoint and the **"Agent Runtime"** Settings card are removed. The `inline_input` toggle (which used to live in that card) now lives in Settings → General and still persists to `WORKFLOW.md::agent.inline_input`.
- The TUI status row no longer displays a `◈ SUB-AGENTS` / `◈ TEAMS` badge.

### Changed (breaking)

- **`workspace.auto_clear: true` semantics changed.** Previously the workspace was removed after **every** successful worker run (mid-pipeline state transitions included). It now clears **only when the issue reaches a terminal tracker state** — either `tracker.completion_state` after a successful run, or `tracker.failed_state` after retries are exhausted. The field name and type are unchanged.

  **Why:** the per-success clear pattern broke the new file-backed agent handoff convention (see Added), which expects `.itervox/handoff/` files to accumulate across multiple workers running on the same branch for one issue. The new semantics preserve the workspace across retries and pipeline mid-states and only clean up once the issue is definitively done.

  **Who is affected:** users relying on per-run disk-space hygiene for flaky issues that retry many times will see workspaces persist across retries until the issue reaches `failed_state`. For features that succeed on first try, behavior is unchanged. `itervox init --update --workflow WORKFLOW.md` now prints a one-line notice when it finds `workspace.auto_clear: true` so the semantic shift is not silent.

  **Workaround for old behavior:** none in-tree. Run an external cleanup (e.g. `find <workspace_root> -mtime +1 -delete`) outside the daemon if eager clearing is required.

- **`workspace.auto_clear` + `agent.auto_review` no longer conflict.** Under the legacy per-success clear semantics the two settings raced — the clear removed the workspace before the reviewer could read it. Validation rejected the combination at config-load and at runtime PUT/PATCH. With the new terminal-state-only semantics the clear is deferred until after the reviewer also completes, so the two now safely coexist. `ValidateAutoClearAutoReview` is retained as a no-op for callers that branch on `ErrAutoClearAutoReviewConflict`; new code should not call it. Operators who relied on the validation error to catch accidental over-configuration should note that both flags will now silently be accepted.

### Added

- **`[SILENT]` no-op convention for tracker comment delivery.** When an agent run's final output begins with `[SILENT]` (case-sensitive, leading whitespace ignored), the session summary is not posted as a tracker or PR comment. The run remains fully visible in the dashboard's per-issue logs — only the tracker notification is suppressed. Designed for recurring automations (hourly scans, nightly checks) whose common case is "nothing to report"; instruct the agent to reply `[SILENT] <short reason>` on the no-findings path. A `[SILENT]` occurring mid-message does not suppress. See the [Automations guide](https://itervox.dev/guides/automations/) for a cron example.
- **`itervox_schema_version: 2`** is now required at the top of every `WORKFLOW.md`. The daemon validates this marker at startup via `MissingWorkflowSchemaMessage` and refuses to run when it is absent. Existing v0.1.x workflows are migrated by `itervox init --update --workflow WORKFLOW.md` (see the Migration section above).
- **File-backed agent profiles.** Profile identity and operating instructions now live in `.itervox/agents/<name>/SOUL.md` (compact who-you-are file) and `.itervox/agents/<name>/INSTRUCTIONS.md` (full behavioural rules), referenced from `WORKFLOW.md` via `agent.profiles.<name>.soul_file` and `instructions_file`. `.itervox/agents/**` is committable; the root `.gitignore` is patched by `itervox init` and `itervox init --update` to allow it while keeping `.itervox/.env`, `.itervox/HEARTBEAT.md`, logs, and runtime queue files ignored.
- **File-backed agent handoff (`.itervox/handoff/`).** Each worker run can leave a Markdown deliverable at `.itervox/handoff/<ISO8601-timestamp>_<profile-name>.md` on the issue's worktree branch. On the next worker dispatch for the same issue, the orchestrator reads every `.md` file in that directory in chronological order (lexicographic = chronological because the prefix is ISO8601), concatenates them with separators, applies a token budget (default 30 KB; oldest dropped first with a `[earlier handoffs truncated]` marker), and inlines the result into the agent's prompt under a `## Prior Agent Handoffs` heading.

  The handoff path for the current run is appended to the prompt as a `## Run Context` block carrying `run.timestamp` and `run.handoff_path`. Profile INSTRUCTIONS.md scaffolds now include a "Handoff Protocol" section instructing the agent to read the prerendered prior-handoffs block and write its own deliverable to `run.handoff_path` before exiting. Files are gitignore-carved-out via `!.itervox/handoff/**` (same pattern as `!.itervox/agents/**`) so the pipeline trail commits cleanly into PRs. See the new [Agent Handoff guide](https://itervox.dev/guides/agent-handoff/).

- **Partial-handoff marking on non-success worker exit.** When a worker exits with `TerminalFailed` or `TerminalStalled` (process crash, max-turn timeout, stall watchdog), the orchestrator renames the most recent matching `<timestamp>_<profile>.md` to `<timestamp>_<profile>.partial.md`. Subsequent agents still see the partial in their handoff context but can distinguish "this agent crashed mid-deliverable" from a clean handoff. `TerminalInputRequired` does NOT mark partial — the agent intentionally paused and will resume. `context.Canceled` failures (orchestrator-initiated stops during reload or shutdown) also do not mark partial — they are not real failures.

- **`itervox init --update` migration notice for `workspace.auto_clear: true`.** When migrating a workflow that has the setting enabled, the updater prints a one-line notice via `result.Warnings` describing the semantic shift to terminal-state-only clearing. Fires on every `--update` run, even when no schema migration is required, so operators discovering the setting late still see the notice.
- **`itervox init --update --workflow <path>`** migrates v0.1.x workflows to schema 2. It writes a `WORKFLOW.md.bak` backup, extracts each profile's inline `prompt:` text into `INSTRUCTIONS.md`, generates a starter `SOUL.md`, patches the root `.gitignore` for `.itervox/agents/**`, and stamps `itervox_schema_version: 2` on the migrated workflow. Re-run on the same file is supported; existing agent files are preserved.
- **`.itervox/HEARTBEAT.md` daemon liveness file.** A human-readable status file is written on startup and refreshed after state changes at a bounded interval (default 15 s). It records the active workflow path, schema version, dashboard URL, tracker/project, capacity, automation queue pressure, dependency audit summary, input-required count, retry count, and last notable error. The file is gitignored as transient runtime state — it is NOT a tracked artefact.
- **Automations v1**: a new top-level `automations:` block in `WORKFLOW.md` replaces the cron-only `schedules:` surface with ten trigger types — `cron`, `input_required`, `tracker_comment_added`, `issue_entered_state`, `issue_moved_to_backlog`, `run_failed`, `pr_opened` (gap B — fires when a worker's PR is detected), `pr_merged` (P1 — fires after a daemon-side merge succeeds or an external GitHub merge is observed), `rate_limited` (gap E — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven; the switch cap only limits/suppresses switching), and `blockers_resolved` (fires when dependency audit observes a previously blocked issue becoming unblocked). Each rule carries its own filter (`states` / `states_any`, `labels_any`, `identifier_regex`, `input_context_regex`, `limit`, `match_mode`) and instruction block layered on top of a selected profile. Legacy `schedules:` blocks are still parsed and upgraded to `cron` automations for back-compat.
- **Automation launch boundary documented**: v0.2.0 is scoped to single-profile helper automations. It does not claim production downstream workflow orchestration: no fatal post-change gates, automation skip-decision logs, schedule run-now/next-fire operations, label-to-profile routing, reserved automation slots, structured gate artifacts, PR-check triggers, cost caps, or native multi-step planner/debate workflow execution. The docs now label dependency readiness as deterministic but intentionally narrow, and debate patterns as prompt-governed/advisory until native multi-step workflow execution lands.
- **Durable automation queue and backpressure**: automation triggers that cannot start immediately are now represented in bounded orchestrator-owned queue state instead of disappearing when worker slots are full. `agent.max_automation_queue_length` defaults to 100; when saturated, cron/polled producers pause and the snapshot exposes `automationQueueBackpressure` for dashboard alerts.
- **Dashboard Live Ops strip**: the top of the dashboard now shows live/waiting/offline state, agent capacity, automation queue length, blocked/unblocked dependency counts, retry/paused/input counters, SSH worker activity, today's automation dispatch count, and a red queue-full warning when automation producers are paused.
- **Dashboard Automation Queue list and detail panel**: queued, blocked, dispatching, and dependency-ready automation entries are now visible on the dashboard with local search, queue-saturation alerts, blocker badges, profile/backend context, queued age, and details/open-issue actions. The left-side details panel shows trigger metadata, dependency audit state, automation filters/policy, profile permissions, worker capacity, and activity path. Retry, resume, and review queues now share local queue search; the review queue defaults collapsed and expands into a bounded scroll area.
- **Issue status timeline**: single-issue detail now shows the issue's recent tracker/status transitions with source chips for tracker observation, dashboard actions, worker lifecycle moves, automation moves, and system cleanup. Automation-sourced rows include automation/profile/backend/worker metadata when available.
- **Dependency audit and `blockers_resolved` automation**: the orchestrator now tracks dependency audit rows, treats unknown blocker state as blocked for dispatch, records unblock transitions, and can emit `blockers_resolved` automations. The documented safe default moves only `backlog` / `Backlog` issues to `Todo`, and only when the selected profile has `move_state`.
- **Dashboard Deps tab**: a display-only React Flow graph now visualizes blocker -> blocked issue relationships from dependency audit data, with running, queued, terminal, blocked, unblocked, and unknown badges. Clicking a node reuses the existing issue detail panel; the graph does not edit dependencies or tracker state.
- **Operator guides for queue/dependency/status surfaces**: new docs cover automation queue/backpressure, dependency management and safe unblock automation, the dashboard Deps tab, and issue status history.
- **Launch-safe profile and automation templates**: Settings now includes comment-first profile templates for readiness, unblock, planner/debate, release, failure, security, capability, docs, and browser-QA roles, plus additional automation starter designs for planning gates, debate, evaluator, release, and skills-hygiene workflows.
- The dedicated **Automations** page now includes a Configure tab (replacing the old Schedules surface) with full CRUD, three everyday suggested templates (input responder, QA validation, PM backlog review), additional launch-safe helper designs, and live-editable save — changes take effect on the next automations tick without a daemon restart.
- **Daemon-backed agent actions**: profiles can now opt into `comment`, `create_issue`, `move_state`, and `provide_input` permissions via `allowed_actions`, with `create_issue_state` for follow-up issue creation. The daemon issues short-lived per-run bearer grants for `/api/v1/agent-actions/*` instead of exposing the main dashboard/API token to agent subprocesses.
- **Bearer-token auth** for the HTTP API and SSE stream: on non-loopback binds, Itervox auto-generates an `ITERVOX_API_TOKEN` and requires `Authorization: Bearer <token>` on every request. Opt-out for trusted LANs via `server.allow_unauthenticated_lan: true`. A login screen in the dashboard handles token entry and persistence (session or remembered), with cross-tab sync.
- **Timezone typeahead** in the Automations cron editor. IANA zone names (e.g. `America/New_York`) auto-suggest from the browser's ICU data via `Intl.supportedValuesOf('timeZone')`, with a fallback list for older browsers. The input still accepts any free-form zone string.
- **Dashboard “Resuming” panel** that lists issues whose human reply has been received and are waiting to resume. Previously `pending_input_resume` was only shown as a small counter in the app header and a per-card badge; the new panel makes stuck resumes visible at a glance. The header counter is now a clickable link that jumps to the panel.
- **Skills Inventory UI and capability analytics groundwork**: Settings now exposes a Skills Inventory view backed by a new `internal/skills` package that scans Claude, Codex, and shared skill/plugin/hook/MCP/instruction layouts, normalizes them into an inventory graph, estimates context-budget cost, produces recommendation data, and shows a current/stale status for tracked capability files.
- **Atomic file writes** via new `internal/atomicfs` package: all WORKFLOW.md mutations, pidfile writes, and scaffold generation now use temp-file + fsync + rename, preventing corrupt config on SIGKILL or power loss. 12 write sites migrated.
- **Single-write cascades** for profile rename/delete: `ApplyAndWriteFrontMatter(path, mutators...)` composes multiple YAML mutations into one atomic write, with per-path `editMu` serialization. Profile renames that update profiles, automations, and reviewer config now produce a single disk write instead of three.
- **Quickstart template** replaces `--demo` flag: an embedded `internal/templates/quickstart.md` with `tracker.kind: memory` and seed issues provides a self-contained evaluation experience without external tracker credentials. The `--demo` flag is removed.
- **SSE `Last-Event-ID` resume**: log-stream and sublog-stream SSE endpoints now emit `id:` lines and honor the `Last-Event-ID` header on reconnect, so `@microsoft/fetch-event-source` resumes mid-stream instead of replaying from the beginning.
- **Logbuffer per-line byte cap** (64 KiB): oversized log lines (e.g. base64 blobs in agent output) are truncated with a `…[truncated N bytes]` marker before storage, preventing unbounded memory growth.
- **Size-budget CI guard**: `make size-budget` (wired into `make verify`) enforces LOC caps on a small set of intentionally budgeted files, failing CI if extractions regress.
- **Local release preflight**: `make release-check` now runs the normal `make verify` gate plus `govulncheck`, `goreleaser check`, and a GoReleaser hook dirty-worktree guard for tag preparation.
- **Reviewer-parity helper**: `resolveBackendForIssue` in `internal/orchestrator/dispatch_resolve.go` deduplicates the backend/command resolution logic shared by worker dispatch and reviewer dispatch (~40 lines collapsed to ~14 at each call site).
- **Automation backend parity**: automation workers now use the same backend resolver as normal workers and reviewers, so wrapper commands honor `agent.backend`, profile overrides, and per-issue backend overrides. Rate-limit auto-switch recovery still uses `switched_to_backend` as the final override.
- **Fence-aware sentinel detection**: `IsSentinelInputRequired` no longer triggers on `<!-- itervox:needs-input -->` markers inside fenced code blocks.
- **Stale-config dashboard banner** (T-26): when a `WORKFLOW.md` reload fails validation, the daemon keeps running on the previously-valid config, exponentially backs off retries (`200ms << attempt`, capped at 30s), and surfaces the failure both in the web dashboard header (`AppHeader` warning banner) and the TUI header (`⚠ CONFIG INVALID` line). Snapshot now carries a typed `configInvalid` field with `error`, `retryAttempt`, and `retryAt`.
- **SSE silence-watchdog poll fallback** (T-27): the dashboard's SSE hook now detects "open but silent" connections (corporate proxies that buffer SSE indefinitely without firing `onclose`) and switches to polling `/api/v1/state` after 30 s of silence, automatically resuming SSE-only mode when a message arrives.
- **`fatalExit(code)` helper** (T-33): every `os.Exit` site in `cmd/itervox/` now routes through a TTY-restoring helper that runs `stty sane` if stdin is a terminal before exiting, so any future post-`statusui.Run` exit path leaves the shell in cooked mode. A `make no-os-exit` CI guard (wired into `make verify`) plus a CLAUDE.md invariant prevent regressions.
- **`logging.Secret` `slog.LogValuer` + redacting handler** (T-29): a new `Secret` string subtype emits `***` instead of its value when used as an slog attribute (key is preserved for audit), and a `RedactingHandler` middleware scrubs Anthropic / Linear / GitHub PAT / `Authorization: Bearer …` patterns from any record's msg and string attrs before they reach the file sink. Wired around both slog defaults so any future regression that logs a token via plain string is silently redacted.
- **Typed `SettingsError` + `ServerErrorSchema`** (T-34): the dashboard's `useSettingsActions` now parses the server's `{error: {code, message, field?}}` body into a typed class, and `AutomationFormModal` pins server validation errors (e.g. `duplicate_automation_id`) directly to the matching form input via React-Hook-Form `setError` instead of surfacing them as a generic toast. Server-side `writeAutomationValidationError` now attaches a `field` discriminator so the client mapping is data-driven.
- **Configurable rate-limit error patterns** : new `agent.rate_limit_error_patterns` field in `WORKFLOW.md` (also `cfg.Agent.RateLimitErrorPatterns` on the runtime allowlist) lets operators override the built-in defaults (`rate_limit_exceeded`, `rate limit`, `429`, `quota`, `too many requests`) when their model provider returns a non-standard rate-limit string. Empty list falls back to defaults; back-compat preserved via `IsRateLimitFailure` while the new path goes through `IsRateLimitFailureWithPatterns`.
- **TTL-based auto-switch revert** : new `agent.switch_revert_hours` field (also `cfg.Agent.SwitchRevertHours` on the runtime allowlist; default `0` = disabled). When set, auto-applied profile/backend switches older than the TTL are dropped on each poll cycle by the new `RevertExpiredAutoSwitches(state, ttl, now)` helper, returning issues to their original profile and backend. Operator-set overrides survive — discriminated by the new `state.AutoSwitchedAt` map (parallel to `AutoSwitchedIdentifiers`), recorded at auto-switch time and cleared on success or TTL revert. Wired into `onTick` so revert work runs on the orchestrator's single goroutine.
- **`useDebouncedCommit<T>` shared hook** : new `web/src/hooks/useDebouncedCommit.ts` generic hook owns draft-state + commit-on-blur for settings inputs. `SwitchCapSection` (E switch cap and window-hours inputs) now uses it. Avoids `Object.prototype.toString` collision in destructure defaults by naming the option `serialize` instead of `toString`.
- **Shared `agenttest` scenario doubles** : new `internal/agent/agenttest/scenarios.go` package provides `SuccessRunner(sessionID)`, `FailRunner(failureText)` (returning `*CountingFailRunner` with atomic `CallCount() int64`), `RateLimitedFailRunner()` (pre-built failure text guaranteed to trip `IsRateLimitFailure`), and `InputRequiredRunner(sessionID, question)`. New tests in `scenarios_test.go` cover each helper. Existing per-test fakes stay for back-compat; new tests should adopt the shared doubles.
- **`TestCfgMuFieldAudit` meta-test** : new `internal/orchestrator/cfg_mu_audit_test.go` walks every non-test `.go` file in the orchestrator package via `go/parser`/`go/ast`, finds every `o.cfg.<X> = ...` assignment, and asserts the field path is in `AllowedMutableCfgFields`. New runtime-mutable cfg fields fail the build until the doc-comment in `orchestrator.go` and the allowlist are both updated. Delivers the typed-`MutableConfig` invariant (deferred refactor) at a fraction of the cost.

### Model catalog refresh (v0.2.0 closing-pass)

- **`itervox models <list|refresh>` CLI subcommand.** `models list` prints
  the current `agent.available_models` block from `WORKFLOW.md`. `models
  refresh` queries Anthropic `/v1/models` (with `ANTHROPIC_API_KEY`) and
  OpenAI `/v1/models` (with `OPENAI_API_KEY`), merges the result into
  WORKFLOW.md, and writes atomically. Flags: `--backend claude|codex|all`
  (default `all`), `--dry-run` (preview without writing), `--workflow PATH`
  (default `WORKFLOW.md`). Per-backend semantics: refreshed backends
  replace their list entirely; backends not named on the command line
  keep their previous entries untouched.
- **`POST /api/v1/settings/models/refresh` HTTP route.** Same logic as
  the CLI; body `{"backend": "claude"|"codex"|"all"}` (default `all`).
  Returns `{ok: true, models: {...}}` on success and triggers an SSE
  state refresh so the dashboard model picker reflects the new options
  without a reload. Non-orchestrator backends (in-memory quickstart,
  tests) return `501 not_implemented` pointing at the CLI subcommand.
- **Settings → Models card on the Agents page.** New `ModelsCard`
  component lists every backend's current models and exposes a single
  "Refresh from APIs" button (all backends) plus per-backend Refresh
  links. Toast on success / failure.

### Multi-daemon coexistence and operator diagnostics (v0.2.0 closing-pass)

- **Default `server.port` is now `0` (OS picks a free port).** When
  `server.port` is omitted from `WORKFLOW.md`, the daemon binds an
  OS-assigned free port instead of skipping the HTTP server. Two itervox
  daemons in two different repos now coexist out of the box. The actual
  bound URL is written to `.itervox/dashboard_url` and surfaced in
  `HEARTBEAT.md`, the startup banner, and `itervox doctor`. **Operators
  who explicitly pinned a port in WORKFLOW.md are unaffected**; an
  explicit port still binds strictly.
- **`EADDRINUSE` is now a fatal startup error.** When `server.port` is
  explicitly set to a port already in use, the daemon prints a structured
  diagnostic naming the holding process (via `lsof`), writes
  `.itervox/STARTUP_ERROR.md`, and exits non-zero. The previous behaviour
  silently auto-shifted to the next free port, mismatching the Vite dev
  proxy / `dashboard_url` / `HEARTBEAT.md` contract. The hint in the error
  recommends `server.port: 0` for "two daemons in parallel" workflows.
- **`itervox init --update --server-port <n>` migration flag.** Rewrites
  `server.port` in an existing `WORKFLOW.md` (back-compat: omit the flag
  to leave the field alone). `0` is the recommended value for new
  multi-daemon setups; explicit ports still work for single-daemon
  setups that pin a known URL.
- **`itervox init` scaffolds `server.port: 0`** by default in fresh
  `WORKFLOW.md` files. Operators who want a fixed port edit the value
  after init.
- **Pidfile + `HEARTBEAT.md` cleanup on shutdown.** `.itervox/daemon.pid`,
  `.itervox/HEARTBEAT.md`, and `.itervox/dashboard_url` are removed
  together on SIGINT/SIGTERM so a clean exit leaves no stale liveness
  state. Doctor's HEARTBEAT-stale check catches the crashed-without-
  cleanup case.
- **Refuse-to-start when a previous daemon is alive.** Startup reads the
  pidfile; if the recorded PID is still alive, the daemon refuses to
  start with `itervox: another daemon already running for this WORKFLOW.md
  (pid=<n>, recorded_workflow=<path>)`. Stale pidfiles (PID dead) are
  silently overwritten with a slog notice. Prevents the "two daemons
  fight over `.itervox/automation_queue.json`" symptom triad.
- **`.itervox/dashboard_url` file + Vite auto-discovery.** Daemon writes
  the actual bound URL atomically after the listener binds. `vite.config.ts`
  walks up from `process.cwd()` looking for the file and uses it as the
  proxy target. `ITERVOX_PROXY_TARGET` env var overrides for CI.
  Fallback to `http://127.0.0.1:8090` keeps fresh-checkout `pnpm dev`
  working.
- **`itervox doctor` mitigation checks.** New `DoctorReport` fields:
  - `PortInUseWarning`: configured port held by a non-itervox process.
  - `HeartbeatStaleWarning`: `HEARTBEAT.md` exists but the recorded
    daemon PID is dead.
  - `DashboardURL` + `DashboardURLReachable`: probe `<dashboard_url>/api/v1/health`.
  - `ItervoxBinEnv`: read from the env so the drift report can
    downgrade severity when the operator has pinned `ITERVOX_BIN`.
  - `--clear-startup-error` flag for the "I already fixed it" workflow.
  - Binary-drift severity heuristic: only the "dev vs stable" case
    (one binary reports `version=dev`, the other a release tag) is
    ERROR; two stable installs with different SHAs render as `info:`.
- **Restart loop bails on fatal startup errors.** A new
  `fatalStartupError` sentinel marks "operator must intervene" failures
  (e.g. configured port in use). The outer `run()` restart loop exits
  via `fatalExit(1)` instead of retrying every second.

### Added (v0.2.0 batched closing-pass items)

- **Built-in `merge-bot` profile (P0-A)**: `internal/profiles/builtin/merge-bot/{SOUL.md,INSTRUCTIONS.md}` ships as the first embedded profile. Operators reference it from `WORKFLOW.md` via `agent.profiles.merge-bot: {}` and the daemon resolves the SOUL/INSTRUCTIONS content from the embedded registry (`internal/profiles/registry.go`). `itervox init` and `itervox init --update` scaffold these files to disk for version control; operator edits on disk override the embedded defaults via the existing `soul_file` / `instructions_file` precedence.
- **`merge_pr` agent action (P0-C)**: new `POST /api/v1/agent-actions/{identifier}/merge_pr` route backed by `internal/server/merge_pr.go::MergePRGate` runs `gh pr view` / `gh pr checks --required` / `gh pr merge` with required-check + block-label + mergeable guards. New config knobs `agent.merge_strategy` (default `squash`) and `agent.merge_block_labels` (default `["needs-human","migration","auth","feature-flag","breaking"]`). Process-level dedup ledger prevents double-merge across re-fire.
- **`tracker_comment_added.filter.body_contains` / `body_regex` (P0-B)**: pre-filter on the comment body so a merge-bot only wakes on its trigger phrase. Wired via `commentBodyMatchesFilter` + `FilterTrackerCommentAutomationsByBody`; substring matching is case-insensitive; both keys AND-combine when present.
- **`STARTUP_ERROR.md` + `itervox doctor` subcommand (P0-D / P0-G)**: on startup config-load failure the daemon writes `.itervox/STARTUP_ERROR.md` with the YAML/schema diagnostic before exiting, and clears it on the next healthy boot. `itervox doctor` reports schema validity, daemon binary path vs. `which itervox` drift (warning/error), built-in profile list, and last STARTUP_ERROR.md.
- **`itervox action comment-pr` and `itervox action merge-pr` CLI subcommands (P0-E)**: structured-findings comment and guarded merge invocation. Menu lines surfaced by `buildAgentActionContext`.
- **`ITERVOX_BIN` env var threading (P0-F)**: daemon sets `ITERVOX_BIN` at startup and `internal/workspace/hooks.go::hookEnv` allowlists it into hook subprocess env, eliminating the stale-system-binary class of "funky automation" bugs.
- **`.itervox/bin/itervox` symlink (P0-H)**: refreshed on every daemon boot to point at `os.Executable()`. Operators can prepend `.itervox/bin/` to PATH for stable per-repo invocation.
- **`pr_merged` automation trigger (P1)**: native merge-side signal, compiled into the automation registry, with per-(issue, PR URL, automation ID) dedup ledger and dispatch/drop counters on `State`.
- **`pause_dispatch_when_any_in_state` config knob (P1)**: when ANY tracked issue is in a listed state (case-insensitive), no new dispatch begins. Use case: pause Todo dispatch while any issue is "In Review" so PRs queue and merge before the next start. Empty (default) disables the guard.
- **Evals suite foundation (P1.a)**: new `internal/evals` package with `Scenario`, `Recording`, deterministic + structural judges, `Report`, and a `make evals-fast` target chained into `make verify`. Six merge-bot scenarios ship (`green-ci-approval`, `red-ci-approval`, `green-ci-block-label`, `wrong-marker-phrase`, `multiple-matching-prs`, `no-matching-pr`) plus a two-scenario reviewer suite locking the producer side of the merge handshake (`/ai-approved` marker on approve; numbered failures + `move_state` on reject). Recordings are hand-authored behavioral contracts pending live-recording mode — see `internal/evals/fixtures/README.md` for the provenance caveat. Stale recordings (older than their `input.yaml`/`SOUL.md`/`INSTRUCTIONS.md` sources) are flagged in the report.
- **`agent.sort.prefer_high_outdegree` dispatch tiebreaker (P2)**: when enabled, ranks candidate issues that block more dependent siblings ahead of others, between the priority and createdAt tiers of the existing comparator.
- **`automation_drops_self_reentry_total` counter on `State` (codex-B1)**: monotonically increments every time an `input_required` automation dispatch is suppressed because the prior worker was itself an automation-driven run. Surfaced for the dashboard's live-ops strip.
- **Linear `trashed` filtering (codex-B9)**: all candidate-issue queries (`QueryCandidateIssues`, `QueryCandidateIssuesAll`, `QueryCandidateIssuesNoProject`, `QueryIssueDetail`) request the `trashed` field; `normalizeIssue` drops issues marked trashed so a Linear archive never produces a dispatch.
- **Queue persistence v2 envelope (todolist4 A.2)**: on-disk `.itervox/automation_queue.json` is now wrapped in `{schema_version, daemon_instance_id, payload, payload_sha256}`. Mismatched envelopes (schema or checksum) move to `.itervox/automation_queue.json.quarantine` instead of being silently consumed. Legacy v1 raw payload files are still read via fallback for back-compat.
- **`agent.transport_error_patterns` transport-failure classifier (todolist4 A.4)**: classify exhausted-retry exits whose error message matches a configurable substring list (default `["stream disconnected","connection reset","i/o timeout"]`) as transient-transport failures instead of generic failures. An internal `TransportFailureCount` counter is tracked in orchestrator state; surfacing it in the snapshot/dashboard is planned for a follow-up release.
- **`itervox init --template <name>` flag (todolist4 A.1)**: accepts `minimal` (default), `full`, `rate-limit-fallback`, `pr-review`, `daily-qa`. Unknown values exit non-zero with the accepted list. Registry lives at `internal/templates/scaffold/`. In v0.2.0 every preset name emits the same default scaffold; preset-specific scaffolds land in a future release.
- **`WORKFLOW.md.bak` stale-detection (todolist4 A.3)**: `init --update` now refuses to overwrite an existing `.bak` unless `--force` is passed.
- **Janitor `issue_terminal` / `absent_from_tracker` status-history reasons (codex-B2 / B9)**: terminal-state pruning and absent-tracker pruning now emit a status-history row with `source: janitor` and a structured `reason` tag so the per-issue timeline explains the disappearance.
- **`automation_dispatches_pr_opened_total` / `automation_dropped_pr_opened_dedup_total` / `automation_dispatches_pr_merged_total` / `automation_dropped_pr_merged_dedup_total` counters (codex-B4)**: surface pr-side dispatch telemetry for dashboards and the live-ops strip.
- **Logs page `'automation'` filter chip (codex-B5)**: the type-driven filter array `FILTER_CHIPS` now includes `'automation'`, alongside the existing dedicated `chip-automation-only` toggle for the per-line prefix filter.
- **Duplicate-key dedup on Timeline / Automation Activity rows (codex-B6)**: list keys always carry a `live`/`done`/`running` discriminator so a live row briefly coexisting with the same `sessionId` in history does not produce a React duplicate-key warning.
- **AutomationQueueList search input renders for running rows (codex-B7)**: previously hidden when only running automations were present; now visible whenever queue OR running rows exist.
- **Structured `LastRejectedAutomationID` / `LastRejectedTrigger` / `LastRejectedIdentifier` on `AutomationQueueBackpressure` (todolist4 P2-2)**: parallel to the legacy colon-joined `LastRejectedReason`.

### Changed

- `schedules:` blocks in `WORKFLOW.md` are still parsed and upgraded to equivalent `cron` automations for back-compat, but now emit a runtime `slog.Warn` at startup so users on the upgrade path are aware the fallback is deprecated. Migrate to the `automations:` block; the legacy path will be removed in a future release.
- `itervox init`-generated `WORKFLOW.md` now includes a commented-out `automations:` starter example so new projects can discover the feature without leaving the file.
- **Automations observability**: on startup the daemon now logs a single summary line with the outcome of automation compilation — total configured, registered, dropped, and counts per trigger type. Input-required dispatches also emit `slog.Debug` lines when automations are registered but none match (typical cause: the configured `input_context_regex` did not match the agent's question text), turning a previously-invisible “why didn't my automation fire?” case into a single `-verbose` run to diagnose.
- **Poll-event automation dispatch** (tracker-comment, issue-entered-state, issue-moved-to-backlog) now logs the queued count at info level and the dropped count at debug level when the events channel is full, mirroring the cron dispatcher's observability.
- **Suggested automation card** now uses an exhaustive trigger-label map, so every trigger type surfaced by a future template shows the correct label (previously any trigger other than `cron` / `input_required` silently fell back to displaying “Cron”).
- **Profile action choices now come from daemon state**: `/api/v1/state` exposes the backend-supported agent action list, and the dashboard filters profile action checkboxes/templates against it instead of relying only on frontend constants.
- Automations editor: the “Why states and labels use suggestions” info block no longer duplicates the filter-label helper sentence.
- Input-required tracker comments are human-facing again: Itervox now persists pending resume metadata locally instead of embedding session, host, backend, or command details in tracker comments.
- Snapshot and dashboard state now distinguish `pending_input_resume` from `input_required`, so “reply received, waiting to resume” is surfaced as a separate live state instead of being inferred as plain waiting-for-input.
- Reviewer settings are now validated consistently: `agent.auto_review` requires `reviewer_profile`. The legacy `auto_clear` + `auto_review` conflict was lifted in this same release (see **Changed (breaking)** above) — both can now be enabled together because `auto_clear` only fires on terminal tracker states.
- The TUI now surfaces input-related issues directly, including both `input_required` and `pending_input_resume`, while keeping replies in the tracker or web dashboard.
- **`--demo` flag removed.** Pre-v0.2 breaking change; replaced by the quickstart template (`cp templates/quickstart/WORKFLOW.md . && itervox`). Config validator now accepts `tracker.kind: memory` and skips the `api_key` requirement for it.
- **Persist-then-mutate for `SetWorkers` / `BumpWorkers`**: both now write to WORKFLOW.md before mutating runtime state, returning 500 on persist failure instead of silently reverting on restart.
- **Settings validation toasts** now surface the server's structured error message (e.g. `”Failed to update automations: invalid cron expression: …”`) instead of a generic label.
- **Bearer token no longer logged to disk**: the tokenized dashboard URL is now emitted via a stderr-only logger, bypassing the rotating log file fanout.
- **Reload-loop log spam eliminated**: `context.Canceled` (clean WORKFLOW.md reload) now logs at Debug instead of Warn, and YAML validation failures on reload keep the last good config instead of killing the daemon.
- **`copyStringMap` / `copyStructMap` / `copyPausedSessionsMap` / `copyInputRequiredMap` / `copyPendingInputResumeMap` replaced with `maps.Clone`** across `snapshot.go` and `event_loop.go`.
- **Reviewer-injected profile overrides are now cleaned up on terminal** — the orchestrator tracks which `issueProfiles` entries were injected by the reviewer and clears only those on `TerminalSucceeded`, preserving user-set overrides.
- **TTY panic safety net**: a `defer recover()` at the top of `main()` runs `stty sane` if a panic surfaces while stdin is a terminal, then re-raises — prevents leaving the terminal in raw mode after an unhandled panic.
- **Dead Windows TTY stub removed**: `internal/statusui/tty_guard_windows.go` deleted (no Windows support).
- **`loadDotEnv` security visibility**: when a `.itervox/.env` file sets sensitive keys (`ITERVOX_API_TOKEN`, `LINEAR_API_KEY`, `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`), the daemon now emits a single `slog.Info` naming the keys (never values) that were configured. Non-sensitive keys remain at Debug level.
- **Automation TOCTOU re-check**: the `EventDispatchAutomation` handler now re-checks `state.InputRequiredIssues` before dispatching, preventing a race where an issue enters `input_required` between queue and execution.
- **workerCancels reconcile leak eliminated**: `cancelAndCleanupWorker` atomically cancels the context and removes the map entry, so reconcile-driven cancellations no longer leak cancel funcs when the event channel is saturated.
- **`cmd/itervox/main.go` extracted** (T-24): the `runInit` cluster (`repoInfo`/`detectedStack` types, `scanRepo`, `parseGitRemote`, `detectStacks`, `detectNodeCommands`, `generateWorkflow`, `runInit`) moved to a sibling `init.go`. Size-budget caps tightened.
- **`internal/statusui/model.go` extracted** (T-24): the `keyMap` type, `defaultKeys`, `ShortHelp`, `FullHelp` moved to a sibling `keys.go`.
- **Persist-then-mutate convention guard** (T-25): an AST-based test in `cmd/itervox/adapter_convention_test.go` walks every `*orchestratorAdapter` setter and asserts the `workflow.Patch*` persist call appears before the `a.orch.Set*Cfg` mutation, preventing future setters from silently regressing the lost-update guarantee.
- **Unified automation eligibility check** (T-35): the watcher pre-filter (`shouldSkipAutomatedIssue`) and the event-loop TOCTOU re-check both delegate to the new `orchestrator.IneligibleReasonForAutomation` exported helper. Adding a new dispatch guard is now a one-place edit; the parity gap that previously had `discarding`/`no_slots`/`per_state_limit`/`blocked_by` only on the event-loop side is closed.
- **Rollback-on-mutate-failure audit** (T-36): every multi-step setter in `*orchestratorAdapter` now carries an inline comment documenting why no rollback is needed (orch setter is infallible) — preserving the audit trail for future contributors when validation errors get added.
- **`ValidateAutomations` rejects disabled-automation-pointing-at-unknown-profile** (T-42): the unknown-profile check now fires regardless of `entry.Enabled`. Previously a disabled rule pointing at a deleted profile passed validation at startup and only crashed dispatch the moment a user re-enabled it from the dashboard. The disabled-profile (vs unknown) check stays scoped to enabled automations to preserve `UpsertProfile`'s cascade semantics.
- **`PatchIntField` under `editMu`** (T-46): the `internal/workflow/PatchIntField` helper now grabs the same per-path mutex as the other `Patch*` helpers via `lockForPath`. Concurrent calls (HTTP `SetWorkers` + HTTP `BumpWorkers` + TUI `AdjustWorkers`) can no longer race on the read-modify-write cycle. `TestPatchIntFieldConcurrent` (10 parallel writers) verifies the final file always contains exactly one of the written values.
- **Goroutines posting tracker comments now tracked by `commentWg`** (T-44): the two `go func(...)` blocks in `event_loop.go` that post user input and input-required questions to Linear/GitHub are now `Add(1)`/`defer Done()`-tracked. `Orchestrator.Run` waits on the wait-group before returning, so a graceful shutdown no longer drops a comment that the tracker API was about to persist.
- **`runClear` refuses to delete from system / home directories** (T-43): a new `unsafeWorkspaceRoot(root)` helper exits with a refusal message instead of recursively `os.RemoveAll`-ing under `/`, `/tmp`, `/var`, `/etc`, `/usr`, `/opt`, `/Users`, `/home`, `/root`, the user's home directory, or its parent. Mitigates a misconfigured `workspace.root: ~` that would otherwise wipe the user's home dir.
- **SSE sublog endpoint emits `event: error` on tracker-fetch failure** (T-45): the per-issue `/api/v1/issues/:id/sublogs/stream` handler now writes a structured `event: error\ndata: {code,message}\n\n` frame before returning when `FetchSubLogs` fails. Dashboard can distinguish a tracker error from a user-closed-tab disconnect.
- **SSE keepalive timer resets on real-event activity** : the `internal/server/handlers.go` SSE handler now calls `ticker.Reset(keepaliveInterval)` on every real `<-sub` send, so a busy stream no longer ALSO emits a keepalive ping every 25s. Halves outbound byte volume on heavy systems while still firing the keepalive within 25s of any quiet period.
- **Stricter Zod schemas at the SSE parse boundary** : removed `.default()` from `maxRetries`, `maxSwitchesPerIssuePerWindow`, and `switchWindowHours` in `web/src/types/schemas.ts`. A server bug that omits any of these three now fails loudly at the parse boundary instead of silently defaulting client-side. Test fixtures and `useItervoxSSE.test.ts` SSE message factories updated to supply the values.

### Fixed

- **Shutdown cancel-race no longer drops a user's queued `pending_input_resume` reply** (production data-durability fix). The event loop's `select` could non-deterministically pick a trailing `EventWorkerUpdate` after `ctx` cancellation; that event's progress flags cleared the `PendingInputResumes` entry, and the final `storeSnap` then persisted an awaiting-only file — so on the next daemon start there was no record the user had replied. Fixed by prioritising `ctx.Err()` with a pre-check at the top of the `Run()` select loop so trailing events cannot mutate state after cancel. Verified with 10× `-count` runs of the previously-flaky `TestProvideInputPendingResumeSurvivesRestartBeforeResumedTurnCompletes`.
- **Claude and Codex CLI invocation is now safe against prompts beginning with `-`**. Prompts that started with `-` (common for markdown-list prompt bodies, or any accidental YAML/CLI-looking content) previously tripped the agent CLI's argument parser — surfacing as `error: unknown option '- …'` and putting the issue into a permanent retry loop. Itervox now defensively prepends a single space when necessary; the agent trims it server-side, so the user-visible behavior is unchanged for legitimate prompts.
- Input-required state rehydration from the tracker (the fallback path when local `input_required.json` is lost to a daemon restart or file cleanup) now emits a `slog.Warn` making the downgrade visible. Resume will start a fresh agent session in this case rather than `claude --resume <sid>` because the session ID was never persisted to the tracker.
- **`EventDispatchAutomation` now dispatches workers for issues in non-active states** (CRIT-3 regression fix). Automation trigger types that intentionally target backlog or otherwise-inactive issues (`issue_moved_to_backlog`, non-active `issue_entered_state`, `tracker_comment_added` on a backlog issue) were being silently dropped by the shared `IneligibleReason` check's `isActiveState` gate. Introduced `ineligibleReasonForAutomation` that retains every other guard (terminal, paused, discarding, input-required, pending-resume, running, claimed, no-slots, per-state-limit, blocked-by) but omits the active-state requirement, used from the `EventDispatchAutomation` branch only. Reconcile-loop dispatch keeps the original `IneligibleReason` unchanged.
- **`SetAutomations` now updates in-memory config** so settings-UI edits take effect on the automations goroutine's next 15-second tick, rather than silently appearing successful until the next full daemon restart (CRIT-1 regression fix). Added `AutomationsCfg()` / `SetAutomationsCfg()` to the orchestrator under `cfgMu`, wired the adapter to call the setter before `PatchAutomationsBlock`, and refactored `startAutomations` to recompile per tick. `cfg.Automations` is now on the documented `cfgMu` guard list.
- **`cfg.Tracker.ActiveStates`, `TerminalStates`, and `CompletionState` are no longer read without a lock** by the automations goroutine (CRIT-2 data-race fix). `runOnce` now snapshots the trio via `orch.TrackerStatesCfg()` once per tick and passes the copies down to `cronAutomationFetchStates` and `automationPollStates`, so HTTP-handler updates to tracker states can no longer race the automations reader.
- **Polled-event automation dispatches now propagate `policy.auto_resume`** (previously the `AutomationDispatch` struct was built without the field in the polled path, so only cron and input-required automations saw the intended auto-resume behaviour).
- **AutomationsCard duplicate-ID guard** now shows an explicit error and blocks the save instead of letting the UI submit a list with colliding IDs.
- **Automation ID input in `AutomationFormModal`** now has a proper `htmlFor`/`id` label-input association (WCAG 1.3.1).
- **Timezone input in `AutomationEditorFields`** also has a `htmlFor`/`id` pair for the IANA-zone typeahead combobox.
- **Automation setter race-safety for hot-reload**: `SetInputRequiredAutomations` / `SetRunFailedAutomations` are now guarded by a dedicated `automationsMu` mutex with matching `snap*()` helpers on the read side, so the automations goroutine can re-register rules on each tick while the event loop dispatches concurrently.
- **`ProfileEditorFields` no longer calls `setState` synchronously inside a `useEffect`**. The auto-open-advanced behaviour now uses the standard “adjust state while rendering” pattern (tracked previous-value state), eliminating the cascading-render warning flagged by the React Compiler lint rule.
- **Settings cards remove the TypeScript `unknown[]` payload on `setAutomations`** — `useSettingsActions.ts` now types it as `AutomationDef[]`, restoring compile-time type safety at call sites.
- **All prompts passed to agent CLIs are now consistent across direct and shell execution paths**: `buildDirectArgs` / `buildShellCmd` (Claude) and `buildCodexDirectArgs` / `buildCodexShellCmd` (Codex) all route through the same `safePromptArg` helper.
- **Multiple input-required integration tests are now stable under parallel test load**: `TestProvideInputPendingResumeSurvivesRestartBeforeResumedTurnCompletes`, `TestInputRequiredPersistenceResumesAfterTrackerReplyWithoutTrackerMetadata`, and `TestRecoveredTrackerReplySkipsSameAuthorCommentsAndUsesExactQuestionCommentID` now wait for each `orchestrator.Run` goroutine to fully exit before `t.TempDir`'s `os.RemoveAll` cleanup runs, eliminating “directory not empty” flakes observed under high-parallelism test execution.
- Input-required resume now continues the existing Claude or Codex session with the actual user reply, instead of re-entering a fresh-dispatch path.
- Input-required resume now reuses the existing workspace and skips setup steps that could reset repo state, including PR detection, branch checkout, and `before_run`.
- Input-required resume now persists the exact tracker question comment ID and author identity locally, so tracker replies and dashboard replies can both resume the same saved session, backend, command, branch, profile, and SSH host after restart.
- Input-required resume now reruns fresh-dispatch setup only when the original workspace is gone and had to be recreated, instead of resuming into an uninitialized checkout.
- Pending input replies now survive early resumed-worker failures until the resumed run actually makes progress, instead of being discarded on any worker exit.
- Input-required persistence now writes atomically, reducing the chance of losing waiting/pending resume state on interruption.
- Successful turns that end with a real blocking question or confirmation request now enter `input_required` via a deterministic fallback detector, even when the agent omitted the explicit `<!-- itervox:needs-input -->` marker.
- Codex sessions that request user input now enter `input_required` correctly instead of falling through the single-turn success path.
- Resume command resolution now preserves Codex backends when the saved entry has no explicit command, including backend-only profile setups.
- Claude resume invocations now append `-p <reply>` when a resumed turn needs to send fresh user input. Closes [#30](https://github.com/vnovick/itervox/issues/30): `claude --resume` without a prompt was silently permissive in Claude Code ≤ 2.1.118 and now errors with `"No deferred tool marker found in the resumed session"` on 2.1.119+. The `buildShellCmd` / `buildDirectArgs` paths in `internal/agent/claude.go` now always pass `-p <prompt>` together with `--resume <sessionID>` when there is reply text to send.
- **TUI no longer suspends with `zsh: suspended (tty output)` on startup**. `internal/statusui/statusui.go::Run` now ignores `SIGTTOU` before `tea.NewProgram`, and a new `checkForegroundTTYOwnershipWithRetry` (20× / 25ms) wins the startup race against the parent shell's `tcsetpgrp(2)`. Without these, `term.MakeRaw`'s `tcsetattr` could land before the shell finished handing over the foreground process group, causing the kernel to raise `SIGTTOU` and the shell to print `zsh: suspended (tty output)` while the HTTP server, orchestrator, and dashboard kept running. Closes [#31](https://github.com/vnovick/itervox/issues/31). (Distinct from the earlier `SIGTTOU`/`SIGTTIN` ignore for browser-spawn from the TUI; that fix is unrelated.)
- Agent command resolution now recognizes zsh alias output in addition to direct paths and bash-style aliases.
- The SSE/query invalidation bridge now reacts when an issue transitions from `input_required` to `pending_input_resume`, avoiding stale issue detail and board views after a reply is accepted.
- The dashboard header and logs view now reflect pending resumes explicitly instead of showing `idle`, and the logs sidebar preserves visible live issues even before the first log line exists.
- **Mobile dashboard responsive hardening**: v0.2.0 queue/status surfaces now keep the queue-full alert readable, avoid document-level horizontal overflow at 390px, keep automation detail buttons at 36px touch height, and constrain the Deps graph and detail panels inside mobile-safe containers.
- The logs view restores branch/profile/host context for selected issues.
- The reviewer settings card now preserves pending edits when a save fails, and workspace-reset actions refresh snapshot/log state on success.
- Opening a browser from the TUI now isolates the child process and ignores `SIGTTOU`/`SIGTTIN`, preventing terminal freezes after `open`/`xdg-open`.

### Tests

- Added end-to-end orchestrator coverage for input-required resume, including workspace continuity, saved session reuse, and `before_run` suppression.
- Added restart/recovery coverage for locally persisted input-required sessions, including tracker replies detected after restart and exact saved session/backend/host reuse.
- Added coverage for pending input resumes that survive retryable worker failures until progress is observed.
- Added Codex-specific resume regression coverage to assert saved session ID reuse, exact user-reply forwarding, and preserved command selection.
- Added coverage for plain-English blocking questions so successful turns that ask the user to choose or confirm are queued for `input_required`.
- Added frontend regression coverage for pending-resume snapshot rows, snapshot invalidation fingerprints, app-header state, and logs-page rendering.
- Tightened manual pause/resume tests to assert the resumed prompt, and added direct argument coverage for Claude and Codex resume flows.
- **Orchestrator end-to-end test for the automation dispatch pipeline**: `TestOrchestratorAutomationDispatchPipeline` exercises the full path from `DispatchAutomation` through the event channel, event loop, `ineligibleReasonForAutomation`, `startAutomationRun`, and into the worker — using a backlog-state issue to double as the CRIT-3 regression guard.
- **Whitebox tests for the automation dispatch eligibility split** (`dispatch_automation_test.go`): covers `ineligibleReasonForAutomation` accepting backlog states, still rejecting terminal states, agreement with `IneligibleReason` on shared guards, and a race-safety test for `SetInputRequiredAutomations` against concurrent `snapInputRequiredAutomations` reads.
- **`internal/agentactions` unit tests** covering the `ttl <= 0 → 1 hour` fallback, nil-receiver safety on `Revoke` / `Validate`, `missing_token` / `unknown_token` / `issue_mismatch` / `action_not_allowed` / `expired_token` error strings, opportunistic deletion of expired grants on read, and the allowed-actions clone-and-sort invariant that prevents caller-side mutation of stored grants.
- **`internal/schedule` unit tests** covering cron OR-semantics between day-of-month and day-of-week, invalid expressions (too few / too many fields, out-of-range minute / hour / month / day / weekday, descending ranges like `5-3`, zero-step `*/0`), step and range-with-step expressions, comma-list expressions, and zero-value-Expression-matches-nothing as a pinned invariant.
- **`AutomationsCard` frontend mutation tests**: duplicate-ID guard surfaces the error banner and blocks `onSave`; successful save surfaces the success banner. Assertions use named message constants imported from `automationMessages.ts` rather than hard-coded copy, so future message edits stay in sync.
- **`timezones.ts` module tests** covering memoization (same reference on second call), frozen result, locale-ascending sort order, and the fallback path exercised by deleting `Intl.supportedValuesOf` before module import.
- **`useSettingsPageData` hook tests** no longer trip the `@typescript-eslint/no-unnecessary-condition` rule on `profileDefs[...]` accesses — switched to `in`-operator membership checks that survive even under `noUncheckedIndexedAccess` being off.
- **6 ESLint errors closed across Settings page files** (`AutomationsCard`, `ProfilesCard`, `AutomationRow`, `automationForm`, `ProfileEditorFields`, `useSettingsPageData`) that previously blocked `pnpm lint` / CI.
- **Migrated `react-hook-form` `watch()` calls to `useWatch`** in `AutomationFormModal`, `AddSSHHostModal`, `TrackerStatesCard`, and `ProfileFormModal`, reducing the `react-hooks/incompatible-library` lint-warning count from 9 to 3 and improving React Compiler memoization eligibility.
- **Stale test filename renamed**: `ScheduleEditorFields.test.tsx` → `AutomationEditorFields.test.tsx`. The file already tested `AutomationEditorFields`; only the name lagged the component rename.
- **Dead code removed**: `markAutomationComment` wrapper (never called by any production path — `tracker.MarkManagedComment` is the real entry point); two copies of `containsFold` (collapsed to inline `slices.ContainsFunc` + `strings.EqualFold`); `sort.Strings` / `sort.Slice` calls in `internal/orchestrator/event_loop.go`, `worker.go`, and `cmd/itervox/automations.go` replaced with `slices.Sort` / `slices.SortFunc`.
- **Current-functionality QA baseline**: route-mocked Playwright smoke (`make qa-current-ui`), real-daemon smoke (`make qa-daemon`), and the combined `make qa-current` gate now protect the existing dashboard before UI-overhaul work. Added the repeatable `.claude/skills/current-ui-qa/SKILL.md` exploratory QA skill that drives both lanes and captures qualitative issues automated tests cannot.
- **Atomic write tests** (`internal/atomicfs`): happy path, permission preservation, no leftover temps, and read-only-dir failure leaving original untouched.
- **Single-write cascade tests**: mutators run in order and write once, error leaves file untouched, concurrent edits serialize, rename atomicity on write failure.
- **Persist-then-mutate tests**: `SetWorkers` and `BumpWorkers` return 500 on persist failure.
- **AuthGate URL-token race test**: verifies `?token=X` in the URL takes precedence over a stale stored token on initial mount.
- **Quickstart template tests**: `TestQuickstartTemplate_HasRequiredFields` (parses + validates the embedded template), `TestQuickstartWorkflow_DaemonStartsAndServesHTTP` (loads template, builds memory tracker).
- **workerCancels leak tests** (`cleanup_test.go`): cancel-and-delete atomicity, no-op for unknown identifiers, stress test for zero-leak under saturation.
- **Reviewer-injected override cleanup test**: confirms only reviewer-injected profile overrides are cleared on terminal, user-set overrides survive.
- **Fence-aware sentinel tests**: backtick fence, tilde fence, language-tagged fence (all return false), and “after a closed fence” (still triggers).
- **Automation TOCTOU tests**: cron automation skipped when `input_required` arrived after queue; `input_required`-typed automations bypass the gate.
- **SSE Last-Event-ID tests**: resume from cursor skips earlier events; stale cursor replays from start.
- **Settings validation toast tests** (`useSettingsActions.extractServerMessage.test.ts`): structured-JSON, plain-text, missing-message, non-string-message, body-not-consumed-by-clone.
- **Logbuffer truncation tests**: 1 MiB line truncated to ≤64 KiB with marker, small lines pass through unchanged.
- **Sensitive dotenv tests**: `ITERVOX_API_TOKEN` configured from `.env` emits Info naming the key (never the value); non-sensitive-only load stays at Debug.
- **Reload-loop tests**: `context.Canceled` classified as clean reload; wrapped `context.Canceled` also clean.
- **Dispatch-resolve tests**: 5 cases covering all-defaults, backend override, profile command, profile backend, per-issue override.
- **Size-budget CI guard** wired into `make verify`.

### Security

- Bearer token is no longer written to the rotating log file at `~/.itervox/logs/`. The tokenized dashboard URL now goes through a stderr-only logger that bypasses the file sink.
- `loadDotEnv` now emits a single structured Info log naming sensitive keys configured from `.itervox/.env` (never their values), giving operators visibility into which secrets are file-sourced.
- **SSH host-key checking now defaults to `accept-new` (TOFU)** instead of `no` (T-32). On first contact with a new SSH worker host the daemon pins the key in `known_hosts`; subsequent connections that present a different key are rejected with a clear `ssh: host key verification failed` instead of being silently MITM'd. Per-host overrides are configurable via `agent.ssh_strict_host_checking` (default for all hosts) and `agent.ssh_strict_host_by_host` (`{host: mode}` map) in `WORKFLOW.md`. Modes: `yes`, `no`, `ask`, `accept-new`, `off`. Closes F-NEW-F.
- **Defense-in-depth secret redaction at the log sink** (T-29). A new `internal/logging.RedactingHandler` middleware wrapping the file fanout scrubs Anthropic / Linear / GitHub PAT / `Authorization: Bearer …` patterns from any record that reaches `~/.itervox/logs/`. Pairs with the new `logging.Secret` `slog.LogValuer` for the structured-attr path. Stderr-only emits (the dashboard-token URL) are deliberately left unwrapped so the operator can still copy the URL once on startup.

### Documentation

- Documented the updated human-input contract in the README, generated `WORKFLOW.md` guidance, and site docs: the explicit `<!-- itervox:needs-input -->` marker remains preferred, with the deterministic fallback acting as backup behavior for plain-English blocking questions and its English-oriented limitation now called out explicitly.
- Documented the v0.2.0 `auto_review` / `workspace.auto_clear` semantics across the README, generated workflow template comments, and site docs: the two settings now coexist (`auto_clear` fires only on terminal tracker states, deferred behind any pending reviewer), and `auto_review` still requires `reviewer_profile`. Stale "mutually exclusive" / "cannot be combined" copy was removed from every public surface as part of the v0.2.0 doc reconciliation.
- **Full `automations:` reference added to both `docs/configuration.md` and `site/src/content/docs/configuration.mdx`**: every trigger type, filter field, policy option, and the legacy `schedules:` deprecation subsection. The site guide `site/src/content/docs/guides/automations.mdx` now also documents the IANA timezone typeahead that the Settings UI offers for `cron` triggers.
- **API references refreshed across both repo docs and the docs site**: authentication semantics now match the secure-by-default non-loopback behavior, typed error envelopes are documented, SSE `Last-Event-ID` resume and sublog `event: error` frames are covered, and the newer surfaces (`/settings/automations`, `/settings/profiles`, `/settings/models`, `/settings/reviewer`, `/settings/inline-input`, `/agent-actions/*`, `PATCH /issues/{id}/state`, `POST /refresh`, `DELETE /workspaces`) are documented with current request/response shapes.
- **README gained a “Remote access & bearer-token auth” section** before the SSH section, explaining the auto-generated token, the login-screen capture of `?token=…` from URL, `sessionStorage` / `localStorage` persistence, and the `allow_unauthenticated_lan` opt-out. Cross-links to the site remote-access guide.
- **Agent-profile / automation docs now explain daemon-backed action permissions** (`allowed_actions`, `create_issue_state`) and the modern reviewer-profile flow, including the `auto_review` guardrails and the short-lived action-grant model used by automation helpers and reviewer/comment flows.
- **`domain.Issue.Comments` now documents its ascending-`CreatedAt` ordering contract** so future tracker adapters know they must sort before returning — preventing silent breakage of `latestAutomationComment` which takes the last element as “newest”.
- **`internal/agentactions` package-level and exported-symbol doc comments** covering `Grant`, `Store`, `NewStore`, `Issue`, `Revoke`, and `Validate`, including the previously-undocumented `ttl <= 0 → 1 hour` footgun.
- **`internal/schedule` package-level and exported-symbol doc comments** on `Expression`, `Parse`, and `Matches`, making the 5-field cron syntax and the day-of-month / day-of-week OR semantics explicit for future callers.
- **`internal/orchestrator/automation.go` exported type doc comments** on `AutomationTriggerContext`, `AutomationDispatch`, `InputRequiredAutomation`, and `RunFailedAutomation`, explaining which fields are populated for which trigger types and the runtime invariant that automation dispatch targets may be in non-active states.
- **Orphan GET handlers documented as API-only** (`GET /settings/profiles`, `GET /settings/models`, `GET /settings/reviewer`) — dashboard reads via the `/state` snapshot; these endpoints are exposed for non-web clients.
- **Manual release checklist** covering auth-gate first-run, server-down recovery, automation CRUD validation, profile-delete cascade, and a 30-minute single-page releasability smoke. Maintained by release engineering and run before every tag.

### Conformance hardening — adversarial-audit final pass (2026-07-05/06)

Before tagging, an adversarial audit verified Itervox against the [Orchestrated Coding spec](https://github.com/vnovick/orchestrated-coding) it claims L3 conformance with (one auditor per subsystem, refute-first verification of every finding, per-fix regression tests run under `-race`). The user-facing changes:

#### Changed (behavior)

- **`merge_pr` refuses unarmed gates.** On a repository with zero required checks, `gh pr checks --required` passes vacuously — "a gate that can never fail is not a gate" (spec F3). The merge action now refuses with reason `unarmed_gate` and an actionable message. Opt out per project via the new `agent.allow_unchecked_merge: true` (default `false`), which merges with a loud warning instead. Check-refusal details are no longer empty: gh's stderr diagnostic is captured into the `checks_failed:`/`unarmed_gate:` reason.
- **`merge_pr` no longer passes `--delete-branch`.** gh's post-merge *local* branch delete fails when the PR branch is checked out in an Itervox worktree, turning a successful merge into an error (dedup unrecorded, `pr_merged` automations lost). The merge now runs without the flag; the remote branch is deleted best-effort afterwards, and local branches are cleaned by workspace auto-clear.
- **GitHub blocker lookups are strictly fail-safe (spec D4).** Any blocker fetch error — including 404, which GitHub also returns for permission loss and transferred issues — now leaves the dependency state *unknown*, keeping dependents blocked. Previously a 404 fabricated a `closed` state and could falsely unblock work. A genuinely deleted blocker now blocks its dependents until the reference is removed from the issue body; the Deps tab shows the row as `unknown`.
- **"Update the shared state" is now part of done (spec F2).** A worker that exits successfully without writing its handoff gets one **synthesized** from the session summary (explicitly marked as synthesized); if synthesis fails, the unit fails. Handoffs (agent-authored and synthesized) are committed on the issue branch with a pathspec-scoped `chore(itervox): record agent handoff` commit before the PR push, so the pipeline trail is durable — expect this commit to appear on issue branches.
- **Pause windows park automations instead of deleting them (spec D7).** While `agent.pause_dispatch_when_any_in_state` is active, triggered automations are now durably queued with reason `paused_by_state` and released when the window ends. Previously the drain pass deleted queued entries — including restart-persisted ones — and one-shot events were lost. Parked entries consume queue capacity during long pauses (bounded by `max_automation_queue_length`).
- **`blockers_resolved` no longer re-fires on tracker outages.** A transient blocker-fetch outage used to arm the unblock latch and fire the automation again (dispatching real agents) once the outage cleared — even for issues that were never blocked. The latch now arms only on *known-blocked* status and disarms after each fire.

#### Added

- **`hooks.after_run_required`** (default `false`): makes the `after_run` hook a per-unit completion gate — a worker whose final hook exits non-zero fails the unit instead of completing it (spec F3: a unit is not done on the agent's self-assessment alone).
- **`agent.allow_unchecked_merge`** (default `false`): explicit opt-in to merge on repositories with zero required checks (see the `unarmed_gate` change above).
- **Dependency-audit ledger persists across restarts.** Blocked/unblocked state (and the unblock-transition watermark) is now stored alongside the durable automation queue (additive payload — existing state files load unchanged), so a blocker that closes while the daemon is down still fires `blockers_resolved` after restart. Previously such transitions were lost forever.
- **Nested `.itervox/.gitignore` covers all runtime files** (`daemon.pid`, `dashboard_url`, `STARTUP_ERROR.md`, `*.db` added), self-heals on every daemon startup, and `itervox doctor` warns when required lines are missing.
- **README "Spec conformance" section** stating the L3 claim with evidence links, per the spec's claiming procedure; the docs site gained a matching [Orchestrated Coding guide](https://itervox.dev/guides/orchestrated-coding/).

#### Fixed

- **Daemon crash:** a worker-exit event arriving after a reconcile kill path had already removed the issue from the running set panicked the event loop (nil dereference) and took down the whole daemon.
- **Duplicate workers / retry-ceiling reset:** the same stall-kill race could release the retry's claim, letting a fresh dispatch start at attempt 0 while the orphaned retry later overwrote the running entry — two agents on one worktree.
- **Failed runs' handoffs were never marked `.partial.md` for named profiles** (the exit event carried no profile name), and a failed default-profile run could rename a *predecessor's completed* handoff to partial. Partial-marking now uses the live run entry's profile and start time.
- **Dashboard-blanking enum gap:** the `pr_merged` automation trigger was missing from the dashboard's schema — one `pr_merged` rule in `WORKFLOW.md` made every snapshot fail validation and blanked the entire dashboard (silently, in production builds). Added, plus a Go↔TS parity test covering *all* automation trigger types.
- **Settings silently revoked `merge_pr`:** saving any profile from the Settings page stripped the `merge_pr` action (three drifted copies of the action list, all missing it) — including from the built-in merge-bot profile. The Settings UI now derives actions from the canonical schema and can grant `merge_pr`.
- **Data race on runtime tracker-state edits:** the board-fetch and TUI dispatch paths read `tracker.active_states`/`terminal_states`/`completion_state` without the lock that guards the settings-API writer (proven with the race detector); all runtime readers now use the guarded accessor.
- **`blockers_resolved` was structurally dead on GitHub for issues outside `active_states`:** the audit's refresh paths never populated blocker states, so watched issues could never transition to unblocked. All GitHub fetch paths now populate blocker states.
- **Snapshot aliasing:** the PR-dispatch dedup ledgers were the only state maps not deep-copied into snapshots; `PRMergedDispatched` was also never initialized nor janitor-pruned.

## [0.1.3] — 2026-04-08

### Added

- Codex backend support, including JSONL stream parsing, backend-aware runner selection, and Codex log/event parity across the web UI and TUI.
- Named agent profiles with per-issue assignment, profile-aware settings payloads, backend visibility in session tables, and an agent queue view.
- Per-run log isolation using daemon app-session IDs and stamped agent session IDs, so Timeline expansions show only logs from the selected run.
- Git worktree mode, auto-clear workspace support, project-scoped default log directories, `.env` loading, and `itervox init --runner`.
- Fast single-issue fetch support, typed tracker/workflow errors, Claude CLI validation helpers, and broader automated coverage across agent, server, TUI, and orchestrator paths.

### Changed

- `server.New` now takes a validated `server.Config` instead of relying on positional arguments and late setter injection.
- Settings snapshots now expose profile definitions, backend metadata, tracker state configuration, and auto-clear workspace state.
- Workspace management can resolve issue-specific worktree branches and skips manual branch checkout when worktree mode is enabled.
- The Linear workflow template now defaults `working_state` to `"In Progress"`.

### Fixed

- Action logging is now consistent across Claude and Codex: `action_started` and `action_detail` parse correctly, shell metadata is preserved, and duplicate tool counts are avoided.
- Dashboard token accounting now remains cumulative across turns instead of resetting at turn boundaries.
- Orchestrator lifecycle bugs were fixed around paused-to-discard races, event-channel exit handling, scanner goroutine shutdown, nil-safe exit handling, and after-run hook log forwarding.
- GitHub tracker state casing and fallback behavior no longer create duplicate columns or invalid terminal-state labels.
- Web and TUI log views now surface worker errors more reliably, including `ERROR` lines and hook/prompt failures.
- SSH worker execution now allocates a PTY so remote agent processes receive `SIGHUP` and do not become orphaned.

### Documentation

- Expanded project docs with `SECURITY.md`, `CONTRIBUTING.md`, `.env.example`, compatibility notes, ADRs, and dashboard design documentation.

## [0.1.0] — 2026-03-18

### Added

- Initial public release of Itervox.
- Kanban web dashboard for real-time issue monitoring.
- Terminal UI (TUI) with split-panel issue list and log viewer.
- Linear and GitHub tracker integration.
- Claude agent runner with SSH worker host support.
- Agent profiles for per-issue command overrides.
- Agent teams mode for multi-agent collaboration.
- Timeline view for historical agent run review.
- `itervox --version` flag.
- `itervox init` command for `WORKFLOW.md` scaffolding.
- `itervox clear` command for workspace cleanup.
- `CONTRIBUTING.md` and `CODE_OF_CONDUCT.md`.

### Security

- SSH host key checking remains permissive (`StrictHostKeyChecking=no`); strict/configurable verification is deferred. Tracked as F-NEW-F.
- Added HTTP server `ReadTimeout` (5s) and `IdleTimeout` (120s) to prevent connection exhaustion from slow or idle clients.

## [0.0.2] — 2025-03-xx

### Fixed

- GitHub issues sync label duplication and refresh behavior.
- GitHub issues users loading bug.

## [0.0.1] — initial release

### Added

- Linear and GitHub tracker integration.
- Claude Code agent runner.
- Bubbletea TUI.
- REST API.
- Web dashboard.

[0.2.0]: https://github.com/vnovick/itervox/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/vnovick/itervox/compare/v0.1.0...v0.1.3
[0.1.0]: https://github.com/vnovick/itervox/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/vnovick/itervox/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/vnovick/itervox/releases/tag/v0.0.1
