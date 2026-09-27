# OpenTofu modules (CORE-106)

Declarative equivalents of `deploy/<cloud>/provision.sh`: one VM, one persistent
data disk, private networking, a least-privilege identity, and a first-boot
script that runs `deploy/bootstrap.sh`. Works with OpenTofu ≥ 1.6 (and Terraform).

| Module | Mirrors | Access (no public dashboard port by default) |
|---|---|---|
| [`gcp-vm/`](gcp-vm/) | `deploy/gcp/provision.sh` | IAP SSH (35.235.240.0/20 → tcp/22 only), forward 8090 over it |
| [`aws-ec2/`](aws-ec2/) | `deploy/aws/provision.sh` | SSM Session Manager port forwarding; the security group has no inbound rule |
| [`azure-vm/`](azure-vm/) | `deploy/azure/provision.sh` | Bastion/Tailscale SSH, forward 8090; the NSG has no inbound allow rule |

Each has `examples/basic/` (a root module with the provider block) — start there:

```bash
cd deploy/terraform/gcp-vm/examples/basic
tofu init
tofu plan -var project_id=my-project -var repo_url=https://github.com/you/project.git
```

## What every module creates

| Concern | GCP | AWS | Azure |
|---|---|---|---|
| VM | `google_compute_instance`, Shielded VM, OS Login, no external IP | `aws_instance`, IMDSv2 required, encrypted root, AL2023 by arch | `azurerm_linux_virtual_machine`, SSH key only, Ubuntu 24.04 |
| Data disk (CORE-062) | `google_compute_disk` → `/dev/disk/by-id/google-itervox-data` | `aws_ebs_volume` (encrypted) → `/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol…` | `azurerm_managed_disk` LUN 0 → `/dev/disk/azure/scsi1/lun0` |
| Deletion protection | `lifecycle.prevent_destroy` on the disk; VM `deletion_protection` | `prevent_destroy` + `final_snapshot` on the volume; `disable_api_termination` | `prevent_destroy` + a `CanNotDelete` management lock on the disk |
| Retention | daily snapshot schedule, `snapshot_retention_days` (kept after disk deletion) | Data Lifecycle Manager daily, `snapshot_retention_days` | Azure Backup vault + disk policy, `backup_retention_days` |
| Egress | Cloud Router + Cloud NAT | your subnet's NAT gateway (or `associate_public_ip`) | NAT gateway on the VM subnet; `default_outbound_access_enabled = false` |
| Identity | dedicated service account: `logging.logWriter`, `monitoring.metricWriter`, `secretAccessor` on listed secrets only | instance role: `AmazonSSMManagedInstanceCore`, `CloudWatchAgentServerPolicy`, `GetSecretValue` on listed ARNs only | system identity: `Monitoring Metrics Publisher` on the VM, `Key Vault Secrets User` on one vault |
| Startup | `startup-script` metadata | `user_data` | `custom_data` (cloud-init) |

The data disk is a separate resource in all three, never a disk the VM owns, so
replacing or deleting the VM leaves it (and every state root on it) intact.
`prevent_destroy` cannot be switched by a variable (OpenTofu requires a literal);
to really delete a disk, edit the module (and on Azure remove the lock) on purpose.

## First boot

`shared/startup.sh.tftpl` runs once (a marker in `/var/lib/itervox/` makes later
boots a no-op — GCP re-runs startup scripts on every boot):

1. waits for the data disk device, installs curl/jq;
2. downloads the release archive **and `checksums.txt`** and verifies the sha256
   before extracting (the archive ships `deploy/`, CORE-064);
3. writes `/etc/itervox/secrets.env` with **names only** (provider + `ITERVOX_SECRETS_MAP`)
   when `secrets_map` is set; `fetch-secrets.sh` resolves the values at every
   service start (on Azure it also installs the az CLI and signs in with the VM identity);
4. runs `deploy/bootstrap.sh --repo … --version … --root … --data-disk …`;
5. `systemctl enable --now itervox` only with `start_service = true`, and the
   cleanup timer only with `enable_cleanup_timer = true`.

Its log is `/var/log/itervox-startup.log`.

## Secrets

No module takes a secret value as input, so no variable is `sensitive`: anything
passed to OpenTofu lands in state in plain text, and anything in startup metadata
is readable on the VM. Put credentials in the cloud secret manager, grant the VM
access to exactly those secrets (`secret_ids` / `secret_arns` / `key_vault_id`) and
map them with `secrets_map`. `repo_url` rejects URLs with embedded credentials.
Private repositories need a deploy key or `GH_TOKEN` provisioned out of band
(see `deploy/README.md`); the first-boot clone fails without one.

## Validation

```bash
make deploy-tofu        # = deploy/terraform/validate.sh
```

runs `tofu fmt -check -recursive`, then `tofu init -backend=false` and
`tofu validate` for every module and example, in a temp copy, from
`ghcr.io/opentofu/opentofu`. **Validate proves the configuration is internally
consistent, not that it deploys**: a `tofu plan` against a sandbox account, an
apply, private reachability, disk retention across VM replacement and a restore
are not exercised by CI (CORE-043/CORE-062 own those claims).

## Alerting

These modules create no alert policies. Use `deploy/monitoring/` —
`prometheus-rules.yml` over the daemon's `/metrics` plus the HEARTBEAT.md content
exporter. Never alert on HEARTBEAT.md mtime: the writer skips writes while
nothing changes, so an idle healthy daemon looks "stale".
