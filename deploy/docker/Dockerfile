# syntax=docker/dockerfile:1
#
# CORE-060: official itervox container image.
#
# Build from the REPO ROOT (the context needs web/, cmd/, internal/, go.mod):
#
#   docker build -f deploy/docker/Dockerfile -t itervox:dev .
#
# Stages (mirrors `make build`: the web bundle is built first because
# internal/server/embed.go embeds internal/server/web/dist via //go:embed —
# vite's outDir is ../internal/server/web/dist):
#
#   web     pnpm install --frozen-lockfile && pnpm build   → internal/server/web/dist
#   agent-clis  claude + codex + Node 22 into /opt/agent (only with INSTALL_AGENT_CLIS=true)
#   build   go build ./cmd/itervox with that dist embedded → /out/itervox (static, CGO off)
#   runtime debian-slim + git/gh/ssh/tini + the binary, non-root uid 10001
#
# Runtime contract:
#   - HOME=/data (CORE-062). Every daemon state root hangs off HOME or the
#     checkout: ~/.itervox/workspaces (config.go defaultWorkspaceRoot),
#     ~/.itervox/logs/<project>/ (main.go defaultLogsDir: itervox.log,
#     history/paused/input_required/automation_queue/auto_switched/
#     backend_health .json, sessions/, api-token), ~/.claude, ~/.codex,
#     ~/.gitconfig, and the checkout /data/repo with its .itervox/ (outbox,
#     deps overrides, HEARTBEAT.md). Mount ONE volume at /data.
#   - ITERVOX_SERVER_HOST=0.0.0.0 so the published port is reachable (CORE-058;
#     the daemon's own default is 127.0.0.1). The port is server.port from
#     WORKFLOW.md, overridden by PORT (Cloud Run/Heroku) or ITERVOX_SERVER_PORT.
#   - tini is PID 1 and forwards SIGTERM to the daemon, which drains
#     (CORE-057). Give the runtime a stop timeout of at least
#     --shutdown-grace + 35 s: `docker run --stop-timeout 300`, compose
#     `stop_grace_period: 300s`, k8s `terminationGracePeriodSeconds: 300`.
#   - HEALTHCHECK probes /api/v1/health (liveness), NOT /api/v1/ready — see the
#     comment on the HEALTHCHECK line.
#
# Agent CLIs (claude, codex). The PUBLISHED image (.github/workflows/
# docker-publish.yml) is built with INSTALL_AGENT_CLIS=true and therefore
# ships both, at the versions pinned by the ARG defaults below. A plain local
# `docker build` leaves them out (INSTALL_AGENT_CLIS=false): it makes no npm
# registry call for them, stays smaller, and you can extend it with your
# project's toolchain and CLIs instead. Opt in locally with
#   --build-arg INSTALL_AGENT_CLIS=true [--build-arg CLAUDE_CODE_VERSION=x.y.z --build-arg CODEX_VERSION=x.y.z]
# @anthropic-ai/claude-code needs Node >= 22, newer than Debian bookworm's
# nodejs (18.x), so the CLIs and their Node runtime come from the official
# node image (stage agent-clis) into /opt/agent.

ARG GO_VERSION=1.26.9
ARG NODE_VERSION=22.20.0

# ── Stage 1: web bundle ─────────────────────────────────────────────────────
FROM node:${NODE_VERSION}-bookworm-slim AS web
WORKDIR /src/web
# corepack provisions the pnpm pinned by web/package.json "packageManager".
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# vite writes to ../internal/server/web/dist, i.e. /src/internal/server/web/dist
RUN pnpm build

# ── Stage 1b: agent CLIs (only when INSTALL_AGENT_CLIS=true) ─────────────────
FROM node:${NODE_VERSION}-bookworm-slim AS agent-clis
ARG INSTALL_AGENT_CLIS=false
# Pinned: bump deliberately (npm view @anthropic-ai/claude-code version).
ARG CLAUDE_CODE_VERSION=2.1.283
ARG CODEX_VERSION=0.157.1
# hadolint ignore=DL3016
RUN mkdir -p /opt/agent/bin \
 && if [ "${INSTALL_AGENT_CLIS}" = "true" ]; then \
      npm install -g --prefix /opt/agent \
        "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@openai/codex@${CODEX_VERSION}" \
      && cp /usr/local/bin/node /opt/agent/bin/node \
      && npm cache clean --force \
      && /opt/agent/bin/claude --version \
      && /opt/agent/bin/codex --version; \
    fi

# ── Stage 2: Go binary with the bundle embedded ────────────────────────────
FROM golang:${GO_VERSION}-bookworm AS build
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /src/internal/server/web/dist ./internal/server/web/dist
RUN go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/itervox ./cmd/itervox \
 && /out/itervox --version

# ── Stage 3: runtime ────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Distro packages are intentionally NOT version-pinned: this is a rolling base
# that must pick up Debian security updates on every rebuild (DL3008).
# hadolint ignore=DL3008
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git gh openssh-client tini \
 && rm -rf /var/lib/apt/lists/*

# Agent CLIs + their Node runtime (empty /opt/agent/bin when not installed).
COPY --from=agent-clis /opt/agent /opt/agent

RUN useradd --uid 10001 --user-group --home-dir /data --no-create-home --shell /bin/bash itervox \
 && install -d -o 10001 -g 10001 -m 0750 /data

COPY --from=build /out/itervox /usr/local/bin/itervox
COPY --chmod=0755 deploy/docker/entrypoint.sh /usr/local/bin/itervox-entrypoint
COPY --chmod=0755 deploy/docker/healthcheck.sh /usr/local/bin/itervox-healthcheck

ENV HOME=/data \
    PATH=/opt/agent/bin:$PATH \
    ITERVOX_SERVER_HOST=0.0.0.0 \
    ITERVOX_LOG_FORMAT=json

USER 10001:10001
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8090

# Liveness, not readiness. /api/v1/ready answers 503 for the whole drain
# (CORE-057, up to --shutdown-grace) and after repeated tracker poll failures
# (CORE-043). Docker Swarm replaces tasks whose HEALTHCHECK turns unhealthy,
# and autoheal-style sidecars restart them, so probing /ready here would kill
# a daemon mid-drain — cancelling the very agent turns the drain protects —
# and restart-loop it through a tracker outage a restart cannot fix.
# /api/v1/health stays 200 while the HTTP server is up. Route load-balancer
# traffic with /api/v1/ready instead (k8s readinessProbe, ALB target group,
# Cloud Run startup probe); see deploy/README.md.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/itervox-healthcheck"]

STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/itervox-entrypoint"]
CMD ["--workflow", "WORKFLOW.md", "--shutdown-grace", "240s"]
