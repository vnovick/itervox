---
name: deploy
description: 'Use when the user wants to run Itervox on a cloud VM (AWS, GCP or Azure): "deploy itervox", "host the daemon", "run it on a server", "set up itervox in the cloud". Walks through cloud, region, size and access choice, provisions with the shipped scripts or OpenTofu modules, bootstraps the VM, puts secrets in .itervox/.env without echoing them, runs itervox doctor --deploy, starts the service and prints the tunnel command and dashboard URL. Confirms before anything billable; never prints a secret.'
---

# Deploy Itervox to a cloud VM

This skill drives the deploy kit in `deploy/` (see `deploy/README.md` for the
long form). Work through the steps in order. Do not skip a confirmation.

## Two rules that override everything below

1. **Confirm before anything billable.** Before running a command that creates
   a VM, disk, IP address, NAT gateway, Bastion, load balancer or any other paid
   resource (`provision.sh`, `tofu apply`, `az network bastion create`, …),
   show the user the exact command and what it creates, and wait for an
   explicit yes. `tofu plan` and `--dry-run` runs are free; run them first.
2. **Never print a secret.** Never put a key, token or password on a command
   line, in an `echo`, in a file you show, or in your reply. Secrets go into
   `.itervox/.env` only through `itervox secret set KEY`, which reads the value
   with echo off (or from a pipe) and prints `saved KEY … (value not shown)`.
   Check them with `itervox secret list`, which shows `set` / `placeholder` /
   `empty`, never values. If the user pastes a secret into the chat, do not
   repeat it; tell them to rotate it if the chat is shared.

## 0. Get the deploy kit

In the Itervox repository, `deploy/` is right here. In any other project,
fetch it at the release you will install (the same tag goes to bootstrap):

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/vnovick/itervox/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
git clone --depth 1 --branch "$VERSION" https://github.com/vnovick/itervox.git /tmp/itervox-kit
cd /tmp/itervox-kit
```

## 1. Ask the four questions

Ask, and record the answers:

| Question | Options | Default |
|---|---|---|
| Cloud | `gcp`, `aws`, `azure` | — (ask) |
| Region / zone | GCP zone (`us-central1-a`), AWS region (`us-east-1`), Azure location (`eastus`) | the cloud's default above |
| Size | by concurrent agents: 1–2 → 2 vCPU/8 GB/50 GB; 3–4 → 4 vCPU/16 GB/100 GB; 5+ → 8 vCPU/32 GB/200 GB | 3–4 agents |
| Access | **private tunnel** (recommended: no public IP, cloud IAM), **Tailscale**, or **public HTTPS via Caddy** (needs a domain pointing at the VM) | private tunnel |

Also ask for the git URL of the project Itervox will work on (`--repo`), and
whether they prefer the provision scripts (imperative, quickest) or the
OpenTofu modules (declarative, keeps state; needs `tofu` ≥ 1.6).

Check the cloud CLI is installed and logged in before anything else:
`gcloud auth list`, `aws sts get-caller-identity`, or `az account show`.

## 2. Provision (billable: confirm first)

**Scripts** (from the kit root). Show the command, confirm, then run:

| Cloud | Command |
|---|---|
| GCP | `./deploy/gcp/provision.sh --name itervox --zone <zone> --machine <type> --disk <GB>` (add `--public` only for Caddy) |
| AWS | `./deploy/aws/provision.sh --name itervox --region <region> --type <type> --disk <GB> --data-disk-size <GB>` |
| Azure | `./deploy/azure/provision.sh --name itervox --group itervox-rg --location <location> --size <size> --disk <GB>` |

Each prints a block with the exact bootstrap and tunnel commands for what it
created (instance ID, data-disk device path). Keep it; steps 3 and 5 use it.

**OpenTofu** (`deploy/terraform/<gcp-vm|aws-ec2|azure-vm>/examples/basic`):
`tofu init`, then `tofu plan -var …` (free; show the summary), confirm, then
`tofu apply`. Pass `itervox_version` = the release from step 0 and `repo_url`;
the first-boot script runs bootstrap for you, so go to step 4 after apply.
The data disk has `prevent_destroy`: say so before the user plans a teardown.

## 3. Bootstrap the VM

Use the bootstrap command `provision.sh` printed, adding the release:
`--version <tag>` (the same tag as step 0) and, for Caddy, `--public <domain>`.
Run `sudo ./deploy/bootstrap.sh … --dry-run` first if the user wants to see
every step. Bootstrap installs the daemon and systemd units, mounts the data
disk at `--data-disk`, scaffolds `/srv/itervox/<repo>/.itervox/.env` with a
generated `ITERVOX_API_TOKEN`, and runs the read-only deploy doctor once.

For a private HTTPS repository, have the user `export GH_TOKEN` from a file or
secret manager on the VM (never a literal) and run bootstrap with
`sudo --preserve-env=GH_TOKEN`.

## 4. Secrets (never echoed)

On the VM, as the service user, in the project directory:

```bash
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox secret list --workflow WORKFLOW.md'
```

`itervox secret` ships with Itervox from the release after v0.2.1. On an
older release, ask the user to edit `.itervox/.env` on the VM themselves
(`sudo -u itervox nano /srv/itervox/<repo>/.itervox/.env`) and do not read
the file back; then check with `itervox doctor --deploy` only.

Then set each missing one; the user types the value at the hidden prompt:

```bash
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox secret set GITHUB_TOKEN --workflow WORKFLOW.md'
```

- Tracker: `LINEAR_API_KEY` or `GITHUB_TOKEN`.
- Agent: `ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` from
  `claude setup-token` (printed once; paste it straight into the hidden
  prompt); `OPENAI_API_KEY` for codex.
- `ITERVOX_API_TOKEN` is already pinned by bootstrap. Keep it (it makes the
  dashboard URL stable across restarts). To rotate it, pipe a new one in:
  `openssl rand -hex 32 | itervox secret set ITERVOX_API_TOKEN --replace --workflow WORKFLOW.md`.

A secret manager instead of `.env` (GCP Secret Manager, AWS Secrets Manager,
Azure Key Vault) is configured in `/etc/itervox/secrets.env`; see "Secrets" in
`deploy/README.md`.

## 5. Check and start

```bash
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox doctor --deploy --workflow WORKFLOW.md'
sudo systemctl enable --now itervox
```

It ends with `deploy: OK` or `deploy: FAILED — fix the [fail] lines above`.
Fix every `[fail]` line before starting. Agent credentials are the usual one: the
daemon starts without them and every dispatch then fails.

## 6. Tell the user how to reach it

Print the command for their cloud and access choice, filled in:

| Access | Command |
|---|---|
| GCP tunnel | `gcloud compute ssh itervox --tunnel-through-iap --zone=<zone> -- -N -L 8090:localhost:8090` |
| AWS tunnel | `aws ssm start-session --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'` |
| Azure Bastion | `az network bastion tunnel -g <group> -n <name>-bastion --target-resource-id <vm-id> --resource-port 22 --port 2222` then `ssh -p 2222 -L 8090:localhost:8090 <admin>@127.0.0.1` (Bastion is billable: confirm before creating it with the commands `provision.sh` printed) |
| Tailscale | install Tailscale on the VM and bind Itervox to the tailnet IP; see the remote-access guide |
| Caddy | `https://<domain>/` |

Then: open `http://localhost:8090/?token=<ITERVOX_API_TOKEN>` once (Caddy:
`https://<domain>/?token=…`). Do not print the token: tell the user to read it
on the VM with `sudo grep ITERVOX_API_TOKEN /srv/itervox/<repo>/.itervox/.env`
in their own terminal. The dashboard stores it and removes it from the URL.

## Done means

- `itervox doctor --deploy` ends with `deploy: OK` (no `[fail]` lines).
- `systemctl is-active itervox` prints `active`.
- The tunnel (or Caddy URL) loads the dashboard.

Report those three with the commands you ran. Mention monitoring
(`deploy/monitoring/`) and upgrades (`deploy/upgrade.sh --version <tag>`) as
next steps.
