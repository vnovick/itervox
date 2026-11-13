---
name: ci
description: 'Use when the user wants Itervox to work on issues from CI instead of a long-running daemon: "run itervox in GitHub Actions", "set up the itervox action", "label an issue and have an agent open a PR", "itervox run --issue", "answer the agent from an issue comment", "nightly itervox sweep". Writes the workflow files from the CI guide with least-privilege permissions, has the user add the API-key secret (set on the step only), creates the trigger and state labels, and explains exit codes, --answer and --approve.'
---

# Run Itervox in CI

Sources: `guides/ci.mdx`, `cli.mdx` (`itervox run`), `action/action.yml`,
`action/trust-check.sh`.

`itervox run --issue <id>` runs one issue to completion and exits: the agent's
turns (same prompt, profiles, rules, memory, guardrails, `agent.budget`), the
reviewer quorum when `agent.auto_review` is on, then push, `gh pr create`, an
issue comment and the move to `tracker.completion_state`. No server, hub or
automations. The `vnovick/itervox/action` GitHub Action installs the release
binary (checksum-verified) and the agent CLI, checks the trigger is trusted,
and runs it.

## Before you write anything

- The repository has a committed schema-2 `WORKFLOW.md` (the `setup` skill)
  and `itervox doctor` passes locally.
- Tracker: GitHub Issues (`api_key: $GITHUB_TOKEN` uses the job's token, which
  the action exports) or Linear (needs a `LINEAR_API_KEY` secret).
- Keep `guardrails` on: they stop the agent pushing anywhere but its issue
  branch and merging its own PR. `WORKFLOW.md` decides what the agent may do;
  review it like code.

## 1. Secrets (the user adds them)

The user runs these, typing the value at `gh`'s prompt (never paste it here):

```bash
gh secret set ANTHROPIC_API_KEY     # Claude Code (or CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`)
gh secret set OPENAI_API_KEY        # Codex, if the action's agent input is codex
gh secret set LINEAR_API_KEY        # Linear tracker only
```

Pass the key as `env:` on the action step only, never on the job.

## 2. Labels (GitHub)

- The trigger label: `gh label create itervox`.
- The state labels (`todo` and the rest): `itervox doctor --fix`.

The issue must be in `tracker.active_states` (the `todo` label with the default
GitHub states), otherwise the run reports nothing to do.

## 3. Workflow: run when the `itervox` label is added

Pin the action and the binary to the newest release.

```yaml
# .github/workflows/itervox.yml
name: itervox
on:
  issues:
    types: [labeled]

permissions:
  contents: write
  issues: write
  pull-requests: write

jobs:
  run:
    if: github.event.label.name == 'itervox'
    runs-on: ubuntu-latest
    timeout-minutes: 75
    concurrency:
      group: itervox-${{ github.event.issue.number }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - id: itervox
        uses: vnovick/itervox/action@v0.3.1
        with:
          issue: ${{ github.event.issue.number }}
          version: 0.3.1
          timeout: 60m
          log-dir: ${{ runner.temp }}/itervox-logs
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      - if: always()
        uses: actions/upload-artifact@v4
        with:
          name: itervox-logs-${{ github.event.issue.number }}
          path: ${{ runner.temp }}/itervox-logs
          if-no-files-found: ignore
```

Copy the other two workflows from `guides/ci.mdx` as written:

- **`itervox-comments.yml`**: `/itervox <answer>` continues a run that asked a
  question (`answer:` input), `/itervox approve <command>` re-runs it with that
  guardrail command allowed (`approve:` input). Comment bodies go to the action
  as inputs, never into a `run:` script.
- **`itervox-nightly.yml`**: a `schedule` matrix over open issues labelled
  `itervox`, `max-parallel: 3`, using the same `concurrency` group.

Keep `concurrency: itervox-<issue>` in every workflow so two jobs never run the
same issue.

## Permissions: these three, nothing else

| Permission | Why |
|---|---|
| `contents: write` | push the issue branch |
| `pull-requests: write` | open the pull request |
| `issues: write` | comment on the issue, move its state labels |

`actions/checkout` keeps its credentials (the run pushes with them) and needs
`fetch-depth: 0`.

**Trust check** (`action/trust-check.sh`): the action runs only for a label
added by a user with write access, a comment by an `OWNER`, `MEMBER` or
`COLLABORATOR`, or a `schedule` / `workflow_dispatch`; never for a pull
request from a fork.

## Action inputs

`issue` (required), `agent` (`claude`, `codex` or `none`), `version` (pin it),
`answer`, `approve`, `timeout` (default `60m`), `workflow`, `log-dir`,
`fail-on-needs-input`, `github-token`, `dry-run`. Outputs: `status`,
`exit-code`, `pr-url`, `summary`.

## Exit codes

| Code | Meaning | Next step |
|---|---|---|
| `0` | PR opened, or nothing to do | Review the PR |
| `2` | Failed (agent failure, timeout, issue not found) | Read the step summary and the log artifact; re-run the job |
| `3` | Needs input: a question, or a guardrail approval (fails closed in CI) | Answer: `/itervox <answer>` or `itervox run --issue 12 --answer "Use Postgres"`. Approve: `/itervox approve vercel --prod` or `itervox run --issue 12 --approve "vercel --prod"` (that command, that run only) |
| `4` | Budget exceeded (`agent.budget`) | Raise the budget or split the issue |
| `5` | Configuration error | Run `itervox doctor` locally |

The step fails on 2, 4 and 5; exit 3 succeeds with `status: needs_input`
unless `fail-on-needs-input` is set. Approving a guardrail command is the
user's decision: do not post `/itervox approve` yourself.

## Without the action

Install the binary and the agent CLI, export the key, and from the checkout:

```bash
itervox run --issue "$ISSUE" --timeout 45m --log-dir logs
```

Branch on the exit code as above. `--worktree` works in a git worktree instead
of switching the checkout's branch; `--json` prints one JSON object (`status`,
`exit_code`, `pr_url`, …).
