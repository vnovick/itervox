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
├── bootstrap.sh              # runs ON the VM: installs deps, itervox, systemd unit
├── systemd/itervox.service   # the unit
├── caddy/Caddyfile           # TLS reverse proxy (only for public exposure)
├── monitoring/
│   ├── heartbeat-metrics.sh  # parses HEARTBEAT.md → cloud custom metrics
│   └── README.md             # logging + alerting guide
├── gcp/provision.sh
├── aws/provision.sh
└── azure/provision.sh
```

---

## Quick start

1. Provision the VM for your cloud:

   ```bash
   ./deploy/gcp/provision.sh      # or aws/ or azure/
   ```

2. SSH in and run the bootstrap:

   ```bash
   sudo ./bootstrap.sh --repo git@github.com:you/yourproject.git --version v0.2.0
   ```

3. Fill in secrets at `/srv/itervox/<repo>/.itervox/.env` (see below), then:

   ```bash
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
```

`.itervox/.env` is gitignored. Prefer materializing it from your cloud's secret manager
in a systemd `ExecStartPre` rather than committing it to the image.

> **`.env` is resolved relative to the process working directory.** The systemd unit sets
> `WorkingDirectory` to the repo root for exactly this reason. Don't change it.

### Agent credentials are the real hurdle

The single most common failure mode for a headless deployment: the daemon starts, the
dashboard comes up, and every dispatch fails because `claude` has no credentials.

`claude` needs either `ANTHROPIC_API_KEY` in the environment, or a token minted
interactively once via `claude setup-token` and persisted to the service account's home
directory. Sort this out **before** you debug anything else.

---

## Authentication

> ⚠️ **Read this before exposing anything.** See
> [issue #48](https://github.com/vnovick/itervox/issues/48).

Itervox auto-generates a bearer token only when it binds a **non-loopback** address.
A reverse proxy or tunnel in front of a `127.0.0.1` bind therefore gets **no
authentication at all** — the daemon cannot see that it is publicly reachable.

On any cloud VM, **set `ITERVOX_API_TOKEN` explicitly**:

```bash
openssl rand -hex 32
```

Then reach the dashboard once at `https://your-host/?token=<token>`. The frontend
captures the token, stores it, and strips it from the URL.

---

## Choosing how to expose the dashboard

**Private tunnel (recommended).** No public IP, no firewall rule, no TLS to manage,
access gated by cloud IAM — strictly stronger than a bearer token, and it sidesteps
the auth gap above entirely.

| Cloud | Command |
|---|---|
| GCP | `gcloud compute start-iap-tunnel itervox 8090 --local-host-port=localhost:8090 --zone=<zone>` |
| AWS | `aws ssm start-session --target <id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'` |
| Azure | `az network bastion tunnel -g <rg> -n <bastion> --target-resource-id <vm-id> --resource-port 8090 --port 8090` |

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
  port: 8090      # PIN THIS. The default of 0 picks a random free port,
                  # which makes any static proxy or health-check config meaningless.
  host: 127.0.0.1 # keep loopback; the tunnel or Caddy fronts it
```

Health checks must target **`/api/v1/health`**, not `/health`. The route is registered
inside `s.router.Route("/api/v1", ...)`; `/health` hits the SPA catch-all. It is the one
auth-exempt endpoint, so load balancers and uptime probes can reach it without a token.

---

## Terminal UI under systemd

`itervox` starts a Bubbletea TUI when run interactively. Headless it is fine: a TTY
ownership guard detects the absence of a controlling terminal, logs
`statusui: refusing to start TUI`, and the daemon continues. No flag needed, no retry
delay when stdin is not a terminal.

**But note where the logs go.** Immediately before launching the TUI, the daemon
redirects `slog` to the rotating **file sink only**. That happens unconditionally,
whether or not the TUI actually starts — so under systemd, journald captures the startup
banner and then goes quiet. Your cloud logging agent must tail the log file:

```
~/.itervox/logs/<kind>/<project>/itervox.log
```

See `monitoring/README.md`.

---

## Why not serverless

Vercel, Cloud Run, Lambda, Container Apps — all a mismatch, and not a marginal one:

| Requirement | Serverless reality |
|---|---|
| One process alive for days holding in-memory state | Instances are recycled; state is lost |
| Subprocesses running for minutes | Request-scoped execution limits |
| Persistent git worktrees on local disk | Ephemeral filesystem |
| Long-lived SSE connections | Connection duration caps |

Vercel specifically: it runs serverless functions and static assets. There is no
execution model there in which this daemon runs at all.

The one thing that *would* technically work is hosting `web/` as a static site pointed at
a remote daemon's API. It isn't worth it — the Vite bundle is already embedded in the Go
binary by design (no sidecar process), so you'd add CORS, a split deploy, and a second
version to keep in lockstep in order to avoid serving ~1MB of assets the daemon already
serves for free.
