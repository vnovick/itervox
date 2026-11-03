---
itervox_schema_version: 2
tracker:
  kind: github
  api_key: $GITHUB_TOKEN # export GITHUB_TOKEN=ghp_...
  project_slug: vnovick/itervox
  # GitHub uses labels to map states. Labels must exist in your repo.
  # NOTE: GitHub Projects v2 'Status' field is separate from labels — Itervox
  #       only reads labels. See README for Projects automation setup.
  # Create them with: gh label create "todo" --color "0075ca" --repo vnovick/itervox
  #                   gh label create "in-progress" --color "e4e669" --repo vnovick/itervox
  #                   gh label create "in-review" --color "d93f0b" --repo vnovick/itervox
  #                   gh label create "done" --color "0e8a16" --repo vnovick/itervox
  #                   gh label create "cancelled" --color "cccccc" --repo vnovick/itervox
  #                   gh label create "backlog" --color "f9f9f9" --repo vnovick/itervox
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled"]
  working_state: "in-progress" # Label applied when an agent starts.
  #                               # MUST exist as a label in your repo.
  #                               # Set to "" to disable, or reuse an active label.
  completion_state: "in-review" # Label applied when the agent finishes.
  # backlog_states: ["backlog"]  # Shown in TUI (b) and Kanban; not auto-dispatched.
  #                               # Must be an array — not a bare string.
  backlog_states: ["backlog"]

polling:
  interval_ms: 60000

agent:
  command: codex
  backend: codex
  max_turns: 60
  max_concurrent_agents: 3
  turn_timeout_ms: 3600000
  read_timeout_ms: 120000
  stall_timeout_ms: 300000
  profiles:
    deps-analyzer:
      command: claude
      soul_file: .itervox/agents/deps-analyzer/SOUL.md
      instructions_file: .itervox/agents/deps-analyzer/INSTRUCTIONS.md
  deps_analyzer_profile: deps-analyzer

workspace:
  root: ~/.itervox/workspaces/itervox

hooks:
  after_create: |
    git clone git@github.com:vnovick/itervox.git .
  # Only fetch: a resumed or retried run keeps its issue branch and any
  # unpushed work. A workspace still on main (no issue branch yet) is
  # fast-forwarded; the prompt's Step 2 checks out the issue branch.
  before_run: |
    git fetch --prune origin
    if [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]; then
      git merge --ff-only origin/main
    fi

server:
  port: 8090
---

You are an expert engineer working on **itervox**.

## Your issue

**{{ issue.identifier }}: {{ issue.title }}**

{% if issue.description %}
{{ issue.description }}
{% endif %}

Issue URL: {{ issue.url }}

{% if issue.comments %}
## Comments

{% for comment in issue.comments %}
**{{ comment.author_name }}**: {{ comment.body }}

{% endfor %}
{% endif %}

---

## Step 1 — Explore before touching anything

Read the issue. Explore the relevant code before making changes.

---

## Step 2 — Create a branch

Use the issue's branch if it already exists (locally, or on `origin` from an earlier run); create it only when it does not:

```bash
BRANCH={{ issue.branch_name | default: issue.identifier | replace: "#", "" | downcase }}
git checkout "$BRANCH" 2>/dev/null || git checkout -b "$BRANCH"
```

---

## Step 3 — Implement

Read `CLAUDE.md` to understand project conventions before writing any code:

```bash
cat CLAUDE.md
```

If `CLAUDE.md` does not exist, explore the repository structure, identify the dominant patterns and conventions, create `CLAUDE.md` documenting them, and then implement.

Detected stacks: Go. Follow their conventions as documented in `CLAUDE.md`.

---

## Step 4 — Run checks

Read `CLAUDE.md` for the project's test and lint commands. If `CLAUDE.md` does not exist, discover the check commands by exploring the repository (look for `Makefile`, `package.json` scripts, CI config, etc.).

```bash
make verify
```

`make verify` mirrors CI: gofmt, vet, golangci-lint, the Go race tests on `./cmd/... ./internal/...` and the web checks. Do not run `go test ./...`: it walks `web/node_modules`. For a quicker loop while iterating, run one package with `go test -race ./internal/<pkg>/...`, but finish with `make verify`.

---

## Step 5 — Commit and open PR

```bash
git add <specific files>
git commit -m "feat: <description> ({{ issue.identifier }})"
git push -u origin HEAD
gh pr create --title "<title> ({{ issue.identifier }})" --body "Closes {{ issue.url }}"
```

---

## Step 6 — Post PR link to tracker

After the PR is open, post its URL as a comment on the tracker issue so it is visible in GitHub:

```bash
PR_URL=$(gh pr view --json url -q .url)
gh issue comment {{ issue.identifier | remove: "#" }} --body "🤖 Opened PR: ${PR_URL}"
```

---

## Rules

- Complete the issue fully before stopping.
- Never commit `.env` files or secrets.

