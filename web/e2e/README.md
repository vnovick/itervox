# Playwright E2E Specs

Three lanes coexist in this directory, distinguished by **filename prefix**.
The two Playwright configs (`web/playwright.config.ts`,
`web/playwright.ui.config.ts`) use prefix-based `testIgnore` /
`testMatch` rules to route specs to the right runner.

| Lane | Filename prefix | Config | What it boots | When to use |
|---|---|---|---|---|
| **Lane 2** — route-mocked browser | `ui-current-*.spec.ts` | `playwright.ui.config.ts` (`make qa-current-ui`) | Vite dev server + `e2e/fixtures/mockApi.ts` | Fast UI smoke; no Go dependency. Most coverage of dashboard, settings, automations editor lives here. |
| **Lane 3** — real daemon | `daemon-*.spec.ts`, `auth-*.spec.ts`, `automation-crud.spec.ts` | `playwright.config.ts` (`make qa-daemon` / `make e2e`) | Compiled `./itervox` binary spawned per spec via `helpers/daemon.ts` | End-to-end: real Go server, real auth gate, real memory tracker. Slower, narrower. |
| **Manual smoke** (none right now) | n/a | n/a | n/a | Reserved for future cross-browser screenshots if needed. |

## Naming convention (gap §7.3)

- **`ui-current-*`** — Lane 2 only. Vite + mocked API. Run with `make qa-current-ui` or `pnpm test:ui-current`.
- **`daemon-*`** — Lane 3 only. Real daemon. Run with `make qa-daemon` or `pnpm test:e2e`.
- **`auth-*`** — Lane 3. Auth-token flow tests against the real binary.
- **`automation-crud.spec.ts`** — Lane 3 (one-off; settings round-trip against the real server).

A directory split was considered (gap §7.3) but rejected: each spec imports
from `e2e/fixtures/...` via relative paths, and a move would touch every
file's import path while delivering only cosmetic readability. The prefix
convention is enforced by the Playwright config and documented here.

## Running

```bash
# Lane 2 only (fast — no Go build required)
make qa-current-ui

# Lane 3 only (rebuilds binary; spawns daemon per spec)
make qa-daemon

# Both lanes + Go race tests + lint + size-budget
make qa-current
```

## Fixtures

- `e2e/fixtures/mockApi.ts` — Lane 2 only. Intercepts `/api/v1/*` requests.
- `e2e/fixtures/scenarios.ts` — Re-exports `src/test/fixtures/scenarios.ts`. Both lanes.
- `e2e/helpers/daemon.ts` — Lane 3 only. Spawns a real daemon, writes a temp `WORKFLOW.md`, returns `{url, token, stop}`.
