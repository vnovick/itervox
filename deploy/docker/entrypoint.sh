#!/usr/bin/env bash
#
# Entrypoint for the itervox container image (CORE-060). Runs under tini
# (PID 1), as uid 10001, with HOME=/data.
#
#   1. Clone ITERVOX_REPO into ITERVOX_REPO_DIR (default /data/repo) ONCE. On
#      every later start the existing clone is reused: it is fetched and
#      fast-forwarded when that is possible without touching local changes,
#      and left exactly as it is otherwise. It is never re-cloned, reset or
#      cleaned.
#   2. exec itervox "$@" from the checkout, so the daemon replaces this shell
#      and receives tini's forwarded SIGTERM directly (CORE-057 drain).
#
# Environment:
#   ITERVOX_REPO          git URL to clone on first start. Optional when
#                         ITERVOX_REPO_DIR already holds a checkout (a
#                         bind-mounted working copy, or a previous start).
#   ITERVOX_REPO_BRANCH   branch to clone/fast-forward (default: remote HEAD).
#   ITERVOX_REPO_DIR      checkout location (default /data/repo).
#   ITERVOX_REPO_UPDATE   "ff" (default) fast-forward on start, "off" = never fetch.
#   GH_TOKEN/GITHUB_TOKEN when set, a git credential helper for https://github.com
#                         is configured that reads the token from the
#                         environment at use time — the token is never written
#                         to disk, put in a URL, or printed.
#   ITERVOX_GIT_NAME/ITERVOX_GIT_EMAIL  commit identity (default itervox-bot).
#
# Secrets: nothing here prints an environment value. Git URLs are printed with
# their userinfo replaced by "***", and git's own stderr is filtered the same
# way. Prefer GH_TOKEN over a token embedded in ITERVOX_REPO.
set -euo pipefail

REPO_URL="${ITERVOX_REPO:-}"
REPO_BRANCH="${ITERVOX_REPO_BRANCH:-}"
REPO_DIR="${ITERVOX_REPO_DIR:-/data/repo}"
REPO_UPDATE="${ITERVOX_REPO_UPDATE:-ff}"

log() { printf 'itervox-entrypoint: %s\n' "$*" >&2; }

# redact strips URL userinfo (user:token@) from a stream.
redact() { sed -E 's#([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@[:space:]]+@#\1***@#g'; }
redact_str() { printf '%s' "$1" | redact; }

# git_quiet runs git with its stderr passed through redact; keeps git's status.
git_quiet() {
  local rc=0
  git "$@" 2> >(redact >&2) || rc=$?
  return "$rc"
}

configure_git() {
  # Identity: only when the operator has not set one (a mounted ~/.gitconfig wins).
  if ! git config --global user.name >/dev/null 2>&1; then
    git config --global user.name "${ITERVOX_GIT_NAME:-itervox-bot}"
  fi
  if ! git config --global user.email >/dev/null 2>&1; then
    git config --global user.email "${ITERVOX_GIT_EMAIL:-itervox-bot@users.noreply.github.com}"
  fi
  # Token-from-environment helper. The single quotes are deliberate: the
  # stored helper text contains the literal "${GH_TOKEN:-$GITHUB_TOKEN}",
  # which git's shell expands only when a credential is requested.
  if [[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]] \
     && ! git config --global --get credential.https://github.com.helper >/dev/null 2>&1; then
    # shellcheck disable=SC2016
    git config --global credential.https://github.com.helper \
      '!f() { test "$1" = get || exit 0; echo username=x-access-token; echo "password=${GH_TOKEN:-$GITHUB_TOKEN}"; }; f'
  fi
}

clone_once() {
  if [[ -d "$REPO_DIR/.git" ]]; then
    log "reusing existing checkout at $REPO_DIR (no re-clone)"
    update_checkout
    return 0
  fi
  if [[ -z "$REPO_URL" ]]; then
    log "error: no checkout at $REPO_DIR and ITERVOX_REPO is not set"
    exit 64
  fi
  if [[ -e "$REPO_DIR" ]] && [[ -n "$(ls -A "$REPO_DIR" 2>/dev/null)" ]]; then
    log "error: $REPO_DIR exists, is not empty, and is not a git checkout; refusing to clone over it"
    exit 64
  fi
  log "cloning $(redact_str "$REPO_URL") into $REPO_DIR (first start)"
  local args=(clone --quiet)
  if [[ -n "$REPO_BRANCH" ]]; then
    args+=(--branch "$REPO_BRANCH")
  fi
  git_quiet "${args[@]}" -- "$REPO_URL" "$REPO_DIR"
}

update_checkout() {
  if [[ "$REPO_UPDATE" == "off" ]]; then
    log "ITERVOX_REPO_UPDATE=off: not fetching"
    return 0
  fi
  local branch upstream
  branch="$(git -C "$REPO_DIR" symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
  if [[ -z "$branch" ]]; then
    log "checkout is on a detached HEAD; leaving it as is"
    return 0
  fi
  upstream="$(git -C "$REPO_DIR" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  if [[ -z "$upstream" ]]; then
    log "branch $branch has no upstream; leaving it as is"
    return 0
  fi
  if ! git_quiet -C "$REPO_DIR" fetch --quiet; then
    log "warning: fetch failed; starting on the existing checkout"
    return 0
  fi
  # --ff-only never creates a merge and aborts (touching nothing) when the
  # branch diverged or local edits would be overwritten.
  if git_quiet -C "$REPO_DIR" merge --ff-only --quiet "$upstream"; then
    log "checkout of $branch is up to date with $upstream"
  else
    log "warning: $branch cannot fast-forward to $upstream (local commits or edits); left untouched"
  fi
}

main() {
  configure_git
  clone_once
  mkdir -p "$REPO_DIR/.itervox"
  cd "$REPO_DIR"
  exec itervox "$@"
}

main "$@"
