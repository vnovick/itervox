---
name: operate
description: 'Use for day-to-day work with a running Itervox daemon from Claude Code: "what is itervox doing", "what needs my attention", "which agents are waiting for input", "what is blocked", "dispatch ENG-12", "pause all dispatch", "resume dispatch", "answer the agent on #42", "tell the running agent to use the v2 API", "what do we know about the staging DB", "remember that...". Uses the bundled itervox MCP tools (board_summary, list_issues, get_issue, explain_blockers, recent_failures, feed_since, recall; with the operator scope dispatch, pause_dispatch, resume_dispatch, reply_input, steer, remember). Guardrail approvals stay human-only.'
---

# Operate Itervox through its MCP tools

Sources: `guides/mcp.mdx`, `guides/pause-and-feed.mdx`, `guides/sessions.mdx`,
`guides/guardrails.mdx`, `guides/memory.mdx`, `cli.mdx` (`itervox mcp`, quick
actions).

## The server

This plugin starts `itervox mcp --project <project dir> --token-scope <scope>`
over stdio as the `itervox` server (tools appear as
`mcp__plugin_itervox_itervox__<tool>`). It talks to the daemon already running
for the project; it never starts one.

- **viewer** (the default) lists the read tools only. It creates a stored
  viewer token named `mcp`, kept in `.itervox/mcp-token.json` (0600,
  gitignored); the secret is never printed.
- **operator** also lists the write tools. To switch, the user runs
  `/plugin configure itervox@itervox`, sets the token scope to `operator`, and
  reconnects the server in `/mcp`. The operator token replaces and revokes the
  viewer one.
- `itervox token list` shows the `mcp` token; `itervox token revoke ID` cuts the
  server off at once.

## Read tools (viewer)

| Tool | Use it for |
|---|---|
| `board_summary` | First call: running agents, retries, paused issues, issues waiting for input, capacity, the dispatch pause, counts of recent failures and blocked issues, the feed cursor |
| `list_issues` | Filter by tracker `state`, `orchestratorState` (`idle`, `running`, `retrying`, `paused`, `input_required`, `pending_input_resume`), `label`, `query`; `limit` |
| `get_issue` | One issue with its run context: current step, last tool call, turns and tokens, the pending question or guardrail approval, changed files, handoffs |
| `explain_blockers` | Tracker blockers, inferred dependencies (confidence, gating, stale, overridden), cycles, stale blockers; pass `identifier` for one issue |
| `recent_failures` | Recent classified failures, newest first |
| `feed_since` | Events after a cursor; pass the returned `nextSeq` as `since` next time |
| `recall` | Project memory for a `query`, ranked as at run time |

Results are compact JSON with secrets redacted; agent transcripts and tool
output are never returned.

## What needs attention (the dashboard's attention inbox)

1. `board_summary`.
2. The needs-input queue: `list_issues` with `orchestratorState: input_required`,
   then `get_issue` on each for its question or guardrail approval.
3. `recent_failures` for failed or stalled runs.
4. `explain_blockers` for issues held by blockers or cycles.

Report them grouped: needs a reply, needs a human approval, failed, blocked.

## Write tools (operator)

Each keeps the dashboard's rules. Say what a write will do before calling it,
and call it only when the user asked for that action.

| Tool | What it does |
|---|---|
| `dispatch` | Moves the issue to the first active state, like **Dispatch** on the board. While all dispatch is paused it refuses and changes nothing; it never resumes dispatch by itself |
| `pause_dispatch` / `resume_dispatch` | Pause all new dispatch (optional `reason`, `minutes`) and resume it. Running agents continue |
| `reply_input` | Answer an agent waiting for input. **Refuses guardrail approvals** |
| `steer` | Send a steering message to the issue's running agent. An agent waiting for input takes `reply_input` instead |
| `remember` | Propose a memory (`text`, optional `type`, `scope`, `tags`, `issue`). It is created as proposed and waits in Memory → Lessons for a human |

## Guardrail approvals stay human-only

A guardrail approval (a command in `guardrails.approval_commands` the agent
wants to run, such as a deploy) is never yours to decide. `reply_input`
refuses it, and the daemon refuses an agent reply even if an approval appears
after you looked. Do not try another route: do not run `itervox approve`, do
not steer the agent around it. Show the user the command from `get_issue` and
tell them to decide on the dashboard (or run `itervox approve <issue>`
themselves).

## Without the MCP server

`itervox watch --once` prints the board once (read-only) and
`itervox logs <issue>` an issue's run log. The quick actions
(`itervox pause`, `itervox resume`, `itervox dispatch <issue>`,
`itervox reply <issue> <text>`) use the daemon's own token, not the scoped
MCP token, so run them only when the user asks for that exact action.

## Troubleshooting

- `no running itervox daemon for …`: start `itervox` in the project (the
  `setup` skill), then reconnect in `/mcp`.
- `the daemon refused the token (401)`: the `mcp` token was revoked or
  rotated; reconnect in `/mcp` and a new one is created.
- A write tool says it needs an operator token: switch the scope as above.
- `itervox` not found: install it (the `setup` skill); the server runs the
  `itervox` on the `PATH` Claude Code was started with.
