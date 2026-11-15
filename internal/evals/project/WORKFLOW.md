---
# Eval project for `make evals-live` / `make evals-record` (#189).
#
# Live mode loads THIS file with the daemon's own config loader and composes
# each scenario's prompt with the daemon's own prompt assembly
# (orchestrator.ComposeEvalPrompt): the WORKFLOW.md body below, the operator
# reply envelope, the run context, the resolved rules layers (user
# ~/.itervox/rules.json plus .itervox/rules.json next to this file), the
# profile's SOUL.md and INSTRUCTIONS.md, the automation's instructions and the
# daemon action notice. Point EVALS_WORKFLOW at your own project's
# WORKFLOW.md to evaluate your own profiles against the same scenarios.
itervox_schema_version: 2
tracker:
  kind: github
  project_slug: itervox-eval/sandbox
  api_key: unused-in-evals
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done", "Cancelled"]
  completion_state: "In Review"

agent:
  command: claude
  profiles:
    # The shipped built-in: SOUL.md / INSTRUCTIONS.md, command and allowed
    # actions come from internal/profiles/builtin/merge-bot/.
    merge-bot: {}
    # The producer side of the merge handshake documented in the automations
    # guide ("Autonomous merge sweep"): approve with the /ai-approved marker,
    # or post numbered failures and move the issue back.
    reviewer:
      command: claude
      soul_file: agents/reviewer/SOUL.md
      instructions_file: agents/reviewer/INSTRUCTIONS.md
      allowed_actions: [comment, move_state]

automations:
  # merge-bot scenarios stand in for a comment-triggered dispatch: the
  # scenario's comment.body is the trigger comment. No body_contains filter,
  # so the wrong-marker scenario reaches the agent (a loose filter in the
  # field can do the same).
  - id: merge-on-approval
    enabled: true
    profile: merge-bot
    trigger:
      type: tracker_comment_added
    instructions: |
      Triggering comment from {{ trigger.comment_author_name }}:

      > {{ trigger.comment_body }}

      Only an explicit reviewer approval ("AI review passed — ready for merge")
      authorises a merge.
---

You are working on issue **{{ issue.identifier }}: {{ issue.title }}** (state: {{ issue.state }}).

Branch: `{{ issue.branch_name }}`

{% if issue.description %}
{{ issue.description }}
{% endif %}

{% if issue.comments %}
## Comments

{% for comment in issue.comments %}
**{{ comment.author_name }}**: {{ comment.body }}
{% endfor %}
{% endif %}
