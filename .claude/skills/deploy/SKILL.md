---
name: deploy
description: 'Use when the user wants to run Itervox on a cloud VM (AWS, GCP or Azure): "deploy itervox", "host the daemon", "run it on a server", "set up itervox in the cloud". Walks through cloud, region, size and access choice, provisions with the shipped scripts or OpenTofu modules, bootstraps the VM, has the user put secrets in .itervox/.env without echoing them, runs itervox doctor --deploy, starts the service and prints the tunnel command and dashboard URL. Confirms before anything billable; never prints a secret.'
---

# Deploy Itervox to a cloud VM

This skill drives the deploy kit in `deploy/` (the long form is
`deploy/README.md`). Work through the steps in order. Do not skip a
confirmation.

## Three rules that override everything below

1. **Confirm before anything billable.** Before running a command that creates
   a VM, disk, IP address, NAT gateway, router, Bastion or any other paid
   resource (`provision.sh`, `tofu apply`, `az network bastion create`, …),
   show the user the exact command and list what it creates (the table in
   step 2), and wait for an explicit yes. `tofu plan` and `--dry-run` runs are
   free; run them first.
2. **Never print a secret, and never handle one.** Secrets are typed by the
   user, in their own interactive SSH session on the VM, at the hidden prompt
   of `itervox secret set KEY` (step 5). You do not run `secret set`,
   `claude setup-token`, or anything that prints a key or token, and you never
   read `.itervox/.env` back. Check secrets only with `itervox secret list`
   (prints `set` / `placeholder` / `empty`, never values) and
   `itervox doctor --deploy`. If the user pastes a secret into the chat, do
   not repeat it; tell them to rotate it if the chat is shared.
3. **Commands on the VM run over the cloud's SSH.** Wrap them as in step 3
   (`gcloud compute ssh … --command`, `aws ssm start-session`, or the Azure
   tunnel). Anything that needs the user's input is theirs to run with a
   terminal (`-t`), never piped by you.

## 0. Pick the release and get the deploy kit

The release must include `itervox secret` (added after v0.2.1; first in
v0.2.2). Find the newest release:

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/vnovick/itervox/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
echo "$VERSION"
```

If it is `v0.2.1` or older, `itervox secret` does not exist there: on those
releases `itervox secret …` is not a command and **starts the daemon instead**.
Tell the user, then use the older-release path in step 5 and never run
`itervox secret` on that VM.

In the Itervox repository, `deploy/` is right here. In any other project,
fetch the kit at that release:

```bash
git clone --depth 1 --branch "$VERSION" https://github.com/vnovick/itervox.git /tmp/itervox-kit
cd /tmp/itervox-kit
```

## 1. Ask the questions

| Question | Options | Default |
|---|---|---|
| Cloud | `gcp`, `aws`, `azure` | — (ask) |
| Region / zone | GCP zone (`us-central1-a`), AWS region (`us-east-1`), Azure location (`eastus`) | those |
| Size | by concurrent agents: 1–2 → 2 vCPU / 8 GB; 3–4 → 4 vCPU / 16 GB; 5+ → 8 vCPU / 32 GB | 3–4 agents |
| Access | **private tunnel** (recommended: no public IP, cloud IAM), **Tailscale**, or **public HTTPS via Caddy** (needs a domain pointing at the VM) | private tunnel |
| Project | git URL Itervox will work on (`--repo`) | — (ask) |
| Tool | provision scripts (quickest) or OpenTofu modules (declarative; `tofu` ≥ 1.6) | scripts |

Disk: the scripts' `--disk` sets the **boot** disk. The data disk that holds
every worktree is fixed at 100 GB by the GCP and Azure scripts (AWS takes
`--data-disk-size`); the OpenTofu modules take `data_disk_gb` /
`data_volume_gb`. Suggest 50 / 100 / 200 GB for the three sizes and say when
the script cannot give it.

Check the cloud CLI is installed and logged in first: `gcloud auth list`,
`aws sts get-caller-identity`, or `az account show`.

## 2. Provision (billable: confirm first)

**Scripts**, from the kit root:

| Cloud | Command | Creates (billable) |
|---|---|---|
| GCP | `./deploy/gcp/provision.sh --name itervox --zone <zone> --machine <type> --disk <GB>` (add `--public` only for Caddy) | VM, 100 GB data disk, Cloud Router + Cloud NAT (no public IP unless `--public`) |
| AWS | `./deploy/aws/provision.sh --name itervox --region <region> --type <type> --disk <GB> --data-disk-size <GB>` | EC2 instance, EBS data volume (plus a reusable security group and instance profile) |
| Azure | `./deploy/azure/provision.sh --name itervox --group itervox-rg --location <location> --size <size> --disk <GB>` | resource group, VM, 100 GB managed data disk, VNet, NAT gateway with a Standard public IP |

Each prints the exact bootstrap and tunnel commands for what it created
(instance ID, data-disk device path). Keep that block; steps 3 and 7 use it.

**OpenTofu**: copy `deploy/terraform/<gcp-vm|aws-ec2|azure-vm>/examples/basic`
to a directory of the user's and edit it before planning. The examples are
starting points, not parameterised for every answer above:

| Module | Required `-var`s | Edit in `main.tf` |
|---|---|---|
| `gcp-vm` | `project_id`, `repo_url` | `region` in the provider; `itervox_version = "<tag>"` (the example says `"latest"`); the zone and `machine_type`; remove or adjust `secret_ids` / `secrets_map` unless those Secret Manager secrets exist |
| `aws-ec2` | `vpc_id`, `subnet_id`, `repo_url` | `region` in the provider; add `itervox_version = "<tag>"` and `instance_type`; replace the placeholder `secret_arns` (it names account `123456789012`) or remove it and `secrets_map` |
| `azure-vm` | `repo_url`, `admin_ssh_public_key` | add `itervox_version`, `location` and `vm_size`; `start_service` is `false` there, so start it in step 6 |

Then `tofu init`, `tofu plan …` (free: show the summary), confirm, and
`tofu apply`. The first-boot script runs bootstrap, so skip step 4. The data
disk has `prevent_destroy`; say so before the user plans a teardown.

## 3. Reach the VM

| Cloud | Run a command | Interactive shell (for the user) |
|---|---|---|
| GCP | `gcloud compute ssh itervox --tunnel-through-iap --zone=<zone> --command '<cmd>'` | `gcloud compute ssh itervox --tunnel-through-iap --zone=<zone>` |
| AWS | `aws ssm send-command` or a session: `aws ssm start-session --target <instance-id>` | `aws ssm start-session --target <instance-id>` |
| Azure | through the Bastion tunnel or Tailscale SSH (see step 7) | `ssh -p 2222 <admin>@127.0.0.1` after the tunnel |

## 4. Bootstrap

Copy the kit and run bootstrap with the command `provision.sh` printed,
adding `--version "$VERSION"` and, for Caddy, `--public <domain>`. Offer
`--dry-run` first. Bootstrap installs the daemon and systemd units, mounts the
data disk at `--data-disk`, clones the project to `/srv/itervox/<repo>`,
scaffolds `.itervox/.env` there with a generated `ITERVOX_API_TOKEN`, and
installs `itervox-upgrade.sh`.

For a private HTTPS repository, the user exports `GH_TOKEN` on the VM from a
file or secret manager (never a literal) and bootstrap runs with
`sudo --preserve-env=GH_TOKEN`.

**The project needs a `WORKFLOW.md`** (schema 2). If the repository does not
have one committed, bootstrap skips its doctor and says so. Either commit one
first (`itervox init --tracker <kind>` locally, then push), or create it on
the VM as the service user:
`sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox init --tracker <linear|github|local>'`.

## 5. Secrets (the user types them)

Give the user these, to run **themselves** in an interactive shell on the VM
(step 3), one per missing key. Each asks with echo off and prints only
`saved KEY … (value not shown)`:

```bash
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox secret list --workflow WORKFLOW.md'
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox secret set GITHUB_TOKEN --workflow WORKFLOW.md'
```

- Tracker: `LINEAR_API_KEY` or `GITHUB_TOKEN` (none for `kind: local`).
- Agent: `ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` (the user runs
  `claude setup-token` on their own machine; it prints the token once, and
  they paste it into the hidden prompt); `OPENAI_API_KEY` for codex.
- `ITERVOX_API_TOKEN` is pinned by bootstrap; keep it. To rotate it:
  `openssl rand -hex 32 | sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox secret set ITERVOX_API_TOKEN --replace --workflow WORKFLOW.md'`,
  then `sudo systemctl restart itervox` (`.env` is read at start).

**Older release (no `itervox secret`):** the user edits the file themselves,
`sudo -u itervox nano /srv/itervox/<repo>/.itervox/.env`. You do not read it
back; check it in step 6.

A secret manager instead of `.env` (GCP Secret Manager, AWS Secrets Manager,
Azure Key Vault) is configured in `/etc/itervox/secrets.env`; see "Secrets" in
`deploy/README.md`.

## 6. Check and start

```bash
sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox doctor --deploy --workflow WORKFLOW.md'
sudo systemctl enable --now itervox
```

The check ends with `deploy: OK` or `deploy: FAILED — fix the [fail] lines
above`. Fix every `[fail]` line before starting; agent credentials are the
usual one (the daemon starts without them and every dispatch then fails).

## 7. Tell the user how to reach it

Print the command for their cloud and access choice, filled in:

| Access | Command |
|---|---|
| GCP tunnel | `gcloud compute ssh itervox --tunnel-through-iap --zone=<zone> -- -N -L 8090:localhost:8090` |
| AWS tunnel | `aws ssm start-session --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'` |
| Azure Bastion | `az network bastion tunnel -g <group> -n <name>-bastion --target-resource-id <vm-id> --resource-port 22 --port 2222`, then `ssh -p 2222 -L 8090:localhost:8090 <admin>@127.0.0.1`. Bastion Standard is billable: confirm before creating it with the commands `provision.sh` printed. |
| Tailscale | install Tailscale on the VM and bind Itervox to the tailnet IP (remote-access guide) |
| Caddy | `https://<domain>/` |

Then the user opens `http://localhost:8090/?token=<ITERVOX_API_TOKEN>` once
(Caddy: `https://<domain>/?token=…`). Do not print the token: the user reads
it on the VM in their own terminal
(`sudo grep ITERVOX_API_TOKEN /srv/itervox/<repo>/.itervox/.env`). The
dashboard stores it and removes it from the URL.

## Per-cloud notes

- **GCP**: access is SSH through IAP, so the user's account needs permission
  to use IAP TCP forwarding on the project. The data disk is
  `/dev/disk/by-id/google-itervox-data`.
- **AWS**: access is SSM; the instance profile with
  `AmazonSSMManagedInstanceCore` is created if missing. The data volume is
  created in the instance's availability zone and both are removed again if a
  later step fails. Its device path names the volume ID.
- **Azure**: no public IP on the VM; outbound goes through the NAT gateway.
  Reach it with Tailscale (no extra cost) or Bastion Standard (billable). The
  data disk is `/dev/disk/azure/scsi1/lun0`; never `/dev/sdb` (ephemeral).

## Done means

- `itervox doctor --deploy` ends with `deploy: OK` (no `[fail]` lines).
- `systemctl is-active itervox` prints `active`.
- The tunnel (or Caddy URL) loads the dashboard.

Report those three with the commands you ran. Mention monitoring
(`deploy/monitoring/`) and upgrades (`sudo itervox-upgrade.sh --version <tag>`
on the VM) as next steps.
