//! Automatic updates (#130) as plain data: version comparison, the
//! `latest.json` release manifest, the check schedule and the state the tray
//! menu shows. `app_update.rs` wires this to `tauri-plugin-updater`, which
//! downloads the signed `.app.tar.gz`, verifies its signature against the
//! public key in `tauri.conf.json`, and swaps the bundle in place.
//!
//! The sidecar daemon lives inside the bundle, so it updates with the app; a
//! command-line tool the app installed is a symlink into the bundle
//! (`cli_tool.rs`) and so follows too. Daemons the app started are detached
//! processes and keep running across the restart, on the binary they were
//! started with, until they are restarted.
//!
//! Linux and Windows (#260) do not update themselves ([`Mode::Notify`]):
//! the app reads the latest GitHub release and, when it is newer, the menu
//! links to its release page instead of offering Install.

use std::cmp::Ordering;
use std::collections::BTreeMap;
use std::time::{Duration, Instant};

use serde::Deserialize;

/// Where the app looks for updates: the `latest.json` asset of the newest
/// published (non-prerelease) GitHub release. Must match
/// `plugins.updater.endpoints` in `tauri.conf.json`.
pub const ENDPOINT: &str =
    "https://github.com/vnovick/itervox/releases/latest/download/latest.json";

/// Linux and Windows (#260): the newest published release, read from the
/// GitHub API. Those builds do not update themselves; the menu links to the
/// release page instead.
pub const LATEST_RELEASE_API: &str = "https://api.github.com/repos/vnovick/itervox/releases/latest";

/// The release page of `version`.
pub fn release_page(version: &str) -> String {
    let v = version.trim();
    let v = v.strip_prefix('v').unwrap_or(v);
    format!("https://github.com/vnovick/itervox/releases/tag/v{v}")
}

/// How a platform updates.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// The Mac: download, verify and install (`tauri-plugin-updater`).
    SelfUpdate,
    /// Linux and Windows: say an update exists and link to its release.
    Notify,
}

/// The update mode of `os`.
pub fn mode_for(os: crate::platform::Os) -> Mode {
    match os {
        crate::platform::Os::Mac => Mode::SelfUpdate,
        crate::platform::Os::Linux | crate::platform::Os::Windows => Mode::Notify,
    }
}

/// The version of the GitHub API's latest release (its `tag_name`); drafts
/// and prereleases never come from that endpoint, and are refused anyway.
pub fn parse_latest_release(json: &str) -> Result<String, String> {
    #[derive(Deserialize)]
    struct Release {
        tag_name: String,
        #[serde(default)]
        draft: bool,
        #[serde(default)]
        prerelease: bool,
    }
    let r: Release =
        serde_json::from_str(json).map_err(|e| format!("the release is malformed: {e}"))?;
    if r.draft || r.prerelease {
        return Err("the latest release is a draft or prerelease".into());
    }
    let v = Version::parse(&r.tag_name)?;
    Ok(format!("{}.{}.{}", v.major, v.minor, v.patch))
}

/// How often the app checks while it runs.
pub const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);

/// The first check runs this long after launch, so it does not compete with
/// the first daemon refresh.
pub const FIRST_CHECK_DELAY: Duration = Duration::from_secs(10);

/// The longest message the menu shows before eliding it.
const MAX_MESSAGE: usize = 80;

/// A semantic version (`MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]`), with an
/// optional leading `v` as in the release tags.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Version {
    pub major: u64,
    pub minor: u64,
    pub patch: u64,
    pre: Vec<PreId>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum PreId {
    Num(u64),
    Alpha(String),
}

impl PartialOrd for PreId {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for PreId {
    fn cmp(&self, other: &Self) -> Ordering {
        match (self, other) {
            (PreId::Num(a), PreId::Num(b)) => a.cmp(b),
            (PreId::Num(_), PreId::Alpha(_)) => Ordering::Less,
            (PreId::Alpha(_), PreId::Num(_)) => Ordering::Greater,
            (PreId::Alpha(a), PreId::Alpha(b)) => a.cmp(b),
        }
    }
}

fn numeric(part: &str, what: &str, s: &str) -> Result<u64, String> {
    if part.is_empty() || !part.bytes().all(|b| b.is_ascii_digit()) {
        return Err(format!("version {s:?}: {what} is not a number"));
    }
    if part.len() > 1 && part.starts_with('0') {
        return Err(format!("version {s:?}: {what} has a leading zero"));
    }
    part.parse()
        .map_err(|_| format!("version {s:?}: {what} is too large"))
}

impl Version {
    pub fn parse(s: &str) -> Result<Version, String> {
        let t = s.trim();
        let t = t.strip_prefix('v').unwrap_or(t);
        let t = t.split_once('+').map_or(t, |(v, _build)| v);
        let (core, pre) = match t.split_once('-') {
            Some((c, p)) => (c, Some(p)),
            None => (t, None),
        };
        let parts: Vec<&str> = core.split('.').collect();
        if parts.len() != 3 {
            return Err(format!("version {s:?}: expected MAJOR.MINOR.PATCH"));
        }
        let pre = match pre {
            None => Vec::new(),
            Some(p) => p
                .split('.')
                .map(|id| {
                    if id.is_empty() || !id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
                    {
                        Err(format!("version {s:?}: bad prerelease {p:?}"))
                    } else if id.bytes().all(|b| b.is_ascii_digit()) {
                        numeric(id, "prerelease", s).map(PreId::Num)
                    } else {
                        Ok(PreId::Alpha(id.to_string()))
                    }
                })
                .collect::<Result<_, _>>()?,
        };
        Ok(Version {
            major: numeric(parts[0], "major", s)?,
            minor: numeric(parts[1], "minor", s)?,
            patch: numeric(parts[2], "patch", s)?,
            pre,
        })
    }

    pub fn is_prerelease(&self) -> bool {
        !self.pre.is_empty()
    }
}

impl PartialOrd for Version {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Version {
    fn cmp(&self, other: &Self) -> Ordering {
        (self.major, self.minor, self.patch)
            .cmp(&(other.major, other.minor, other.patch))
            .then_with(|| match (self.pre.is_empty(), other.pre.is_empty()) {
                // A release sorts after its prereleases.
                (true, true) => Ordering::Equal,
                (true, false) => Ordering::Greater,
                (false, true) => Ordering::Less,
                (false, false) => self.pre.cmp(&other.pre),
            })
    }
}

/// Whether `offered` should be installed over `current`: strictly newer.
/// An unparseable version on either side is never an update, so a broken
/// manifest cannot downgrade or loop.
pub fn is_update(current: &str, offered: &str) -> bool {
    match (Version::parse(current), Version::parse(offered)) {
        (Ok(c), Ok(o)) => o > c,
        _ => false,
    }
}

/// One platform entry of `latest.json`.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct Platform {
    /// The minisign signature of the archive (the `.sig` file's content).
    pub signature: String,
    pub url: String,
}

/// The release manifest the updater fetches (`latest.json`), in the format
/// `tauri-plugin-updater` reads.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct Manifest {
    pub version: String,
    #[serde(default)]
    pub notes: Option<String>,
    #[serde(default)]
    pub pub_date: Option<String>,
    pub platforms: BTreeMap<String, Platform>,
}

/// The manifest keys the release workflow writes: the universal app serves
/// both Mac architectures, and the plugin looks for `{os}-{arch}-app` (an
/// installed `.app`) before `{os}-{arch}`.
pub const MAC_PLATFORM_KEYS: [&str; 4] = [
    "darwin-aarch64-app",
    "darwin-aarch64",
    "darwin-x86_64-app",
    "darwin-x86_64",
];

impl Manifest {
    /// Parses and validates a manifest: a valid version, at least one
    /// platform, and for each an `https` URL and a non-empty signature.
    pub fn parse(json: &str) -> Result<Manifest, String> {
        let m: Manifest =
            serde_json::from_str(json).map_err(|e| format!("latest.json is malformed: {e}"))?;
        Version::parse(&m.version)?;
        if m.platforms.is_empty() {
            return Err("latest.json lists no platforms".into());
        }
        for (key, p) in &m.platforms {
            if !p.url.starts_with("https://") {
                return Err(format!("latest.json: {key} url is not https"));
            }
            if p.signature.trim().is_empty() {
                return Err(format!("latest.json: {key} has no signature"));
            }
        }
        Ok(m)
    }

    /// The entry for `os`/`arch`, searched in the plugin's order.
    pub fn platform(&self, os: &str, arch: &str, installer: Option<&str>) -> Option<&Platform> {
        installer
            .and_then(|i| self.platforms.get(&format!("{os}-{arch}-{i}")))
            .or_else(|| self.platforms.get(&format!("{os}-{arch}")))
    }
}

/// When to check: shortly after launch, then every `interval` after the
/// previous check (manual checks included).
#[derive(Debug, Clone)]
pub struct Schedule {
    interval: Duration,
    first: Instant,
    last: Option<Instant>,
}

impl Schedule {
    pub fn new(started: Instant, first_delay: Duration, interval: Duration) -> Schedule {
        Schedule {
            interval,
            first: started + first_delay,
            last: None,
        }
    }

    fn next(&self) -> Instant {
        self.last.map_or(self.first, |l| l + self.interval)
    }

    pub fn due(&self, now: Instant) -> bool {
        now >= self.next()
    }

    /// How long to sleep before the next check is due.
    pub fn wait(&self, now: Instant) -> Duration {
        self.next().saturating_duration_since(now)
    }

    pub fn checked(&mut self, now: Instant) {
        self.last = Some(now);
    }
}

/// An offered update.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Offer {
    pub version: String,
    pub current: String,
}

/// What the updater is doing, as the menu shows it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum UpdateState {
    /// This build cannot update (no updater public key was built in).
    Disabled(String),
    /// No check has finished yet.
    Idle,
    Checking {
        /// An update found earlier stays installable while re-checking.
        offer: Option<Offer>,
    },
    UpToDate,
    Available(Offer),
    Installing(Offer),
    /// Installing failed; the offer stays so the user can retry.
    InstallFailed {
        offer: Offer,
        message: String,
    },
    CheckFailed(String),
}

/// What a menu item of the update section does.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UpdateAction {
    Check,
    Install,
    /// Linux and Windows: open the offered version's release page.
    OpenRelease,
}

/// One menu item: label, enabled, action (`None` for a label).
pub type UpdateItem = (String, bool, Option<UpdateAction>);

fn short(msg: &str) -> String {
    let line = msg.lines().next().unwrap_or("").trim();
    if line.chars().count() <= MAX_MESSAGE {
        line.to_string()
    } else {
        let mut s: String = line.chars().take(MAX_MESSAGE - 1).collect();
        s.push('…');
        s
    }
}

impl UpdateState {
    pub fn offer(&self) -> Option<&Offer> {
        match self {
            UpdateState::Available(o)
            | UpdateState::Installing(o)
            | UpdateState::InstallFailed { offer: o, .. } => Some(o),
            UpdateState::Checking { offer } => offer.as_ref(),
            _ => None,
        }
    }

    /// A check starts. Returns false (and changes nothing) when no check
    /// should run: updates are disabled, or one is installing or running.
    pub fn begin_check(&mut self) -> bool {
        match self {
            UpdateState::Disabled(_)
            | UpdateState::Installing(_)
            | UpdateState::Checking { .. } => false,
            _ => {
                *self = UpdateState::Checking {
                    offer: self.offer().cloned(),
                };
                true
            }
        }
    }

    /// A check finished: `Ok(Some(offer))` an update, `Ok(None)` none.
    pub fn finish_check(&mut self, result: Result<Option<Offer>, String>) {
        if !matches!(self, UpdateState::Checking { .. }) {
            return;
        }
        *self = match result {
            Ok(Some(o)) => UpdateState::Available(o),
            Ok(None) => UpdateState::UpToDate,
            Err(e) => match self.offer().cloned() {
                // Keep a known update installable through a failed re-check.
                Some(o) => UpdateState::Available(o),
                None => UpdateState::CheckFailed(short(&e)),
            },
        };
    }

    /// The user chose Install. Returns the offer to install, or `None` when
    /// there is nothing to install (or it already is installing).
    pub fn begin_install(&mut self) -> Option<Offer> {
        let offer = match self {
            UpdateState::Available(o) | UpdateState::InstallFailed { offer: o, .. } => o.clone(),
            _ => return None,
        };
        *self = UpdateState::Installing(offer.clone());
        Some(offer)
    }

    pub fn install_failed(&mut self, message: &str) {
        if let UpdateState::Installing(o) = self {
            *self = UpdateState::InstallFailed {
                offer: o.clone(),
                message: short(message),
            };
        }
    }

    /// The update section in `mode`: Notify swaps Install for a link to
    /// the release page.
    pub fn menu_items_for(&self, mode: Mode) -> Vec<UpdateItem> {
        let mut items = self.menu_items();
        if mode == Mode::Notify {
            for item in &mut items {
                if item.2 == Some(UpdateAction::Install) {
                    *item = (
                        "Update available — Open the Release Page…".to_string(),
                        true,
                        Some(UpdateAction::OpenRelease),
                    );
                }
            }
        }
        items
    }

    /// The menu items of the update section.
    pub fn menu_items(&self) -> Vec<UpdateItem> {
        let check = || {
            (
                "Check for Updates…".to_string(),
                true,
                Some(UpdateAction::Check),
            )
        };
        let install = || {
            (
                "Update available — Install and Restart".to_string(),
                true,
                Some(UpdateAction::Install),
            )
        };
        let label = |s: String| (s, false, None);
        let about = |o: &Offer| label(format!("Itervox {} (you have {})", o.version, o.current));
        match self {
            UpdateState::Disabled(reason) => vec![label(format!("Updates off: {reason}"))],
            UpdateState::Idle | UpdateState::UpToDate => vec![check()],
            UpdateState::Checking { offer: None } => {
                vec![label("Checking for Updates…".into())]
            }
            UpdateState::Checking { offer: Some(o) } | UpdateState::Available(o) => {
                vec![install(), about(o)]
            }
            UpdateState::Installing(o) => {
                vec![label(format!("Installing Itervox {}…", o.version))]
            }
            UpdateState::InstallFailed { offer, message } => vec![
                install(),
                about(offer),
                label(format!("Update failed: {message}")),
            ],
            UpdateState::CheckFailed(message) => vec![
                check(),
                label(format!("Last update check failed: {message}")),
            ],
        }
    }
}

/// The initial state for a build whose updater public key is `pubkey`.
pub fn initial_state_for(mode: Mode, pubkey: Option<&str>) -> UpdateState {
    match mode {
        Mode::SelfUpdate => initial_state(pubkey),
        // Nothing is installed, so nothing needs the update key.
        Mode::Notify => UpdateState::Idle,
    }
}

pub fn initial_state(pubkey: Option<&str>) -> UpdateState {
    match pubkey.map(str::trim) {
        Some(k) if !k.is_empty() => UpdateState::Idle,
        _ => UpdateState::Disabled("this build has no update key".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn offer(v: &str) -> Offer {
        Offer {
            version: v.into(),
            current: "0.2.5".into(),
        }
    }

    #[test]
    fn versions_order_like_semver() {
        let ordered = [
            "0.2.4",
            "0.2.5-alpha",
            "0.2.5-alpha.1",
            "0.2.5-alpha.beta",
            "0.2.5-beta.2",
            "0.2.5-beta.11",
            "0.2.5-rc.1",
            "0.2.5",
            "v0.2.6",
            "0.10.0",
            "1.0.0+build.7",
        ];
        for w in ordered.windows(2) {
            let (a, b) = (Version::parse(w[0]).unwrap(), Version::parse(w[1]).unwrap());
            assert!(a < b, "{} < {}", w[0], w[1]);
            assert!(b > a, "{} > {}", w[1], w[0]);
        }
        assert_eq!(
            Version::parse("v1.2.3+abc").unwrap(),
            Version::parse("1.2.3").unwrap()
        );
    }

    #[test]
    fn malformed_versions_are_rejected() {
        for bad in [
            "",
            "1.2",
            "1.2.3.4",
            "1.x.3",
            "01.2.3",
            "1.2.3-",
            "1.2.3-a..b",
            "1.2.3-é",
        ] {
            assert!(Version::parse(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn only_strictly_newer_versions_are_updates() {
        assert!(is_update("0.2.5", "0.2.6"));
        assert!(is_update("0.2.5", "v0.3.0"));
        assert!(is_update("0.2.5-rc.1", "0.2.5"));
        assert!(!is_update("0.2.5", "0.2.5"));
        assert!(!is_update("0.2.6", "0.2.5"));
        assert!(!is_update("0.2.5", "0.2.5-rc.1"));
        assert!(!is_update("0.2.5", "garbage"));
        assert!(!is_update("garbage", "9.9.9"));
    }

    const MANIFEST: &str = r#"{
        "version": "0.2.6",
        "notes": "Fixes",
        "pub_date": "2026-10-14T00:00:00Z",
        "platforms": {
            "darwin-aarch64-app": {"signature": "c2ln", "url": "https://example.test/a.app.tar.gz"},
            "darwin-x86_64": {"signature": "c2ln", "url": "https://example.test/x.app.tar.gz"}
        }
    }"#;

    #[test]
    fn manifest_platform_lookup_follows_the_plugin_order() {
        let m = Manifest::parse(MANIFEST).unwrap();
        assert_eq!(m.version, "0.2.6");
        assert_eq!(m.notes.as_deref(), Some("Fixes"));
        let arm = m.platform("darwin", "aarch64", Some("app")).unwrap();
        assert!(arm.url.ends_with("/a.app.tar.gz"));
        // No `-app` key for x86_64: falls back to `darwin-x86_64`.
        let x86 = m.platform("darwin", "x86_64", Some("app")).unwrap();
        assert!(x86.url.ends_with("/x.app.tar.gz"));
        assert!(m.platform("darwin", "aarch64", None).is_none());
        assert!(m.platform("linux", "x86_64", None).is_none());
    }

    #[test]
    fn invalid_manifests_are_rejected() {
        let cases = [
            ("{", "malformed"),
            (
                r#"{"version":"next","platforms":{"darwin-x86_64":{"signature":"s","url":"https://x"}}}"#,
                "version",
            ),
            (r#"{"version":"1.0.0","platforms":{}}"#, "no platforms"),
            (
                r#"{"version":"1.0.0","platforms":{"darwin-x86_64":{"signature":"s","url":"http://x"}}}"#,
                "not https",
            ),
            (
                r#"{"version":"1.0.0","platforms":{"darwin-x86_64":{"signature":" ","url":"https://x"}}}"#,
                "no signature",
            ),
        ];
        for (json, want) in cases {
            let err = Manifest::parse(json).unwrap_err();
            assert!(err.contains(want), "{json}: {err}");
        }
    }

    #[test]
    fn schedule_checks_after_the_first_delay_then_every_interval() {
        let t0 = Instant::now();
        let mut s = Schedule::new(t0, Duration::from_secs(10), Duration::from_secs(3600));
        assert!(!s.due(t0));
        assert_eq!(s.wait(t0), Duration::from_secs(10));
        assert!(s.due(t0 + Duration::from_secs(10)));
        s.checked(t0 + Duration::from_secs(12));
        assert!(!s.due(t0 + Duration::from_secs(13)));
        assert_eq!(
            s.wait(t0 + Duration::from_secs(12)),
            Duration::from_secs(3600)
        );
        assert!(s.due(t0 + Duration::from_secs(3612)));
        // Overdue: no negative wait.
        assert_eq!(s.wait(t0 + Duration::from_secs(9999)), Duration::ZERO);
    }

    #[test]
    fn found_update_is_offered_as_install_and_restart() {
        let mut s = UpdateState::Idle;
        assert_eq!(s.menu_items()[0].2, Some(UpdateAction::Check));
        assert!(s.begin_check());
        assert!(!s.begin_check(), "no second concurrent check");
        assert_eq!(
            s.menu_items()[0],
            ("Checking for Updates…".into(), false, None)
        );
        s.finish_check(Ok(Some(offer("0.2.6"))));
        let items = s.menu_items();
        assert_eq!(
            items[0],
            (
                "Update available — Install and Restart".into(),
                true,
                Some(UpdateAction::Install)
            )
        );
        assert_eq!(items[1].0, "Itervox 0.2.6 (you have 0.2.5)");
        assert_eq!(s.begin_install(), Some(offer("0.2.6")));
        assert_eq!(s.begin_install(), None, "already installing");
        assert!(!s.begin_check(), "no check while installing");
        assert_eq!(
            s.menu_items(),
            vec![("Installing Itervox 0.2.6…".into(), false, None)]
        );
    }

    #[test]
    fn failed_install_can_be_retried() {
        let mut s = UpdateState::Available(offer("0.2.6"));
        s.begin_install();
        s.install_failed("signature mismatch\nmore detail");
        let items = s.menu_items();
        assert_eq!(items[0].2, Some(UpdateAction::Install));
        assert_eq!(items[2].0, "Update failed: signature mismatch");
        assert_eq!(s.begin_install(), Some(offer("0.2.6")));
    }

    #[test]
    fn check_results_settle_the_state() {
        let mut s = UpdateState::Idle;
        s.begin_check();
        s.finish_check(Ok(None));
        assert_eq!(s, UpdateState::UpToDate);
        s.begin_check();
        s.finish_check(Err("x".repeat(200)));
        let UpdateState::CheckFailed(msg) = &s else {
            panic!("{s:?}")
        };
        assert_eq!(msg.chars().count(), 80);
        assert_eq!(s.menu_items()[0].2, Some(UpdateAction::Check));
        // A failed re-check keeps an update already found.
        let mut s = UpdateState::Available(offer("0.2.6"));
        s.begin_check();
        assert_eq!(s.menu_items()[0].2, Some(UpdateAction::Install));
        s.finish_check(Err("offline".into()));
        assert_eq!(s, UpdateState::Available(offer("0.2.6")));
        // A result arriving when no check runs is ignored.
        let mut s = UpdateState::UpToDate;
        s.finish_check(Ok(Some(offer("9.9.9"))));
        assert_eq!(s, UpdateState::UpToDate);
    }

    #[test]
    fn builds_without_a_key_never_check() {
        let mut s = initial_state(Some("  "));
        assert!(matches!(s, UpdateState::Disabled(_)));
        assert!(!s.begin_check());
        assert_eq!(s.menu_items()[0].2, None);
        assert_eq!(initial_state(None), s);
        assert_eq!(initial_state(Some("dW50cnVzdGVk")), UpdateState::Idle);
    }

    #[test]
    fn endpoint_matches_tauri_conf() {
        let conf: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        let endpoints = &conf["plugins"]["updater"]["endpoints"];
        assert_eq!(endpoints, &serde_json::json!([ENDPOINT]));
        assert!(conf["plugins"]["updater"]["pubkey"].is_string());
    }

    /// #260: Linux and Windows show the update with a link, never install.
    #[test]
    fn linux_and_windows_link_to_the_release_instead_of_installing() {
        use crate::platform::Os;
        assert_eq!(mode_for(Os::Mac), Mode::SelfUpdate);
        assert_eq!(mode_for(Os::Linux), Mode::Notify);
        assert_eq!(mode_for(Os::Windows), Mode::Notify);
        // No update key is needed to tell the user.
        assert_eq!(initial_state_for(Mode::Notify, None), UpdateState::Idle);
        assert!(matches!(
            initial_state_for(Mode::SelfUpdate, None),
            UpdateState::Disabled(_)
        ));
        let s = UpdateState::Available(offer("0.3.1"));
        let items = s.menu_items_for(Mode::Notify);
        assert_eq!(
            items[0],
            (
                "Update available — Open the Release Page…".to_string(),
                true,
                Some(UpdateAction::OpenRelease)
            )
        );
        assert!(items.iter().all(|i| i.2 != Some(UpdateAction::Install)));
        assert_eq!(s.menu_items_for(Mode::SelfUpdate), s.menu_items());
        assert_eq!(
            release_page("v0.3.1"),
            "https://github.com/vnovick/itervox/releases/tag/v0.3.1"
        );
        assert_eq!(release_page("0.3.1"), release_page("v0.3.1"));
    }

    #[test]
    fn the_latest_release_is_read_from_the_github_api() {
        assert_eq!(
            parse_latest_release(
                r#"{"tag_name":"v0.3.1","draft":false,"prerelease":false,"html_url":"x"}"#
            )
            .unwrap(),
            "0.3.1"
        );
        assert!(parse_latest_release(r#"{"tag_name":"v0.4.0-rc.1","prerelease":true}"#).is_err());
        assert!(parse_latest_release(r#"{"tag_name":"nightly"}"#).is_err());
        assert!(parse_latest_release("not json").is_err());
    }
}
