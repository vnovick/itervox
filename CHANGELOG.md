# Changelog

All notable changes to Itervox are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [0.2.0] — 2026-05-03

### Removed (breaking)

- **`agent.agent_mode`** is gone. The previous values were `""` (solo), `"subagents"`, `"teams"`. Behavior reasoning:
  - `""` and `"subagents"` were aliases at runtime — the daemon never actually gated subagent dispatch.
  - `"teams"` injected a "your peer agents are X, Y, Z" roster into worker context; that injection now happens **unconditionally** when more than one profile exists.
  - Profile prompts (`agent.profiles.<name>.prompt`) now always inject when a profile is selected. Previously this was suppressed unless `agent_mode` was non-empty — silent suppression that operators rarely intended.

  **Migration:** delete the `agent.agent_mode` field from your `WORKFLOW.md`. The daemon now hard-fails at startup with a clear pointer to this entry if the field is present (typo guard). The legacy alias `agent.enable_agent_teams` is similarly rejected.

  **Behavior break to expect:** if you had `agent_mode: ""` (default) AND a profile with a non-empty `prompt:` field, that prompt was silently suppressed. After upgrade, the prompt injects. Either embrace the injection, or clear the profile's `prompt:` field if you want the old behavior.
- The **`POST /api/v1/settings/agent-mode`** HTTP endpoint and the **"Agent Runtime"** Settings card are removed. The `inline_input` toggle (which used to live in that card) now lives in Settings → General and still persists to `WORKFLOW.md::agent.inline_input`.
- The TUI status row no longer displays a `◈ SUB-AGENTS` / `◈ TEAMS` badge.

### Added

- **Automations v1**: a new top-level `automations:` block in `WORKFLOW.md` replaces the cron-only `schedules:` surface with eight trigger types — `cron`, `input_required`, `tracker_comment_added`, `issue_entered_state`, `issue_moved_to_backlog`, `run_failed`, `pr_opened` (gap B — fires when a worker's PR is detected), and `rate_limited` (gap E — fires when a worker run exhausts retries and Itervox classifies the terminal failure as rate-limit-driven; the switch cap only limits/suppresses switching). Each rule carries its own filter (`states`, `labels_any`, `identifier_regex`, `input_context_regex`, `limit`, `match_mode`) and instruction block layered on top of a selected profile. Legacy `schedules:` blocks are still parsed and upgraded to `cron` automations for back-compat.
- **Automation launch boundary documented**: v0.2.0 is scoped to single-profile helper automations. It does not claim production downstream workflow orchestration: no fatal post-change gates, automation skip-decision logs, schedule run-now/next-fire operations, label-to-profile routing, reserved automation slots, deterministic blocker predicates, structured gate artifacts, PR-check triggers, cost caps, or native planner/debate run groups. The docs now label dependency readiness and debate patterns as prompt-governed/advisory until engine predicates and run groups land.
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
- Reviewer settings are now validated consistently: `agent.auto_review` requires `reviewer_profile`, and `workspace.auto_clear` cannot be enabled together with automatic review.
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
- **Current-functionality QA baseline**: route-mocked Playwright smoke (`make qa-current-ui`), real-daemon smoke (`make qa-daemon`), and the combined `make qa-current` gate now protect the existing dashboard before UI-overhaul work. Added the repeatable `.claude/skills/current-ui-qa/SKILL.md` exploratory QA skill, `planning/qa_framework.md`, and the tracked `planning/qa_reports/` baseline-report flow.
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
- Documented the `auto_review` / `workspace.auto_clear` guardrail, the requirement that `auto_review` be paired with `reviewer_profile`, and the current lifecycle-hook semantics more precisely in the README, generated workflow template comments, and site docs.
- **Full `automations:` reference added to both `docs/configuration.md` and `site/src/content/docs/configuration.mdx`**: every trigger type, filter field, policy option, and the legacy `schedules:` deprecation subsection. The site guide `site/src/content/docs/guides/automations.mdx` now also documents the IANA timezone typeahead that the Settings UI offers for `cron` triggers.
- **API references refreshed across both repo docs and the docs site**: authentication semantics now match the secure-by-default non-loopback behavior, typed error envelopes are documented, SSE `Last-Event-ID` resume and sublog `event: error` frames are covered, and the newer surfaces (`/settings/automations`, `/settings/profiles`, `/settings/models`, `/settings/reviewer`, `/settings/inline-input`, `/agent-actions/*`, `PATCH /issues/{id}/state`, `POST /refresh`, `DELETE /workspaces`) are documented with current request/response shapes.
- **README gained a “Remote access & bearer-token auth” section** before the SSH section, explaining the auto-generated token, the login-screen capture of `?token=…` from URL, `sessionStorage` / `localStorage` persistence, and the `allow_unauthenticated_lan` opt-out. Cross-links to the site remote-access guide.
- **Agent-profile / automation docs now explain daemon-backed action permissions** (`allowed_actions`, `create_issue_state`) and the modern reviewer-profile flow, including the `auto_review` guardrails and the short-lived action-grant model used by automation helpers and reviewer/comment flows.
- **`domain.Issue.Comments` now documents its ascending-`CreatedAt` ordering contract** so future tracker adapters know they must sort before returning — preventing silent breakage of `latestAutomationComment` which takes the last element as “newest”.
- **`internal/agentactions` package-level and exported-symbol doc comments** covering `Grant`, `Store`, `NewStore`, `Issue`, `Revoke`, and `Validate`, including the previously-undocumented `ttl <= 0 → 1 hour` footgun.
- **`internal/schedule` package-level and exported-symbol doc comments** on `Expression`, `Parse`, and `Matches`, making the 5-field cron syntax and the day-of-month / day-of-week OR semantics explicit for future callers.
- **`internal/orchestrator/automation.go` exported type doc comments** on `AutomationTriggerContext`, `AutomationDispatch`, `InputRequiredAutomation`, and `RunFailedAutomation`, explaining which fields are populated for which trigger types and the runtime invariant that automation dispatch targets may be in non-active states.
- **Orphan GET handlers documented as API-only** (`GET /settings/profiles`, `GET /settings/models`, `GET /settings/reviewer`) — dashboard reads via the `/state` snapshot; these endpoints are exposed for non-web clients.
- **Manual release checklist** (`planning/manual-test-checklist_270426.md`) covering auth-gate first-run, server-down recovery, automation CRUD validation, profile-delete cascade, and a 30-minute single-page releasability smoke.

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
