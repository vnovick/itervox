# Profile evals

`internal/evals` checks that agent profiles behave as their contract says:
which daemon actions a run calls, which it must never call, which phrases its
comments carry and which they must never carry. Every scenario is a fixture
directory under `internal/evals/fixtures/<profile>/<scenario>/`:

| File | Purpose |
|---|---|
| `input.yaml` | The setup: the issue (`identifier`, `state`, `title`, `body`, `branch_name`), the triggering `comment.body`, and the PR state (`pr.number`, `pr.labels`, `pr.required_checks`, `pr.candidates`, `pr.diff`). |
| `expected.yaml` | The contract: `required_action_calls`, `forbidden_actions`, `marker_phrases` (each must appear in some comment) and `forbidden_phrases` (none may appear in any comment). |
| `recording.jsonl` | The promoted transcript recorded mode replays. |
| `recording.candidate.jsonl` | A re-recording waiting for review (never read by recorded mode). |

There are two modes.

## Recorded mode — the free gate

`make evals-fast` (part of `make verify`) replays every `recording.jsonl` and
runs the judges. It costs nothing and takes under a second. It proves the
transcripts still satisfy their contracts; it does not prove the current
prompts produce those transcripts. That is what live mode is for.

## Live mode — what the agent actually does

Live mode runs each scenario against a real backend CLI (`claude` or `codex`)
and judges the transcript with the same judges.

```bash
ITERVOX_EVALS_LIVE=1 make evals-live                                  # every scenario
ITERVOX_EVALS_LIVE=1 make evals-live EVALS_PROFILE=reviewer           # one profile
ITERVOX_EVALS_LIVE=1 make evals-live EVALS_SCENARIO=red-ci-approval   # one scenario
```

It spends tokens, so it refuses to start unless `ITERVOX_EVALS_LIVE=1` is set
(exactly `1`). `make verify` never sets it.

**Which prompt.** The run loads `internal/evals/project/WORKFLOW.md` (override
with `EVALS_WORKFLOW=/path/to/your/WORKFLOW.md` to evaluate your own profiles)
with the daemon's config loader and composes the prompt with the daemon's own
assembly, `orchestrator.ComposeEvalPrompt`, which shares its profile section
with the worker: the WORKFLOW.md body, the operator reply envelope, the run
context, the resolved rules layers (`~/.itervox/rules.json` and
`.itervox/rules.json` next to the workflow), the profile's SOUL.md and
INSTRUCTIONS.md, the automation's instructions with the trigger comment, and
the daemon action notice. Editing a profile's SOUL.md or INSTRUCTIONS.md
changes the next live run. The eval project uses the shipped built-in
`merge-bot` and a `reviewer` whose files are in
`internal/evals/project/agents/reviewer/` (the merge-handshake reviewer from
the automations guide).

**The sandbox.** Each scenario runs in a throwaway directory with two shims
first on the agent's `PATH`:

- `itervox` (also `$ITERVOX_BIN`) answers `itervox action ...` like the action
  CLI does and records the call. No daemon is involved.
- `gh` answers `pr list`, `pr view`, `pr checks` and `pr diff` from
  `input.yaml`, records writes (`pr review`, `pr comment`, `pr merge`,
  `api`, ...), and refuses anything else.

`GH_TOKEN`, `GITHUB_TOKEN` and `LINEAR_API_KEY` are blanked in the agent's
environment. The agent still runs with its profile's permission mode in that
directory, like a real worker, so run live mode on a machine where that is
acceptable.

**The transcript.** Calls become recording events:
`itervox action comment --body X` is `{"event":"action","action":"comment"}`
followed by `{"event":"comment","body":"X"}`; `move-state`, `merge-pr` and
the others are actions named in snake case (`move_state`, `merge_pr`); `gh pr
review --body X` is the action `gh_pr_review` plus a comment `X`; `gh pr
merge` is `gh_pr_merge` (forbidden for both profiles, which merge only through
the daemon). A live recording starts with a `meta` line (backend, time,
`prompt_sha256`, rules hash, tokens, cost) and ends with a `result` line
holding the agent's last message. Recorded mode skips both, and the last
message is never judged as a comment: only what the agent posted through a
tool counts.

**Budget.** Every run is bounded:

| Knob | Default | Meaning |
|---|---|---|
| `EVALS_MAX_TURNS` | 12 | `claude --max-turns`; codex has no such flag, the token cap and timeout bound it |
| `EVALS_MAX_TOKENS` | 400000 | per scenario, input + cached + output; the run is cancelled when it crosses the cap |
| `EVALS_MAX_COST_USD` | 3 | the whole suite; once spent, the remaining scenarios fail without starting |
| `EVALS_TIMEOUT` | 5m | per scenario wall clock; the agent's process group is killed |

`EVALS_COMMAND` runs a different CLI command (for example an absolute path),
and `EVALS_BACKEND=codex` selects the codex runner. Transcripts go to a temp
directory (or `EVALS_OUT`); the fixtures are not touched.

## Re-recording

```bash
ITERVOX_EVALS_LIVE=1 make evals-record     # writes recording.candidate.jsonl next to each fixture
git diff --no-index internal/evals/fixtures/reviewer/approves-with-marker/recording.jsonl \
  internal/evals/fixtures/reviewer/approves-with-marker/recording.candidate.jsonl   # review each one
make evals-promote SCENARIO=reviewer/approves-with-marker    # or SCENARIO=all
make evals-fast
```

`evals-record` never writes `recording.jsonl`, and a run that fails (budget,
timeout, backend error) leaves no candidate. `evals-promote` judges the
candidate against `expected.yaml` first and refuses one that fails; `FORCE=1`
promotes it anyway (for a deliberate contract change you still update
`expected.yaml` yourself). The rename is atomic.

The same commands are `go run ./cmd/itervox-evals recorded|live|record|promote`; run
`go run ./cmd/itervox-evals live -h` for the flags.

## Saved reports and the dashboard

`make evals` judges the committed recordings like `make evals-fast`, through
`go run ./cmd/itervox-evals recorded`, and saves the report. `make evals-live`
and `make evals-record` save theirs too (#201). Each report is JSON at
`.itervox/evals/<ISO8601>.json` (for example `20261021T093015.250Z.json`; set
`EVALS_RESULTS_DIR`, or `-results-dir` on the CLI, `""` to save nothing). The
directory gets its own `.gitignore` and only the newest 20 reports are kept:
it is local runtime state, never committed. `make evals-fast` (in `make
verify`) saves nothing.

A report lists, per scenario, pass or fail, every failed assertion (missing
or forbidden actions, `forbidden_phrases` present, marker phrases missing),
the live run's error, tokens and cost, and the **prompt digest** the scenario
ran against. It is the digest run history records for dispatched runs (#186,
SHA-256 over SOUL.md, INSTRUCTIONS.md, the resolved rules and the prompt
template, `agent.reviewer_prompt` for a reviewer profile), computed by
`orchestrator.ComposeEvalPrompt` from the workflow the live run used, so an
eval result and the dashboard's Prompt versions name the same version. Live
recordings also carry it in their `meta` line (`prompt_digest`). Recorded
mode replays a fixed transcript: it reports the digest only when the
recording was made by a live run since #201 (`digestSource: "recording"`);
the hand-written fixtures have none.

The dashboard's **Repo Analysis → Evals** section (`/repo#evals`, from
`GET /api/v1/repo/evals`) reads the newest reports of the project whose
`WORKFLOW.md` the daemon runs and shows the latest result per profile and
scenario, its failed assertions, its digest and when it ran. A case that
fails on the current digest after passing on the previous one is marked
**regression**. Run the evals from the project root (where `make` writes
`.itervox/evals/`); the dashboard never starts a run.

## CI

`.github/workflows/evals-live.yml` runs the live suite on demand (Actions →
"Evals (live)" → Run workflow): choose `live` or `record`, the backend, the
profile or scenario, and the budget. It reads `ANTHROPIC_API_KEY` (claude) or
`OPENAI_API_KEY` (codex) from the repository secrets, never runs on push or
pull request, and uploads the transcripts — and in `record` mode the
candidates — as an artifact. It never commits; promote locally after review.
A nightly schedule is in the file, commented out.

## Limits

- One agent turn per scenario (the CLI's own tool loop runs inside it, up to
  `max-turns`).
- The `gh` shim lists every scenario PR for `pr list` regardless of `--head`,
  so a profile must pick the PR by `headRefName` itself (the
  `multiple-matching-prs` scenario tests exactly that).
- Phrase checks are substring matches; an LLM judge for prose is not built.
