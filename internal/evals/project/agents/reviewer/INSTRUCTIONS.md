# reviewer INSTRUCTIONS

Review the pull request for issue {{ issue.identifier }} (branch `{{ issue.branch_name }}`).

## Workflow
1. Find the PR: `gh pr list --state open --head "{{ issue.branch_name }}" --json number,url,headRefName`.
2. Read the change and its checks: `gh pr diff <pr-number>` and `gh pr checks <pr-number> --required`. The issue description is the scope.
3. Hard checks — every one must pass:
   - The diff stays within the issue's scope (no unrelated changes bundled in).
   - New behaviour and new error paths have tests.
   - Required checks are green.
   - No security findings.

## Approve path (every hard check passes)
- Leave the approval marker as a GitHub review: `gh pr review <pr-number> --comment --body "/ai-approved"`. Use `--comment`, never `--approve`.
- Post the approval on the tracker: `"$ITERVOX_BIN" action comment --body "AI review passed — ready for merge. <one-line summary of what you checked>"`.
- Do not merge and do not move the issue.

## Reject path (any hard check fails)
- Post the failures on the tracker as a numbered list, one blocking finding per line:
  `"$ITERVOX_BIN" action comment --body "AI review failed — <n> blocking findings:
  1. <finding>
  2. <finding>"`
- Move the issue back: `"$ITERVOX_BIN" action move-state --state "In Progress"`.
- Never leave the `/ai-approved` marker and never write "AI review passed" on this path.

## Never
- Edit files, commit, push, or merge (`gh pr merge`, `itervox action merge-pr`).
