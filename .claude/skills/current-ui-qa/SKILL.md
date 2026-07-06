---
name: current-ui-qa
description: Use when a human or agent needs to do exploratory QA on existing Itervox UI before/after a UI change. Drives both the route-mocked Playwright lane and the real-daemon lane, then captures qualitative issues (visual hierarchy, copy, accessibility) automated tests cannot. Produces a structured report under `docs/qa-reports/`.
---

# Current Itervox UI QA

This skill drives an exploratory QA pass over the **existing** Itervox dashboard, focused on locking the regression baseline before a UI change. It complements `make qa-current` (which catches programmatic regressions) by capturing the qualitative issues humans or agents notice when they actually drive the app.

## Required reading

Read `CLAUDE.md` (project root) for the project invariants — auth boundaries, SSE wiring, Zustand store rules. Everything else this skill needs is inlined below.

## Pre-flight

```bash
git status                              # confirm clean working tree
git rev-parse HEAD                      # capture commit
make qa-current                         # baseline must be green before exploratory pass
pnpm exec playwright install chromium   # one-time per contributor
```

If `make qa-current` fails, do **not** proceed — fix the regression first.

## Setup

Pick a lane:

- **Route-mocked (faster, deterministic):**
  ```bash
  cd web && pnpm test:ui-current --ui   # opens Playwright UI; navigate scenario by scenario
  ```
- **Real-daemon (catches embed/build/auth drift):**
  ```bash
  make build
  ./itervox -workflow path/to/WORKFLOW.md   # in another terminal
  open "http://127.0.0.1:8090/?token=$ITERVOX_API_TOKEN"
  ```

## Viewport matrix

For each scenario, exercise:

| Viewport | Width × Height | Why |
|---|---|---|
| Desktop  | 1440 × 900  | primary target |
| Tablet   | 1024 × 768  | layout breakpoint check |
| Mobile   | 390 × 844   | iPhone 14 — drawer/responsive |

## Per-route checklist

Run for `/`, `/timeline`, `/logs`, `/agents`, `/automations`, `/settings`:

- [ ] Page loads under 2s; no skeletons stuck > 500ms after data arrives.
- [ ] Primary action (Save, Submit, Refresh) is visible without scrolling.
- [ ] Empty state is intentional copy (not "undefined" or blank).
- [ ] Keyboard focus is not trapped — Tab reaches every interactive element.
- [ ] Esc closes any open dialog / slide / dropdown.
- [ ] No horizontal scroll at the smallest supported viewport.
- [ ] Mobile drawer opens, closes, doesn't push body content offscreen.
- [ ] Status / state is conveyed by text or shape, not color alone.
- [ ] No console errors after hydration (`Cmd+Opt+I` → Console tab).
- [ ] Network tab shows zero unexpected `/api/v1/*` failures.

## Required scenarios

Drive every named scenario shipped in the fixture library. Each is exported from `web/src/test/fixtures/scenarios.ts`:

- `emptyScenario` — empty state across every route.
- `quickstartScenario` — memory-tracker default (most common case).
- `activeRunScenario` — running rows present, hero counts non-zero.
- `inputRequiredScenario` — input_required + pending_input_resume rows.
- `retryAndPausedScenario` — retry queue + paused issues + pausedWithPR.
- `configInvalidScenario` — `configInvalid` populated; banner visible.
- `timelineLogsScenario` — every history status + every log event variant.
- `settingsMatrixScenario` — multi-profile, automations, SSH hosts, dispatch strategy.
- `mobileShellScenario` — quickstart + long-title issue for mobile responsiveness.

Use the route-mocked Playwright UI to switch scenarios without restarting the daemon.

## Screenshots

Capture per `(route, viewport, scenario)` triple. Filename pattern:

```
<route>-<viewport>-<scenario>.png
e.g. dashboard-desktop-quickstart.png
     timeline-mobile-empty.png
```

Save under `docs/qa-reports/screenshots/<report-date>/`.

## Severity rubric

| Severity | Definition |
|---|---|
| **P0** | Blocks release. Crash, data loss, auth bypass, ARIA blocker. |
| **P1** | Must-fix before merge. Wrong copy on a primary CTA, infinite spinner, console error in happy path. |
| **P2** | Should-fix. Layout glitch, sub-optimal empty state, console warning. |
| **P3** | Nice-to-have. Minor copy polish, inconsistent spacing, focus ring on uncommon path. |
| **P4** | Subjective polish. Aesthetic preference, naming opinion, fully optional. |

## Promotion rule

If the **same qualitative finding** appears in **two consecutive QA runs**, promote it to an automated test before the third run. Track the promotion target file (e.g. `web/e2e/ui-current-<area>-smoke.spec.ts`) inline in the next report.

## Report path

Write the report to:

```
docs/qa-reports/YYYY-MM-DD-current-ui-qa-<short-scope>.md
```

## Report template

Use this exact structure (inline so the skill stays self-contained — no external template files):

```markdown
# Itervox UI QA — <date> — <scope>

## Summary

- **Commit:** `<sha>`
- **Branch:** `<name>`
- **Lane:** route-mocked / real-daemon / both
- **Viewports:** desktop 1440×900, tablet 1024×768, mobile 390×844
- **Scenarios driven:** <list>
- **`make qa-current` baseline:** PASS / FAIL (sha:X / commit:Y)

## Findings

### P0 — release-blockers
*(none, or list — each finding: short title; affected route + viewport + scenario; reproduction; expected vs actual; issue link or "no GitHub yet — captured here")*

### P1 — must-fix
*(same shape as P0)*

### P2 — should-fix
*(same shape)*

### P3 — nice-to-have

### P4 — subjective polish

## Repeated findings (promotion candidates)

Findings that appeared in the previous report AND in this one. Each row must include the promotion target (test file or refactor target) before the next QA run.

| Finding | First seen | Promotion target |
|---|---|---|

## Screenshots

Stored under `docs/qa-reports/screenshots/<date>/`. Naming: `<route>-<viewport>-<scenario>.png`.

## Notes

Any free-form observations, hypotheses, or open questions for the next iteration.
```

### Where findings go from here

- **P0/P1** — file a GitHub issue immediately, link from the report row.
- **P2** — capture in the report; pick up in the next iteration.
- **P3/P4** — track in the report; revisit when the area is touched.

## Done criteria

- [ ] All 9 scenarios driven across 3 viewports = 27 checkpoints.
- [ ] One report file written at `docs/qa-reports/YYYY-MM-DD-current-ui-qa-<short-scope>.md`.
- [ ] Every P0/P1 finding has an issue link (or a one-line "no GitHub yet — captured here").
- [ ] Two-run rule applied: list any qualitative finding that appeared in the prior report — if matched, document the promotion target.
