# Release checks

Run these before tagging a release, in addition to CI (`make release-check` and
the green pipeline). The first two need real accounts, so CI cannot run them on
every change; the third runs in CI already.

| Check | What it proves | How | Cost |
|---|---|---|---|
| [Self-hosted issue run](#1-self-hosted-issue-run) | Itervox, built from the release commit, completes a real issue on this repository with a real agent and opens a pull request | `Release acceptance` workflow, `check: self-hosted` (or the script locally) | Agent usage for one small issue |
| [Cloud deploy smoke](#2-cloud-deploy-smoke) | The deploy kit provisions a VM, bootstraps the release, passes `itervox doctor --deploy`, serves the dashboard, and tears down | `Release acceptance` workflow, `check: deploy-gcp` (or the script locally), after the tag exists | **Billable:** a small GCE VM, a 100 GB disk and Cloud NAT for ~15 minutes |
| [Upgrade from the previous release](#3-upgrade-from-the-previous-release) | A project the previous release left behind upgrades and keeps its state | `TestUpgradeFromV021`, in CI | none |

Record each result (the run link, or the artifact) in the release's tracking issue.

The Mac app and CLI are signed, notarized and checked with `spctl` by the
release workflow itself; its secrets, the updater key and the commands to
verify a build by hand are in [release-mac.md](release-mac.md).

## One-time setup

Create a GitHub environment named **`acceptance`** (Settings → Environments) with
**required reviewers**, so every run is approved by a maintainer, and give it:

| Name | Kind | Used by |
|---|---|---|
| `ANTHROPIC_API_KEY` *or* `CLAUDE_CODE_OAUTH_TOKEN` | secret | both: the agent; `doctor --deploy` requires an agent credential |
| `GCP_WORKLOAD_IDENTITY_PROVIDER` | secret | deploy-gcp: `projects/<n>/locations/global/workloadIdentityPools/<pool>/providers/<provider>` |
| `GCP_SERVICE_ACCOUNT` | secret | deploy-gcp: the service account the workflow impersonates |
| `GCP_PROJECT_ID` | variable | deploy-gcp: a project used only for this check |

The service account needs, on that project: Compute Instance Admin (v1), Compute
Network Admin (router and NAT), Compute Security Admin (the IAP firewall rule),
IAP-secured Tunnel User, and Service Account User. Use a dedicated project:
the check creates and deletes resources there.

For the self-hosted run, allow **Settings → Actions → General → "Allow GitHub
Actions to create and approve pull requests"**: the agent opens its pull request
with the workflow's `GITHUB_TOKEN`. A pull request opened with that token does
not start CI on its own; that is fine, because the check needs the pull request
to exist, not to pass.

## 1. Self-hosted issue run

1. Open a small, self-contained test issue in this repository, for example
   "Add a sentence to `docs/release-checks.md` naming the release under test".
   Any open issue works; it does not need any label.
2. Actions → **Release acceptance** → Run workflow on the release branch or
   commit: `check: self-hosted`, `issue: <number>`. Approve the run for the
   `acceptance` environment.
3. The job builds Itervox from that ref and runs
   `deploy/acceptance/self-hosted-run.sh`, which:
   - labels only that issue `smoke-<issue>-<run>:todo`. The run's workflow
     uses active state labels unique to that issue and run
     (`smoke-<issue>-<run>:todo` → `smoke-<issue>-<run>:in-progress` →
     `smoke:done`), so no other issue is eligible, not even one an earlier
     failed or timed-out run left in an active smoke state. `<run>` is
     `ITERVOX_SMOKE_RUN_ID` (letters and digits) or the UTC start time. The
     run deletes its two active labels when it ends, pass or fail; the
     terminal `smoke:done` stays on the issue as the record;
   - starts the daemon with a real Claude agent (one at a time, one retry);
   - passes when the issue reaches `smoke:done` **and** a pull request whose
     body names the issue exists. It fails when the daemon exits, the issue
     reaches `smoke:done` with no pull request, or 75 minutes pass.
4. The run summary shows the result; the artifact `self-hosted-run-<issue>-<run>`
   holds the record: `result.md`, the daemon's `itervox.log`, the agent
   `sessions/`, and the generated `WORKFLOW.md` (never the API token file).
   The agent's session transcripts may quote anything the agent printed; the
   artifact is visible to everyone who can read the repository's Actions.
5. Review the pull request. Close it (or merge it, if the change is wanted),
   delete its branch, and close the issue.

Locally, with `gh` logged in and `claude` installed:

```bash
make build
deploy/acceptance/self-hosted-run.sh --issue <number> --dry-run   # writes and checks the workflow only
GH_TOKEN=$(gh auth token) deploy/acceptance/self-hosted-run.sh --issue <number> --itervox ./itervox
```

`ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` must be set in the environment.
The script runs on Linux and macOS: it puts the daemon in its own process group
with `setsid` where that exists and with shell job control where it does not
(stock macOS has no `setsid`).

## 2. Cloud deploy smoke

Runs against a **published release**, because `bootstrap.sh` installs a release
archive: run it on the new tag right after the release workflow publishes it
(or on a release-candidate tag). The release must be v0.2.2 or later
(`itervox secret` and the local tracker).

1. Actions → **Release acceptance** → Run workflow: `check: deploy-gcp`,
   `version: <tag>`, optionally `zone`. Approve the run.
2. `deploy/acceptance/gcp-deploy-smoke.sh` follows the deploy skill's commands
   (`.claude/skills/deploy/SKILL.md`):
   1. `deploy/gcp/provision.sh` (no public IP; IAP SSH; Cloud NAT);
   2. `deploy/bootstrap.sh --version <tag> --data-disk …` on the VM;
   3. `itervox init --tracker local --force` in the VM's checkout, so no
      tracker credential is needed, and `itervox secret set` for the agent
      credential, which reaches the VM on stdin and is never printed;
   4. `itervox doctor --deploy` must end with `deploy: OK`;
   5. `systemctl enable --now itervox`, then `systemctl is-active` must print
      `active`, and `GET /api/v1/state` with the bearer token must answer 200
      (checked on the VM, so the token never leaves it);
   6. the dashboard page and `/api/v1/ready` must load through the IAP tunnel.
3. Before creating anything, the check refuses (exit 2) when an instance
   named `--name` or a disk named `<name>-data` already exists in the zone,
   so a name collision never deletes someone else's VM or disk. Teardown then
   always runs, pass or fail, and deletes only what this run created: the
   instance and data disk if provisioning got as far as creating them (a
   failure part-way deletes only what exists), and the Cloud Router/NAT and
   IAP firewall rule **if this run created them** (a project that already had
   them keeps them). The summary names what it removed. Exit 3 means the check
   passed but something could not be deleted; the log names what to delete by
   hand.

Locally, with `gcloud` logged in to the check's project:

```bash
deploy/acceptance/gcp-deploy-smoke.sh --version <tag> --project <project-id>
# --keep leaves the VM up for debugging; the summary lists what to delete.
```

## 3. Upgrade from the previous release

`TestUpgradeFromV021` runs in CI with every change; see
[Upgrade test](compatibility.md#upgrade-test-release-check). At release time,
confirm it is green on the release commit. After the release, add a fixture for
the release just shipped so the next release is tested against it.
