---
itervox_schema_version: 2

tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done", "Cancelled"]
  backlog_states: ["Backlog"]
  working_state: "In Progress"
  completion_state: "Done"
polling:
  interval_ms: 10000
agent:
  command: "echo agent placeholder"
  max_concurrent_agents: 3
  max_turns: 5
  max_retries: 2
  turn_timeout_ms: 60000
  read_timeout_ms: 30000
  stall_timeout_ms: 30000
workspace:
  root: "/tmp/itervox-quickstart"
server:
  bind: "127.0.0.1:8090"
  allow_unauthenticated: false
---

# Quickstart prompt

You are an itervox quickstart agent. The tracker is in-memory with a few synthetic issues —
no Linear or GitHub access, no real changes will be made. Use this to explore the dashboard
and the daemon's lifecycle.

When you have completed your work on the issue, transition it to **Done**.
