---
name: issue
description: 'Use when the user wants to write, improve or file an issue for an Itervox agent: "write an agent-ready issue", "make this ticket ready for itervox", "add the itervox issue template", "file a local issue", "how do I mark a blocker", "hand this issue to the agents". Writes the agent-task sections (Goal, Acceptance criteria, Likely files or areas, How to verify, Out of scope, Blockers), declares blockers the way each tracker (Linear, GitHub, local files) reads them, and moves the issue into an active state only when the user says so.'
---

# Write an agent-ready issue

Sources: `guides/github-issues.mdx` (Agent-ready issue template, Blockers),
`guides/local-issues.mdx` (The issue file), `guides/dependency-management.mdx`,
`cli.mdx` (`itervox init --issue-template`, `itervox task`). The template
itself is `internal/templates/agent_task_body.md`.

## The template

`itervox init --tracker github --issue-template` writes
`.github/ISSUE_TEMPLATE/agent-task.md` (an "Agent task" template; never over an
existing file; with a `WORKFLOW.md` already present it adds only the template).
For Linear, `itervox init --tracker linear --issue-template` prints the same
sections to paste into a Linear issue template. The template sets no labels.

Write every issue with these sections, in this order:

```markdown
## Goal
<What is true when this is done. One or two sentences.>

## Acceptance criteria
- [ ] <checkable item>
- [ ] <checkable item>

## Likely files or areas
<Paths, packages or components to start from.>

## How to verify
<The exact commands or steps that prove it works, e.g. the test target.>

## Out of scope
<What the agent must not change in this issue.>

## Blockers
Blocked by #12, #15
```

## Make it agent-ready

- **One outcome per issue.** Split anything with two unrelated goals.
- **Acceptance criteria are checks, not wishes.** "`go test ./internal/report/...`
  passes and `report --json` prints an array" beats "JSON output works". The
  agent works until each box is met and says how it checked.
- **How to verify names real commands.** Read the repository's Makefile,
  `package.json` or CI config and copy the exact target.
- **Likely files** saves the agent a search; name paths you confirmed exist.
- **Out of scope** prevents drive-by refactors, renames and dependency bumps.
- Put links, error text and reproduction steps in the description itself.

## Blockers: one syntax per tracker

| Tracker | How a blocker is declared |
|---|---|
| GitHub | A phrase in the **issue body** followed by `#N`: `blocked by`, `blocked on`, `depends on`, `depends upon`, `requires`, `waiting on`, `waiting for`. Same repository only; comments are not read; a bare `#12` is not a blocker. Delete the line when there are none: an unfilled `Blocked by #` declares nothing. |
| Linear | Linear's own **Blocked by** relation, and sub-issues (a parent with open children is blocked by them). |
| Local files | `blocked_by: [ITX-3]` in the file's front matter. |

An issue is not dispatched while any blocker is unresolved. With
`dependencies.stacked_prs` on, an issue whose only blocker is in review starts
on that blocker's branch (see the `automations` skill).

## File it

- **GitHub / Linear:** create the issue in the tracker (the `gh` CLI or the
  tracker's UI). It must be inside the workflow's scope (for Linear,
  `tracker.project_slug`, `tracker.assignee`, `tracker.labels`).
- **Local:** create `.itervox/issues/<PREFIX>-<n>.md`. The file name is the
  identifier; `title` and `state` are required:

  ```markdown
  ---
  title: Add a --json flag to report
  state: Backlog
  priority: 2
  labels: [cli]
  blocked_by: [ITX-3]
  ---

  ## Goal
  ...
  ```

## Hand it to the agents (only when the user says so)

Nothing runs until the issue is in an active state (`tracker.active_states`,
default `Todo` and `In Progress`; on GitHub the `todo` label). Moving it there
starts an agent, so ask first. Then either set the state in the tracker (for a
local file, `state: Todo`), use the MCP `dispatch` tool (the `operate`
skill), or run `itervox dispatch <issue>`.

`itervox task "<title>" --instructions @file.md` creates a new task directly
in the first active state, so it starts work as soon as a slot is free; use it
only when the user wants the work started now.
