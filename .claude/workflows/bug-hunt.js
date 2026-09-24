export const meta = {
  name: "bug-hunt",
  description:
    "Multi-lens Go defect hunt (concurrency, correctness, leaks, silent failures, test integrity, DRY) with adversarial verification, looping until two consecutive rounds find nothing new.",
  whenToUse:
    'Repo-wide or diff-scoped defect discovery in the Go tree; the mechanized round engine for goal-loop-round hunts. Args: { scope?: string[] | "diff", diffBase?: string, lenses?: string[], maxRounds?: number }.',
  phases: [
    { title: "Seed", detail: "deterministic signal: vet, lint, race, greps" },
    { title: "Hunt", detail: "one finder per lens per round, until dry" },
    { title: "Verify", detail: "two adversarial refuters per fresh finding" },
  ],
};

// Doctrine (CLAUDE.md "Gap analysis - avoiding false positives"): a finder's
// output is RECON, never a claim. Only findings that survive adversarial
// refutation ship, each with file:line and quoted code. Refuted findings are
// returned too - they are goal-loop-round's falsification trail and the dedup
// memory that stops a later round re-finding them.

// Lenses are scoped to what actually breaks in THIS repo. Generic OWASP-style
// sweeps waste verifier budget on a single-binary local daemon.
const LENSES = {
  concurrency:
    "violations of the single-goroutine state machine: any write to orchestrator.State from a goroutine that is not the event loop (workers must send OrchestratorEvent instead); reads of cfgMu-guarded cfg fields without the lock, or NEW mutable cfg fields missing from the guard list; snapshot access holding cfgMu; goroutines started without WaitGroup registration or without an exit path; channel sends that can block forever with no select/timeout fallback; a mutex held across a channel send or a subprocess call",
  correctness:
    "Go-specific logic defects: writes to a nil map; append aliasing a shared backing array; a loop variable captured by a goroutine or closure; err shadowed by := in an inner scope; time boundary errors - .After/.Before where the equality case is the bug, or a zero time.Time compared as if meaningful; integer conversion truncation; defer inside a loop accumulating until return; off-by-one in slicing; a struct copied by value when the caller expects mutation",
  "resource-leaks":
    "goroutines with no path to return (blocked forever on a channel or waiting on a context nobody cancels); context.WithCancel/WithTimeout whose cancel is never called on some path; time.NewTicker/NewTimer never Stopped; response bodies, files or rows never Closed; maps or slices on long-lived structs that only ever grow with no prune/janitor; subprocesses started without a kill path",
  "silent-failures":
    "errors discarded with _ = where the failure is meaningful; an error logged and then execution continuing as if it succeeded; a returned error swallowed by a bare return; errors.Is/errors.As used where the sentinel is never wrapped with %w (so the check can never match); a fallback default substituted for a failed fetch with no signal to the caller; error text that loses the original cause",
  "test-integrity":
    "tests that cannot fail: assertions on values the test itself just constructed; a fixture whose zero values make the assertion true by accident; a test whose name claims a behaviour its fixtures never route through (e.g. it calls the function directly and never traverses the code path under test); t.Skip or an empty body; a test asserting only that a symbol exists rather than that it does anything; table cases with identical inputs; missing -race coverage on a concurrent path",
  dry: "substantive duplicated logic across internal/ and cmd/ - the same algorithm, validation, comparator or state transition re-implemented in two places where they can silently diverge. NOT cosmetic similarity, NOT idiomatic Go repetition (err != nil blocks, struct literals). Propose the shared home for each.",
};

// itervox-specific false-positive filters, injected into every finder AND every
// refuter. These come straight from CLAUDE.md "Gap analysis" and exist because
// each one has previously produced a confident, wrong finding.
const FALSE_POSITIVE_DOCTRINE = `
Before reporting, rule these out - each has produced a wrong finding here before:

- DATA-RACE claim on a cfg field: grep EVERY write site. If no HTTP setter
  exists, there is no concurrent writer and no race. Only the fields in
  CLAUDE.md's cfgMu list need the lock; all other cfg fields are read-only after
  startup and need none.
- TIMEOUT claim (\`context.WithTimeout(ctx, 0)\`): check which parser the field
  uses. positiveIntField replaces <=0 with the default so 0 cannot reach
  runtime; intField lets it through, and for some fields <=0 DELIBERATELY
  disables the feature. Read config.go before claiming.
- "USES LIVE CONFIG INSTEAD OF SNAPSHOT": read the function BODY, not the
  signature. A parameter named cfg may exist while the decision point actually
  reads the snapshotted state field.
- FILE-MISSING claim: ls the path first. Lazy route imports resolve to real files.
- ALREADY-FIXED claim: read the current file. Do not report from a stale memory
  of how the code used to look.
- CLAUDE.md's "Known dead code" section lists what is deliberately unused. Check
  it before reporting dead code.
`;

const FINDINGS_SCHEMA = {
  type: "object",
  required: ["findings"],
  properties: {
    findings: {
      type: "array",
      items: {
        type: "object",
        required: ["title", "file", "line", "evidence", "severity"],
        properties: {
          title: { type: "string", description: "one-line defect statement" },
          file: { type: "string" },
          line: { type: "integer" },
          evidence: {
            type: "string",
            description: "quoted code plus why it is a defect",
          },
          severity: { type: "string", enum: ["blocking", "major", "minor"] },
        },
      },
    },
  },
};

const VERDICT_SCHEMA = {
  type: "object",
  required: ["refuted", "reason"],
  properties: {
    refuted: { type: "boolean" },
    reason: {
      type: "string",
      description: "the decisive evidence, with file:line",
    },
  },
};

// Workflow footgun: args can arrive stringified.
const input = typeof args === "string" ? JSON.parse(args) : args || {};
const rawScope = input.scope || ["internal/", "cmd/"];
const scope =
  typeof rawScope === "string" && rawScope !== "diff"
    ? [rawScope]
    : Array.isArray(rawScope) && rawScope.length === 0
      ? ["internal/", "cmd/"]
      : rawScope;
const diffBase = input.diffBase || "main";
const asArr = (v, d) => (v == null ? d : Array.isArray(v) ? v : [v]);
const lensArg = asArr(input.lenses, null);
const lensKeys = lensArg && lensArg.length ? lensArg : Object.keys(LENSES);
// 6 lenses x 3 rounds is already a large fleet before verifiers; raise
// deliberately, not by default.
const maxRounds = input.maxRounds || 3;

const scopeDesc =
  scope === "diff"
    ? `ONLY the change under review: run \`git diff ${diffBase}...HEAD\` for the diff and the changed-file list, and ALSO grep for unchanged CALLERS of any changed exported symbol - diff-only scope misses breakage in callers that did not change.`
    : `these paths: ${scope.join(", ")}. Go source only; skip _test.go unless the lens is test-integrity, and skip web/node_modules and generated files.`;

phase("Seed");
const seed = await agent(
  `Read-only. Gather deterministic defect signal for ${scopeDesc}

Run these and report raw hits with file:line (skip gracefully what is missing, noting it):
- \`go vet ./cmd/... ./internal/...\`
- \`golangci-lint run ./cmd/... ./internal/...\`
- \`go test -race -count=1 ./cmd/... ./internal/...\` - capture the CURRENT baseline. Pre-existing failures are signal, not findings; label them clearly as baseline.
- cheap greps for the classic Go smells: \`go func(\` near loop bodies; \`_ = \` on calls returning error; \`time.NewTicker\`/\`NewTimer\` without a nearby Stop; \`context.WithCancel\`/\`WithTimeout\` without a nearby cancel; \`.After(\` on time comparisons (the equality case is a known bug class here); \`t.Skip\`; empty \`if err != nil {}\` bodies.

Note: the canonical package list in this repo is \`./cmd/... ./internal/...\`, NOT \`./...\` - the latter walks web/node_modules.

Return a compact summary of raw hits. Signal for downstream finders, not conclusions. No opinions, no filtering beyond obvious noise.`,
  { label: "seed:deterministic", phase: "Seed", effort: "low" },
);

const seen = new Set();
const confirmed = [];
const suspected = [];
const refutedTrail = [];
const key = (f) => `${f.file}:${(f.title || "").toLowerCase().slice(0, 60)}`;

let dry = 0;
let round = 0;
while (dry < 2 && round < maxRounds) {
  if (budget.total && budget.remaining() < 60_000) {
    log(
      `Budget floor reached after round ${round} - stopping early; coverage is PARTIAL.`,
    );
    break;
  }
  round += 1;

  const found = (
    await parallel(
      lensKeys.map(
        (lens) => () =>
          agent(
            `Read-only Go defect finder, lens = ${lens}. Hunt in ${scopeDesc}

Look ONLY for: ${LENSES[lens]}

${FALSE_POSITIVE_DOCTRINE}

Deterministic seed signal (verify before trusting it):
${(seed || "(none)").slice(0, 4000)}

Already-known findings - do NOT re-report these or trivial variants:
${[...seen].slice(-150).join("\n") || "(none yet)"}${seen.size > 150 ? `\n(...and ${seen.size - 150} earlier findings omitted - prefer NEW files and regions)` : ""}

Round ${round}${round > 1 ? " - earlier rounds took the obvious ones; dig where they would not have looked: less-trafficked files, cross-file interactions, the edges of the scope" : ""}.

Every finding needs file:line and quoted code. If you cannot point at the line, it is not a finding. An empty list is a valid and good answer.`,
            {
              label: `find:${lens}:r${round}`,
              phase: "Hunt",
              schema: FINDINGS_SCHEMA,
            },
          ).then((r) => (r ? r.findings.map((f) => ({ ...f, lens })) : [])),
      ),
    )
  ).flat();

  let fresh = found.filter((f) => !seen.has(key(f)));
  fresh.forEach((f) => seen.add(key(f)));
  log(`Round ${round}: ${found.length} reported, ${fresh.length} fresh`);
  if (fresh.length === 0) {
    dry += 1;
    continue;
  }
  dry = 0;

  // Cap per-round verification fan-out (2 refuters each). Capped findings are
  // NOT lost - they stay in `seen` and are reported as suspected/unverified.
  const VERIFY_CAP = 24;
  if (fresh.length > VERIFY_CAP) {
    const dropped = fresh.slice(VERIFY_CAP);
    suspected.push(
      ...dropped.map((f) => ({
        ...f,
        round,
        verdicts: ["unverified - over per-round verify cap"],
      })),
    );
    log(
      `Round ${round}: verify cap ${VERIFY_CAP} hit - ${dropped.length} findings recorded as suspected/unverified`,
    );
    fresh = fresh.slice(0, VERIFY_CAP);
  }

  const judged = await parallel(
    fresh.map(
      (f) => () =>
        parallel([
          () =>
            agent(
              `Adversarial refuter. Try to prove this is a FALSE POSITIVE by reading the actual code and its context - callers, guards upstream, types or invariants that make the case impossible.

${f.file}:${f.line} [${f.lens}] ${f.title}
Claimed evidence: ${f.evidence}

${FALSE_POSITIVE_DOCTRINE}

refuted=true if the defect cannot occur as claimed. Default to refuted=true when uncertain.`,
              {
                label: `refute:${f.file.split("/").pop()}`,
                phase: "Verify",
                schema: VERDICT_SCHEMA,
              },
            ),
          () =>
            agent(
              `Reproduction-lens verifier. Construct the CONCRETE failure scenario for this claimed defect: exact input or state, then the wrong output, panic, race, or leak. Read ${f.file}:${f.line} and trace it.

[${f.lens}] ${f.title} - ${f.evidence}

If the claim is testable, say precisely which test would catch it (anchored name and package). If a race is claimed, say whether \`go test -race -count=5\` on that package would surface it.

refuted=true if no concrete failure scenario exists; put the scenario, or why none exists, in reason.`,
              {
                label: `repro:${f.file.split("/").pop()}`,
                phase: "Verify",
                schema: VERDICT_SCHEMA,
              },
            ),
        ]).then((votes) => ({ f, votes: votes.filter(Boolean) })),
    ),
  );

  for (const { f, votes } of judged.filter(Boolean)) {
    const refutations = votes.filter((v) => v.refuted);
    const entry = { ...f, round, verdicts: votes.map((v) => v.reason) };
    if (votes.length < 2) {
      // Fail closed: "confirmed" requires BOTH verdicts. A lone
      // non-refutation is not survival of the panel.
      suspected.push({
        ...entry,
        verdicts: [
          `only ${votes.length}/2 verifiers returned - fail closed, unverified`,
          ...entry.verdicts,
        ],
      });
      continue;
    }
    if (refutations.length === votes.length) {
      refutedTrail.push({
        ...entry,
        killedBy: refutations.map((v) => v.reason),
      });
    } else if (refutations.length > 0 || f.lens === "resource-leaks") {
      // Split vote -> suspected. Leak findings are ALWAYS capped at suspected:
      // confirming a goroutine or memory leak needs a runtime profile
      // (`go test -race`, pprof, a goroutine count before/after), not reading code.
      suspected.push(entry);
    } else {
      confirmed.push(entry);
    }
  }
  log(
    `Round ${round}: ${confirmed.length} confirmed, ${suspected.length} suspected, ${refutedTrail.length} refuted so far`,
  );
}

if (round >= maxRounds && dry < 2) {
  log(
    `Stopped at maxRounds=${maxRounds} without a dry round - coverage is NOT exhaustive.`,
  );
}

// confirmed:    survived both refuters - reportable, with file:line evidence.
// suspected:    split vote, a leak heuristic, or over the verify cap - needs a
//               human or a runtime check. NOT evidence; say so when reporting.
// refutedTrail: the falsification trail - feeds goal-loop-round's rounds log so
//               no later round, or person, re-tries a ruled-out candidate.
return {
  confirmed,
  suspected,
  refutedTrail,
  rounds: round,
  exhaustive: dry >= 2,
};
