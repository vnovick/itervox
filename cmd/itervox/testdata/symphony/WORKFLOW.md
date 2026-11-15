---
# A Symphony workflow (OpenAI Symphony SPEC front matter).
tracker:
  kind: linear
  endpoint: https://api.linear.app/graphql
  api_key: $LINEAR_API_KEY
  project_slug: symphony-demo
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Closed
    - Cancelled
polling:
  interval_ms: 45000
workspace:
  root: ~/symphony-workspaces
hooks:
  after_create: |
    git clone --depth 1 git@github.com:acme/app.git .
  before_run: git fetch origin && git rebase origin/main
  after_run: echo done
  before_remove: echo bye
  timeout_ms: 90000
agent:
  max_concurrent_agents: 4
  max_turns: 15
  max_retry_backoff_ms: 120000
  max_concurrent_agents_by_state:
    in progress: 2
codex:
  command: codex app-server
  approval_policy: never
  thread_sandbox: workspace-write
  turn_sandbox_policy:
    type: workspaceWrite
  turn_timeout_ms: 1800000
  read_timeout_ms: 5000
  stall_timeout_ms: 240000
server:
  port: 8191
---
You are working on Linear issue {{ issue.identifier }}: {{ issue.title }}.

{{ issue.description }}

{% if attempt %}This is retry attempt {{ attempt }}; pick up where the last run stopped.{% endif %}

Open a pull request when the work is done and move the issue to Human Review.
