//! Dry runs of the release scripts (#130) with fake inputs: the manifest
//! `gen-latest-json.sh` writes must be one the app's updater accepts,
//! `macos-release.sh mode` must pick signed/unsigned from the secrets that
//! are present and refuse half-configured ones, and `homebrew-cask.sh`
//! (#244) must render the cask from a DMG and never publish an unsigned
//! build. None needs macOS, Apple credentials or the network; all need bash
//! and jq (skipped without jq unless ITERVOX_E2E_REQUIRE=1, as under `make
//! desktop-test` and CI).

use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use itervox_desktop::update::{is_update, Manifest, MAC_PLATFORM_KEYS};

fn scripts() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../scripts")
}

fn have_jq() -> bool {
    let ok = Command::new("jq")
        .arg("--version")
        .output()
        .is_ok_and(|o| o.status.success());
    if !ok {
        assert!(
            std::env::var_os("ITERVOX_E2E_REQUIRE").is_none_or(|v| v != "1"),
            "ITERVOX_E2E_REQUIRE=1 but jq is not installed"
        );
        eprintln!("skipping: jq is not installed");
    }
    ok
}

fn run(cmd: &mut Command) -> Output {
    cmd.output().expect("run script")
}

fn text(b: &[u8]) -> String {
    String::from_utf8_lossy(b).into_owned()
}

#[test]
fn generated_latest_json_is_accepted_by_the_updater() {
    if !have_jq() {
        return;
    }
    let dir = tempfile::tempdir().unwrap();
    let sig = dir.path().join("Itervox_0.2.6_universal.app.tar.gz.sig");
    std::fs::write(&sig, "dW50cnVzdGVkIGNvbW1lbnQ6IGZha2U=\n").unwrap();
    let notes = dir.path().join("notes.md");
    std::fs::write(&notes, "### Added\n- **\"Quoted\" & multi-line** notes\n").unwrap();
    let out_file = dir.path().join("latest.json");
    let url = "https://github.com/vnovick/itervox/releases/download/v0.2.6/Itervox_0.2.6_universal.app.tar.gz";
    let o = run(Command::new("bash")
        .arg(scripts().join("gen-latest-json.sh"))
        .args(["--version", "v0.2.6", "--url", url])
        .arg("--signature-file")
        .arg(&sig)
        .arg("--notes-file")
        .arg(&notes)
        .args(["--pub-date", "2026-10-14T00:00:00Z", "--out"])
        .arg(&out_file));
    assert!(o.status.success(), "{}", text(&o.stderr));

    let m = Manifest::parse(&std::fs::read_to_string(&out_file).unwrap()).unwrap();
    assert_eq!(m.version, "0.2.6", "the leading v is stripped");
    assert_eq!(m.pub_date.as_deref(), Some("2026-10-14T00:00:00Z"));
    assert_eq!(
        m.notes.as_deref(),
        Some("### Added\n- **\"Quoted\" & multi-line** notes")
    );
    let keys: Vec<_> = m.platforms.keys().map(String::as_str).collect();
    let mut want = MAC_PLATFORM_KEYS.to_vec();
    want.sort_unstable();
    assert_eq!(keys, want);
    for arch in ["aarch64", "x86_64"] {
        for installer in [Some("app"), None] {
            let p = m.platform("darwin", arch, installer).unwrap();
            assert_eq!(p.url, url);
            assert_eq!(p.signature, "dW50cnVzdGVkIGNvbW1lbnQ6IGZha2U=");
        }
    }
    assert!(is_update("0.2.5", &m.version));
}

#[test]
fn latest_json_generator_rejects_bad_inputs() {
    if !have_jq() {
        return;
    }
    let dir = tempfile::tempdir().unwrap();
    let sig = dir.path().join("a.sig");
    std::fs::write(&sig, "c2ln").unwrap();
    let empty = dir.path().join("empty.sig");
    std::fs::write(&empty, "").unwrap();
    let https = "https://example.test/a.app.tar.gz";
    let cases: [(&str, &str, &Path, &str); 4] = [
        ("0.2", https, &sig, "is not MAJOR.MINOR.PATCH"),
        ("0.2.6", "http://example.test/a", &sig, "https"),
        ("0.2.6", https, &empty, "signature-file"),
        (
            "0.2.6",
            https,
            Path::new("/nonexistent.sig"),
            "signature-file",
        ),
    ];
    for (version, url, sig, want) in cases {
        let o = run(Command::new("bash")
            .arg(scripts().join("gen-latest-json.sh"))
            .args(["--version", version, "--url", url, "--signature-file"])
            .arg(sig));
        assert!(!o.status.success(), "{version} {url} accepted");
        assert!(text(&o.stderr).contains(want), "{}", text(&o.stderr));
    }
}

/// Runs `macos-release.sh mode` with only `env` set (plus PATH) against a
/// tauri.conf.json whose updater pubkey is `pubkey`.
fn mode(env: &[(&str, &str)], pubkey: &str) -> (bool, String, String, String) {
    let dir = tempfile::tempdir().unwrap();
    let conf = dir.path().join("tauri.conf.json");
    std::fs::write(
        &conf,
        format!(r#"{{"plugins":{{"updater":{{"pubkey":"{pubkey}"}}}}}}"#),
    )
    .unwrap();
    let gh_out = dir.path().join("github_output");
    let mut cmd = Command::new("bash");
    cmd.env_clear()
        .env("PATH", std::env::var_os("PATH").unwrap_or_default())
        .env("GITHUB_OUTPUT", &gh_out)
        .arg(scripts().join("macos-release.sh"))
        .arg("mode")
        .arg(&conf);
    for (k, v) in env {
        cmd.env(k, v);
    }
    let o = run(&mut cmd);
    let outputs = std::fs::read_to_string(&gh_out).unwrap_or_default();
    (
        o.status.success(),
        text(&o.stdout),
        text(&o.stderr),
        outputs,
    )
}

const CERT: [(&str, &str); 3] = [
    ("APPLE_CERTIFICATE", "ZmFrZQ=="),
    ("APPLE_CERTIFICATE_PASSWORD", "secret-cert-pass"),
    (
        "APPLE_SIGNING_IDENTITY",
        "Developer ID Application: Test (TEAM123456)",
    ),
];
const API_KEY: [(&str, &str); 3] = [
    ("APPLE_API_KEY_ID", "KEY123"),
    ("APPLE_API_ISSUER", "issuer-uuid"),
    ("APPLE_API_PRIVATE_KEY", "-----BEGIN PRIVATE KEY-----"),
];
const APPLE_ID: [(&str, &str); 3] = [
    ("APPLE_ID", "dev@example.test"),
    ("APPLE_PASSWORD", "secret-app-pass"),
    ("APPLE_TEAM_ID", "TEAM123456"),
];
const UPDATER_KEY: (&str, &str) = ("TAURI_SIGNING_PRIVATE_KEY", "secret-minisign-key");

#[test]
fn signing_mode_follows_the_secrets_that_are_present() {
    if !have_jq() {
        return;
    }
    // No secrets (a fork or a pull request): unsigned, with a warning.
    let (ok, out, err, gh) = mode(&[], "");
    assert!(ok, "{err}");
    assert_eq!(gh, "sign=false\nnotarize=false\nupdater=false\n");
    assert!(
        out.contains("::warning title=Unsigned Mac build::"),
        "{out}"
    );

    // Certificate + API key + updater key: everything.
    let all: Vec<_> = CERT
        .iter()
        .chain(&API_KEY)
        .copied()
        .chain([UPDATER_KEY])
        .collect();
    let (ok, out, err, gh) = mode(&all, "cHVia2V5");
    assert!(ok, "{err}");
    assert_eq!(gh, "sign=true\nnotarize=true\nupdater=true\n");
    for (_, secret) in all.iter() {
        assert!(
            !out.contains(secret) && !err.contains(secret),
            "a secret was printed"
        );
    }

    // The Apple ID path works too; no updater key: signed, with a warning.
    let env: Vec<_> = CERT.iter().chain(&APPLE_ID).copied().collect();
    let (ok, out, _, gh) = mode(&env, "");
    assert!(ok);
    assert_eq!(gh, "sign=true\nnotarize=true\nupdater=false\n");
    assert!(out.contains("no latest.json"), "{out}");
}

#[test]
fn signing_mode_refuses_half_configured_secrets() {
    if !have_jq() {
        return;
    }
    // Part of the certificate.
    let (ok, _, err, _) = mode(&CERT[..2], "");
    assert!(!ok);
    assert!(err.contains("set all three or none"), "{err}");
    // A certificate but no notarization credentials.
    let (ok, _, err, _) = mode(&CERT, "");
    assert!(!ok);
    assert!(err.contains("notarization credentials"), "{err}");
    // An incomplete API key does not count.
    let env: Vec<_> = CERT.iter().chain(&API_KEY[..2]).copied().collect();
    assert!(!mode(&env, "").0);
    // An updater key with no public key committed: the app could never
    // verify the update.
    let env: Vec<_> = CERT
        .iter()
        .chain(&API_KEY)
        .copied()
        .chain([UPDATER_KEY])
        .collect();
    let (ok, _, err, _) = mode(&env, "");
    assert!(!ok);
    assert!(err.contains("plugins.updater.pubkey"), "{err}");
}

// --- the Homebrew cask (#244) ----------------------------------------------

/// The fixture "DMG": any bytes do, the cask only carries their checksum.
const FIXTURE_DMG: &[u8] = b"not a real disk image: itervox cask fixture\n";

/// SHA-256 of a file by a different tool than the script prefers, so the
/// test does not just repeat the script's own computation.
fn independent_sha256(path: &Path) -> String {
    let o = Command::new("sha256sum")
        .arg(path)
        .output()
        .or_else(|_| {
            Command::new("openssl")
                .args(["dgst", "-sha256", "-r"])
                .arg(path)
                .output()
        })
        .expect("sha256sum or openssl");
    assert!(o.status.success());
    text(&o.stdout)
        .split_whitespace()
        .next()
        .unwrap()
        .to_string()
}

fn cask_script() -> PathBuf {
    scripts().join("homebrew-cask.sh")
}

#[test]
fn cask_is_rendered_with_version_and_sha() {
    if !have_jq() {
        return;
    }
    let dir = tempfile::tempdir().unwrap();
    let dmg = dir.path().join("Itervox_0.3.0_universal.dmg");
    std::fs::write(&dmg, FIXTURE_DMG).unwrap();
    let want_sha = independent_sha256(&dmg);
    assert_eq!(want_sha.len(), 64);
    let conf = dir.path().join("tauri.macos.conf.json");
    std::fs::write(
        &conf,
        r#"{"bundle":{"macOS":{"minimumSystemVersion":"13.0"}}}"#,
    )
    .unwrap();
    let out = dir.path().join("itervox-app.rb");
    let o = run(Command::new("bash")
        .arg(cask_script())
        .args(["render", "--version", "v0.3.0", "--dmg"])
        .arg(&dmg)
        .arg("--conf")
        .arg(&conf)
        .arg("--out")
        .arg(&out));
    assert!(o.status.success(), "{}", text(&o.stderr));
    let cask = std::fs::read_to_string(&out).unwrap();
    for line in [
        "cask \"itervox-app\" do",
        "  version \"0.3.0\"",
        &format!("  sha256 \"{want_sha}\""),
        "  url \"https://github.com/vnovick/itervox/releases/download/v#{version}/Itervox_#{version}_universal.dmg\"",
        "  name \"Itervox\"",
        "  homepage \"https://github.com/vnovick/itervox\"",
        "  auto_updates true",
        "  depends_on macos: \">= :ventura\"",
        "  app \"Itervox.app\"",
        "  caveats <<~EOS",
        "    CLI formula with: brew install vnovick/tap/itervox",
        "end",
    ] {
        assert!(cask.lines().any(|l| l == line), "missing {line:?} in\n{cask}");
    }
    // The cask's token is not the CLI formula's name: a plain
    // `brew install vnovick/tap/itervox` stays the command-line tool.
    let goreleaser = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../.goreleaser.yaml"),
    )
    .unwrap();
    let formula = goreleaser
        .split("brews:")
        .nth(1)
        .and_then(|b| b.lines().find_map(|l| l.trim().strip_prefix("- name: ")))
        .expect("the CLI formula in .goreleaser.yaml");
    assert_eq!(formula, "itervox");
    assert!(
        !cask.contains(&format!("cask \"{formula}\" do")),
        "the cask must not share the formula's name\n{cask}"
    );

    // The app's real minimum macOS has a Homebrew name; --sha256 works
    // without a DMG.
    let sha = "0123456789abcdef".repeat(4);
    let o = run(Command::new("bash").arg(cask_script()).args([
        "render",
        "--version",
        "0.3.1",
        "--sha256",
        &sha,
    ]));
    assert!(o.status.success(), "{}", text(&o.stderr));
    let cask = text(&o.stdout);
    assert!(cask.contains(&format!("sha256 \"{sha}\"")));
    assert!(cask.contains("depends_on macos: \">= :big_sur\""), "{cask}");

    // Bad inputs are refused.
    let cases: [(&[&str], &str); 4] = [
        (
            &["--version", "0.3", "--sha256", &sha],
            "is not MAJOR.MINOR.PATCH",
        ),
        (
            &["--version", "0.3.0", "--sha256", "abc"],
            "64 lowercase hex",
        ),
        (&["--version", "0.3.0"], "64 lowercase hex"),
        (
            &["--version", "0.3.0", "--dmg", "/nonexistent.dmg"],
            "missing or empty",
        ),
    ];
    for (args, want) in cases {
        let o = run(Command::new("bash")
            .arg(cask_script())
            .arg("render")
            .args(args));
        assert!(!o.status.success(), "{args:?} accepted");
        assert!(
            text(&o.stderr).contains(want),
            "{args:?}: {}",
            text(&o.stderr)
        );
    }

    // Drift guard: the URL names the DMG the release workflow uploads.
    let wf = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../.github/workflows/desktop-release.yml"),
    )
    .unwrap();
    assert!(wf.contains(r#"dmg="$OUT/Itervox_${VERSION}_universal${suffix}.dmg""#));
}

/// A fake `gh` on PATH that records its arguments and the token it got
/// (as "set"/"unset", never the value) to `log`.
fn fake_gh(dir: &Path) -> (PathBuf, PathBuf) {
    let bin = dir.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let log = dir.join("gh.log");
    let gh = bin.join("gh");
    std::fs::write(
        &gh,
        format!(
            "#!/usr/bin/env bash\n\
             echo \"gh token=${{GH_TOKEN:+set}} $*\" >> '{}'\n\
             case \"$*\" in *'--jq .sha'*) echo blobsha1 ;; esac\n",
            log.display()
        ),
    )
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&gh, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    (bin, log)
}

fn publish(dir: &Path, notarized: &str, version: &str, token: Option<&str>) -> (Output, String) {
    let (bin, log) = fake_gh(dir);
    let cask = dir.join("itervox-app.rb");
    std::fs::write(
        &cask,
        format!("cask \"itervox-app\" do\n  version \"{version}\"\nend\n"),
    )
    .unwrap();
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let mut cmd = Command::new("bash");
    cmd.env_clear()
        .env("PATH", path)
        .arg(cask_script())
        .args(["publish", "--cask"])
        .arg(&cask)
        .args(["--version", version, "--notarized", notarized]);
    if let Some(t) = token {
        cmd.env("HOMEBREW_TAP_GITHUB_TOKEN", t);
    }
    let o = run(&mut cmd);
    let calls = std::fs::read_to_string(&log).unwrap_or_default();
    (o, calls)
}

#[test]
fn cask_step_is_skipped_without_signing_secrets() {
    let secret = "tap-secret-value";

    // No signing secrets: `mode` says notarize=false, and the step does not
    // run; run anyway, the script refuses to touch the tap, even with the
    // tap token present.
    let dir = tempfile::tempdir().unwrap();
    let (o, calls) = publish(dir.path(), "false", "0.3.0", Some(secret));
    assert!(o.status.success(), "{}", text(&o.stderr));
    assert!(text(&o.stdout).contains("::notice title=Homebrew cask skipped::"));
    assert!(calls.is_empty(), "gh was called: {calls}");

    // Notarized but no tap token: skipped with a warning.
    let dir = tempfile::tempdir().unwrap();
    let (o, calls) = publish(dir.path(), "true", "0.3.0", None);
    assert!(o.status.success(), "{}", text(&o.stderr));
    assert!(text(&o.stdout).contains("HOMEBREW_TAP_GITHUB_TOKEN is not set"));
    assert!(calls.is_empty(), "gh was called: {calls}");

    // A pre-release: skipped.
    let dir = tempfile::tempdir().unwrap();
    let (o, calls) = publish(dir.path(), "true", "0.3.0-rc.1", Some(secret));
    assert!(o.status.success(), "{}", text(&o.stderr));
    assert!(calls.is_empty(), "gh was called: {calls}");

    // Notarized with the token: the cask is committed to the tap, with
    // the token only in gh's environment.
    let dir = tempfile::tempdir().unwrap();
    let (o, calls) = publish(dir.path(), "true", "0.3.0", Some(secret));
    assert!(o.status.success(), "{}", text(&o.stderr));
    let put = calls
        .lines()
        .find(|l| l.contains("-X PUT"))
        .unwrap_or_else(|| panic!("no PUT: {calls}"));
    assert!(
        put.starts_with(
            "gh token=set api -X PUT repos/vnovick/homebrew-tap/contents/Casks/itervox-app.rb"
        ),
        "{put}"
    );
    assert!(put.contains("message=itervox-app 0.3.0 (cask)"), "{put}");
    assert!(
        put.contains("sha=blobsha1"),
        "updates the existing file: {put}"
    );
    for out in [text(&o.stdout), text(&o.stderr), calls.clone()] {
        assert!(!out.contains(secret), "the tap token was printed");
    }

    // The workflow runs the step only for a notarized build, after the
    // notarization and the release upload.
    let wf = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../.github/workflows/desktop-release.yml"),
    )
    .unwrap();
    let step = wf
        .find("- name: Update the Homebrew cask")
        .expect("cask step");
    let tail = &wf[step..];
    let cond = tail.lines().nth(1).unwrap().trim();
    assert_eq!(
        cond,
        "if: inputs.tag != '' && steps.mode.outputs.notarize == 'true'"
    );
    for earlier in [
        "- name: Notarize, staple and verify the disk image",
        "- name: Upload to the GitHub release",
    ] {
        assert!(
            wf.find(earlier).expect(earlier) < step,
            "{earlier} must come first"
        );
    }
    assert!(tail.contains("HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}"));
    assert!(tail.contains("--notarized \"$NOTARIZED\""));
    // The same tap and token secret as the CLI formula.
    let goreleaser = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../.goreleaser.yaml"),
    )
    .unwrap();
    assert!(goreleaser.contains("name: homebrew-tap"));
    assert!(goreleaser.contains("{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"));
}
