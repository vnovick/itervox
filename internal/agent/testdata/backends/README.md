# Backend stream fixtures (synthetic)

Every `.jsonl` file here is SYNTHETIC: written by hand from the documented
output formats below, not captured from a real CLI run (itervox's tests never
start a real agent CLI). Re-check them against a real run when a CLI changes
its format.

| File | CLI | Source of the format | Version checked |
|---|---|---|---|
| `opencode.jsonl` | `opencode run --format json` | `packages/cli/src/run/noninteractive.ts` and `run/run.ts` in https://github.com/sst/opencode (event types `step_start`, `text`, `reasoning`, `tool_use`, `step_finish`, `error`; flags `--format json`, `--session`, `--model`, `--auto`; prompt read from stdin when it is not a TTY) | `@opencode/cli` 2.2.5, commit d9184f2 (2026-11-02) |
| `cursor.jsonl` | `cursor-agent -p --output-format stream-json` | https://cursor.com/docs/cli/reference/output-format and https://cursor.com/docs/cli/headless (`system`/`init`, `user`, `assistant`, `tool_call` `started`/`completed` with `call_id` and a `tool_call` object keyed by tool kind, `result` with `session_id`, `duration_ms`, `is_error`; flags `-p`, `--resume`, `--force`, `--trust`, `--model`; the prompt is an argument) | docs as of 2026-11; the `usage` object on `result` is optional and only read when present |
| `custom.jsonl` | itervox agent protocol v1 | `internal/agent/custom.go` and `site/src/content/docs/guides/backends.mdx` (this repository) | protocol 1 |

The Cursor docs host was not reachable from the environment the parsers were
written in; the Cursor format was taken from the published docs pages' text
as indexed by search and from the Claude stream-json parser it mirrors.
