#!/usr/bin/env bash
#
# Runs ON the VM. Installs the itervox daemon, its dependencies, and a systemd unit.
# Cloud-agnostic — the per-cloud provision.sh scripts only create the VM and then
# hand off to this.
#
# Usage:
#   sudo ./bootstrap.sh --repo git@github.com:you/project.git [options]
#
# Options:
#   --repo <url>       git remote to clone and operate on          (required)
#   --version <tag>    itervox release tag                         (default: latest)
#   --user <name>      service account to create                   (default: itervox)
#   --root <dir>       parent dir for the checkout                 (default: /srv/itervox)
#   --public <domain>  install Caddy + TLS for this domain         (default: none)
#
set -euo pipefail

REPO=""
VERSION="latest"
SVC_USER="itervox"
ROOT="/srv/itervox"
PUBLIC_DOMAIN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)    REPO="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --user)    SVC_USER="$2"; shift 2 ;;
    --root)    ROOT="$2"; shift 2 ;;
    --public)  PUBLIC_DOMAIN="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$REPO" ]] || { echo "error: --repo is required" >&2; exit 2; }
[[ $EUID -eq 0 ]] || { echo "error: run as root" >&2; exit 2; }

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

# ── system packages ─────────────────────────────────────────────────────────
log "installing system packages"
if command -v apt-get >/dev/null; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq git curl ca-certificates jq build-essential openssl gnupg
elif command -v dnf >/dev/null; then
  dnf install -y -q git curl ca-certificates jq gcc make openssl gnupg2
else
  echo "error: unsupported distro (need apt-get or dnf)" >&2; exit 1
fi

# ── Node.js (for the claude CLI) ────────────────────────────────────────────
if ! command -v node >/dev/null; then
  log "installing Node.js 22"
  curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1 || {
    echo "error: NodeSource setup failed; install Node 20+ manually" >&2; exit 1; }
  apt-get install -y -qq nodejs
fi

log "installing the claude CLI"
npm install -g @anthropic-ai/claude-code >/dev/null

# ── service account ─────────────────────────────────────────────────────────
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  log "creating service account: $SVC_USER"
  useradd --system --create-home --shell /bin/bash "$SVC_USER"
fi

# ── itervox binary ──────────────────────────────────────────────────────────
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) echo "error: unsupported arch $ARCH" >&2; exit 1 ;;
esac

if [[ "$VERSION" == "latest" ]]; then
  VERSION="$(curl -fsSL https://api.github.com/repos/vnovick/itervox/releases/latest | jq -r .tag_name)"
fi

log "installing itervox $VERSION (linux/$ARCH)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL -o "$TMP/itervox.tar.gz" \
  "https://github.com/vnovick/itervox/releases/download/${VERSION}/itervox_linux_${ARCH}.tar.gz"
tar -xzf "$TMP/itervox.tar.gz" -C "$TMP"
install -m 0755 "$TMP/itervox" /usr/local/bin/itervox
/usr/local/bin/itervox --version

# ── project checkout ────────────────────────────────────────────────────────
REPO_NAME="$(basename "${REPO%.git}")"
WORKDIR="$ROOT/$REPO_NAME"
mkdir -p "$ROOT"
chown "$SVC_USER:$SVC_USER" "$ROOT"

if [[ ! -d "$WORKDIR/.git" ]]; then
  log "cloning $REPO"
  sudo -u "$SVC_USER" git clone "$REPO" "$WORKDIR"
else
  log "checkout already present at $WORKDIR"
fi

sudo -u "$SVC_USER" mkdir -p "$WORKDIR/.itervox"

# ── .env scaffold ───────────────────────────────────────────────────────────
ENV_FILE="$WORKDIR/.itervox/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  log "scaffolding $ENV_FILE (fill in the blanks before starting)"
  # ITERVOX_API_TOKEN is pre-generated deliberately. A loopback bind behind a
  # proxy or tunnel installs NO auth middleware unless this is set — see
  # https://github.com/vnovick/itervox/issues/48
  cat > "$ENV_FILE" <<EOF
# Tracker — set exactly one
LINEAR_API_KEY=
# GITHUB_TOKEN=

# Dashboard bearer auth. Pre-generated; do not leave empty on a cloud VM.
ITERVOX_API_TOKEN=$(openssl rand -hex 32)

# Agent credentials. Without this every dispatch fails.
ANTHROPIC_API_KEY=
EOF
  chown "$SVC_USER:$SVC_USER" "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
fi

# ── systemd unit ────────────────────────────────────────────────────────────
log "installing systemd unit"
sed -e "s|@USER@|$SVC_USER|g" -e "s|@WORKDIR@|$WORKDIR|g" \
  "$(dirname "$0")/systemd/itervox.service" > /etc/systemd/system/itervox.service
systemctl daemon-reload

# ── optional public TLS ─────────────────────────────────────────────────────
if [[ -n "$PUBLIC_DOMAIN" ]]; then
  log "installing Caddy for $PUBLIC_DOMAIN"
  if ! command -v caddy >/dev/null; then
    apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
      > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq && apt-get install -y -qq caddy
  fi
  sed "s|@DOMAIN@|$PUBLIC_DOMAIN|g" "$(dirname "$0")/caddy/Caddyfile" > /etc/caddy/Caddyfile
  systemctl restart caddy
  echo
  echo "  Caddy is fronting itervox at https://$PUBLIC_DOMAIN"
  echo "  Open once at: https://$PUBLIC_DOMAIN/?token=<ITERVOX_API_TOKEN>"
fi

cat <<EOF

────────────────────────────────────────────────────────────────
 Bootstrap complete.

 Next:
   1. Fill in $ENV_FILE
   2. Ensure $WORKDIR/WORKFLOW.md sets  server.port: 8090
   3. sudo systemctl enable --now itervox
   4. sudo journalctl -u itervox -f          # startup only — see note
      sudo -u $SVC_USER tail -f ~$SVC_USER/.itervox/logs/*/*/itervox.log

 NOTE: itervox redirects its logger to the rotating file sink at startup,
 so journald sees the banner and then goes quiet. That is expected.
────────────────────────────────────────────────────────────────
EOF
