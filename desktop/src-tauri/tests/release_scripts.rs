//! Dry runs of the release scripts (#130) with fake inputs: the manifest
//! `gen-latest-json.sh` writes must be one the app's updater accepts, and
//! `macos-release.sh mode` must pick signed/unsigned from the secrets that
//! are present and refuse half-configured ones. Neither needs macOS or
//! Apple credentials; both need bash and jq (skipped without jq unless
//! ITERVOX_E2E_REQUIRE=1, as under `make desktop-test` and CI).

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
