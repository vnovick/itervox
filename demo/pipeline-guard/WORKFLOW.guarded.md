---
# Pipeline-guard demo, part 2 (guarded). pipeline-guard-demo.sh copies this file to WORKFLOW.md
# in a temporary project and replaces __RUN__ with that directory.
itervox_schema_version: 2
tracker:
  kind: local
  active_states: ["Todo", "In Progress"]
  working_state: "In Progress"
  completion_state: "In Review"
  terminal_states: ["Done", "Cancelled"]
  backlog_states: ["Backlog"]
  push_interval_ms: 0 # write the issue file at once (no 10s batching)
polling:
  interval_ms: 1000
agent:
  backend: custom
  command: __RUN__/bin/scripted-agent.sh
  max_concurrent_agents: 1
  max_turns: 1
  max_retries: 0
  profiles:
    fixer:
      backend: custom
      command: __RUN__/bin/scripted-agent.sh
      soul_file: .itervox/agents/fixer/SOUL.md
      instructions_file: .itervox/agents/fixer/INSTRUCTIONS.md
      verify:
        - name: test
          run: "go test -count=1 ./..."
guardrails:
  lanes: {} # the default human lane: .github/**, WORKFLOW.md, Makefile, …
workspace:
  root: __RUN__/workspaces
  worktree: true
  clone_url: __RUN__/remote.git
  base_branch: main
hub:
  enabled: false # no machine-wide hub port for a throwaway demo
server:
  host: 127.0.0.1
  port: 0
  allow_unauthenticated: true
---

You are working on {{ issue.identifier }}: {{ issue.title }}

{{ issue.description }}
