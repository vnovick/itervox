---
name: goal-loop-round
description: Use for non-trivial planning, analysis, auditing, or remediation work where being right matters more than being fast and a confident first pass is likely to hide gaps. Trigger when the user asks for an iterative gap hunt, adversarial self-review, "keep going until there are no gaps", "rounds", or asks you to harden a plan or analysis until it is airtight. Enforces a grounded Round 0, empirical falsification of every load-bearing claim, a completeness proof where units are countable, and termination only when a full round finds nothing new.
---

# goal-loop-round

An adversarial, self-correcting loop for work where a confident-but-wrong answer
is expensive: plans, audits, remediations, reconciliations. You produce a
grounded deliverable, then spend each later round attacking your **own previous
round**, running the claims it leans on rather than restating them, and stop
only when a whole round turns up nothing new.

**When not to use:** a mechanical edit, or work whose correctness one command
already settles. Do not ceremony-wrap a one-liner.

## The round protocol

### Round 0 — ground it

Build the initial deliverable from evidence gathered out of the repo, never from
memory. Every number traces to a source: a grep count, a file listing, command
output. A figure you cannot trace is not grounded yet, and saying "roughly" does
not fix that.

### Each later round — attack the previous round

Critique your **last round's output**, not the original task.

1. **Label gaps stably** — G1, G2, … Reuse a label when a later round deepens
   the same gap, so a reader can follow one thread through the log.
2. For each gap: state it precisely, state the fix, **apply** the fix to the
   deliverable, and log it.
3. **Run every load-bearing claim.** This is the whole loop. Anything the
   deliverable rests on gets confirmed or falsified by a real command before it
   ships.
4. **Keep the falsification trail inline.** When you rule a candidate out,
   record the command that killed it, so no later round re-tries it.
5. **Prove completeness where units are countable.** "All 47 rows fall into
   exactly one of 4 buckets; the buckets sum to 47; no row appears twice" is one
   falsifiable check that catches omissions *and* double-counting. Far stronger
   than "looks thorough".

### Termination

- A round that changed the deliverable **requires another round**. You cannot
  declare done on the same round you last edited.
- Terminate only when a full round finds **zero** new gaps.
- End with a summary table: `Round | Outcome | Gaps found`.

### Mechanizing a round — the `bug-hunt` workflow

When the goal is repo-wide defect discovery ("find every leak / race / dead
path in X"), a round's gap-hunt can be delegated to the `bug-hunt` workflow
(`.claude/workflows/bug-hunt.js`). Its finder fleet plus adversarial refuters
*are* steps 1-3 of a round, run at scale.

Mapping its return into the artifact:

| Workflow output | Artifact section |
|---|---|
| scope + commands run | **Probed** |
| `confirmed` | **Findings / Gaps**, with their evidence |
| `suspected` | open items — say plainly these are **not** evidence |
| `refutedTrail` | the falsification trail, verbatim |

Termination composes but does not shortcut: `exhaustive: true` means two dry
rounds *inside* the workflow and counts as one clean hunt — but a round that
materially changed the deliverable still requires a further confirmation round.
`exhaustive: false` is an open coverage gap and must be logged as one, never
reported as full coverage.

Note it spawns roughly `lenses × rounds` finders plus two refuters per fresh
finding. Scope it (`{scope: ["internal/orchestrator/"]}`) or narrow the lenses
rather than running the full fleet by reflex.

## Verify, don't assert — three failures from this repo

These are real, and each is a shape to watch for.

**A claim that reads as obviously true.** A design document stated that a
dependency-audit field was durable across passes. It was not: the per-tick audit
rebuilt the row from scratch and dropped it. A ten-line probe settled what
re-reading the prose never would have — a row refreshed two seconds earlier was
re-selected against a **one-hour** interval. The feature it enabled had been a
no-op since the day it landed.

**A test that had quietly stopped testing.** An ordering test still passed after
the bug it was written to catch was deliberately reintroduced. An unrelated
change had made its assertion reachable by a second route, so the assertion held
for the wrong reason. Running the suite proved nothing; *reintroducing the bug*
proved everything.

**Fixtures passing by accident.** Four tests were green only because two
zero-valued timestamps compared equal. Tightening an unrelated comparison
flipped all four to red — they had never been exercising the branch they named.

The rule those share: **a claim the work leans on is one command away from
confirmed or falsified, so run it.** "This fixes it", "these all live in that
package", "the counts reconcile", "the tests cover this" — each is a command,
not a judgement. And when the claim is about a *test*, the command is usually
"break the thing and watch it fail", not "run the suite".

## Anti-patterns — stop if you catch yourself here

| Anti-pattern | What it looks like | Do instead |
|---|---|---|
| **Assert without verify** | "This will work" / "those are false positives", no command run | Run it. Quote the output. Inference is not evidence. |
| **Rationalising instead of fixing** | A self-review notices a number that does not add up and writes a justification for why that is acceptable | Close the gap. A review that finds a discrepancy fixes it; it does not excuse it. |
| **Rabbit-holing** | Thrashing one sub-problem past two or three honest attempts | Stop. Record it as a bounded open investigation with its trail and a concrete next step. An honestly-scoped open item is not a logic gap. |
| **Declaring done on an edit round** | Calling it complete in the round you changed it | Run one more confirmation round. Done means a *clean* round. |
| **Ungrounded Round 0** | Figures from memory | Reconcile each against command output. |
| **Trusting a green suite** | "All tests pass, so it works" | Confirm the suite reaches the code in question. A suite that never executes the path is silent, not confirming. |

## The artifact

The loop's output is one durable markdown file holding the deliverable **and**
its rounds log. The log is what makes the reasoning auditable and what survives
context compaction — without it, a later round re-tries candidates an earlier
round already killed.

Put it where it names the goal: `planning/<topic>_pass.md` for a verification
hunt, `planning/<topic>_plan.md` for a remediation plan. Write to the structure
from Round 0 rather than retrofitting it.

Two shapes, chosen by what the goal is.

### Shape 1 — round log (prove a condition holds)

For *"prove X is true / green / safe"*. The file **is** the log; each round is a
`Probed / Findings / Verdict` block and each gap feeds the next round.

```markdown
# <Topic> — round log (<the goal condition, one line>)

> <the condition, and the baseline gate: the exact command whose output
> defines done — e.g. `make verify` exits 0>

## Round 0 — establish the baseline (run it; do not trust the claim)

**Probed:** <commands/sources run to ground the starting state>
**Findings:** <grounded facts, each traceable to output>
**Verdict:** <what is established; what Round 1 investigates>

## Round N — <short name>

**Probed:** <the load-bearing question this round tests>
**Findings:** <result of running it — quote real output or exit code>
**Fixes:** <gaps closed, applied to the deliverable>   *(omit if none)*
**Verification:** <command + real result proving the fix>  *(omit if none)*
**Gaps found (→ Round N+1):**
- **G<k> — <label>.** <precise statement + evidence>

<one line: material change → another round required; or clean → done>

### Round summary

| Round | Outcome | Gaps found |
|---|---|---|
| 0 | baseline established | — |
| N | <outcome> | G… |
```

### Shape 2 — deliverable with an embedded rounds log

For *"produce a plan or analysis and make it airtight"*. The file is the
deliverable — inventory, invariants, partition, phases — followed by a `Rounds`
log where each round critiques the content above it, lists gaps, applies fixes
to that content, and decides whether to continue.

```markdown
# <Deliverable title>

> <the goal, and the invariants that bound it>

## Inventory / grounding
<totals and tables — every number reconciled to a source>

## Invariants (read first)
1. <a constraint that, if violated, turns this work into a regression>

## Partition
<sort the countable units into buckets; prove the partition — counts sum to
 the known total, zero duplicates>

## Phase A … / Phase B …
<the plan content the rounds will harden>

---

# Rounds — review log

> Each round: critique the deliverable above, list gaps, apply fixes to it,
> decide if another round is needed. Done only on a round that finds nothing.

## Round 1 — initial deliverable
<how it was grounded>

## Round 2 — review of Round 1, with empirical verification
- **G1 (critical) — <label>.** <what was wrong; the command that falsified it;
  the fix applied above.>
<decision: material change → Round 3 required>

## Round K — terminal confirmation
<the decisive completeness check, run programmatically>
**No new gaps. Goal met.**

### Round summary

| Round | Outcome | Gaps found |
|---|---|---|
| 1 | initial grounded deliverable | — |
| K | completeness proof; no new gaps | none → done |
```

### What every artifact must satisfy

1. **Stable gap labels** that persist across rounds.
2. **A falsification trail, inline** — each ruled-out candidate recorded with
   the command that killed it.
3. **Commands, not reasoning** — a Findings or Verification line quotes real
   output or an exit code. "This should work" is not a finding.
4. **A completeness proof at termination** wherever units are countable.
5. **The summary table is mandatory and terminal** — the loop ends on a clean
   round, and that round is the last row. You cannot declare done on a row where
   you also made an edit.

One file may carry more than one rounds log when the deliverable has independent
sub-goals — each gets its own heading and its own summary table, each run to its
own clean round.

## Escalating

When a round's conclusion is a completion claim someone will act on, hand it to
the **verification-skeptic** agent rather than confirming it yourself. It
re-derives the evidence in fresh context and returns a fail-closed verdict — the
one check this loop structurally cannot perform on itself, because a loop
auditing its own output shares its own blind spots.
