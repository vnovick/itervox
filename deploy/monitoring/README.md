# Logging and alerting

## What itervox exposes today

| Surface | Detail |
|---|---|
| Rotating log file | `~/.itervox/logs/<kind>/<project>/itervox.log` — 10MB, 5 backups, gzipped (lumberjack) |
| stderr | With a terminal: human-readable charm text until the TUI takes the screen. Headless (systemd, containers): every record for the whole run, as charm text or — with `--log-format json` / `ITERVOX_LOG_FORMAT=json` — JSON lines, redacted (CORE-059) |
| Secret redaction | A `RedactingHandler` wraps both sinks and scrubs bearer tokens, Anthropic/OpenAI/Linear/GitHub/Slack/AWS key shapes, `UPPER_CASE_SECRET=value` assignments, URL passwords and long random-looking tokens before write (error attributes included). Agent failure text is redacted the same way before it reaches the dashboard or a tracker comment |
| `GET /api/v1/health` | Auth-exempt liveness probe (static: proves the HTTP server is up, nothing more) |
| `GET /api/v1/ready` | Auth-exempt readiness probe: 200/503 from the event loop's own liveness, the tracker poll failure run and the rate-limit gate. Body: `ready`, `loop_fresh`, `last_poll_ok`, `config_invalid`, `degraded`, `tracker_rate_limited_until`, `draining`. **503 with `"draining":true` for the whole shutdown or reload drain** (CORE-057) — use it for load-balancer readiness only, never for restarts |
| `GET /metrics` | Opt-in (`server.metrics.enabled: true`), bearer-token Prometheus text endpoint — families in `docs/api.md` "Metrics" |
| `GET /api/v1/logs` | Authenticated; backs the dashboard log pane |
| `GET /api/v1/state` → `recentFailures` | Authenticated; the last 100 operator-relevant failures (worker, tracker, persistence, outbox, panics, dashboard errors), redacted; also the dashboard's Recent failures panel |
| `POST /api/v1/client-errors` | Authenticated; the dashboard reports its own crashes and schema drift here (logged at WARN as `web client error reported`, counted in `itervox_client_errors_total`) |
| `.itervox/HEARTBEAT.md` | Capacity, queue depth, saturation, dependency audit (incl. cycles), backend breakers, input-required, retry depth, outbox pending/degraded, last error, last crash |

**Metrics are opt-in.** With `server.metrics.enabled: true` the dashboard listener serves
`GET /metrics` in the Prometheus text format, behind the same bearer token as the API
(scrape with `authorization: {credentials_file: ...}`). No OpenTelemetry or expvar.
Without it, everything below is log-, probe-, and heartbeat-based.

## One thing that will surprise you

**`HEARTBEAT.md` mtime is not a liveness signal.** The writer only rewrites when
`dirty && intervalElapsed`, so a healthy but idle daemon legitimately stops touching the
file. Alerting on staleness pages you for a quiet Sunday. Use it for *content*
(saturation, input-required, last error), never freshness.

## journald under systemd

Headless (no controlling terminal), the daemon fans logs out to **both stderr and the
rotating file sink** for the whole run, so `journalctl -u itervox` keeps working past
the startup banner rather than going quiet — see `deploy/README.md`'s "Terminal UI
under systemd" section. Point your cloud logging agent at the **log file** as well,
since journald retention is typically shorter-lived than the rotating file sink:

```yaml
# GCP — /etc/google-cloud-ops-agent/config.yaml
logging:
  receivers:
    itervox:
      type: files
      include_paths: [/srv/itervox/home/.itervox/logs/*/*/itervox.log]
  service:
    pipelines:
      default_pipeline:
        receivers: [itervox]
```

```json
// AWS — /opt/aws/amazon-cloudwatch-agent/etc/config.json
{"logs":{"logs_collected":{"files":{"collect_list":[
  {"file_path":"/srv/itervox/home/.itervox/logs/*/*/itervox.log",
   "log_group_name":"itervox","log_stream_name":"{instance_id}"}]}}}}
```

Azure: add a custom-text-log data collection rule pointed at the same glob.

## Log format

Pass `--log-format=json` (or `ITERVOX_LOG_FORMAT=json`) to switch **both** headless sinks
to structured JSON (CORE-059, `cmd/itervox/logformat.go`): the rotating file
(`itervox.log`) and stderr — so under systemd `journalctl -u itervox -o cat` and any
journald or container log shipper receive one redacted JSON object per line. With a
controlling terminal stderr stays human-readable text whatever the flag says. The
official container image sets `ITERVOX_LOG_FORMAT=json`; the systemd unit leaves the
default (logfmt file, charm stderr) — add `--log-format json` to its `ExecStart` to opt in.

With JSON, alert on the field (`jsonPayload.level="ERROR"` on GCP,
`{ $.level = "ERROR" }` in CloudWatch). Without it, match the logfmt key:

```
# GCP log-based metric filter
textPayload =~ "level=ERROR"

# CloudWatch metric filter
[..., level="level=ERROR", ...]
```

## What to alert on

**Recommended:** run the daemon with `ITERVOX_LOG_FORMAT=json` (or `--log-format json`)
so log alerts match a field instead of a regex, and enable `/metrics` so the rules below
have series.

`prometheus-rules.yml` is the maintained rule set (CORE-113). Every family it reads is one
the daemon exports (`internal/metrics/exposition.go`) or one `heartbeat-metrics.sh` writes;
`rules_names_test.sh` (in `make deploy-test`) fails when either side is renamed, and
`prometheus-rules.test.yml` has one promtool unit case per alert (`make deploy-promtool`).

| Alert | Expression (abridged) | Why it matters |
|---|---|---|
| `ItervoxScrapeDown` | `up{job="itervox"} == 0` for 5m | Process down, or the scrape token is wrong (401 → `up=0`) |
| `ItervoxEventLoopStalled` | `time() - itervox_event_loop_last_idle_timestamp_seconds > 600` for 5m | The orchestrator loop is wedged: no dispatch, retries or reconciliation |
| `ItervoxTrackerPollFailing` | `itervox_tracker_poll_consecutive_failures >= 3` for 5m | Tracker outage (rate-limited polls never count); `/ready` is 503 meanwhile |
| `ItervoxTrackerRateLimitedLong` | `itervox_tracker_rate_limited_until_seconds - time() > 900` | Writes and polls held for 15+ min |
| `ItervoxGoroutinePanic` | `increase(itervox_goroutine_panics_total[15m]) > 0` | A recovered panic (the site is in the `level=ERROR` log line) |
| `ItervoxEventsDropped` | `increase(itervox_events_dropped_total[15m]) > 0` | Events the loop never saw |
| `ItervoxPersistWriteErrors` | `increase(itervox_persist_write_errors_total[15m]) > 0` | Ledgers not saved (disk full / read-only): state will not survive a restart |
| `ItervoxOutboxBacklog` | `itervox_outbox_entries{state="degraded"} > 0` or `{state="pending"} > 20`, for 30m | Tracker comments/moves stuck; see the dashboard Outbox panel |
| `ItervoxInputRequired` | `itervox_input_required > 0` for 30m | An agent is waiting on a human — the one that actually needs a person |
| `ItervoxSaturated` | `itervox_dispatch_eligible_waiting > 0 and itervox_workers_running >= itervox_workers_max` for 1h | Capacity is wrong |
| `ItervoxWorkerFailureRate` | `increase(itervox_worker_exits_total{reason=~"failed\|stalled"}[1h]) >= 5` | Agents failing repeatedly |
| `ItervoxDashboardClientErrors` | `increase(itervox_client_errors_total{outcome="accepted"}[15m]) > 0` | Dashboard crashes; `kind="schema"` is dashboard/daemon version drift |
| `ItervoxDaemonDegraded` | `itervox_heartbeat_degraded == 1` for 5m | Came up with a startup error (HEARTBEAT `Daemon: degraded`) |
| `ItervoxDependencyCycle` | `itervox_heartbeat_deps_cycles > 0` for 30m | Issues held by a dependency cycle |
| `ItervoxBackendLimited` | `itervox_heartbeat_backends_limited > 0` for 2h | An agent backend has been usage-limited for hours |

Outside Prometheus:

| Signal | Source | Why it matters |
|---|---|---|
| Process down | systemd restart count (journald) | The only true liveness signal without `/metrics` |
| Not ready | uptime check on `/api/v1/ready` expecting 200 | Wedged event loop or 3+ failed polls; **503 during a drain is expected** |
| `level=ERROR` rate | log-based metric (`jsonPayload.level="ERROR"` / `{ $.level = "ERROR" }`) | Tracker outages (3+ consecutive failed polls), exhausted retries, a failed failed-state move, recovered panics. A single failed poll and ordinary worker failures log at WARN/INFO |
| Disk > 80% | cloud agent default | Worktrees plus dependency trees accumulate; enable `itervox-cleanup.timer` (`docs/deploy-runbook.md`) |

Never alert on `heartbeat_age_seconds` / HEARTBEAT.md mtime (see above).

## Installing the heartbeat exporter

`heartbeat-metrics.sh` parses every HEARTBEAT.md section the daemon writes — capacity,
queue, dependency audit (blocked/unknown/unblocked/cycles/attention), backends
(limited/probing, auto-switched), attention (input required, retry queue, outbox
pending/degraded, last crash) and `Daemon: degraded` — and reports a line the writer
omitted while zero as 0. Every series carries an **`instance`** label (`--instance`,
`ITERVOX_METRICS_INSTANCE`, default the host name); on GCP the series are written against
the `gce_instance` resource, on AWS with an `Instance` dimension.
`ITERVOX_METRICS_DRY_RUN=1` prints the series instead of pushing them.

```bash
sudo install -m0755 deploy/monitoring/heartbeat-metrics.sh /usr/local/bin/

# --cloud gcp|aws|azure pushes to the cloud; --cloud prometheus writes a
# node_exporter textfile (--textfile, default
# /var/lib/node_exporter/textfile_collector/itervox.prom) for prometheus-rules.yml.
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

## Optional watchdog

systemd `WatchdogSec=` is **not** used: it needs `sd_notify` pings from the event loop and
the daemon sends none, so `Type=notify` would kill a healthy daemon. Instead
`itervox-watchdog.timer` (installed by `bootstrap.sh`, not enabled) probes
`/api/v1/ready` every minute and restarts the unit after 3 consecutive
`"loop_fresh": false` (or timed-out) probes. It never restarts a draining daemon, a
daemon whose only problem is the tracker, or one that is not listening
(`Restart=always` owns that). `itervox-watchdog_test.sh` replays each case.

```bash
sudo systemctl enable --now itervox-watchdog.timer
```
