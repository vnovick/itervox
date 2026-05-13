---
name: verify-before-done
description: Use before claiming any change is complete, before creating a commit or PR, or when the user asks "is this ready" / "are we done" / "can I merge". Also use when the user mentions test failures, coverage drops, or CI red. Enforces running `make verify` and related gates with evidence before asserting success.
---

# verify-before-done

Evidence before assertions. Never claim "done", "tests pass", "ready to merge", or "CI will be green" without showing the exact command output from this checklist.

## 1. `make verify` is the source of truth

Run from the repo root:

```bash
make verify
```

This runs, in order: `fmt` -> `vet` -> `lint-go` (golangci-lint) -> `test` (`go test -race ./cmd/... ./internal/... -count=1`) -> `web-coverage` (`pnpm test:coverage` in `web/`) -> `web-build` -> `web-spelling` (guards against the old "Symphony" name in user-visible strings) -> `size-budget` -> `no-os-exit`.

`go test ./...` alone is NOT sufficient. It misses:
- golangci-lint failures (CI lint job will fail)
- race conditions hidden by the test cache (no `-count=1`)
- frontend regressions and coverage drops (`pnpm test:coverage`)
- the spelling guard

`make verify` must exit 0. Show the tail of the output as evidence.

## 2. Frontend coverage gate

`make verify` now runs `pnpm test:coverage`, with a 70% threshold on **statements, branches, functions, and lines** (all four). For faster frontend-only iteration, run the target directly:

```bash
make web-coverage
```

Read the summary. Confirm every one of the four axes is at or above 70%. A green `pnpm test` says nothing about whether the coverage gate will pass in CI. New files showing 0% mean you forgot the test - write it before claiming done.

## 3. Race detector is mandatory and non-deterministic

`go test -race` samples at runtime. A race can hide for many runs. When touching `internal/orchestrator/` or any new concurrent code, stress it:

```bash
go test -race -count=5 ./internal/orchestrator/...
```

If a race appears intermittently, that is a real race, not flakiness. Rerun with `-count=10` to confirm and fix the root cause. The `retry_test.go` `callCount` race in this repo was latent for months before firing.

## 4. `govulncheck` for dependency bumps

If you touched `go.mod`, `go.sum`, or any Go dependency, run:

```bash
govulncheck -tags dev ./cmd/... ./internal/...
```

The `-tags dev` flag matches what the CI `govulncheck` job in `.github/workflows/ci-go.yml` uses. CI will fail the PR on new vulnerabilities.

## 5. Required order before commit

Run sequentially. Do not skip steps.

a. `make verify` - exits 0, output captured, including frontend coverage
b. `govulncheck -tags dev ./cmd/... ./internal/...` - if you touched `go.mod` or Go deps
c. `git status` - review staged files; watch for `.env`, credentials, accidental binaries
d. Only THEN `git commit`

## 6. Never claim "tests pass" without evidence

Show the exact command and a trimmed tail of its output. Claiming green based on a previous cached run is how regressions ship. Every success assertion must be accompanied by the command output that supports it, from the current run.

## 7. Common failure modes and signatures

- `go: go.mod requires go >= X.Y.Z` during `make verify`: the `Makefile` `GOTOOLCHAIN` is out of sync with the `go` directive in `go.mod`. Bump both together.
- Coverage report shows `0%` on a new file: that file has no test. Write one before calling it done.
- `pnpm lint` errors but `pnpm test` is green: ESLint failures still block CI. Fix them; do not blanket `eslint-disable`.
- Race detector passes once then fails on rerun: real race, not flakiness. Confirm with `-count=10` and fix the root cause; do not retry until it passes.

## Exit criteria

You may claim "done" only when:
- `make verify` exited 0 in the current session, output shown
- frontend coverage in `make verify` shows all four axes >= 70%, output shown
- (if Go deps touched) `govulncheck -tags dev ./cmd/... ./internal/...` exited 0, output shown
- `git status` reviewed, no secrets staged
