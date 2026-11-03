# Mac release: signing, notarization and updates

Every `vX.Y.Z` tag publishes, next to GoReleaser's archives, a signed and
notarized Mac build (#130). This page covers the one-time setup, what the
workflow does, and how to verify a build by hand. The app itself is
described in the [Mac app guide](../site/src/content/docs/guides/mac-app.mdx).

## What a release contains

| Asset | What |
|---|---|
| `Itervox_<version>_universal.dmg` | The app (Apple silicon and Intel), signed, notarized and stapled; the disk image is signed, notarized and stapled too |
| `itervox_<version>_darwin_universal.zip` | The `itervox` CLI, universal, signed with the hardened runtime and notarized |
| `Itervox_<version>_universal.app.tar.gz` (+ `.sig`) | The archive installed apps update from, and its updater signature |
| `latest.json` | The updater manifest installed apps read |

After those are uploaded, a notarized release also updates the Homebrew cask
`Casks/itervox-app.rb` in `vnovick/homebrew-tap` (#244), so that
`brew install --cask vnovick/tap/itervox-app` installs the `.dmg` above.

GoReleaser's `itervox_darwin_amd64.tar.gz` / `_arm64.tar.gz` (and the
Homebrew formula built from them) stay as they were: built on Linux and not
signed. Homebrew does not quarantine what it installs, so Gatekeeper does not
check them; the `.zip` above is for a download through a browser.

Without the Apple secrets (a fork, or before they are configured) the job
still builds everything, unsigned, names the files `…-unsigned.dmg` /
`…-unsigned.zip`, prints a warning, and publishes **no** `latest.json`, so an
unsigned build is never offered as an update.

## One-time setup

Add these as **repository secrets** (Settings → Secrets and variables →
Actions). The workflow only passes each one to the step that needs it; the
scripts never print them.

| Secret | What | How to get it |
|---|---|---|
| `APPLE_CERTIFICATE` | base64 of the **Developer ID Application** certificate with its private key, as `.p12` | Keychain Access → My Certificates → the certificate → Export… (`.p12`, with a password), then `base64 -i cert.p12 \| pbcopy` |
| `APPLE_CERTIFICATE_PASSWORD` | the `.p12` export password | — |
| `APPLE_SIGNING_IDENTITY` | the certificate's name, e.g. `Developer ID Application: Jane Doe (ABCDE12345)` | `security find-identity -v -p codesigning` |
| `APPLE_API_KEY_ID` | App Store Connect API key ID (**preferred** notarization credential) | App Store Connect → Users and Access → Integrations → App Store Connect API → Team Keys → generate a key with the **Developer** role |
| `APPLE_API_ISSUER` | the Issuer ID shown above the keys | same page |
| `APPLE_API_PRIVATE_KEY` | the downloaded `AuthKey_<id>.p8`, its PEM text (or base64 of it) | `gh secret set APPLE_API_PRIVATE_KEY < AuthKey_<id>.p8` |
| `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID` | **instead of the API key**: an Apple ID, an [app-specific password](https://support.apple.com/102654) for it, and the team ID | — |
| `TAURI_SIGNING_PRIVATE_KEY` | the updater private key (below) | — |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | its password | — |
| `HOMEBREW_TAP_GITHUB_TOKEN` | a token that can write to `vnovick/homebrew-tap` (the same secret GoReleaser uses for the CLI formula) | a fine-grained token with **Contents: read and write** on the tap |

The job fails, instead of shipping something half-signed, when only some of
the three certificate secrets are set, when the certificate is set but
neither notarization credential set is complete, or when the updater key is
set but no public key is committed.

### The updater key

Updates are verified with a separate minisign key pair, not with the Apple
certificate: the app refuses an archive whose signature does not match the
public key built into it. Generate it once, on a trusted machine:

```bash
make desktop-tauri-cli
cargo tauri signer generate -w ~/.tauri/itervox-updater.key   # asks for a password
gh secret set TAURI_SIGNING_PRIVATE_KEY < ~/.tauri/itervox-updater.key
gh secret set TAURI_SIGNING_PRIVATE_KEY_PASSWORD               # paste the password
cat ~/.tauri/itervox-updater.key.pub                          # the public key
```

Put the public key (the whole one-line content of the `.pub` file) into
`desktop/src-tauri/tauri.conf.json` → `plugins.updater.pubkey` and commit it.
A build whose `pubkey` is empty shows **Updates off: this build has no update
key** in its menu and never checks.

Keep the private key backed up outside GitHub. If it is lost, apps already
installed can never verify another update: users would have to download the
next release by hand. To rotate it, publish one release signed with the old
key that carries the new public key, then switch the secrets.

## What the workflow does

`release.yml` runs GoReleaser (which creates the GitHub release), then calls
`.github/workflows/desktop-release.yml` with the tag. On a macOS runner it:

1. decides the mode from which secrets exist (`desktop/scripts/macos-release.sh mode`);
2. builds the dashboard and the daemon for both architectures and `lipo`s them
   into one universal binary (`make desktop-sidecar`);
3. imports the certificate into a temporary keychain (deleted at the end,
   even when a step fails);
4. signs the standalone CLI (hardened runtime, secure timestamp,
   identifier `dev.itervox.cli`), zips it with `ditto` and notarizes the zip
   with `notarytool`; a bare Mach-O binary cannot be stapled, so macOS looks
   its ticket up online on first run;
5. builds `Itervox.app` for `universal-apple-darwin` with the version taken
   from the tag; Tauri signs the bundled daemon (`Contents/MacOS/itervox`)
   and then the app, both with the hardened runtime and
   `desktop/src-tauri/Entitlements.plist` (network client and server only:
   no JIT, no unsigned memory, no library-validation exception);
6. notarizes the app, staples the ticket, and fails the job unless
   `codesign --verify --deep --strict` and `spctl --assess --type execute -vv`
   both pass, `spctl` reports `source=Notarized Developer ID`, the ticket
   validates, and the app and the bundled daemon both carry the hardened
   runtime, a Developer ID signature and a timestamp;
7. builds, signs, notarizes, staples and checks the `.dmg`
   (`spctl --assess --type open --context context:primary-signature`);
8. archives the stapled app as `.app.tar.gz`, signs it with the updater key
   (`cargo tauri signer sign`), and writes `latest.json`
   (`desktop/scripts/gen-latest-json.sh`, with the CHANGELOG section as the
   notes);
9. uploads everything to the release, `latest.json` last, so a manifest is
   never published before the archive it points at;
10. for a notarized build only, renders `Casks/itervox-app.rb` with the version,
    the `.dmg`'s SHA-256, `depends_on macos:` from
    `bundle.macOS.minimumSystemVersion` and `auto_updates true`
    (`desktop/scripts/homebrew-cask.sh render`), and commits it to the tap
    through the GitHub API (`… publish`). The step does not run without the
    signing secrets, and the script skips (with a notice) without
    `HOMEBREW_TAP_GITHUB_TOKEN` or for a pre-release, so an unsigned or
    pre-release app never reaches the tap.

Re-run it on its own (Actions → **Desktop release** → Run workflow) to build
and verify without publishing: the files are kept as a workflow artifact for
30 days. Re-running the release job for a tag replaces the assets
(`--clobber`).

## How installed apps update

The app reads
`https://github.com/vnovick/itervox/releases/latest/download/latest.json`
10 seconds after launch and every 6 hours (and on **Check for Updates…**).
"Latest" is the newest release that is not marked as a pre-release, so a
pre-release tag is never offered. When the manifest's version is newer, the
menu shows **Update available — Install and Restart**; choosing it downloads
the archive, verifies its signature, replaces the app in place and relaunches.

- The bundled daemon is inside the app, so it is updated with it.
- **Install Command-Line Tool…** in the menu puts a symlink `itervox` into
  `/usr/local/bin` (or `~/.local/bin` when `/usr/local/bin` is not writable)
  pointing at `Itervox.app/Contents/MacOS/itervox`. That symlink is "the CLI
  the app manages": it follows every update with nothing to do. An `itervox`
  the app did not install (Homebrew, a copied binary) is never replaced.
- Daemons the app started are separate, detached processes: they keep
  running through the restart, on the version they were started with. Restart
  a project from the menu to move it to the new version. No agent is
  interrupted by an update.

## Verify a build by hand

On a Mac, with the release's files downloaded (a browser download is
quarantined, which is what Gatekeeper checks):

```bash
# The app: expect "accepted" and "source=Notarized Developer ID".
hdiutil attach Itervox_<version>_universal.dmg
spctl --assess --type execute -vv /Volumes/Itervox/Itervox.app
codesign --verify --deep --strict --verbose=2 /Volumes/Itervox/Itervox.app
xcrun stapler validate /Volumes/Itervox/Itervox.app
# The bundled daemon: flags must include "runtime", Authority "Developer ID Application".
codesign -dv --verbose=4 /Volumes/Itervox/Itervox.app/Contents/MacOS/itervox
lipo -archs /Volumes/Itervox/Itervox.app/Contents/MacOS/itervox      # x86_64 arm64

# The disk image itself.
spctl --assess --type open --context context:primary-signature -vv Itervox_<version>_universal.dmg
xcrun stapler validate Itervox_<version>_universal.dmg

# The CLI (spctl only assesses app bundles; check the signature, then run it).
ditto -x -k itervox_<version>_darwin_universal.zip cli && cd cli
codesign --verify --strict --verbose=2 itervox
codesign -dv --verbose=4 itervox 2>&1 | grep -E 'flags|Authority|Timestamp'
./itervox --version          # no Gatekeeper prompt on a quarantined download

# The updater manifest.
curl -sL https://github.com/vnovick/itervox/releases/latest/download/latest.json | jq .
```

The same checks run inside the workflow through
`desktop/scripts/macos-release.sh verify-app|verify-dmg|verify-cli <path>`,
which also work locally. To sign and notarize from your own Mac with the
same script, export the secrets above in your shell (with the certificate
already in your login keychain, skip `import-cert`) and run
`sign`, `notarize` and `staple`; `notarize` prints `xcrun notarytool log`
output when Apple rejects a submission.

To check the update path end to end: install the previous release from its
`.dmg`, open it, choose **Check for Updates…** after the new release's
`latest.json` is up, then **Update available — Install and Restart**; the app
relaunches and `Itervox.app/Contents/Info.plist` (`CFBundleShortVersionString`)
shows the new version, as does `itervox --version` through an installed
command-line tool.
