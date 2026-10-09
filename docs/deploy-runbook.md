# Operations runbook: upgrade, cleanup, backup and restore

For a VM installed with `deploy/bootstrap.sh` (or the `deploy/terraform/` modules,
which run it). Paths use the defaults: `--root /srv/itervox`, service user `itervox`,
`HOME=/srv/itervox/home`, checkout `/srv/itervox/<repo>`. Substitute yours.

| Task | Tool | Section |
|---|---|---|
| Install a new release | `itervox-upgrade.sh` (`deploy/upgrade.sh`) | [Upgrade](#upgrade) |
| Prune idle workspaces and old logs | `itervox-cleanup.timer` (`deploy/cleanup.sh`) | [Cleanup](#cleanup) |
| Restart a wedged event loop | `itervox-watchdog.timer` (`deploy/monitoring/itervox-watchdog.sh`) | [Watchdog](#watchdog) |
| Protect / recover state | snapshots or tar of the data disk | [Backup](#backup), [Restore](#restore) |

---

## Upgrade

Upgrading from v0.2.0 or earlier: first run `itervox doctor --upgrade --workflow WORKFLOW.md` as the service user in the project directory. It prints only the v0.2.1 upgrade notes that apply to this workflow, environment and installed unit, each with the fix (exit `0` and an "all clear" line when none do).

```bash
sudo itervox-upgrade.sh --version v0.2.1          # or --version latest
```

What it does, in order — everything before step 5 leaves the running service untouched:

1. downloads `itervox_linux_<arch>.tar.gz` and `checksums.txt` for the tag;
2. checks the archive's sha256 against its line in `checksums.txt`. **A missing line or a
   mismatch exits 1 before any `systemctl` call**;
3. extracts it and runs `<new> --version`; a binary that cannot run exits 1;
4. copies the current binary to `/usr/local/bin/itervox.prev` and renames the new one over
   `/usr/local/bin/itervox` (same directory, so the swap is atomic);
5. `systemctl restart itervox` — a normal drain (up to `TimeoutStopSec=300`), then the new binary;
6. polls `GET /api/v1/ready` until 200, for up to `--ready-timeout` (default 420 s);
7. if the restart fails or `/ready` never answers 200, **renames `itervox.prev` back,
   restarts, and re-checks `/ready`**.

| Exit | Meaning | Do |
|---|---|---|
| 0 | upgraded (or that build was already installed) | nothing |
| 1 | aborted before the swap; nothing changed | read the message (checksum, download, broken binary) |
| 2 | rolled back; the service is ready on the previous binary | read `journalctl -u itervox` from the failed start; `/ready` is also 503 during a tracker outage, so retry once the tracker is healthy |
| 3 | rolled back but the old binary did not turn ready either | `systemctl status itervox`, `journalctl -u itervox -n 200`, `sudo -u itervox -H itervox doctor --deploy --workflow WORKFLOW.md` |
| 64 | usage error | — |

`--ready-url` must match `server.port` (default `http://127.0.0.1:8090/api/v1/ready`).
Read the release's CHANGELOG "Upgrade notes" first: a changed systemd unit is **not**
installed by the upgrade script — re-run `bootstrap.sh` (idempotent) for that.
Manual rollback at any later time: `sudo cp -p /usr/local/bin/itervox.prev /usr/local/bin/itervox.new && sudo mv /usr/local/bin/itervox.new /usr/local/bin/itervox && sudo systemctl restart itervox`.

## Cleanup

Worktrees and per-worktree dependency trees fill the disk; nothing in the daemon deletes
a workspace unless `workspace.auto_clear` is on. `bootstrap.sh` installs a daily timer but
does **not** enable it:

```bash
sudo tee /etc/itervox/cleanup.env <<'EOF'
ITERVOX_CLEANUP_WORKSPACE_DAYS=14   # remove workspaces idle for longer
ITERVOX_CLEANUP_LOG_DAYS=30         # rotated itervox-*.log(.gz) and sessions/ files
ITERVOX_CLEANUP_GRACE_MINUTES=10    # never touch anything this fresh
EOF
sudo -u itervox -H itervox-cleanup.sh --workdir /srv/itervox/<repo> --dry-run   # see what would go
sudo systemctl enable --now itervox-cleanup.timer
```

A workspace is **kept** when its issue is running, retrying, paused (including paused
with a PR), waiting for input or queued according to `GET /api/v1/state`, **or** its
identifier appears in any continuation ledger on disk (`paused.json`,
`input_required.json` — which also holds pending input resumes the API does not show —
`pending_reviews.json`, `automation_queue.json`), or anything in it changed within the
retention or grace window. The state is re-queried right before each removal. When the
daemon answers `/api/v1/health` but the state query fails or answers 401, the sweep
refuses and removes nothing: the token is `ITERVOX_API_TOKEN` in the environment or in
`<checkout>/.itervox/.env`. The live `itervox.log`, the ledgers and `api-token` are never
touched. Results are in `journalctl -u itervox-cleanup`.

## Watchdog

Optional. `itervox-watchdog.timer` probes `/api/v1/ready` every minute and runs
`systemctl restart itervox` after 3 consecutive probes that report `"loop_fresh": false`
(or time out) — a wedged event loop. It never restarts during a drain
(`"draining": true`), during a tracker outage (loop fresh, poll failing — a restart
cannot fix that), or when the port refuses connections (systemd's `Restart=always`
owns a dead process). systemd's own `WatchdogSec=` is not used because the daemon does
not send `sd_notify` pings.

```bash
sudo systemctl enable --now itervox-watchdog.timer
journalctl -u itervox-watchdog     # "strike n/3" lines, then "restarting itervox"
```

---

## Backup

### What to back up

Everything below lives under `--root` (the data disk mount) on a bootstrap install:

| Path | Contents | Needed for |
|---|---|---|
| `/srv/itervox/home/.itervox/logs/<kind>/<project>/` | `history.json`, `paused.json`, `input_required.json` (incl. pending input resumes), `automation_queue.json`, `pending_reviews.json`, `auto_switched.json`, `backend_health.json`, `sessions/`, `api-token`, `itervox.log*` | paused work, input-required sessions, queued automations, run history, the dashboard token |
| `/srv/itervox/home/.itervox/workspaces/<project>/` | per-issue worktrees (`worktrees/<id>` or `<id>`) and the `.bare` clone | resuming paused/input-required issues on their branches |
| `/srv/itervox/<repo>/.itervox/` | `.env` (secrets — see below), `outbox.json` (tracker writes not yet delivered), `deps_overrides.json`, `agents/**`, `handoff/**` | undelivered tracker comments, dependency overrides |
| `/srv/itervox/home/.claude`, `.claude.json`, `.codex` | agent CLI logins (OAuth) | avoiding a re-login |
| `/srv/itervox/home/.config/gh`, `.ssh`, `.gitconfig` | GitHub CLI auth, deploy key, git identity | pushing branches, opening PRs |
| `/etc/itervox/*.env`, `/etc/systemd/system/itervox*.{service,timer}` | secret-manager mapping, cleanup/watchdog knobs, units (on the boot disk) | rebuilding the VM identically |

Skip `HEARTBEAT.md`, `dashboard_url` and `.itervox/runtime/` (rewritten on start; the
PID lock must not be restored). `.env`, `api-token`, `~/.claude*`, `~/.codex` and
`~/.config/gh` are credentials: encrypt the backup and restrict who can read it, or
exclude them and re-provision from the secret manager.

### Snapshots (preferred)

The `deploy/terraform` modules schedule daily data-disk snapshots
(`snapshot_retention_days` / `backup_retention_days`). For a manual one, drain first so
the ledgers are flushed and no worktree is mid-write:

```bash
sudo systemctl stop itervox            # drains; can take up to 5 min
gcloud compute disks snapshot itervox-data --zone us-central1-a --snapshot-names itervox-data-$(date +%Y%m%d)
#   aws ec2 create-snapshot --volume-id vol-… --description itervox-data
#   az snapshot create -g itervox-rg -n itervox-data-$(date +%Y%m%d) --source itervox-data
sudo systemctl start itervox
```

A scheduled snapshot of a running daemon is crash-consistent: every ledger is written
atomically (temp file + rename), so the worst case is losing the last few seconds.

### File-level

```bash
sudo systemctl stop itervox
sudo tar -C / --numeric-owner -czpf /var/backups/itervox-$(date +%Y%m%d).tgz \
  --exclude='srv/itervox/*/.itervox/runtime' \
  srv/itervox etc/itervox etc/systemd/system/itervox.service
sudo systemctl start itervox
```

---

## Restore

### Onto a new VM from a snapshot or the old disk

1. Create a disk from the snapshot (or detach the old data disk) and attach it to the new VM.
2. Run bootstrap with the **same** `--root` and `--user`:
   `sudo ./deploy/bootstrap.sh --repo <url> --version <tag> --data-disk <device>`.
   The disk already carries a filesystem, so it is mounted and **never formatted**; the
   existing checkout is reused (no re-clone) and the account gets `HOME=<root>/home`.
3. Verify before starting the service:

```bash
cd /srv/itervox/<repo>
sudo -u itervox -H itervox doctor --deploy --workflow WORKFLOW.md
sudo -u itervox ls -l .itervox/outbox.json /srv/itervox/home/.itervox/logs/*/*/{paused,input_required,history}.json
sudo -u itervox git -C /srv/itervox/home/.itervox/workspaces/<project>/.bare worktree list
sudo systemctl enable --now itervox
curl -s localhost:8090/api/v1/ready
```

### From a tar backup

```bash
sudo systemctl stop itervox                      # if it is installed already
sudo tar -C / --numeric-owner -xzpf itervox-YYYYMMDD.tgz
sudo systemctl daemon-reload && sudo systemctl start itervox
```

### Worktree linkage

Git records worktrees with **absolute** paths (the worktree's `.git` file points into
`.bare/worktrees/<id>`, and back via `gitdir`). A restore to the same paths keeps every
link; a restore under a different root or HOME breaks them. Repair from the restored
location, then check:

```bash
git -C <new-workspaces>/<project>/.bare worktree repair <new-workspaces>/<project>/worktrees/*
git -C <new-workspaces>/<project>/.bare worktree list      # every worktree on its branch, none "prunable"
```

Keeping `--root` and the service user the same avoids the repair entirely.
