---
name: verify-before-done
description: Use before claiming any change is complete, before creating a commit or PR, or when the user asks "is this ready" / "are we done" / "can I merge" / "did it work". Also use when the user mentions test failures, coverage drops, races, or CI red. Enforces both halves of the evidence contract - the gates prove nothing regressed, named evidence proves the thing was actually built - with quoted output for each.
---

# verify-before-done

Evidence before assertions. Never claim "done", "fixed", "tests pass", "ready to
merge", or "CI will be green" without showing the exact command and its output
from the current session.

**Two different questions, two different kinds of evidence. You need both.**

| Question | Evidence | What it proves |
|---|---|---|
| Did anything regress? | The gates (`make verify`) | Nothing broke. **Not** that your change works. |
| Was the named thing built? | Per-criterion evidence | The specific acceptance is satisfied |

Conflating these is the most common way a false "done" ships here. A green
`make verify` on a branch that implements nothing is still green.

---

## Part 1 - the gates (did anything regress?)

### `make verify` is the source of truth

```bash
make verify
```

Runs in order: `fmt` → `vet` → `lint-go` (golangci-lint) → `test`
(`go test -race ./cmd/... ./internal/... -count=1`) → `web-coverage`
(`pnpm test:coverage`) → `web-build` → `web-spelling` (guards the old
"Symphony" name in user-visible strings) → `size-budget` → `no-os-exit`.

`go test ./...` alone is **not** sufficient. It misses golangci-lint failures,
races hidden by the test cache (no `-count=1`), frontend regressions and
coverage drops, and the spelling guard.

Must exit 0. Quote the exit code, not a vibe.

### Read the exit code, not the tail

```bash
make verify | tail -20        # WRONG - reports tail's status, not make's
```

A piped failure reads as success. Do one of:

```bash
set -o pipefail; make verify 2>&1 | tail -20
make verify >/tmp/v.log 2>&1; echo "exit=$?"; grep -ciE '^(FAIL|Error:)' /tmp/v.log
```

Quote the exit line. This trap has produced false "green" claims in this repo.

### Frontend coverage

70% threshold on **all four** axes - statements, branches, functions, lines.
Faster inner loop: `make web-coverage`. A green `pnpm test` says nothing about
whether the coverage gate passes. A new file at 0% means the test was never
written.

### The race detector is sampling, not proof

`go test -race` samples at runtime; a race can hide for many runs. When touching
`internal/orchestrator/` or any new concurrent code:

```bash
go test -race -count=5 ./internal/orchestrator/...
```

An intermittent race is a **real race**, not flakiness. Confirm with `-count=10`
and fix the root cause - never rerun until green. The `retry_test.go`
`callCount` race sat latent for months before it fired.

### `govulncheck` on dependency changes

If you touched `go.mod`, `go.sum`, or any Go dependency:

```bash
govulncheck -tags dev ./cmd/... ./internal/...
```

`-tags dev` matches the CI job in `.github/workflows/ci-go.yml`.

### Establish the baseline yourself

Do not assume a clean tree, and do not trust a frozen list of known failures -
those rot. Run the gates **before** your change, or on the merge base. Anything
failing both before and after is pre-existing: do not claim you introduced it,
and do not claim you fixed it without a quoted passing run.

---

## Part 2 - named evidence (was the thing actually built?)

CLAUDE.md's "Verification before completion" section is the contract. A green
gate is **explicitly rejected** as evidence for a named criterion. For every
acceptance item, produce one of:

**Named-test execution** - anchored, always:

```bash
go test -race -run '^TestFooBar$' ./internal/orchestrator/... -v
```

Output must contain `--- PASS: TestFooBar`. `[no tests to run]` means the test
does not exist - the criterion is UNMET no matter how complete the code looks.
An unanchored `-run TestFoo` proves nothing about the named test existing.

**Symbol-presence grep** - and then the site that *uses* it:

```bash
grep -rn 'DepsRefreshGeneration\b' internal/
```

Presence proves declaration only. For a behaviour claim, also grep the read
site, call site, or increment site. A field nothing reads and a counter nothing
increments are both UNMET.

**Runtime output** - run the command, quote the line that satisfies the
criterion.

**Endpoint / file shape** - `curl … | jq` and quote the response shape, or read
the path and quote the `file:line` that matches verbatim.

### Mandatory annotation

Every item marked done carries a `Verified by:` line with the command and a
one-line excerpt:

```
- [x] Add DepsRefreshGeneration to invalidate abandoned batches
  Verified by: grep -rn 'DepsRefreshGeneration\b' internal/
             → 4 hits: state.go:376 (decl), dependency_refresh.go:509 (watchdog bump),
               :537 (launch bump), :311 (handler guard)
             go test -race -run '^TestReconcileDependencyRefreshWatchdogBumpsGeneration$' ./internal/orchestrator/... -v
             → --- PASS
```

No `Verified by:` → not done. If the acceptance is too vague to verify,
escalate to clarify what counts as evidence before marking anything.

---

## Part 3 - when a test is the evidence, prove it can fail

A passing test is only evidence if it would fail when the behaviour breaks. In
this repo, four separate times, it would not have:

- The AUTO-1 flap tests stayed green through three real regressions, because
  they call the audit function directly and never traverse the refresh path.
- An ordering test kept passing after the bug it was written for was
  deliberately reintroduced.
- Four tests passed only because two zero-valued timestamps compared equal;
  tightening an unrelated comparison flipped all four to red.
- An interval test passed while the feature was a no-op, because its fixtures
  never went through the code path that broke it.

So when a criterion rests on a test you or a subagent just wrote:

**Delete the line under test, or revert the fix, and confirm the test fails.**
Then restore it and confirm it passes. Quote both. A test that passes either way
is not evidence - it is decoration.

Also refuse these, per CLAUDE.md:

- **Symbol exists, nothing calls it** - grep the call site.
- **Test body is `t.Skip`** - read the body before quoting a PASS.
- **One sub-criterion of N** - done means every bullet.
- **Route registered, handler returns 501** - verify the success path does the work.
- **Component imported, never rendered** - grep the JSX usage, not the import.
- **Config field declared, never read** - grep the read.

---

## Part 4 - escalate the high-risk claims

For anything non-trivial - concurrency, `internal/orchestrator/`, state
mutations, auth, or multi-criterion work - dispatch the **`verification-skeptic`**
agent with the claim and its acceptance criteria. It re-derives the evidence in
fresh context and returns a fail-closed CONFIRMED / NOT CONFIRMED verdict.

It exists because this session's own optimism is the thing least able to audit
itself. Treat any completion claim from another agent, a tool, or the user as a
**claim**, and verify it independently.

---

## Exit criteria

Claim done only when all of these hold, each with output shown:

- `make verify` exited 0 this session - exit code quoted, not inferred from tail
- Frontend coverage shows all four axes ≥ 70%
- (Go deps touched) `govulncheck -tags dev ./cmd/... ./internal/...` exited 0
- **Every** named acceptance item has a `Verified by:` annotation
- Any test serving as evidence was shown to fail when the behaviour is broken
- `git status` reviewed - no `.env`, credentials, or stray binaries

Note on committing: this repo denies `Bash(git commit*)` in
`.claude/settings.json`. Stage nothing and commit nothing - report the verified
state and let the user run the commit themselves.

## Known failure signatures

- `go: go.mod requires go >= X.Y.Z` during `make verify` - the Makefile's
  `GOTOOLCHAIN` is out of sync with `go.mod`'s `go` directive. Bump both.
- Coverage `0%` on a new file - no test exists. Write it.
- ESLint red while `pnpm test` is green - lint still blocks CI. Fix it; do not
  blanket-disable.
- `[no tests to run]` - the named test does not exist. UNMET, not a pass.
- Race passes once, fails on rerun - real race. `-count=10`, then fix the cause.
