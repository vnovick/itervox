# Logging and alerting

## What itervox exposes today

| Surface | Detail |
|---|---|
| Rotating log file | `~/.itervox/logs/<kind>/<project>/itervox.log` — 10MB, 5 backups, gzipped (lumberjack) |
| stderr | Human-readable charm handler — **only until the TUI launch point**, see below |
| Secret redaction | A `RedactingHandler` wraps both sinks and scrubs bearer tokens, `lin_api_*`, `ghp_*` before write |
| `GET /api/v1/health` | Auth-exempt liveness probe |
| `GET /api/v1/logs` | Authenticated; backs the dashboard log pane |
| `.itervox/HEARTBEAT.md` | Capacity, queue depth, saturation, dependency audit, input-required, retry depth, last error |

**There is no metrics endpoint.** No Prometheus, no OpenTelemetry, no expvar anywhere in
`internal/` or `cmd/`. Nothing to scrape. Everything below is log-, probe-, and
heartbeat-based.

## Two things that will surprise you

**1. journald goes quiet after startup.** Just before launching the TUI, the daemon
redirects `slog` to the rotating **file sink only**. That happens unconditionally — even
headless, where the TUI's TTY guard declines to start. So under systemd you get the
startup banner in journald and then silence.

Point your cloud logging agent at the **log file**, not at journald:

```yaml
# GCP — /etc/google-cloud-ops-agent/config.yaml
logging:
  receivers:
    itervox:
      type: files
      include_paths: [/home/itervox/.itervox/logs/*/*/itervox.log]
  service:
    pipelines:
      default_pipeline:
        receivers: [itervox]
```

```json
// AWS — /opt/aws/amazon-cloudwatch-agent/etc/config.json
{"logs":{"logs_collected":{"files":{"collect_list":[
  {"file_path":"/home/itervox/.itervox/logs/*/*/itervox.log",
   "log_group_name":"itervox","log_stream_name":"{instance_id}"}]}}}}
```

Azure: add a custom-text-log data collection rule pointed at the same glob.

**2. `HEARTBEAT.md` mtime is not a liveness signal.** The writer only rewrites when
`dirty && intervalElapsed`, so a healthy but idle daemon legitimately stops touching the
file. Alerting on staleness pages you for a quiet Sunday. Use it for *content*
(saturation, input-required, last error), never freshness.

## Log format caveat

The file sink is `slog.NewTextHandler` (logfmt), stderr is charm's human format. Neither
is JSON, so every log-based metric and alert has to regex unstructured text rather than
query a field. Tracked in [issue #49](https://github.com/vnovick/itervox/issues/49).

Until that lands, match on the logfmt key:

```
# GCP log-based metric filter
textPayload =~ "level=ERROR"

# CloudWatch metric filter
[..., level="level=ERROR", ...]
```

## What to alert on

| Signal | Source | Why it matters |
|---|---|---|
| Process down | systemd restart count (journald) | The only true liveness signal |
| Endpoint down | uptime check on `/api/v1/health` | Catches a wedged-but-alive process |
| `level=ERROR` rate | log-based metric | Tracker API failures, worker crashes |
| Rate-limit hits | log match on the configured rate-limit patterns | Distinguishes "agents blocked" from "agents idle" |
| `saturated=1` sustained | `heartbeat-metrics.sh` | Queue is backing up; capacity is wrong |
| `input_required > 0` | `heartbeat-metrics.sh` | An agent is waiting on a human — the one that actually needs a person |
| `degraded=1` | `heartbeat-metrics.sh` | Daemon came up with a startup error |
| Disk > 80% | cloud agent default | Worktrees plus per-worktree dependency trees accumulate. This *will* bite you |

Disk is the highest-signal alert nothing exports today, which is what
`heartbeat-metrics.sh` plus your agent's default host metrics are for.

## Installing the metric pusher

```bash
sudo install -m0755 deploy/monitoring/heartbeat-metrics.sh /usr/local/bin/

sudo tee /etc/systemd/system/itervox-metrics.service <<'EOF'
[Service]
Type=oneshot
User=itervox
ExecStart=/usr/local/bin/heartbeat-metrics.sh --cloud gcp \
  --heartbeat /srv/itervox/<repo>/.itervox/HEARTBEAT.md
EOF

sudo tee /etc/systemd/system/itervox-metrics.timer <<'EOF'
[Timer]
OnBootSec=60
OnUnitActiveSec=60
[Install]
WantedBy=timers.target
EOF

sudo systemctl enable --now itervox-metrics.timer
```
