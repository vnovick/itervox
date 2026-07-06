# Eval fixtures

Each `<profile>/<scenario>/` directory holds:

- `input.yaml` — the scenario setup. In recorded mode this file is
  documentation plus a staleness source (the recording must be newer than
  it); live mode will feed it to a real agent run.
- `expected.yaml` — the behavioral contract the judges enforce:
  `required_action_calls` / `forbidden_actions` against the transcript's
  action events, `marker_phrases` as substrings of its comment events.
- `recording.jsonl` — the transcript replayed by recorded mode. One JSON
  object per line: `{"event":"action","action":"<name>", ...}` or
  `{"event":"comment","body":"..."}`.

## Provenance — read before trusting a green run

The recordings here are **hand-authored behavioral contracts**, not captures
of real agent runs (live-recording mode is future work). They lock the
*expected transcript shape* — which actions a correct run calls, which it
must never call, which phrases its comments carry — so a prompt edit that
changes the contract fails `make evals-fast` and forces a deliberate fixture
update. They do NOT prove the current SOUL/INSTRUCTIONS actually produce
these transcripts; that is exactly what live-recording mode adds. When it
lands, re-record every scenario and delete this caveat.

Known judge limitation: marker phrases assert presence only. "The approve
path must NOT emit `/ai-approved` on a failing PR" is not expressible —
absence checks also wait for live mode.
