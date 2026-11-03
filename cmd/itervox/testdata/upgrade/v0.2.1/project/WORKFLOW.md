---
itervox_schema_version: 2

tracker:
  kind: github
  api_key: $GITHUB_TOKEN            # export GITHUB_TOKEN=ghp_...
  project_slug: owner/repo
  # GitHub uses labels to map states. Labels must exist in your repo.
  # NOTE: GitHub Projects v2 'Status' field is separate from labels — Itervox
  #       only reads labels. See README for Projects automation setup.
  # Create them with: gh label create "todo" --color "0075ca" --repo owner/repo
  #                   gh label create "in-progress" --color "e4e669" --repo owner/repo
  #                   gh label create "in-review" --color "d93f0b" --repo owner/repo
  #                   gh label create "done" --color "0e8a16" --repo owner/repo
  #                   gh label create "cancelled" --color "cccccc" --repo owner/repo
  #                   gh label create "backlog" --color "f9f9f9" --repo owner/repo
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled"]
  working_state: "in-progress"  # Label applied when an agent starts.
  #                               # MUST exist as a label in your repo.
  #                               # Set to "" to disable, or reuse an active label.
  completion_state: "in-review"  # Label applied when the agent finishes.
  # backlog_states: ["backlog"]  # Shown in TUI (b) and Kanban; not auto-dispatched.
  #                               # Must be an array — not a bare string.
  backlog_states: ["backlog"]
  # failed_state: "backlog"        # Label for issues that exhaust all retries.

polling:
  interval_ms: 60000

agent:
  command: claude
  max_turns: 60
  max_concurrent_agents: 3
  max_retries: 5
  turn_timeout_ms: 3600000
  read_timeout_ms: 120000
  stall_timeout_ms: 300000
  profiles:
    implementer:
      command: claude
      soul_file: .itervox/agents/implementer/SOUL.md
      instructions_file: .itervox/agents/implementer/INSTRUCTIONS.md
      allowed_actions: [comment, comment_pr]
    reviewer:
      command: claude
      soul_file: .itervox/agents/reviewer/SOUL.md
      instructions_file: .itervox/agents/reviewer/INSTRUCTIONS.md
      allowed_actions: [comment, comment_pr]
    input-responder:
      command: claude
      soul_file: .itervox/agents/input-responder/SOUL.md
      instructions_file: .itervox/agents/input-responder/INSTRUCTIONS.md
      allowed_actions: [comment, provide_input]
    deps-analyzer:
      command: claude
      soul_file: .itervox/agents/deps-analyzer/SOUL.md
      instructions_file: .itervox/agents/deps-analyzer/INSTRUCTIONS.md
  reviewer_profile: reviewer         # Profile used by the AI Review button and optional auto-review.
  deps_analyzer_profile: deps-analyzer    # Profile used by the dashboard "Analyze dependencies" button. Empty disables the button.
  # auto_review: false               # Set to true to auto-review after each successful agent run. Coexists with workspace.auto_clear as of v0.2.0 — the clear fires on terminal tracker state, after the reviewer also completes.
  # backend_fallback:                 # CORE-054: reroute when a backend hits its usage limit (docs/configuration.md).
  #   chain: [claude, codex]
  #   profile_map:                     # each target must exist, be enabled, and run that backend
  #     implementer: { codex: implementer-codex }
  #   switch_back: at_reset            # at_reset | on_success | manual
  #   min_dwell_minutes: 30
  reviewer_prompt: |
    You are an AI code reviewer for issue {{ issue.identifier }}: {{ issue.title }}.

    ## Your task

    Review the pull request created for this issue.

    1. Run `gh pr diff` to read the PR changes
    2. Review for: correctness, test coverage, edge cases, security issues, code style
    3. If you find problems:
       - Fix them directly in the workspace
       - Commit and push: `git add -A && git commit -m "fix: reviewer corrections" && git push`
       - Post a comment on the tracker issue summarising what you fixed
    4. If the PR is clean:
       - Post an approval comment: "AI review passed — no issues found"

    Be concise. Focus on real bugs, not style preferences.
  available_models:
    claude:
      - { id: "claude-haiku-4-5-20251001", label: "Haiku 4.5 - Fast, cost-effective" }
      - { id: "claude-sonnet-4-5-20251001", label: "Sonnet 4.5 - Previous gen balanced" }
      - { id: "claude-sonnet-4-6", label: "Sonnet 4.6 - Balanced" }
      - { id: "claude-opus-4-5-20251001", label: "Opus 4.5 - Previous gen powerful" }
      - { id: "claude-opus-4-6", label: "Opus 4.6 - Most powerful" }
    codex:
      - { id: "gpt-5.3-codex", label: "GPT-5.3-Codex - Frontier coding" }
      - { id: "gpt-5.2-codex", label: "GPT-5.2-Codex - Long-horizon agentic coding" }
      - { id: "gpt-5.1-codex-max", label: "GPT-5.1-Codex Max - Deep reasoning" }
      - { id: "gpt-5.1-codex", label: "GPT-5.1-Codex - Balanced" }
      - { id: "gpt-5.1-codex-mini", label: "GPT-5.1-Codex Mini - Faster, cheaper" }
      - { id: "gpt-5-codex", label: "GPT-5-Codex - Stable baseline" }
      - { id: "codex-mini-latest", label: "codex-mini-latest - Deprecated compatibility alias" }

workspace:
  root: ~/.itervox/workspaces/fx021-2bdd15e2
  worktree: true
  clone_url: git@github.com:owner/my-project.git
  base_branch: main

hooks:
  # after_create and before_run are no longer needed for clone/reset —
  # Itervox maintains a bare clone and creates worktrees automatically.
  # Add custom hooks here if your project needs extra setup.
  # before_run runs once per worker attempt; after_run runs after each turn.
  # after_create: |
  #   pnpm install --frozen-lockfile
  # before_run: |
  #   make prepare-agent-workspace
  # after_run: |
  #   git status --short
  # before_remove: |
  #   tar -czf ../workspace-backup.tgz .

server:
  port: 0

# ── automations (optional) ────────────────────────────────────────────
# Cron and event-driven helper rules layered on top of your agent profiles.
# Trigger types: cron, input_required, tracker_comment_added, issue_entered_state,
#                issue_moved_to_backlog, run_failed, pr_opened, pr_merged,
#                rate_limited, blockers_resolved.
# Tip: an automation whose final output starts with [SILENT] posts no tracker
# comment (run stays in the dashboard logs) — use it for scan-and-found-nothing crons.
# Full reference: https://itervox.dev/guides/automations/
# automations:
#   - id: input-responder
#     enabled: true
#     profile: input-responder   # must match an entry under agent.profiles
#     trigger:
#       type: input_required
#     filter:
#       input_context_regex: "continue|branch|which file|test command"
#     policy:
#       auto_resume: true
#     instructions: |
#       Answer narrow, low-risk unblocker questions.
#       If the request is ambiguous, state the safest bounded assumption.
---

You are an expert engineer working on **my-project**.

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

```bash
git checkout -b {{ issue.branch_name | default: issue.identifier | replace: "#", "" | downcase }}
```

---

## Step 3 — Implement

Read `CLAUDE.md` to understand project conventions before writing any code:

```bash
cat CLAUDE.md
```

If `CLAUDE.md` does not exist, explore the repository structure, identify the dominant patterns and conventions, create `CLAUDE.md` documenting them, and then implement.

---

## Step 4 — Run checks

Read `CLAUDE.md` for the project's test and lint commands. If `CLAUDE.md` does not exist, discover the check commands by exploring the repository (look for `Makefile`, `package.json` scripts, CI config, etc.).

```bash
# Run the project's test and lint commands (check CLAUDE.md or discover from repo)
```

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

---

## Asking for human input — CRITICAL

If you genuinely cannot proceed without a human decision (ambiguous spec,
destructive action, missing credentials, conflicting requirements), you **must**
emit the literal token `<!-- itervox:needs-input -->` on its own line at the
end of your final message, followed by exactly one concise question. Itervox
detects this token, pauses the issue, and surfaces the question in the
dashboard and tracker so a human can reply. The token is an HTML comment so
it stays invisible in rendered Linear/GitHub markdown.

**This is the preferred contract.** Itervox also has a deterministic fallback for
successful final messages that clearly block on a human decision or
confirmation request, but the sentinel is still better because it is
deterministic, cheaper, and less ambiguous. That fallback is heuristic and
tuned for common English phrasing. If you know you need input, emit the
sentinel instead of relying on plain-English phrasing to be inferred.

### Example — correct usage

```
I've reviewed the auth module and there are two viable approaches:
1. Refresh tokens with rotation (more secure, breaking change)
2. Extend the JWT TTL (backwards compatible, lower security)

I need your decision before proceeding because this affects all existing
API clients.

<!-- itervox:needs-input -->
Which approach should I take: rotation (1) or extended TTL (2)?
```

### When to use
- Ambiguous requirements that have multiple valid interpretations
- Destructive actions that cannot be automatically reverted
- Missing credentials, API keys, or access tokens
- Conflicting instructions in the issue description
- Decisions that affect backwards compatibility

### When NOT to use
- Status updates or progress reports
- Rhetorical questions in your reasoning
- Questions that you can answer yourself by exploring the code
- Minor style or naming decisions (make a reasonable choice and move on)

### Plain-English fallback

If you forget the sentinel but end your final message with a real blocking
question such as "Which option should I take?" or "Type discard to confirm",
Itervox will usually still pause the issue and wait for a reply. Treat this as
backup behavior, not the primary contract.

- The explicit marker is more reliable.
- The fallback is English-oriented; non-English or unusual phrasing may not be detected.
