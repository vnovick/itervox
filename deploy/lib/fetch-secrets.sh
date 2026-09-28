#!/usr/bin/env bash
#
# CORE-061: fetch itervox secrets from a cloud secret manager and write them
# as a systemd EnvironmentFile (default /run/itervox/env, mode 0600).
#
# Wired as `ExecStartPre=/usr/local/bin/fetch-secrets.sh` in
# deploy/systemd/itervox.service; the unit's `EnvironmentFile=-/run/itervox/env`
# is read when ExecStart is spawned, after this has run.
#
# Configuration (environment; for systemd put it in /etc/itervox/secrets.env,
# which the unit loads with EnvironmentFile=-):
#
#   ITERVOX_SECRETS_PROVIDER   gcp | aws | azure   (unset/empty: no-op, exit 0)
#   ITERVOX_SECRETS_FILE       output path (default /run/itervox/env)
#   ITERVOX_SECRETS_MAP        space-separated ENV_NAME=secret-name pairs, e.g.
#                              "LINEAR_API_KEY=itervox-linear ANTHROPIC_API_KEY=itervox-anthropic"
#   gcp:   ITERVOX_SECRETS_GCP_PROJECT (required)
#   aws:   ITERVOX_SECRETS_AWS_REGION  (default: the CLI's configured region)
#   azure: ITERVOX_SECRETS_AZURE_VAULT (required)
#
#   Legacy per-key variables are still honoured and appended to the map:
#   ITERVOX_SECRETS_{GCP,AWS,AZURE}_{LINEAR,ANTHROPIC}_SECRET.
#
# Output format — systemd.exec(5) EnvironmentFile, double-quoted values:
#   KEY="value"
# Inside double quotes systemd's parser (src/basic/env-file.c) turns \" \\ \$
# and \` into the literal character and keeps every other byte, including
# spaces, '=', '#', single quotes and real newlines, verbatim. So escaping
# exactly those four characters round-trips any value. Command substitution
# strips trailing newlines from what the CLI prints (the CLIs append one).
#
# Guarantees:
#   - never prints a secret value (stdout stays empty; errors name the key and
#     secret name only; the CLIs' stderr is discarded because some echo input);
#   - the file is built in a 0600 temp file (umask 077) in the same directory
#     and renamed into place, so a reader never sees a partial file and a
#     failed run leaves the previous file intact;
#   - re-running with the same secrets produces a byte-identical file.
#
# Exit: 0 success or no provider; 1 fetch/config error; 2 usage error.
set -euo pipefail
set -f # no globbing: ITERVOX_SECRETS_MAP is word-split below
umask 077

SECRETS_FILE="${ITERVOX_SECRETS_FILE:-/run/itervox/env}"
PROVIDER="${ITERVOX_SECRETS_PROVIDER:-}"

err() { printf 'fetch-secrets: %s\n' "$*" >&2; }

# systemd_quote VALUE -> "VALUE" with \ " $ ` escaped (see header).
systemd_quote() {
  local v="$1"
  v="${v//\\/\\\\}"
  v="${v//\"/\\\"}"
  v="${v//\$/\\\$}"
  v="${v//\`/\\\`}"
  printf '"%s"' "$v"
}

valid_key() { [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; }

# fetch_one PROVIDER SECRET_NAME -> secret on stdout (captured by caller only).
fetch_one() {
  local name="$2"
  case "$1" in
    gcp)
      gcloud secrets versions access latest --secret="$name" \
        --project="${ITERVOX_SECRETS_GCP_PROJECT}" --quiet 2>/dev/null
      ;;
    aws)
      local region_args=()
      if [[ -n "${ITERVOX_SECRETS_AWS_REGION:-}" ]]; then
        region_args=(--region "$ITERVOX_SECRETS_AWS_REGION")
      fi
      aws secretsmanager get-secret-value --secret-id "$name" ${region_args[@]+"${region_args[@]}"} \
        --query SecretString --output text 2>/dev/null
      ;;
    azure)
      az keyvault secret show --vault-name "${ITERVOX_SECRETS_AZURE_VAULT}" \
        --name "$name" --query value --output tsv 2>/dev/null
      ;;
  esac
}

build_map() {
  local map="${ITERVOX_SECRETS_MAP:-}" up
  up="$(printf '%s' "$PROVIDER" | tr '[:lower:]' '[:upper:]')"
  local legacy_linear="ITERVOX_SECRETS_${up}_LINEAR_SECRET"
  local legacy_anthropic="ITERVOX_SECRETS_${up}_ANTHROPIC_SECRET"
  if [[ -n "${!legacy_linear:-}" ]]; then map+=" LINEAR_API_KEY=${!legacy_linear}"; fi
  if [[ -n "${!legacy_anthropic:-}" ]]; then map+=" ANTHROPIC_API_KEY=${!legacy_anthropic}"; fi
  printf '%s' "$map"
}

main() {
  if [[ -z "$PROVIDER" ]]; then
    exit 0 # nothing configured: the unit's EnvironmentFile=- tolerates absence
  fi
  case "$PROVIDER" in
    gcp)
      [[ -n "${ITERVOX_SECRETS_GCP_PROJECT:-}" ]] || { err "provider gcp needs ITERVOX_SECRETS_GCP_PROJECT"; exit 1; }
      command -v gcloud >/dev/null || { err "gcloud not found on PATH"; exit 1; } ;;
    aws)
      command -v aws >/dev/null || { err "aws not found on PATH"; exit 1; } ;;
    azure)
      [[ -n "${ITERVOX_SECRETS_AZURE_VAULT:-}" ]] || { err "provider azure needs ITERVOX_SECRETS_AZURE_VAULT"; exit 1; }
      command -v az >/dev/null || { err "az not found on PATH"; exit 1; } ;;
    *)
      err "unknown ITERVOX_SECRETS_PROVIDER '$PROVIDER' (want gcp, aws or azure)"; exit 2 ;;
  esac

  local map pair key name value
  map="$(build_map)"
  if [[ -z "${map// /}" ]]; then
    err "no secrets mapped: set ITERVOX_SECRETS_MAP (e.g. \"LINEAR_API_KEY=my-linear-secret\")"
    exit 2
  fi

  local dir tmp
  dir="$(dirname "$SECRETS_FILE")"
  mkdir -p "$dir"
  tmp="$(mktemp "$dir/.env.XXXXXX")"
  # shellcheck disable=SC2064 # expand $tmp now: the trap must remove this file
  trap "rm -f '$tmp'" EXIT
  chmod 0600 "$tmp"

  local seen=" "
  for pair in $map; do
    key="${pair%%=*}"
    name="${pair#*=}"
    if [[ "$pair" != *=* ]] || ! valid_key "$key" || [[ -z "$name" ]]; then
      err "bad ITERVOX_SECRETS_MAP entry '$pair' (want ENV_NAME=secret-name)"
      exit 2
    fi
    if [[ "$seen" == *" $key "* ]]; then
      continue # first mapping of a key wins; keeps the output deterministic
    fi
    seen+="$key "
    if ! value="$(fetch_one "$PROVIDER" "$name")"; then
      err "failed to fetch $key (secret '$name') from $PROVIDER"
      exit 1
    fi
    printf '%s=%s\n' "$key" "$(systemd_quote "$value")" >> "$tmp"
  done

  mv -f "$tmp" "$SECRETS_FILE"
  trap - EXIT
}

main "$@"
