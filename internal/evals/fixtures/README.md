# Eval fixtures

Each `<profile>/<scenario>/` directory holds:

- `input.yaml` — the scenario setup: the issue, the triggering comment and
  the PR state. Recorded mode only uses it as a staleness source (the
  recording must be newer than it); live mode feeds it to a real agent run
  (the issue and comment through the composed prompt, the PR through the
  sandbox's `gh` shim).
- `expected.yaml` — the behavioral contract the judges enforce:
  `required_action_calls` / `forbidden_actions` against the transcript's
  action events, `marker_phrases` as substrings that must appear in its
  comment events, and `forbidden_phrases` as substrings that must appear in
  none of them (absence checks — e.g. the reviewer's reject path must never
  carry `/ai-approved`).
- `recording.jsonl` — the transcript replayed by recorded mode. One JSON
  object per line: `{"event":"action","action":"<name>", ...}` or
  `{"event":"comment","body":"..."}`. A live-recorded file also starts with
  a `{"event":"meta","source":"live",...}` line and ends with a
  `{"event":"result",...}` line; recorded mode skips both.
- `recording.candidate.jsonl` (transient) — a re-recording waiting for a
  human to review and promote. Recorded mode never reads it.

How live mode, re-recording and the CI job work: [docs/evals.md](../../../docs/evals.md).

## Provenance — read before trusting a green run

The recordings here are still **hand-authored behavioral contracts**, not
captures of real agent runs. They lock the *expected transcript shape* —
which actions a correct run calls, which it must never call, which phrases
its comments carry and which they must not — so a change that breaks the
contract fails `make evals-fast`. They do NOT prove the current
SOUL/INSTRUCTIONS actually produce these transcripts.

Live mode now exists to close that gap; what is left is running it with real
tokens, which only the maintainer does. To replace every recording with a
live capture:

1. `ITERVOX_EVALS_LIVE=1 make evals-record` (or run the "Evals (live)"
   workflow with `mode: record` and download the candidates artifact into
   these directories).
2. Review each `recording.candidate.jsonl` against its `recording.jsonl` and
   `expected.yaml`. A candidate that fails its judges is a real finding:
   fix the profile's INSTRUCTIONS.md (or, for a deliberate contract change,
   `expected.yaml`) and record again.
3. `make evals-promote SCENARIO=all`, then `make evals-fast`.
4. Once every `recording.jsonl` starts with a `"source":"live"` meta line,
   delete this section.
