#!/usr/bin/env bash
#
# Container HEALTHCHECK (CORE-060): LIVENESS via GET /api/v1/health.
#
# Deliberately not /api/v1/ready — /ready is 503 for the whole drain and during
# a tracker outage, and Swarm/autoheal act on HEALTHCHECK by killing the
# container (see the Dockerfile). /health needs no token and is exempt from the
# Host guard.
#
# Port resolution mirrors the daemon (CORE-058): ITERVOX_SERVER_PORT > PORT >
# the port the daemon last published in <checkout>/.itervox/dashboard_url
# (it follows server.port from WORKFLOW.md) > 8090.
# ITERVOX_HEALTHCHECK_URL overrides everything.
#
# An env port of 0 means "the OS picks" (CORE-058), so it is never probed
# (M4-close D7: the check used to curl 127.0.0.1:0 forever). The daemon
# publishes the port it actually bound in dashboard_url, so that is read
# instead; until it exists the check fails without probing (start-period
# covers startup). Note that with an OS-picked port nothing outside the
# container can know which port to publish — PORT=0 is for host networking.
#
# The probe address follows the bound address FAMILY (CORE-174): the host is
# ITERVOX_SERVER_HOST (the daemon's own override) > the host the daemon
# published in dashboard_url > 127.0.0.1. A wildcard or IPv4 bind is probed
# on 127.0.0.1 (or that IPv4 address); an IPv6 bind ("::", "::1", any v6
# literal) on [::1] (or that v6 address), so a daemon bound solely to ::1 is
# no longer reported unhealthy.
set -euo pipefail

# probe_host HOST — the address to probe for a daemon bound to HOST.
probe_host() {
  local h="${1#[}"
  h="${h%]}"
  case "$h" in
    "" | "0.0.0.0" | "*") printf '127.0.0.1' ;;
    "::" | "::0" | "0:0:0:0:0:0:0:0") printf '[::1]' ;;
    *:*) printf '[%s]' "$h" ;;
    *) printf '%s' "$h" ;;
  esac
}

dash="${ITERVOX_REPO_DIR:-/data/repo}/.itervox/dashboard_url"
dash_url=""
if [[ -r "$dash" ]]; then dash_url="$(head -n 1 "$dash")"; fi

url="${ITERVOX_HEALTHCHECK_URL:-}"
if [[ -z "$url" ]]; then
  port="${ITERVOX_SERVER_PORT:-${PORT:-}}"
  if [[ -z "$port" || "$port" == "0" ]]; then
    os_picked=false
    if [[ "$port" == "0" ]]; then os_picked=true; fi
    port=""
    if [[ -n "$dash_url" ]]; then
      # dashboard_url is http://host:port[/...] (host may be [v6]); keep
      # only the port digits.
      port="$(printf '%s\n' "$dash_url" | sed -nE 's#^[a-z]+://[^/]*:([0-9]+)([/?].*)?$#\1#p')"
    fi
    if [[ -z "$port" ]] && $os_picked; then
      echo "itervox-healthcheck: server port is 0 (OS-assigned) and the daemon has not published dashboard_url yet" >&2
      exit 1
    fi
  fi
  host="${ITERVOX_SERVER_HOST:-}"
  if [[ -z "$host" && -n "$dash_url" ]]; then
    # the authority's host: [v6] or name/v4, before the last :port
    host="$(printf '%s\n' "$dash_url" | sed -nE 's#^[a-z]+://(\[[^]]*\]|[^/:]*)(:[0-9]+)?([/?].*)?$#\1#p')"
  fi
  url="http://$(probe_host "$host"):${port:-8090}/api/v1/health"
fi

exec curl -fsS --max-time 4 -o /dev/null "$url"
