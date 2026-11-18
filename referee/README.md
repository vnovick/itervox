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

On GitLab CI, CircleCI and Buildkite, use the templates in [`ci/`](ci/):
[`gitlab-ci.yml`](ci/gitlab-ci.yml),
[`circleci-config.yml`](ci/circleci-config.yml) (a setup config, with
[`circleci-continue-config.yml`](ci/circleci-continue-config.yml)) and
[`buildkite-pipeline.yml`](ci/buildkite-pipeline.yml). Each installs a pinned
`referee_<os>_<arch>` release, refused unless its SHA-256 matches both the
release's `checksums.txt` and `REFEREE_SHA256`, fetches the base branch, runs
`lanes` and `audit` with the Action's exit codes (`fail-on: human`), and hands
an exit 20 to the CI's own approval step: a manual job in a protected
environment (GitLab), a `type: approval` job (CircleCI), a `block` step
(Buildkite). `REFEREE_APPROVAL: none` fails the job with 20 instead. CircleCI
has no base-branch variable: the template uses `REFEREE_BASE_BRANCH` (default
`main`). `bash referee/ci/templates_test.sh` (`make referee-ci-test`) runs each
template's shell on the Action's fixture repository
([`testdata/fixture.sh`](testdata/fixture.sh)).

Every Itervox release ships it as `itervox referee <command>`. For a
referee-only install, build `make referee` (writes `bin/referee`) or
`go install github.com/vnovick/itervox/cmd/referee@<version>`; both run the
same code (`internal/referee`).
`bash referee/run_test.sh` tests the Action's step script.

Full guide: [site/src/content/docs/guides/referee.mdx](../site/src/content/docs/guides/referee.mdx).
