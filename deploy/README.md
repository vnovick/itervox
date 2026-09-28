# Deploying Itervox on a cloud VM

Itervox is a long-running stateful daemon. It holds all orchestrator state in memory
in a single goroutine, spawns `claude`/`codex` subprocesses that live for minutes,
keeps git worktrees on a local filesystem, and serves long-lived SSE connections.

That means **one VM with a persistent disk and a systemd unit** — not serverless, not
a scale-to-zero container platform. See [Why not serverless](#why-not-serverless) below.

The deployment is identical on GCP, AWS, and Azure. Only the provisioning commands and
the private-access primitive differ, which is why the per-cloud scripts here are thin
wrappers around one shared `bootstrap.sh`.

---

## Layout

```
deploy/
├── bootstrap.sh              # runs ON the VM: installs deps, itervox (sha256-verified), units, data disk
├── bootstrap_test.sh         # data-disk / HOME-relocation test (make deploy-test)
├── upgrade.sh                # checksum-verified atomic upgrade with /ready rollback (CORE-107)
├── cleanup.sh                # prunes idle workspaces + old logs, never retained ones (CORE-107)
├── upgrade_test.sh, cleanup_test.sh
├── systemd/
│   ├── itervox.service       # the unit
│   ├── itervox-cleanup.{service,timer}   # daily cleanup (installed, not enabled)
│   └── itervox-watchdog.{service,timer}  # optional wedged-loop restart (installed, not enabled)
├── lib/fetch-secrets.sh      # secret manager -> /run/itervox/env (ExecStartPre)
├── lib/fetch-secrets_test.sh # quoting / mode / idempotency / no-leak test
├── docker/                   # official image: Dockerfile, entrypoint, healthcheck, compose, smoke_test.sh
├── caddy/Caddyfile           # TLS reverse proxy (only for public exposure)
├── monitoring/
│   ├── heartbeat-metrics.sh  # HEARTBEAT.md → cloud custom metrics or a node_exporter textfile
│   ├── prometheus-rules.yml  # alert rules over /metrics + heartbeat series (+ .test.yml)
│   ├── itervox-watchdog.sh   # /ready loop_fresh probe behind itervox-watchdog.timer
│   └── README.md             # logging + alerting guide
├── terraform/                # OpenTofu modules: gcp-vm, aws-ec2, azure-vm (CORE-106)
├── gcp/provision.sh
├── aws/provision.sh
└── azure/provision.sh
```

Operations — upgrades, the cleanup timer, the watchdog, backup and restore — are in
[`docs/deploy-runbook.md`](../docs/deploy-runbook.md). `make deploy-test` runs every
shell test here; `make deploy-lint` runs shellcheck, hadolint, actionlint, promtool and
`tofu validate` from pinned images (the same jobs as `.github/workflows/deploy-lint.yml`),
and `make deploy-smoke` builds the image and requires `/api/v1/health` and
`/api/v1/ready` to answer 200.

---

## Quick start

1. Provision the VM for your cloud:

   ```bash
   ./deploy/gcp/provision.sh      # or aws/ or azure/
   ```

2. SSH in and run the bootstrap:

   ```bash
   sudo ./bootstrap.sh --repo git@github.com:you/yourproject.git --version v0.2.0 \
     --data-disk <stable device path printed by provision.sh>
   ```

   For a private **HTTPS** repository, `export GH_TOKEN` (read it from a file or a secret
   manager, not a literal on the command line) and run
   `sudo --preserve-env=GH_TOKEN ./bootstrap.sh --repo https://… …`.
   bootstrap passes it to `gh auth setup-git`, the clone and the deploy doctor with
   `sudo --preserve-env=GH_TOKEN` — it never appears in a command line, and `--dry-run`
   output shows `***` in its place.

   `aws/provision.sh` creates the data volume in the availability zone EC2 actually
   placed the instance in, waits for it to attach, and deletes the instance and volume
   it created if a later step fails.

3. Fill in secrets at `/srv/itervox/<repo>/.itervox/.env` (see below), check them with
   the read-only deploy doctor (bootstrap already ran it once), then start:

   ```bash
   sudo -H -u itervox bash -c 'cd /srv/itervox/<repo> && itervox doctor --deploy --workflow WORKFLOW.md'
   sudo systemctl enable --now itervox
   ```

4. Reach the dashboard via your cloud's private tunnel (recommended) or Caddy.

---

## Sizing

Size for **concurrent agents**, not for the daemon. Each agent is a full Claude Code
process running your project's builds and tests. `agent.max_concurrent_agents` in
`WORKFLOW.md` is the real knob.

| Concurrency | Suggested machine | Disk |
|---|---|---|
| 1–2 agents | 2 vCPU / 8GB | 50GB |
| 3–4 agents | 4 vCPU / 16GB | 100GB |
| 5+ agents | 8 vCPU / 32GB | 200GB |

Disk fills faster than you expect — each issue gets its own git worktree, plus node_modules
or equivalent per worktree. Alert on disk usage (see `monitoring/`).

---

## Secrets

Everything goes in `<repo>/.itervox/.env`, which the daemon loads at startup and injects
into its own process environment — so spawned agent subprocesses inherit it too.

```bash
# tracker
LINEAR_API_KEY=lin_api_xxxx        # or GITHUB_TOKEN=ghp_xxxx

# dashboard auth — see "Authentication" below. NOT optional on a cloud VM.
ITERVOX_API_TOKEN=<64 hex chars>

# agent credentials — the daemon starts fine without this and every dispatch fails
ANTHROPIC_API_KEY=sk-ant-xxxx
# ...or the token `claude setup-token` prints (it is shown once and saved nowhere)
# CLAUDE_CODE_OAUTH_TOKEN=
```

`.itervox/.env` is gitignored.

**Secret managers (CORE-061).** Instead of `.env`, the unit's
`ExecStartPre=/usr/local/bin/fetch-secrets.sh` can pull secrets from GCP Secret Manager,
AWS Secrets Manager or Azure Key Vault on every start. Configure it in
`/etc/itervox/secrets.env` (root-owned; the unit loads it with `EnvironmentFile=-`):

```bash
ITERVOX_SECRETS_PROVIDER=gcp            # gcp | aws | azure
ITERVOX_SECRETS_GCP_PROJECT=my-project  # aws: ITERVOX_SECRETS_AWS_REGION; azure: ITERVOX_SECRETS_AZURE_VAULT
ITERVOX_SECRETS_MAP="LINEAR_API_KEY=itervox-linear ANTHROPIC_API_KEY=itervox-anthropic ITERVOX_API_TOKEN=itervox-dashboard"
```

It writes `/run/itervox/env` (tmpfs, 0600, atomically replaced, removed on stop) with
systemd EnvironmentFile quoting — any value round-trips, newlines included — and never
prints a value. The VM's identity (service account / instance profile / managed identity)
needs read access to those secrets, and the CLI (`gcloud`/`aws`/`az`) must be installed.
Variables it provides take precedence over `.itervox/.env`, which only fills in what is
still unset.

> **`.env` is resolved relative to the process working directory.** The systemd unit sets
> `WorkingDirectory` to the repo root for exactly this reason. Don't change it.

### Agent credentials are the real hurdle

The single most common failure mode for a headless deployment: the daemon starts, the
dashboard comes up, and every dispatch fails because `claude` (or `codex`) has no
credentials. Sort this out **before** you debug anything else — the symptom looks like
an orchestrator problem and isn't. Options, in order of how VM-friendly they are:

- **`ANTHROPIC_API_KEY`** in `.itervox/.env` — simplest, works everywhere, no
  interactive step.
- **`CLAUDE_CODE_OAUTH_TOKEN`** in `.itervox/.env` — a one-year OAuth token minted with
  `claude setup-token` (run once, interactively, anywhere you have a browser; needs a
  Pro, Max, Team or Enterprise plan). The command **prints the token once and does not
  save it anywhere**, so copy it straight into the `CLAUDE_CODE_OAUTH_TOKEN=` line of
  the `.itervox/.env` scaffold (see [Secrets](#secrets)); the CLI honours it headlessly
  with no `~/.claude` login state required on the VM itself.
- **Cloud-native model hosting via instance identity** — Bedrock (AWS instance
  profile), Vertex AI (GCP service account), or Azure AI Foundry (managed identity):
  no API key in `.env` at all, the CLI picks up ambient cloud credentials the same
  way the AWS/GCP/Azure `provision.sh` scripts already attach for logging/monitoring.
  Verify the CLI actually authenticates this way before relying on it in production.
- **Interactive login (`claude`, then `/login`) run directly on the VM as the service
  account** — persists to the service account's own `~/.claude` directory (a state root,
  see below). Works, but needs an interactive session on a machine that's otherwise
  headless, so prefer one of the options above for a fresh VM. Running
  `claude setup-token` on the VM is *not* this option: it persists nothing, and its
  printed token still has to go into `.itervox/.env` as `CLAUDE_CODE_OAUTH_TOKEN`.

Codex fallback (`agent.backends` / `switch_to_backend`) needs `OPENAI_API_KEY` in the
same file; see `bootstrap.sh`'s `.env` scaffold.

### State roots

Itervox and the agent CLIs it spawns keep state in three places:

| Root | Contents | Lifecycle |
|---|---|---|
| `<repo>/.itervox/` | `.env`, `HEARTBEAT.md`, `dashboard_url`, per-issue logs, agent profiles (`agents/**`, committable), runtime queue/dependency state | Per project checkout; most of it gitignored — see `.gitignore` after `itervox init` |
| `$HOME/.itervox/workspaces/` | Per-issue worktrees (`workspace.root` default, `internal/config/config.go` `defaultWorkspaceRoot`) | Per service-account home |
| `$HOME/.itervox/logs/<project>/` | `itervox.log`, `history.json`, `paused.json`, `input_required.json`, `automation_queue.json`, `auto_switched.json`, `backend_health.json`, `sessions/`, `api-token` (`cmd/itervox/main.go` `defaultLogsDir` and the files set from it) | Per service-account home; restart durability of queue, history and breakers depends on it |
| Agent CLI homes (`~/.claude`, `~/.claude.json`, `~/.codex`), `~/.config/gh`, `~/.ssh`, `~/.gitconfig` | Login state, OAuth credentials, deploy key, git identity | Managed by the CLIs, not itervox |

**Persistent data disk (CORE-062).** `bootstrap.sh` gives the service account
`HOME=<root>/home` (default `/srv/itervox/home`) and clones into `<root>/<repo>`, so every
root above lives under `--root`. With `--data-disk <device>` that directory is the mount
point of a separate persistent disk (the `provision.sh` scripts create and attach one and
print its stable device path): the VM can be deleted and recreated and the state roots
come back by re-attaching the disk. The disk is formatted **only** when `blkid -p` finds
no signature on it at all; an existing filesystem is reused, a partitioned disk or an
unreadable one is refused, and nothing is formatted over a non-empty mount point. The
fstab entry is `UUID=<uuid> <root> <fs> defaults,nofail,x-systemd.device-timeout=30s 0 2`
and the unit has `RequiresMountsFor=<root>`. Re-running bootstrap with `--data-disk` on a
VM whose account already has another HOME copies the paths above over and runs
`usermod -d` (stop the service first; the old home is kept as a rollback).

---

## Authentication

Itervox auto-generates an ephemeral bearer token on **every** bind — including
loopback — unless you explicitly opt out with `server.allow_unauthenticated: true`.
Bind address is deliberately not a signal: a loopback bind behind a tunnel or reverse
proxy is exactly as internet-exposed as a public bind, and the daemon cannot tell the
difference from inside the process. (This closed
[issue #48](https://github.com/vnovick/itervox/issues/48), which described the older
behaviour where a proxied loopback bind ran with no auth at all.) With the opt-out set,
the daemon only refuses cross-origin state-changing browser requests (a CSRF guard) and
requests addressed to a host name other than `localhost`, `server.host` or one listed in
`server.allowed_hosts` (403 `host_not_allowed`, a DNS-rebinding guard; IP addresses always pass — see
`docs/configuration.md` → Authentication): anyone who can reach the port has full API access.
A Caddy or tunnel hostname in front of an unauthenticated daemon must be added to
`server.allowed_hosts`.

So you are authenticated by default. The reason to still pin a token on a VM is
**stability, not security** — an auto-generated token is regenerated on every restart,
breaking bookmarks and saved sessions each time systemd restarts the unit:

```bash
openssl rand -hex 32
```

Then reach the dashboard once at `https://your-host/?token=<token>`. The frontend
captures the token, stores it, and strips it from the URL.

The `?token=` URL is **not** in `journalctl` or any other log sink: under systemd stderr is
not a terminal, so the daemon logs only `dashboard URL (token withheld from logs)` with the
token-free URL and a `sha256:` fingerprint. If you did not pin `ITERVOX_API_TOKEN`, the
auto-generated token is in `<logs-dir>/api-token` (mode 0600, rewritten on every start;
the log line names the path — by default it sits next to `itervox.log` in
`~/.itervox/logs/<kind>/<slug>-<project>/`, or `~/.itervox/logs/workflow/<project>/`
when no `project_slug` is set). Set `ITERVOX_PRINT_TOKEN=1` to put the tokenised URL on stderr anyway;
`--no-print-token` suppresses it everywhere.

---

## Choosing how to expose the dashboard

**Private tunnel (recommended).** No public IP, no firewall rule, no TLS to manage,
access gated by cloud IAM — strictly stronger than a bearer token, since it doesn't
depend on the daemon's own auth at all.

| Cloud | Command |
|---|---|
| GCP | `gcloud compute ssh itervox --tunnel-through-iap --zone=<zone> -- -N -L 8090:localhost:8090` |
| AWS | `aws ssm start-session --target <id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'` |
| Azure | `az network bastion tunnel -g <rg> -n <bastion> --target-resource-id <vm-id> --resource-port 22 --port 2222` then `ssh -p 2222 -L 8090:localhost:8090 <user>@127.0.0.1` (Bastion only relays SSH/RDP, not arbitrary web servers, so 8090 is forwarded locally over the SSH tunnel) |

Each per-cloud `provision.sh` defaults to this: no public IP is allocated.

**Tailscale.** Best option if you want phone access without running a tunnel client per
session. Install on the VM, bind itervox to the tailnet IP. See
[the remote-access guide](https://itervox.dev/guides/remote-access/).

**Public HTTPS via Caddy.** Only if you genuinely need a public URL. Itervox has no TLS
of its own (there is no `ListenAndServeTLS` anywhere in `internal/server/`), so something
must terminate it. `caddy/Caddyfile` is a two-line config that gets an automatic
Let's Encrypt certificate. Pass `--public your.domain.com` to `bootstrap.sh` to install it.

---

## `WORKFLOW.md` settings that matter for a VM

```yaml
server:
  port: 8090      # PIN THIS. Omitting server.port defaults to 8090, but
                  # `itervox init` scaffolds `port: 0` (OS-assigned, picks a
                  # random free port), which makes any static proxy or
                  # health-check config meaningless — pin it explicitly on a VM.
  host: 127.0.0.1 # keep loopback; the tunnel or Caddy fronts it
```

Health checks must target **`/api/v1/health`**, not `/health`. The route is registered
inside `s.router.Route("/api/v1", ...)`; `/health` hits the SPA catch-all. It is the one
auth-exempt liveness endpoint, so load balancers and uptime probes can reach it without a token.
For readiness (route traffic / alert only while the orchestrator is actually working) use
**`/api/v1/ready`**, also auth-exempt: it returns 503 when the event loop is wedged or the
tracker has failed three polls in a row, 503 with `"draining": true` for the whole of a
shutdown or `WORKFLOW.md` reload drain, and 200 with `degraded: true` while rate-limited.

Keep the two apart: **`/ready` is for load-balancer readiness only, `/health` for
liveness** (restart decisions). A liveness probe on `/ready` kills a daemon mid-drain —
cancelling the agent turns the drain is protecting — and restart-loops it through a
tracker outage that a restart cannot fix. Both are exempt from the Host guard, so probes
may address the daemon by any name.

---

## Stopping: the drain (CORE-057)

SIGTERM starts a drain: no new work is admitted, `/ready` turns 503, and in-flight agent
turns get up to `--shutdown-grace` to finish; then they are cancelled and the daemon
allows up to 30 s more for the generation to stop. A second SIGTERM forces an immediate
stop — never send one early. The unit therefore runs

```ini
ExecStart=/usr/local/bin/itervox --workflow WORKFLOW.md --shutdown-grace 240s
KillMode=mixed        # SIGTERM to the daemon only; it stops its own agents
TimeoutStopSec=300    # >= shutdown-grace + 35 s, or systemd SIGKILLs a draining daemon
```

Change `--shutdown-grace` and `TimeoutStopSec` together. `KillMode=mixed` matters: the
default `control-group` SIGTERMs every agent subprocess at the same moment as the
daemon, aborting exactly the turns the drain exists to let finish.

`itervox stop` outside systemd defaults to `--grace 30s` before it SIGKILLs, which is
shorter than any real drain: pass `itervox stop --grace 275s` (shutdown-grace + 35 s).

---

## Container image (CORE-060)

`deploy/docker/Dockerfile` builds the web bundle and the Go binary with it embedded
(what `make build` does) and ships them on `debian:bookworm-slim` with git, gh, ssh and
tini, as uid 10001 with `HOME=/data`. Build from the repo root:

```bash
docker build -f deploy/docker/Dockerfile -t itervox:dev .
# with the claude/codex CLIs (pinned versions, like the published image):
#   --build-arg INSTALL_AGENT_CLIS=true
docker run -d --name itervox --stop-timeout 300 \
  -p 127.0.0.1:8090:8090 -v itervox-data:/data \
  -e ITERVOX_REPO=https://github.com/you/project.git -e ITERVOX_API_TOKEN -e GH_TOKEN \
  -e ANTHROPIC_API_KEY itervox:dev
```

- The entrypoint clones `ITERVOX_REPO` into `/data/repo` on the first start and reuses
  that clone afterwards (fetch + fast-forward only; local edits and commits are left
  untouched). `GH_TOKEN` is used through a git credential helper that reads it from the
  environment — it is never written to disk or printed.
- `ITERVOX_SERVER_HOST=0.0.0.0` is baked in; the port is `server.port`, overridden by
  `PORT` (Cloud Run) or `ITERVOX_SERVER_PORT`.
- `ITERVOX_LOG_FORMAT=json`: stderr is JSON lines (`docker logs`).
- tini is PID 1 and forwards SIGTERM; the default command passes `--shutdown-grace 240s`,
  so give the runtime **≥ 275 s** to stop (`--stop-timeout 300`, compose
  `stop_grace_period: 300s`, k8s `terminationGracePeriodSeconds: 300`).
- `HEALTHCHECK` probes `/api/v1/health`, deliberately not `/ready`: Swarm and autoheal
  restart containers on it (see above).
- `deploy/docker/docker-compose.yml` wires all of this; the image is published to
  `ghcr.io/vnovick/itervox` by `.github/workflows/docker-publish.yml` on release tags.
  The **published** image is built with `INSTALL_AGENT_CLIS=true` and ships `claude`
  and `codex` at the versions pinned in the Dockerfile (`CLAUDE_CODE_VERSION`,
  `CODEX_VERSION`; Node 22 from the official node image under `/opt/agent`). A plain
  local build leaves them out. `:latest` moves only on a final release tag, never on a
  pre-release such as `v1.2.0-rc.1`.
- `PORT=0` / `ITERVOX_SERVER_PORT=0` (the OS picks the port) works — the healthcheck reads
  the bound port from `/data/repo/.itervox/dashboard_url` instead of probing port 0 — but
  nothing outside the container can know which port to publish, so use it only with host
  networking.
- Secret managers: use the platform's own injection (compose/Kubernetes secrets, Cloud
  Run `--set-secrets`, ECS `secrets`, ACA `secretref`); the image does not ship
  `gcloud`/`aws`/`az`.

Kubernetes (one replica — itervox is a single-writer daemon):

```yaml
spec:
  replicas: 1
  strategy: { type: Recreate }
  template:
    spec:
      terminationGracePeriodSeconds: 300
      securityContext: { runAsUser: 10001, runAsGroup: 10001, fsGroup: 10001 }
      containers:
        - name: itervox
          image: ghcr.io/vnovick/itervox:latest
          ports: [{ containerPort: 8090 }]
          readinessProbe: { httpGet: { path: /api/v1/ready, port: 8090 }, periodSeconds: 10 }
          livenessProbe:  { httpGet: { path: /api/v1/health, port: 8090 }, periodSeconds: 30, failureThreshold: 3 }
          volumeMounts: [{ name: data, mountPath: /data }]
```

---

## Terminal UI under systemd

`itervox` starts a Bubbletea TUI when run interactively. Headless it is fine: a TTY
ownership guard detects the absence of a controlling terminal, logs
`statusui: refusing to start TUI`, and the daemon continues. No flag needed, no retry
delay when stdin is not a terminal.

**Where the logs go.** With no controlling terminal the daemon fans logs out to
**both stderr and the rotating file**, so `journalctl -u itervox` keeps working for the
whole run rather than going quiet after the startup banner (the tokenised dashboard URL
is withheld from it — see "Authentication" above). Pass `--log-format=json`
(or `ITERVOX_LOG_FORMAT=json`) to make both headless sinks — the file and stderr, hence
journald — structured, redacted JSON (CORE-059). The file sink lives at:

```
~/.itervox/logs/<kind>/<slug>-<project>/itervox.log
```

(or `~/.itervox/logs/workflow/<project>/itervox.log` when no `project_slug` is set)

See `monitoring/README.md`.

---

## Why not serverless

Request-scoped and scale-to-zero platforms — Vercel, Lambda, Cloud Functions, Cloud Run
or Container Apps in their default request-driven modes — are a mismatch, and not a
marginal one (always-on container SKUs with a persistent volume can run the official
image; see the platform matrix on itervox.dev/guides/deployment):

| Requirement | Serverless reality |
|---|---|
| One process alive for days holding in-memory state | Instances are recycled; state is lost |
| Subprocesses running for minutes | Request-scoped execution limits |
| Persistent git worktrees on local disk | Ephemeral filesystem |
| Long-lived SSE connections | Connection duration caps |

Vercel specifically: it runs serverless functions and static assets, and functions are
killed at a fixed max execution duration (10s–900s depending on plan and runtime) with no
guaranteed process reuse between invocations. Itervox needs one process staying up for
days, holding in-memory orchestrator state and long-lived SSE connections — durations and
lifecycle Vercel's function model does not provide.

The one thing that *would* technically work is hosting `web/` as a static site pointed at
a remote daemon's API. It isn't worth it — the Vite bundle is already embedded in the Go
binary by design (no sidecar process), so you'd add CORS, a split deploy, and a second
version to keep in lockstep in order to avoid serving ~1MB of assets the daemon already
serves for free.
