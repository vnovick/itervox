#!/usr/bin/env bash
# Writes the updater manifest (latest.json) of a release (#130).
#
# The Mac app's updater (tauri-plugin-updater) fetches
# https://github.com/<repo>/releases/latest/download/latest.json, compares
# `version` with its own, and installs the archive of its platform after
# checking the archive's minisign signature against the public key built
# into the app. The release ships one universal app, so every Mac platform
# key points at the same archive and signature.
#
# Usage:
#   gen-latest-json.sh --version 0.2.6 \
#     --url https://github.com/o/r/releases/download/v0.2.6/Itervox_0.2.6_universal.app.tar.gz \
#     --signature-file Itervox_0.2.6_universal.app.tar.gz.sig \
#     [--notes-file notes.md] [--pub-date 2026-10-14T00:00:00Z] [--out latest.json]
#
# Needs jq. Prints the manifest to stdout unless --out is given.
set -euo pipefail

die() {
  echo "gen-latest-json: $*" >&2
  exit 1
}

version="" url="" sig_file="" notes_file="" pub_date="" out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version="${2-}"; shift 2 ;;
    --url) url="${2-}"; shift 2 ;;
    --signature-file) sig_file="${2-}"; shift 2 ;;
    --notes-file) notes_file="${2-}"; shift 2 ;;
    --pub-date) pub_date="${2-}"; shift 2 ;;
    --out) out="${2-}"; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required"
version="${version#v}"
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
[[ "$version" =~ $semver ]] || die "--version '$version' is not MAJOR.MINOR.PATCH[-PRERELEASE]"
[[ "$url" == https://* ]] || die "--url must be an https URL"
if [ -z "$sig_file" ] || [ ! -s "$sig_file" ]; then
  die "--signature-file is missing or empty"
fi
if [ -n "$notes_file" ] && [ ! -f "$notes_file" ]; then
  die "--notes-file $notes_file does not exist"
fi
[ -n "$pub_date" ] || pub_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

signature="$(tr -d '\r' <"$sig_file")"
notes=""
[ -z "$notes_file" ] || notes="$(cat "$notes_file")"

manifest="$(jq -n \
  --arg version "$version" \
  --arg notes "$notes" \
  --arg pub_date "$pub_date" \
  --arg url "$url" \
  --arg signature "$signature" \
  '{version: $version, notes: $notes, pub_date: $pub_date,
    platforms: ({signature: $signature, url: $url} as $p
      | {"darwin-aarch64-app": $p, "darwin-aarch64": $p,
         "darwin-x86_64-app": $p, "darwin-x86_64": $p})}')"

if [ -n "$out" ]; then
  printf '%s\n' "$manifest" >"$out"
  echo "gen-latest-json: wrote $out (version $version)" >&2
else
  printf '%s\n' "$manifest"
fi
