# Compatibility Matrix

This document records which external tool versions Itervox is developed and tested against.
Use it to understand what upgrades are safe and where silent breakage can occur.

---

## Go runtime

| Component | Tested version | Notes |
|---|---|---|
| Go toolchain | **1.25.10** | Minimum required to build (`go.mod`). Uses `min()` builtin (available since 1.21) and `log/slog` (available since 1.21). Older toolchains will fail at compile time. |
| golangci-lint | **v2.1.6** | Pinned in Go CI and release preflight. Local contributors can use newer versions for ad-hoc checks, but release CI uses this pin. |
| govulncheck | **v1.1.4** | Pinned in Go CI and release preflight. Run with `-tags dev ./cmd/... ./internal/...`. |
| GoReleaser | **v2.14.3** | Pinned in release CI. v0.2.0 intentionally keeps the existing `brews` stanza under this version; `goreleaser check` validates it with a deprecation warning. |

---

## Agent CLIs

Itervox spawns an agent subprocess per issue. Each CLI has its own release cadence; Itervox does not pin a version.

| CLI | Tested against | Breaking-change risk |
|---|---|---|
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) | **latest** | Itervox checks for `--resume` and `--dangerously-skip-permissions` flags; a flag rename in a Claude Code release would break resume behavior. Watch Claude Code release notes. |
| [Codex](https://github.com/openai/codex) (`codex`) | **latest** | Itervox passes `--approval-mode full-auto`. Flag renames in Codex releases would break dispatch. |

> **How to detect breakage:** If agents exit immediately with a non-zero code, check the log for "unknown flag" or "unrecognised option". Run `claude --help` / `codex --help` to verify flag names match what Itervox sends.

---

## Tracker APIs

| Tracker | API surface | Pinned version | Notes |
|---|---|---|---|
| [Linear](https://linear.app) | GraphQL | **Unversioned** (`api.linear.app/graphql`) | Linear's GraphQL schema is unversioned and evolves without breaking existing queries in practice. Queries use a stable subset: `issues`, `pageInfo`, `labels`, `inverseRelations`. Field additions are safe; field removals or renames would surface as JSON decode failures. |
| [GitHub Issues](https://docs.github.com/en/rest) | REST | **2022-11-28** (`X-GitHub-Api-Version: 2022-11-28`) | Pinned via request header. GitHub guarantees 24-month support for dated API versions. This version is valid until at least late 2026. Itervox uses `GET /repos/{owner}/{repo}/issues`, `POST /repos/{owner}/{repo}/issues/{number}/comments`, `GET /repos/{owner}/{repo}/pulls`. |

---

## Web dashboard build

The dashboard is embedded in the Go binary at build time (`go generate ./internal/server/`).
Pre-built dashboards are included in release binaries — Node.js is **not** required at runtime.

| Component | Tested version | Notes |
|---|---|---|
| Node.js | **20 LTS** for `web/`, **22 LTS** for `site/` | Required only to build the dashboard or docs site from source. |
| pnpm | **10+** | Used as the package manager (`web/` and `site/` directories). Both `web/package.json` and `site/package.json` pin the exact version via `packageManager: pnpm@10.9.0`; running pnpm via [corepack](https://nodejs.org/api/corepack.html) selects the pinned version automatically. |
| Vite | See `web/package.json` | Bundler; version pinned in lockfile. |

---

## Operating systems

| OS | Status | Notes |
|---|---|---|
| macOS (arm64, x86_64) | **Supported** | Primary development platform. |
| Linux (x86_64, arm64) | **Supported** | Used in CI (`ubuntu-latest`). |
| Windows | **Unsupported** | Agent CLIs (Claude Code, Codex) do not support Windows. Itervox's workspace/shell plumbing assumes a POSIX shell. |

---

## Upgrade guidance

| Scenario | Risk | Action |
|---|---|---|
| New minor Go release | Low | Update `go.mod`, run `go test ./cmd/... ./internal/...` and `go test -race ./cmd/... ./internal/...`. |
| New Claude Code release | Medium | Check Claude Code changelog for flag renames. Run `itervox --dry-run` and verify dispatch logs show agent starting. |
| New Codex release | Medium | Same as above. |
| Linear GraphQL schema change | Low | Monitor [Linear changelog](https://linear.app/changelog). If `FetchCandidateIssues` starts returning unexpected shapes, Zod-style errors will surface in logs. |
| GitHub API version expiry (2026+) | High | Update `X-GitHub-Api-Version` header in `internal/tracker/github/client.go` to a current dated version. |
