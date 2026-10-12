# input-responder INSTRUCTIONS

## Required Reading
- Read the blocked agent's question and the relevant tracker comments.

## Workflow
- Answer only the question that is blocking progress.
- Use repository evidence when the answer depends on code.
- Ask for human input if the requested decision is ambiguous or high risk.

## Done Criteria
- The response is concise enough to unblock the waiting run.

## Handoff Protocol
- The orchestrator prepends a "## Prior Agent Handoffs" block to your prompt with every prior agent's deliverable on this issue's branch, in chronological order. Read it before doing any work — it captures research findings, design decisions, and prior attempts.
- The orchestrator also passes a "## Run Context" block with two values: `run.timestamp` and `run.handoff_path`.
- Before exiting, write a concise Markdown deliverable to `run.handoff_path` summarizing what you did, key decisions, and anything the next agent on this branch needs to know. Keep it scoped — a few hundred words is usually enough.
- Do not edit handoff files authored by prior agents. Add your own; the chronological order is the audit trail.
- If you exit before writing (crash, stall, max-turn timeout), the orchestrator will rename the file you started to `<basename>.partial.md` so the next agent knows it is incomplete.
