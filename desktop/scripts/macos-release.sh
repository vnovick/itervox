#!/usr/bin/env bash
# Signing, notarization and verification steps of the Mac release (#130),
# used by .github/workflows/desktop-release.yml and runnable on a Mac.
#
# Credentials come only from the environment and are never printed:
#   APPLE_CERTIFICATE            base64 of the Developer ID Application .p12
#   APPLE_CERTIFICATE_PASSWORD   its export password
#   APPLE_SIGNING_IDENTITY       e.g. "Developer ID Application: Name (TEAMID)"
# Notarization, either an App Store Connect API key (preferred) ...
#   APPLE_API_KEY_ID, APPLE_API_ISSUER, APPLE_API_PRIVATE_KEY (.p8 content,
#   PEM or base64 of it)
# ... or an Apple ID with an app-specific password:
#   APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
# Updater archive signature:
#   TAURI_SIGNING_PRIVATE_KEY, TAURI_SIGNING_PRIVATE_KEY_PASSWORD
#
# Subcommands:
#   mode [tauri.conf.json]        print sign=/notarize=/updater= (and append
#                                 them to $GITHUB_OUTPUT when set); fails on a
#                                 half-configured secret set
#   import-cert                   import the certificate into a temporary keychain
#   delete-keychain               remove that keychain
#   sign <file> <identifier>      codesign with hardened runtime + timestamp
#   notarize <file.zip|.dmg>      submit with notarytool and wait; fail unless Accepted
#   staple <app|dmg>              staple the notarization ticket and validate it
#   make-dmg <app> <out.dmg>      a compressed disk image with an Applications link
#   updater-archive <app> <out>   the .app.tar.gz the updater installs
#   verify-app <app>              codesign --deep --strict, spctl execute, stapler, sidecar runtime
#   verify-dmg <dmg>              codesign --strict, spctl open, stapler
#   verify-cli <binary>           codesign --strict, hardened runtime, Developer ID, universal
set -euo pipefail
set +x # never trace: commands below receive credentials

die() {
  echo "macos-release: $*" >&2
  exit 1
}

keychain_path() {
  echo "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/itervox-signing.keychain-db"
}

has() { [ -n "${!1-}" ]; }

# --- mode -----------------------------------------------------------------

cmd_mode() {
  local conf="${1:-desktop/src-tauri/tauri.conf.json}"
  local sign=false notarize=false updater=false
  local cert_vars=(APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY)
  local set_count=0 v
  for v in "${cert_vars[@]}"; do has "$v" && set_count=$((set_count + 1)); done
  if [ "$set_count" -eq 3 ]; then
    sign=true
  elif [ "$set_count" -gt 0 ]; then
    die "only some of ${cert_vars[*]} are set; set all three or none"
  fi

  if $sign; then
    if has APPLE_API_KEY_ID && has APPLE_API_ISSUER && has APPLE_API_PRIVATE_KEY; then
      notarize=true
    elif has APPLE_ID && has APPLE_PASSWORD && has APPLE_TEAM_ID; then
      notarize=true
    else
      die "the signing certificate is set but notarization credentials are not: set APPLE_API_KEY_ID, APPLE_API_ISSUER and APPLE_API_PRIVATE_KEY, or APPLE_ID, APPLE_PASSWORD and APPLE_TEAM_ID (a signed but unnotarized app is still blocked by Gatekeeper)"
    fi
  else
    echo "::warning title=Unsigned Mac build::Apple signing secrets are not available (a fork, a pull request, or not configured). The app and CLI are built unsigned and not notarized; Gatekeeper will block them on other Macs. No updater manifest is published."
  fi

  if has TAURI_SIGNING_PRIVATE_KEY; then
    local pubkey
    pubkey="$(jq -r '.plugins.updater.pubkey // ""' "$conf")"
    if [ -z "$pubkey" ]; then
      die "TAURI_SIGNING_PRIVATE_KEY is set but plugins.updater.pubkey in $conf is empty: commit the matching public key first (docs/release-mac.md), or the shipped app could never verify an update"
    fi
    if $sign; then
      updater=true
    else
      echo "::warning title=No updater manifest::the build is unsigned, so no latest.json is published (an unsigned update would be offered to signed installs)."
    fi
  elif $sign; then
    echo "::warning title=No updater manifest::TAURI_SIGNING_PRIVATE_KEY is not set: the release is signed but publishes no latest.json, so installed apps will not be offered this update."
  fi

  local out="sign=$sign
notarize=$notarize
updater=$updater"
  echo "$out"
  if [ -n "${GITHUB_OUTPUT-}" ]; then
    echo "$out" >>"$GITHUB_OUTPUT"
  fi
}

# --- keychain -------------------------------------------------------------

cmd_import_cert() {
  local v
  for v in APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY; do
    has "$v" || die "$v is not set"
  done
  local kc kc_pass p12
  kc="$(keychain_path)"
  kc_pass="$(openssl rand -hex 24)"
  p12="$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/cert.XXXXXX")"
  # shellcheck disable=SC2064 # expand now: p12 is local
  trap "rm -f '$p12'" EXIT
  printf '%s' "$APPLE_CERTIFICATE" | base64 --decode >"$p12" \
    || die "APPLE_CERTIFICATE is not valid base64"

  security create-keychain -p "$kc_pass" "$kc"
  security set-keychain-settings -lut 21600 "$kc"
  security unlock-keychain -p "$kc_pass" "$kc"
  security import "$p12" -k "$kc" -P "$APPLE_CERTIFICATE_PASSWORD" -f pkcs12 -t cert \
    -T /usr/bin/codesign -T /usr/bin/security >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$kc_pass" "$kc" >/dev/null
  # Put the temporary keychain first in the search list, keeping the others.
  local existing=()
  while IFS= read -r line; do
    line="${line//\"/}"
    line="${line#"${line%%[![:space:]]*}"}"
    [ -n "$line" ] && existing+=("$line")
  done < <(security list-keychains -d user)
  security list-keychains -d user -s "$kc" "${existing[@]}"
  if ! security find-identity -v -p codesigning "$kc" | grep -qF -- "$APPLE_SIGNING_IDENTITY"; then
    die "the imported certificate does not provide APPLE_SIGNING_IDENTITY (is it a Developer ID Application certificate with its private key?)"
  fi
  echo "macos-release: signing identity imported into a temporary keychain"
}

cmd_delete_keychain() {
  local kc
  kc="$(keychain_path)"
  [ -f "$kc" ] || return 0
  security delete-keychain "$kc" || true
  echo "macos-release: temporary keychain deleted"
}

# --- signing and notarization --------------------------------------------

cmd_sign() {
  local file="${1:?usage: sign <file> <identifier>}" ident="${2:?usage: sign <file> <identifier>}"
  has APPLE_SIGNING_IDENTITY || die "APPLE_SIGNING_IDENTITY is not set"
  codesign --force --timestamp --options runtime \
    --identifier "$ident" --sign "$APPLE_SIGNING_IDENTITY" "$file"
  echo "macos-release: signed $file ($ident)"
}

notary_auth() {
  # Fills the global NOTARY_AUTH array.
  NOTARY_AUTH=()
  if has APPLE_API_KEY_ID && has APPLE_API_ISSUER && has APPLE_API_PRIVATE_KEY; then
    local dir key
    dir="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/notary.XXXXXX")"
    key="$dir/AuthKey_${APPLE_API_KEY_ID}.p8"
    (
      umask 077
      case "$APPLE_API_PRIVATE_KEY" in
        -----BEGIN*) printf '%s\n' "$APPLE_API_PRIVATE_KEY" >"$key" ;;
        *) printf '%s' "$APPLE_API_PRIVATE_KEY" | base64 --decode >"$key" ;;
      esac
    ) || die "APPLE_API_PRIVATE_KEY is neither a PEM key nor base64 of one"
    NOTARY_KEY_DIR="$dir"
    NOTARY_AUTH=(--key "$key" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER")
  elif has APPLE_ID && has APPLE_PASSWORD && has APPLE_TEAM_ID; then
    NOTARY_AUTH=(--apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID")
  else
    die "no notarization credentials (API key or Apple ID) are set"
  fi
}

cmd_notarize() {
  local file="${1:?usage: notarize <file>}"
  [ -f "$file" ] || die "$file does not exist"
  NOTARY_KEY_DIR=""
  notary_auth
  if [ -n "$NOTARY_KEY_DIR" ]; then
    # shellcheck disable=SC2064 # expand now
    trap "rm -rf '$NOTARY_KEY_DIR'" EXIT
  fi
  echo "macos-release: notarizing $file (this usually takes a few minutes)"
  local result status id
  result="$(xcrun notarytool submit "$file" "${NOTARY_AUTH[@]}" \
    --wait --timeout 45m --output-format json)" || true
  status="$(printf '%s' "$result" | jq -r '.status // "unknown"' 2>/dev/null || echo unknown)"
  id="$(printf '%s' "$result" | jq -r '.id // ""' 2>/dev/null || true)"
  echo "macos-release: notarization of $(basename "$file"): $status${id:+ (submission $id)}"
  if [ "$status" != "Accepted" ]; then
    if [ -n "$id" ]; then
      xcrun notarytool log "$id" "${NOTARY_AUTH[@]}" || true
    fi
    die "notarization of $file was not accepted"
  fi
}

cmd_staple() {
  local path="${1:?usage: staple <app|dmg>}"
  xcrun stapler staple "$path"
  xcrun stapler validate "$path"
}

cmd_make_dmg() {
  local app="${1:?usage: make-dmg <app> <out.dmg>}" out="${2:?usage: make-dmg <app> <out.dmg>}"
  local stage
  stage="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/dmg.XXXXXX")"
  ditto "$app" "$stage/$(basename "$app")"
  ln -s /Applications "$stage/Applications"
  rm -f "$out"
  hdiutil create -volname Itervox -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$out" >/dev/null
  rm -rf "$stage"
  echo "macos-release: wrote $out"
}

cmd_updater_archive() {
  local app="${1:?usage: updater-archive <app> <out.app.tar.gz>}" out="${2:?usage: updater-archive <app> <out.app.tar.gz>}"
  # The updater skips the first path component and swaps the bundle in
  # place, so the archive holds exactly <Name>.app/… . COPYFILE_DISABLE
  # keeps AppleDouble ._ files out.
  COPYFILE_DISABLE=1 tar -czf "$out" -C "$(dirname "$app")" "$(basename "$app")"
  echo "macos-release: wrote $out"
}

# --- verification ---------------------------------------------------------

require_runtime() {
  local file="$1" info
  info="$(codesign -dv --verbose=4 "$file" 2>&1)"
  grep -Eq 'flags=0x[0-9a-f]*\(.*runtime' <<<"$info" \
    || die "$file is not signed with the hardened runtime"
  grep -q '^Authority=Developer ID Application' <<<"$info" \
    || die "$file is not signed with a Developer ID Application certificate"
  grep -q '^Timestamp=' <<<"$info" || die "$file has no secure timestamp"
}

cmd_verify_app() {
  local app="${1:?usage: verify-app <app>}"
  codesign --verify --deep --strict --verbose=2 "$app"
  spctl --assess --type execute -vv "$app" 2>&1 | tee "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/spctl.txt"
  grep -q 'source=Notarized Developer ID' "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/spctl.txt" \
    || die "spctl accepted $app but not as Notarized Developer ID"
  xcrun stapler validate "$app"
  require_runtime "$app"
  require_runtime "$app/Contents/MacOS/itervox"
  echo "macos-release: $app verified (signature, Gatekeeper, ticket, sidecar)"
}

cmd_verify_dmg() {
  local dmg="${1:?usage: verify-dmg <dmg>}"
  codesign --verify --strict --verbose=2 "$dmg"
  spctl --assess --type open --context context:primary-signature -vv "$dmg"
  xcrun stapler validate "$dmg"
  echo "macos-release: $dmg verified"
}

cmd_verify_cli() {
  local bin="${1:?usage: verify-cli <binary>}"
  codesign --verify --strict --verbose=2 "$bin"
  require_runtime "$bin"
  local archs
  archs="$(lipo -archs "$bin")"
  [[ " $archs " == *" arm64 "* && " $archs " == *" x86_64 "* ]] \
    || die "$bin is not universal (archs: $archs)"
  # Bare Mach-O binaries cannot be stapled and spctl only assesses bundles;
  # the notarization ticket is looked up online on first run.
  echo "macos-release: $bin verified (signature, hardened runtime, $archs)"
}

main() {
  local sub="${1-}"
  [ -n "$sub" ] || die "usage: $0 <subcommand> [args] (see the header)"
  shift
  case "$sub" in
    mode) cmd_mode "$@" ;;
    import-cert) cmd_import_cert ;;
    delete-keychain) cmd_delete_keychain ;;
    sign) cmd_sign "$@" ;;
    notarize) cmd_notarize "$@" ;;
    staple) cmd_staple "$@" ;;
    make-dmg) cmd_make_dmg "$@" ;;
    updater-archive) cmd_updater_archive "$@" ;;
    verify-app) cmd_verify_app "$@" ;;
    verify-dmg) cmd_verify_dmg "$@" ;;
    verify-cli) cmd_verify_cli "$@" ;;
    *) die "unknown subcommand $sub" ;;
  esac
}

main "$@"
