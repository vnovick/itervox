# referee

Agents in CI can fix the build. Don't let them touch the pipeline.

`referee` is a standalone tool for projects that do not use Itervox, with any
agent harness and any CI. The rule: an agent may change **what** CI checks,
never **how** CI checks. The referee lives outside the branch (this Action's
pinned ref), reads its policy from the **base** branch
(`git show <base>:.referee.yml`, never the working tree), and the writer is
never the judge.

| Command | Does | Exit |
|---|---|---|
| `referee lanes --base <ref>` | classifies the diff into human / second_opinion / auto by `.referee.yml` on the base ref | 0 auto, 10 second_opinion, 20 human |
| `referee audit --base <ref>` | lists changes that weaken checks (deleted tests, `t.Skip`, `.only(`, lowered coverage, `continue-on-error: true`, `\|\| true`, `--no-verify`) | 0 none, 20 findings |
| `referee pin --policy-sha256 <hex>` | fails when the policy file's sha256 changed (`--print` prints it) | 0 match, 20 mismatch |
| `referee run --name N --key K --out F -- cmd` | runs a check and appends a signed record (real exit code, pipefail, tree hash) | the check's exit code |
| `referee keygen --out DIR` | writes the ed25519 key pair (`attest.key` 0600, `attest.pub`) | 0 |
| `referee verify F --pubkey P [--tree DIR]` | verifies the records' signatures and tree | 0 ok, 1 not verified |

Every error exits `2`, never `0`: referee fails closed.

```yaml
# .referee.yml on the default branch (absent: a default human lane of CI
# workflows, test-runner, coverage and lint config, CODEOWNERS, Makefile)
human: [".github/**", "Makefile", "jest.config.*"]
second_opinion: ["src/auth/**"]
```

The Action (`uses: vnovick/itervox/referee@<sha>`, inputs `mode`
lanes|audit|pin|verify, `base`, `policy-sha256`, `fail-on`
human|second_opinion|never) builds referee with `actions/setup-go` and runs one
mode. [`examples/referee-pr.yml`](examples/referee-pr.yml) is a `pull_request`
workflow that fails human-lane pull requests without a `pipeline-approved`
label, with the trade-offs of label approval written out.

Every Itervox release ships it as `itervox referee <command>`. For a
referee-only install, build `make referee` (writes `bin/referee`) or
`go install github.com/vnovick/itervox/cmd/referee@<version>`; both run the
same code (`internal/referee`).
`bash referee/run_test.sh` tests the Action's step script.

Full guide: [site/src/content/docs/guides/referee.mdx](../site/src/content/docs/guides/referee.mdx).
