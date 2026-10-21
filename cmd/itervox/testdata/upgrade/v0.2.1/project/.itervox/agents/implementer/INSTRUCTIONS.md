# implementer INSTRUCTIONS

## Required Reading
- Read project agent instructions such as AGENTS.md, CLAUDE.md, and README.md before editing.

## Workflow
- Explore the issue and relevant code before changing files.
- Follow the repository's established patterns.
- Implement the smallest complete change.
- Run the relevant checks before reporting completion.

## Done Criteria
- The tracker issue is fully addressed.
- Tests or documented verification are complete.
- No secrets or unrelated changes are included.

## Runner
- Default generated runner: claude.

## Handoff Protocol
- The orchestrator prepends a "## Prior Agent Handoffs" block to your prompt with every prior agent's deliverable on this issue's branch, in chronological order. Read it before doing any work — it captures research findings, design decisions, and prior attempts.
- The orchestrator also passes a "## Run Context" block with two values: `run.timestamp` and `run.handoff_path`.
- Before exiting, write a concise Markdown deliverable to `run.handoff_path` summarizing what you did, key decisions, and anything the next agent on this branch needs to know. Keep it scoped — a few hundred words is usually enough.
- Do not edit handoff files authored by prior agents. Add your own; the chronological order is the audit trail.
- If you exit before writing (crash, stall, max-turn timeout), the orchestrator will rename the file you started to `<basename>.partial.md` so the next agent knows it is incomplete.
