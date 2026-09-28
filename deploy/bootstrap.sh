#!/usr/bin/env bash
#
# Runs ON the VM. Installs the itervox daemon, its dependencies, and a systemd unit.
# Cloud-agnostic — the per-cloud provision.sh scripts only create the VM and then
# hand off to this.
#
# Usage:
#   sudo ./bootstrap.sh --repo https://github.com/you/project.git [options]
#
# Options:
#   --repo <url>         git remote to clone and operate on          (required)
#   --version <tag>      itervox release tag                         (default: latest)
#   --user <name>        service account to create                   (default: itervox)
#   --root <dir>         parent dir for the checkout                 (default: /srv/itervox)
#   --public <domain>    install Caddy + TLS for this domain         (default: none)
#   --deploy-key <path>  SSH deploy key to install for the service user (only
#                        needed for a git@/ssh:// --repo; generated automatically
#                        when omitted and the URL is SSH)
#   --git-name <name>    git config user.name for the service user    (default: itervox-bot)
#   --git-email <email>  git config user.email for the service user   (default: itervox-bot@users.noreply.github.com)
#   --data-disk <dev>    persistent data disk to mount at --root (CORE-062), e.g.
#                        /dev/disk/by-id/google-itervox-data (GCP), /dev/disk/azure/scsi1/lun0
#                        (Azure), /dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol... (AWS).
#                        Formatted ONLY when it carries no signature at all (no filesystem,
#                        no partition table); never auto-detected. The service user's HOME is
#                        --root/home, so every state root lands on it.
#   --dry-run            print every mutating command instead of running it (CI smoke test)
#   --dry-run-network    run local/filesystem steps for real but print network-bound
#                        commands (package installs, downloads) instead of running them —
#                        lets a --repo file:///path/to/repo.git smoke test exercise the
#                        real clone + git-identity path without internet access
#   --functions-only     (source only) define install_* functions and return without
#                        running the rest of the script
#
set -euo pipefail

REPO=""
VERSION="latest"
SVC_USER="itervox"
ROOT="/srv/itervox"
PUBLIC_DOMAIN=""
DEPLOY_KEY=""
GIT_NAME="itervox-bot"
GIT_EMAIL="itervox-bot@users.noreply.github.com"
DATA_DISK=""
DRY_RUN=false
DRY_RUN_NETWORK=false
FUNCTIONS_ONLY=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)            REPO="$2"; shift 2 ;;
    --version)         VERSION="$2"; shift 2 ;;
    --user)            SVC_USER="$2"; shift 2 ;;
    --root)            ROOT="$2"; shift 2 ;;
    --public)          PUBLIC_DOMAIN="$2"; shift 2 ;;
    --deploy-key)      DEPLOY_KEY="$2"; shift 2 ;;
    --git-name)        GIT_NAME="$2"; shift 2 ;;
    --git-email)       GIT_EMAIL="$2"; shift 2 ;;
    --data-disk)       DATA_DISK="$2"; shift 2 ;;
    --dry-run)         DRY_RUN=true; shift ;;
    --dry-run-network) DRY_RUN_NETWORK=true; shift ;;
    --functions-only)  FUNCTIONS_ONLY=true; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

# redact prints its arguments with any secret this script handles replaced by
# *** (M4-close BH-M4-4). Secrets are never put in argv on purpose — GH_TOKEN
# reaches sudo'd commands through --preserve-env — this is the backstop for
# the [dry-run] echo.
redact() {
  local s="$*"
  if [[ -n "${GH_TOKEN:-}" ]]; then
    s="${s//"$GH_TOKEN"/***}"
  fi
  printf '%s' "$s"
}

# run_local executes filesystem/user-management commands that don't touch the
# network. Skipped only under a full --dry-run.
run_local() {
  if $DRY_RUN; then
    printf '[dry-run] %s\n' "$(redact "$*")"
  else
    "$@"
  fi
}

# run_net executes anything that reaches the internet (package managers,
# curl/npm downloads, GitHub API calls). Skipped under --dry-run AND under
# --dry-run-network, so the latter can exercise the real clone/git-identity
# path against a local file:// repo with no network access at all.
run_net() {
  if $DRY_RUN || $DRY_RUN_NETWORK; then
    printf '[dry-run] %s\n' "$(redact "$*")"
  else
    "$@"
  fi
}

# ── system packages ─────────────────────────────────────────────────────────
install_system_packages() {
  log "installing system packages"
  if command -v apt-get >/dev/null; then
    export DEBIAN_FRONTEND=noninteractive
    run_net apt-get update -qq
    run_net apt-get install -y -qq git curl ca-certificates jq build-essential openssl gnupg
  elif command -v dnf >/dev/null; then
    # AL2023 preinstalls curl-minimal/gnupg2-minimal, which conflict with the
    # full curl/gnupg2 packages pulled in below. --allowerasing lets dnf swap
    # the -minimal variants instead of failing outright. Reproduced: `dnf
    # install -y curl gnupg2 ...` on a fresh amazonlinux:2023 container exits 1
    # with a curl-minimal conflict; the same line with --allowerasing exits 0.
    run_net dnf install -y -q --allowerasing git curl ca-certificates jq gcc make openssl gnupg2
  else
    echo "error: unsupported distro (need apt-get or dnf)" >&2
    return 1
  fi
}

# ── Node.js (for the claude CLI) ────────────────────────────────────────────
install_node() {
  if command -v node >/dev/null; then
    return 0
  fi
  log "installing Node.js 22"
  if command -v apt-get >/dev/null; then
    run_net bash -c 'curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1' || {
      echo "error: NodeSource setup failed; install Node 20+ manually" >&2; return 1; }
    run_net apt-get install -y -qq nodejs
  elif command -v dnf >/dev/null; then
    run_net bash -c 'curl -fsSL https://rpm.nodesource.com/setup_22.x | bash - >/dev/null 2>&1' || {
      echo "error: NodeSource setup failed; install Node 20+ manually" >&2; return 1; }
    run_net dnf install -y -q nodejs
  else
    echo "error: unsupported distro (need apt-get or dnf)" >&2
    return 1
  fi
}

# ── gh (GitHub CLI) ──────────────────────────────────────────────────────────
install_gh() {
  if command -v gh >/dev/null; then
    return 0
  fi
  log "installing gh"
  if command -v apt-get >/dev/null; then
    run_net bash -c 'curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg'
    # shellcheck disable=SC2016 # $(dpkg ...) must expand in the inner bash, not here
    run_net bash -c 'echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list'
    run_net apt-get update -qq
    run_net apt-get install -y -qq gh
  elif command -v dnf >/dev/null; then
    run_net dnf install -y -q 'dnf-command(config-manager)'
    run_net dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
    run_net dnf install -y -q gh
  else
    echo "error: unsupported distro (need apt-get or dnf)" >&2
    return 1
  fi
}

# ── Caddy (optional public TLS) ─────────────────────────────────────────────
install_caddy() {
  if command -v caddy >/dev/null; then
    return 0
  fi
  log "installing Caddy"
  if command -v apt-get >/dev/null; then
    run_net apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https
    run_net bash -c "curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg"
    run_net bash -c "curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list"
    run_net apt-get update -qq
    run_net apt-get install -y -qq caddy
  elif command -v dnf >/dev/null; then
    run_net bash -c "curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/rpm.txt' > /etc/yum.repos.d/caddy-stable.repo"
    run_net dnf install -y -q caddy
  else
    echo "error: unsupported distro (need apt-get or dnf)" >&2
    return 1
  fi
}

# ── persistent data disk (CORE-062) ────────────────────────────────────────
# The service user's HOME is $ROOT/home and the checkout is $ROOT/<repo>, so
# mounting the data disk at $ROOT puts every state root on it (see
# svc_state_paths). FSTAB is overridable for the test harness only.
FSTAB="${ITERVOX_BOOTSTRAP_FSTAB:-/etc/fstab}"

# probe_disk DEV — read-only. Prints "fs <type> <uuid>", "empty", or
# "partitioned <pttype>"; returns non-zero when blkid itself fails.
# blkid -p is a low-level superblock probe (no cache): exit 2 with nothing on
# stderr means no signature of any kind was found, which is the ONLY state we
# format. blkid -p ALSO exits 2 when it cannot open the device ("blkid:
# error: <dev>: Permission denied"), and that must never read as a blank disk
# (M4-close D10): an unreadable device, or exit 2 with an error message, is
# refuse-to-format (return 4).
probe_disk() {
  local dev="$1" out rc=0 type="" uuid="" pttype="" line errf err=""
  if [[ ! -r "$dev" ]]; then
    echo "error: cannot read $dev" >&2
    return 4
  fi
  errf="$(mktemp)"
  out="$(blkid -p -o export "$dev" 2>"$errf")" || rc=$?
  err="$(cat "$errf")"
  rm -f "$errf"
  if [[ $rc -eq 2 && -z "$err" ]]; then
    echo "empty"; return 0
  elif [[ $rc -ne 0 ]]; then
    if [[ -n "$err" ]]; then printf '%s\n' "$err" >&2; fi
    if [[ $rc -eq 2 ]]; then return 4; fi
    return "$rc"
  fi
  while IFS= read -r line; do
    case "$line" in
      TYPE=*)   type="${line#TYPE=}" ;;
      UUID=*)   uuid="${line#UUID=}" ;;
      PTTYPE=*) pttype="${line#PTTYPE=}" ;;
    esac
  done <<< "$out"
  if [[ -n "$type" ]]; then
    echo "fs $type ${uuid:-none}"
  elif [[ -n "$pttype" ]]; then
    echo "partitioned $pttype"
  else
    return 3 # a signature blkid could not name: never format over it
  fi
}

# fstab_entry UUID FSTYPE — the line we own for $ROOT.
fstab_entry() {
  printf 'UUID=%s %s %s defaults,nofail,x-systemd.device-timeout=30s 0 2\n' "$1" "$ROOT" "$2"
}

setup_data_disk() {
  if [[ -z "$DATA_DISK" ]]; then
    log "no --data-disk given: state stays on the boot disk under $ROOT"
    return 0
  fi
  if [[ ! -e "$DATA_DISK" ]]; then
    echo "error: --data-disk $DATA_DISK does not exist" >&2
    return 2
  fi
  local dev probe kind fstype uuid mounted root_busy=false
  dev="$(readlink -f "$DATA_DISK")"
  log "data disk: $DATA_DISK ($dev) -> $ROOT"

  if ! probe="$(probe_disk "$dev")"; then
    echo "error: blkid could not classify $dev; refusing to touch it" >&2
    return 2
  fi
  read -r kind fstype uuid <<< "$probe"
  mounted="$(findmnt -rn -o TARGET -S "$dev" 2>/dev/null || true)"

  # Everything that can refuse runs BEFORE anything is written.
  if [[ "$kind" == partitioned ]]; then
    echo "error: $dev carries a $fstype partition table; pass the partition (e.g. ${DATA_DISK}-part1) as --data-disk" >&2
    return 2
  fi
  if [[ -n "$mounted" && "$mounted" != "$ROOT" ]]; then
    echo "error: $dev is already mounted at $mounted" >&2
    return 2
  fi
  if [[ "$kind" == fs ]] && grep -qE "^UUID=${uuid}[[:space:]]" "$FSTAB" 2>/dev/null; then
    :
  elif awk -v r="$ROOT" '$1 !~ /^#/ && $2 == r {found=1} END {exit !found}' "$FSTAB" 2>/dev/null; then
    echo "error: $FSTAB already mounts something else at $ROOT; fix it by hand" >&2
    return 2
  fi
  if [[ "$mounted" != "$ROOT" && -d "$ROOT" && -n "$(ls -A "$ROOT" 2>/dev/null)" ]]; then
    root_busy=true
    if [[ "$kind" == fs ]]; then
      echo "error: $ROOT is not empty and $dev already holds a $fstype filesystem;" \
           "mounting it would hide one of the two. Move one aside first." >&2
      return 2
    fi
  fi

  if [[ "$kind" == fs ]]; then
    log "  existing $fstype filesystem (UUID=$uuid) — reusing it, NOT formatting"
  else
    log "  no filesystem or partition table on $dev — formatting ext4"
    run_local mkfs.ext4 -q -L itervox-data "$dev"
    fstype=ext4
    if $DRY_RUN; then
      uuid="<uuid-after-mkfs>"
    elif ! probe="$(probe_disk "$dev")"; then
      echo "error: $dev unreadable after mkfs" >&2; return 2
    else
      read -r kind fstype uuid <<< "$probe"
    fi
    if $root_busy; then
      # A previous run without --data-disk left HOME and the checkout on the
      # boot disk under $ROOT: seed the new disk with them before mounting
      # over. The boot-disk copy stays underneath the mount as a rollback.
      log "  seeding the new disk with the existing contents of $ROOT"
      run_local mkdir -p "$ROOT.seed"
      run_local mount "$dev" "$ROOT.seed"
      run_local cp -a "$ROOT/." "$ROOT.seed/"
      run_local umount "$ROOT.seed"
      run_local rmdir "$ROOT.seed"
    fi
  fi

  if grep -qE "^UUID=${uuid}[[:space:]]" "$FSTAB" 2>/dev/null; then
    log "  fstab already has UUID=$uuid"
  else
    run_local bash -c "printf '%s\n' '$(fstab_entry "$uuid" "$fstype")' >> '$FSTAB'"
  fi

  if [[ "$mounted" == "$ROOT" ]]; then
    log "  already mounted at $ROOT"
  else
    run_local mkdir -p "$ROOT"
    run_local mount -T "$FSTAB" "$ROOT"  # through the fstab line: proves it before a reboot does
  fi
}

# svc_state_paths — relative to HOME, what migrate_home carries over. Every
# daemon state root is below HOME or the checkout (which is under $ROOT):
# ~/.itervox/workspaces, ~/.itervox/logs/<project>/{itervox.log, *.json,
# sessions/, api-token}, plus agent and git credentials.
svc_state_paths() {
  printf '%s\n' .itervox .claude .claude.json .codex .config/gh .ssh .gitconfig
}

# migrate_home OLD NEW — copy state (never move: the old home stays as a
# rollback until the operator deletes it) and repoint the account.
migrate_home() {
  local old="$1" new="$2" p
  if command -v systemctl >/dev/null && systemctl is-active --quiet itervox 2>/dev/null; then
    echo "error: itervox is running; 'systemctl stop itervox' before moving $SVC_USER's home" >&2
    return 2
  fi
  log "moving $SVC_USER's HOME $old -> $new (copy; $old is kept)"
  run_local install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$new"
  while IFS= read -r p; do
    if [[ -e "$old/$p" ]]; then
      run_local bash -c "cd '$old' && cp -a --parents '$p' '$new/'"
    fi
  done < <(svc_state_paths)
  run_local chown -R "$SVC_USER:$SVC_USER" "$new"
  run_local usermod -d "$new" "$SVC_USER"
}

# ── repository access, clone, identity, .env ─────────────────────────────
# Functions so bootstrap_test.sh can drive them with stubs.
#
# GH_TOKEN never goes into argv (M4-close BH-M4-4): `sudo … env
# GH_TOKEN=…` put it in every process listing and in the --dry-run echo. It
# is exported and passed with `sudo --preserve-env=GH_TOKEN`, which also
# reaches the HTTPS clone (BH-M4-5): `gh auth setup-git` only installs gh as
# git's credential helper, and that helper reads GH_TOKEN at clone time —
# sudo's env_reset used to strip it, so a private repository clone had no
# credentials at all.
setup_repo_access() {
  case "$REPO" in
    git@*|ssh://*)
      log "setting up SSH deploy access for $SVC_USER"
      SSH_DIR="$(eval echo "~$SVC_USER")/.ssh"
      run_local install -d -m 0700 -o "$SVC_USER" -g "$SVC_USER" "$SSH_DIR"
      if [[ -n "$DEPLOY_KEY" ]]; then
        run_local install -m 0600 -o "$SVC_USER" -g "$SVC_USER" "$DEPLOY_KEY" "$SSH_DIR/id_ed25519"
      elif [[ ! -f "$SSH_DIR/id_ed25519" ]]; then
        run_local sudo -u "$SVC_USER" ssh-keygen -t ed25519 -N '' -f "$SSH_DIR/id_ed25519" -C "$SVC_USER@itervox"
        if ! $DRY_RUN && ! $DRY_RUN_NETWORK; then
          echo
          echo "  No deploy key found or provided. A new one was generated for $SVC_USER."
          echo "  Enroll this public key as a deploy key on the git host, then re-run bootstrap.sh:"
          echo
          cat "$SSH_DIR/id_ed25519.pub"
          echo
          exit 3
        fi
      fi
      GIT_HOST="$(printf '%s' "$REPO" | sed -E 's#^(git@|ssh://)([^:/]+).*#\2#')"
      run_net bash -c "sudo -u '$SVC_USER' ssh-keyscan -H '$GIT_HOST' >> '$SSH_DIR/known_hosts' 2>/dev/null" || true
      ;;
    https://*)
      if [[ -n "${GH_TOKEN:-}" ]]; then
        log "authenticating gh for $SVC_USER via GH_TOKEN"
        export GH_TOKEN
        run_net sudo --preserve-env=GH_TOKEN -u "$SVC_USER" gh auth setup-git
      fi
      ;;
    *)
      # file:// and other local paths need no remote auth.
      ;;
  esac
}

clone_repo() {
  if [[ -d "$WORKDIR/.git" ]]; then
    log "checkout already present at $WORKDIR"
    return 0
  fi
  log "cloning $REPO"
  if [[ -n "${GH_TOKEN:-}" ]]; then
    export GH_TOKEN
    run_local sudo --preserve-env=GH_TOKEN -u "$SVC_USER" git clone "$REPO" "$WORKDIR"
  else
    run_local sudo -u "$SVC_USER" git clone "$REPO" "$WORKDIR"
  fi
}

# set_git_identity passes the name and email as single argv words; the old
# `bash -c "… git config user.name '$GIT_NAME'"` broke on (and executed
# whatever followed) a quote in --git-name.
set_git_identity() {
  log "setting git identity for $SVC_USER (never a guessed operator identity)"
  run_local sudo -u "$SVC_USER" git -C "$WORKDIR" config user.name "$GIT_NAME"
  run_local sudo -u "$SVC_USER" git -C "$WORKDIR" config user.email "$GIT_EMAIL"
}

# scaffold_env_file creates .itervox/.env under umask 077, so the file is
# 0600 from the moment it exists: it holds a freshly generated
# ITERVOX_API_TOKEN, and it used to be created 0644 and chmod'ed only after
# the token had been written.
scaffold_env_file() {
  ENV_FILE="$WORKDIR/.itervox/.env"
  if [[ -f "$ENV_FILE" ]]; then
    return 0
  fi
  log "scaffolding $ENV_FILE (fill in the blanks before starting)"
  if $DRY_RUN; then
    echo "[dry-run] would scaffold $ENV_FILE (LINEAR_API_KEY, ITERVOX_API_TOKEN, ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, OPENAI_API_KEY, GH_TOKEN)"
    return 0
  fi
  (
    umask 077
    cat > "$ENV_FILE" <<EOF
# Tracker — set exactly one
LINEAR_API_KEY=
# GITHUB_TOKEN=

# Dashboard bearer auth. Itervox auto-generates an ephemeral token on every
# bind unless server.allow_unauthenticated is set, but a pinned token here
# keeps bookmarks and saved dashboard sessions stable across restarts.
ITERVOX_API_TOKEN=$(openssl rand -hex 32)

# Agent credentials — at least one is required or every dispatch fails.
ANTHROPIC_API_KEY=
# Headless Claude Code login token, alternative to ANTHROPIC_API_KEY:
CLAUDE_CODE_OAUTH_TOKEN=
# Codex fallback backend:
OPENAI_API_KEY=

# gh (PR detection, session comments, merge endpoint) and the HTTPS clone
# path above both use this when set.
GH_TOKEN=
EOF
  )
  chmod 0600 "$ENV_FILE"
  chown "$SVC_USER:$SVC_USER" "$ENV_FILE"
}

if $FUNCTIONS_ONLY; then
  # shellcheck disable=SC2317 # exit is reached when the script is executed, not sourced
  return 0 2>/dev/null || exit 0
fi

[[ -n "$REPO" ]] || { echo "error: --repo is required" >&2; exit 2; }
[[ $EUID -eq 0 ]] || { echo "error: run as root" >&2; exit 2; }

install_system_packages
install_node

log "installing the claude CLI"
run_net npm install -g @anthropic-ai/claude-code >/dev/null

log "installing gh and codex"
install_gh
run_net npm install -g @openai/codex >/dev/null

# ── persistent data disk + service account (CORE-062) ─────────────────────
setup_data_disk

SVC_HOME="$ROOT/home"
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  log "creating service account: $SVC_USER (HOME=$SVC_HOME)"
  run_local mkdir -p "$ROOT"
  run_local useradd --system --create-home --home-dir "$SVC_HOME" --shell /bin/bash "$SVC_USER"
else
  CURRENT_HOME="$(getent passwd "$SVC_USER" | cut -d: -f6)"
  if [[ "$CURRENT_HOME" != "$SVC_HOME" ]]; then
    if [[ -n "$DATA_DISK" ]]; then
      migrate_home "$CURRENT_HOME" "$SVC_HOME"
    else
      log "note: $SVC_USER's HOME is $CURRENT_HOME (not on $ROOT); pass --data-disk to move it"
    fi
  fi
fi

# ── itervox binary ──────────────────────────────────────────────────────────
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) echo "error: unsupported arch $ARCH" >&2; exit 1 ;;
esac

if [[ "$VERSION" == "latest" ]]; then
  if $DRY_RUN || $DRY_RUN_NETWORK; then
    echo "[dry-run] would resolve latest release tag from the GitHub API"
  else
    VERSION="$(curl -fsSL https://api.github.com/repos/vnovick/itervox/releases/latest | jq -r .tag_name)"
  fi
fi

log "installing itervox $VERSION (linux/$ARCH)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
run_net curl -fsSL -o "$TMP/itervox.tar.gz" \
  "https://github.com/vnovick/itervox/releases/download/${VERSION}/itervox_linux_${ARCH}.tar.gz"
# CORE-107: verify the archive against the release's checksums.txt before
# installing anything from it (same check as upgrade.sh).
run_net curl -fsSL -o "$TMP/checksums.txt" \
  "https://github.com/vnovick/itervox/releases/download/${VERSION}/checksums.txt"
if ! $DRY_RUN && ! $DRY_RUN_NETWORK; then
  want="$(awk -v f="itervox_linux_${ARCH}.tar.gz" '$2 == f || $2 == "*"f {print $1; exit}' "$TMP/checksums.txt")"
  got="$(sha256sum "$TMP/itervox.tar.gz" | cut -d' ' -f1)"
  if [[ -z "$want" || "$want" != "$got" ]]; then
    echo "error: itervox_linux_${ARCH}.tar.gz does not match checksums.txt (want ${want:-no entry}, got $got)" >&2
    exit 1
  fi
  log "sha256 verified against checksums.txt"
fi
run_net tar -xzf "$TMP/itervox.tar.gz" -C "$TMP"
run_net install -m 0755 "$TMP/itervox" /usr/local/bin/itervox
if ! $DRY_RUN && ! $DRY_RUN_NETWORK; then
  /usr/local/bin/itervox --version
fi

# CORE-061: install secret-manager fetch script
log "installing fetch-secrets script"
run_local install -m 0755 "$(dirname "$0")/lib/fetch-secrets.sh" /usr/local/bin/fetch-secrets.sh

# CORE-107/CORE-113: upgrade, cleanup and (optional) watchdog helpers.
log "installing upgrade, cleanup and watchdog helpers"
run_local install -m 0755 "$(dirname "$0")/upgrade.sh" /usr/local/bin/itervox-upgrade.sh
run_local install -m 0755 "$(dirname "$0")/cleanup.sh" /usr/local/bin/itervox-cleanup.sh
run_local install -m 0755 "$(dirname "$0")/monitoring/itervox-watchdog.sh" /usr/local/bin/itervox-watchdog.sh

# ── project checkout ────────────────────────────────────────────────────────
REPO_NAME="$(basename "${REPO%.git}")"
WORKDIR="$ROOT/$REPO_NAME"
run_local mkdir -p "$ROOT"
run_local chown "$SVC_USER:$SVC_USER" "$ROOT"

# Repository access MUST be established before the clone below — a fresh
# service user has no credentials, and set -euo pipefail aborts the script
# the moment `git clone` fails.
setup_repo_access
clone_repo
set_git_identity

run_local sudo -u "$SVC_USER" mkdir -p "$WORKDIR/.itervox"

# ── .env scaffold ───────────────────────────────────────────────────────────
scaffold_env_file

# ── systemd unit ────────────────────────────────────────────────────────────
log "installing systemd unit"
run_local bash -c "sed -e 's|@USER@|$SVC_USER|g' -e 's|@WORKDIR@|$WORKDIR|g' -e 's|@ROOT@|$ROOT|g' \
  '$(dirname "$0")/systemd/itervox.service' > /etc/systemd/system/itervox.service"
# Timers are installed but NOT enabled: cleanup deletes workspaces and the
# watchdog restarts the service, so both are an explicit operator choice
# (see the closing message and deploy/README.md).
for unit in itervox-cleanup.service itervox-cleanup.timer itervox-watchdog.service itervox-watchdog.timer; do
  run_local bash -c "sed -e 's|@USER@|$SVC_USER|g' -e 's|@WORKDIR@|$WORKDIR|g' -e 's|@ROOT@|$ROOT|g' \
    '$(dirname "$0")/systemd/$unit' > /etc/systemd/system/$unit"
done
if command -v systemctl >/dev/null; then
  run_local systemctl daemon-reload
fi

# ── optional public TLS ─────────────────────────────────────────────────────
if [[ -n "$PUBLIC_DOMAIN" ]]; then
  install_caddy
  run_local bash -c "sed 's|@DOMAIN@|$PUBLIC_DOMAIN|g' '$(dirname "$0")/caddy/Caddyfile' > /etc/caddy/Caddyfile"
  if command -v systemctl >/dev/null; then
    run_local systemctl restart caddy
  fi
  echo
  echo "  Caddy is fronting itervox at https://$PUBLIC_DOMAIN"
  echo "  Open once at: https://$PUBLIC_DOMAIN/?token=<ITERVOX_API_TOKEN>"
fi

# ── deploy doctor (CORE-063) ────────────────────────────────────────────────
# Read-only probes: agent credentials, gh auth, git push auth (dry-run), the
# tracker API, and the daemon's /api/v1/ready. It exits 1 on any [fail]; a
# fresh install fails the credential probes until .env is filled in, so the
# result is reported, not fatal. /ready is [warn] until the service starts.
DOCTOR_RC=0
if $DRY_RUN || $DRY_RUN_NETWORK; then
  echo "[dry-run] (cd $WORKDIR && sudo -H --preserve-env=GH_TOKEN -u $SVC_USER /usr/local/bin/itervox doctor --deploy --workflow WORKFLOW.md)"
elif [[ -f "$WORKDIR/WORKFLOW.md" ]]; then
  log "running itervox doctor --deploy"
  # GH_TOKEN (when set) reaches the gh / git push probes the way it reached
  # the clone: --preserve-env, never argv.
  (cd "$WORKDIR" && sudo -H --preserve-env=GH_TOKEN -u "$SVC_USER" /usr/local/bin/itervox doctor --deploy --workflow WORKFLOW.md) || DOCTOR_RC=$?
else
  log "no $WORKDIR/WORKFLOW.md yet — run 'itervox init' there, then 'itervox doctor --deploy'"
  DOCTOR_RC=1
fi

cat <<EOF

────────────────────────────────────────────────────────────────
 Bootstrap complete.$( [[ $DOCTOR_RC -ne 0 ]] && printf '  itervox doctor --deploy reported problems (exit %s) — see above.' "$DOCTOR_RC" )

 Next:
   1. Fill in $ENV_FILE
      (or configure a secret manager in /etc/itervox/secrets.env — see
       deploy/lib/fetch-secrets.sh)
   2. Ensure $WORKDIR/WORKFLOW.md sets  server.port: 8090  (omitted also defaults to 8090)
   3. sudo -H -u $SVC_USER bash -c "cd $WORKDIR && itervox doctor --deploy --workflow WORKFLOW.md"
      until it reports no [fail]
   4. sudo systemctl enable --now itervox
   5. sudo journalctl -u itervox -f
      sudo -u $SVC_USER tail -f $SVC_HOME/.itervox/logs/*/*/itervox.log
   6. Optional: sudo systemctl enable --now itervox-cleanup.timer   (prunes idle
      workspaces/old logs; retention in /etc/itervox/cleanup.env) and
      itervox-watchdog.timer (restarts a wedged event loop).

 Upgrading: sudo itervox-upgrade.sh --version <tag>  (checksum-verified,
 atomic swap, rolls back when /api/v1/ready does not come back).

 Stopping: 'systemctl stop itervox' drains in-flight agent turns for up to
 --shutdown-grace (240s in the unit; TimeoutStopSec=300). To stop a daemon
 by hand, give 'itervox stop' a longer grace than the drain, e.g.
   itervox stop --grace 275s
 (its default --grace 30s SIGKILLs a daemon that is still draining).

 NOTE: itervox fans logs out to both stderr and the rotating file sink when
 headless (no controlling terminal), so journald keeps working for the whole
 run — see monitoring/README.md.
────────────────────────────────────────────────────────────────
EOF
