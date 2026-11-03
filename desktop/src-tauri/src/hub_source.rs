//! #232 — the app uses the machine's hub.
//!
//! While the hub's events stream is connected ([`crate::hub_notify`]), the
//! refresh reads every hub member from ONE `GET /hub/api/v1/projects` (each
//! row carries the member's `/api/v1/summary`) instead of polling each
//! member daemon. Daemons the hub does not cover — `--no-hub` daemons,
//! daemons older than the hub, members the hub cannot read right now — keep
//! the per-daemon polling of #212. When the hub stops (the stream ends, or
//! the projects read fails) every daemon is polled again until a new hub
//! takes over.

use std::collections::{HashMap, HashSet};
use std::time::Duration;

use serde::Deserialize;

use crate::client::{ClientPool, FetchError};
use crate::daemon_state::{InputRequired, StateSummary, INPUT_STATE_WAITING};
use crate::model::FetchJob;
use crate::status::DaemonRecord;

/// `inputRequired[].state` of an answered question whose agent resumes.
const INPUT_STATE_RESUMING: &str = "pending_input_resume";

/// Per-request bound of the projects read.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(2);

/// The serving hub in `GET /hub/api/v1/projects` (internal/hub `HubInfo`).
#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct HubInfo {
    pub version: String,
    pub pid: i32,
    pub url: String,
    pub head_seq: i64,
}

/// One member row (internal/hub `ProjectRow`).
#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
#[serde(default, rename_all = "camelCase")]
pub struct HubProject {
    /// The `/hub/p/{name}` segment.
    pub name: String,
    pub workflow: String,
    pub pid: i32,
    pub url: String,
    /// "ok", "unreachable", "unauthorized" or "error".
    pub health: String,
    pub error: String,
    pub proxied: bool,
    /// The member's `GET /api/v1/summary`, as the hub last read it.
    pub summary: Option<serde_json::Value>,
}

/// The body of `GET /hub/api/v1/projects`.
#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct HubProjects {
    pub hub: HubInfo,
    pub projects: Vec<HubProject>,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct SummaryCounts {
    running: usize,
    retrying: usize,
    paused: usize,
    input_required: usize,
    pending_input_resume: usize,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Summary {
    project_name: String,
    counts: SummaryCounts,
    paused: bool,
}

/// What the tray shows of a daemon from its `/api/v1/summary`: the counts
/// and the daemon-wide pause. The summary carries counts, not the issues,
/// so each question is a placeholder without identifier or timestamp: it
/// counts in the menu ("1 needs input") and keeps the refresh busy, but
/// never raises an alert (the hub notifies for its members).
pub fn summary_state(summary: &serde_json::Value) -> Option<StateSummary> {
    let s: Summary = serde_json::from_value(summary.clone()).ok()?;
    let placeholder = |state: &str| InputRequired {
        identifier: String::new(),
        state: state.into(),
        queued_at: String::new(),
        context: String::new(),
    };
    let mut input_required = vec![placeholder(INPUT_STATE_WAITING); s.counts.input_required];
    input_required.extend(vec![
        placeholder(INPUT_STATE_RESUMING);
        s.counts.pending_input_resume
    ]);
    Some(StateSummary {
        project_name: s.project_name,
        running: s.counts.running,
        retrying: s.counts.retrying,
        paused: s.counts.paused,
        input_required,
        failures: Vec::new(),
        dispatch_paused: s.paused,
    })
}

impl HubProject {
    /// The member's state, when the hub read it: healthy, with a summary.
    pub fn state(&self) -> Option<StateSummary> {
        if self.health != "ok" {
            return None;
        }
        summary_state(self.summary.as_ref()?)
    }
}

/// Reads `GET /hub/api/v1/projects` over one kept-alive connection.
pub struct HubReader {
    agent: ureq::Agent,
}

impl Default for HubReader {
    fn default() -> Self {
        HubReader {
            agent: ureq::Agent::config_builder()
                .timeout_global(Some(REQUEST_TIMEOUT))
                .http_status_as_error(false)
                // Loopback only: never through an HTTP(S)_PROXY.
                .proxy(None)
                .build()
                .into(),
        }
    }
}

impl HubReader {
    /// The hub's member list with each member's summary.
    pub fn projects(&self, origin: &str, token: &str) -> Result<HubProjects, FetchError> {
        let url = format!("{}/hub/api/v1/projects", origin.trim_end_matches('/'));
        let mut resp = self
            .agent
            .get(&url)
            .header("Authorization", &format!("Bearer {token}"))
            .call()
            .map_err(|e| FetchError::Transport(e.to_string()))?;
        let status = resp.status().as_u16();
        let body = resp
            .body_mut()
            .with_config()
            .limit(8 << 20)
            .read_to_vec()
            .map_err(|e| FetchError::Transport(e.to_string()))?;
        match status {
            200 => serde_json::from_slice(&body).map_err(|e| FetchError::Parse(e.to_string())),
            401 => Err(FetchError::Unauthorized),
            other => Err(FetchError::Status(other)),
        }
    }
}

/// Whether the hub accepts `token` (`GET /hub/api/v1/whoami`): `None` when
/// the hub does not answer.
pub fn token_accepted(origin: &str, token: &str) -> Option<bool> {
    let url = format!("{}/hub/api/v1/whoami", origin.trim_end_matches('/'));
    let mut resp = HubReader::default()
        .agent
        .get(&url)
        .header("Authorization", &format!("Bearer {token}"))
        .call()
        .ok()?;
    let _ = resp.body_mut().with_config().limit(1 << 16).read_to_vec();
    match resp.status().as_u16() {
        200 => Some(true),
        401 => Some(false),
        _ => None,
    }
}

/// The hub as the tray shows it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub enum HubStatus {
    /// No hub serves on this machine: every daemon is polled.
    #[default]
    Absent,
    /// `hub.lock` names a hub; the app is connecting to it.
    Connecting,
    /// Connected: the hub covers its members.
    Serving { members: usize, version: String },
    /// The hub stopped answering: every daemon is polled until a hub serves
    /// again.
    Unreachable(String),
}

impl HubStatus {
    pub fn serving(&self) -> bool {
        matches!(self, HubStatus::Serving { .. })
    }

    /// The tray's hub line.
    pub fn line(&self) -> String {
        match self {
            HubStatus::Absent => "Hub: not running — each project polled".into(),
            HubStatus::Connecting => "Hub: connecting…".into(),
            HubStatus::Serving { members, .. } => format!(
                "Hub · {members} project{}",
                if *members == 1 { "" } else { "s" }
            ),
            HubStatus::Unreachable(_) => "Hub unreachable — polling each project".into(),
        }
    }
}

/// The hub views the tray opens (web/src/pages/Hub `?view=`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HubPage {
    /// The project switcher, `/hub/`.
    Dashboard,
    /// The Phones view: the pairing QR code (#231).
    Phones,
    /// The notification settings (#229).
    Notifications,
}

/// The URL of a hub view on `origin`.
pub fn hub_page_url(origin: &str, page: HubPage) -> String {
    let base = format!("{}/hub/", origin.trim_end_matches('/'));
    match page {
        HubPage::Dashboard => base,
        HubPage::Phones => format!("{base}?view=phones"),
        HubPage::Notifications => format!("{base}?view=notifications"),
    }
}

/// One local read: every daemon's state, and which of them the hub covered.
#[derive(Debug, Default)]
pub struct LocalRead {
    pub states: HashMap<String, Result<StateSummary, FetchError>>,
    /// Keys (WORKFLOW.md paths) whose state came from the hub; the
    /// refresh does not poll them, and their alerts are the hub's.
    pub via_hub: HashSet<String>,
    pub hub: HubStatus,
}

/// Reads the local daemons for one refresh. With a live hub `session`
/// (origin, token) the hub's projects list answers for every member it has
/// a healthy summary of, and only the other `jobs` are polled; without one,
/// or when the hub does not answer, every job is polled (#212).
/// `lock_present` says whether `hub.lock` names a hub (for the tray).
pub fn read_local(
    pool: &ClientPool,
    reader: &HubReader,
    session: Option<(&str, &str)>,
    lock_present: bool,
    records: &[DaemonRecord],
    jobs: &[FetchJob],
) -> LocalRead {
    let mut out = LocalRead::default();
    match session {
        Some((origin, token)) => match reader.projects(origin, token) {
            Ok(p) => {
                out.hub = HubStatus::Serving {
                    members: p.projects.len(),
                    version: p.hub.version.clone(),
                };
                let rows: HashMap<&str, &HubProject> = p
                    .projects
                    .iter()
                    .map(|r| (r.workflow.as_str(), r))
                    .collect();
                for r in records.iter().filter(|r| r.hub_member) {
                    if let Some(st) = rows.get(r.workflow.as_str()).and_then(|p| p.state()) {
                        out.via_hub.insert(r.workflow.clone());
                        out.states.insert(r.workflow.clone(), Ok(st));
                    }
                }
            }
            Err(e) => out.hub = HubStatus::Unreachable(e.to_string()),
        },
        None if lock_present => out.hub = HubStatus::Connecting,
        None => out.hub = HubStatus::Absent,
    }
    let polled: Vec<FetchJob> = jobs
        .iter()
        .filter(|j| !out.via_hub.contains(&j.key))
        .cloned()
        .collect();
    out.states.extend(crate::client::fetch_all(pool, &polled));
    out
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
    use std::sync::{Arc, Mutex};

    /// A keep-alive HTTP server answering every request with `route(path)`
    /// (status, body); counts requests and keeps their paths and
    /// Authorization headers. `stop` makes it refuse new connections.
    pub(crate) struct FakeServer {
        pub origin: String,
        pub hits: Arc<AtomicUsize>,
        pub seen: Arc<Mutex<Vec<(String, String)>>>,
        stop: Arc<AtomicBool>,
    }

    impl FakeServer {
        pub(crate) fn start(route: impl Fn(&str) -> (u16, String) + Send + Sync + 'static) -> Self {
            let l = TcpListener::bind("127.0.0.1:0").unwrap();
            let origin = format!("http://{}", l.local_addr().unwrap());
            let hits = Arc::new(AtomicUsize::new(0));
            let seen = Arc::new(Mutex::new(Vec::new()));
            let stop = Arc::new(AtomicBool::new(false));
            let route = Arc::new(route);
            let (h, sn, st) = (hits.clone(), seen.clone(), stop.clone());
            std::thread::spawn(move || {
                for s in l.incoming() {
                    if st.load(Ordering::SeqCst) {
                        return; // the listener drops: connections refused
                    }
                    let Ok(mut s) = s else { continue };
                    let (h, sn, st, route) = (h.clone(), sn.clone(), st.clone(), route.clone());
                    std::thread::spawn(move || {
                        let mut r = BufReader::new(s.try_clone().unwrap());
                        loop {
                            let mut first = String::new();
                            if r.read_line(&mut first).unwrap_or(0) == 0 {
                                return;
                            }
                            let mut auth = String::new();
                            loop {
                                let mut line = String::new();
                                if r.read_line(&mut line).unwrap_or(0) == 0 {
                                    return;
                                }
                                if line == "\r\n" {
                                    break;
                                }
                                if line.to_ascii_lowercase().starts_with("authorization:") {
                                    auth = line["authorization:".len()..].trim().to_string();
                                }
                            }
                            if st.load(Ordering::SeqCst) {
                                return;
                            }
                            let path = first.split_whitespace().nth(1).unwrap_or("").to_string();
                            h.fetch_add(1, Ordering::SeqCst);
                            sn.lock().unwrap().push((path.clone(), auth));
                            let (code, body) = route(&path);
                            let _ = write!(
                                s,
                                "HTTP/1.1 {code} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
                                body.len()
                            );
                        }
                    });
                }
            });
            FakeServer {
                origin,
                hits,
                seen,
                stop,
            }
        }

        pub(crate) fn hits(&self) -> usize {
            self.hits.load(Ordering::SeqCst)
        }

        /// How many state reads (polls) the server answered.
        pub(crate) fn polls(&self) -> usize {
            self.seen
                .lock()
                .unwrap()
                .iter()
                .filter(|(p, _)| p.ends_with("/api/v1/state"))
                .count()
        }

        /// Stops answering, as a hub process that exited.
        pub(crate) fn stop(&self) {
            self.stop.store(true, Ordering::SeqCst);
            // Wake the accept loop so the listener is dropped.
            let _ = std::net::TcpStream::connect(self.origin.trim_start_matches("http://"));
        }
    }

    /// A fake daemon: summary (no ETag) and state with `running` agents.
    pub(crate) fn fake_daemon(running: usize) -> FakeServer {
        FakeServer::start(move |path| {
            if path.ends_with("/summary") {
                (404, String::new())
            } else {
                (200, format!("{{\"counts\":{{\"running\":{running}}}}}"))
            }
        })
    }

    fn record(workflow: &str, url: &str, member: bool) -> DaemonRecord {
        DaemonRecord {
            schema_version: 1,
            pid: 1,
            workflow: workflow.into(),
            url: format!("{url}/"),
            token_required: false,
            hub_member: member,
            ..Default::default()
        }
    }

    pub(crate) fn projects_body(rows: &[(&str, &str, usize, usize)]) -> String {
        let projects: Vec<serde_json::Value> = rows
            .iter()
            .map(|(name, wf, running, waiting)| {
                serde_json::json!({
                    "name": name, "workflow": wf, "pid": 1, "url": "", "health": "ok",
                    "proxied": true, "capabilities": [],
                    "summary": {"etag": "e", "projectName": name,
                        "counts": {"running": running, "inputRequired": waiting}, "paused": false}
                })
            })
            .collect();
        serde_json::json!({"hub": {"version": "v0.2.9", "pid": 7, "url": "", "headSeq": 3}, "projects": projects})
            .to_string()
    }

    /// #232 — with a hub serving, the hub's members are read from ONE
    /// projects request and their daemons are not polled at all, while a
    /// `--no-hub` daemon is still polled. When the hub stops, every daemon is
    /// polled again (the #212 path), and the tray says the hub is down.
    #[test]
    fn uses_hub_when_present_and_falls_back() {
        let a = fake_daemon(5);
        let b = fake_daemon(5);
        let solo = fake_daemon(1);
        let records = vec![
            record("/w/a/WORKFLOW.md", &a.origin, true),
            record("/w/b/WORKFLOW.md", &b.origin, true),
            record("/w/solo/WORKFLOW.md", &solo.origin, false),
        ];
        let body = projects_body(&[
            ("a", "/w/a/WORKFLOW.md", 2, 1),
            ("b", "/w/b/WORKFLOW.md", 0, 0),
        ]);
        let hub = FakeServer::start(move |path| {
            if path == "/hub/api/v1/projects" {
                (200, body.clone())
            } else {
                (404, String::new())
            }
        });
        let jobs: Vec<FetchJob> = records
            .iter()
            .map(|r| FetchJob {
                key: r.workflow.clone(),
                url: r.base_url(),
                token: None,
            })
            .collect();
        let pool = ClientPool::default();
        let reader = HubReader::default();
        let viewer = ["hub", "viewer"].join("-");

        // The hub serves: members from the hub, the --no-hub daemon polled.
        for _ in 0..2 {
            let read = read_local(
                &pool,
                &reader,
                Some((&hub.origin, &viewer)),
                true,
                &records,
                &jobs,
            );
            assert_eq!(
                read.hub,
                HubStatus::Serving {
                    members: 2,
                    version: "v0.2.9".into()
                }
            );
            let mut via: Vec<&str> = read.via_hub.iter().map(String::as_str).collect();
            via.sort();
            assert_eq!(via, ["/w/a/WORKFLOW.md", "/w/b/WORKFLOW.md"]);
            let st = read.states["/w/a/WORKFLOW.md"].as_ref().unwrap();
            assert_eq!((st.running, st.waiting_for_input().count()), (2, 1));
            assert_eq!(
                read.states["/w/solo/WORKFLOW.md"].as_ref().unwrap().running,
                1
            );
        }
        assert_eq!(hub.hits(), 2, "one projects request per refresh");
        assert_eq!(
            (a.hits(), b.hits()),
            (0, 0),
            "hub members are not polled while the hub serves"
        );
        assert_eq!(solo.polls(), 2, "a --no-hub daemon keeps its polling");
        let (path, auth) = hub.seen.lock().unwrap()[0].clone();
        assert_eq!(path, "/hub/api/v1/projects");
        assert_eq!(auth, format!("Bearer {viewer}"));

        // The hub stops: every daemon is polled again.
        hub.stop();
        let read = read_local(
            &pool,
            &reader,
            Some((&hub.origin, &viewer)),
            true,
            &records,
            &jobs,
        );
        assert!(
            matches!(read.hub, HubStatus::Unreachable(_)),
            "{:?}",
            read.hub
        );
        assert!(read.via_hub.is_empty());
        assert_eq!(read.states["/w/a/WORKFLOW.md"].as_ref().unwrap().running, 5);
        assert_eq!((a.polls(), b.polls(), solo.polls()), (1, 1, 3));
        assert_eq!(read.hub.line(), "Hub unreachable — polling each project");

        // The stream ended (no session): still per-daemon until a new hub.
        let read = read_local(&pool, &reader, None, false, &records, &jobs);
        assert_eq!(read.hub, HubStatus::Absent);
        assert!(read.via_hub.is_empty());
        assert_eq!((a.polls(), b.polls(), solo.polls()), (2, 2, 4));

        // A new hub takes over: members leave the polling again.
        let body = projects_body(&[
            ("a", "/w/a/WORKFLOW.md", 0, 0),
            ("b", "/w/b/WORKFLOW.md", 0, 0),
        ]);
        let hub2 = FakeServer::start(move |_| (200, body.clone()));
        let read = read_local(
            &pool,
            &reader,
            Some((&hub2.origin, &viewer)),
            true,
            &records,
            &jobs,
        );
        assert!(read.hub.serving());
        assert_eq!(read.via_hub.len(), 2);
        assert_eq!((a.polls(), b.polls(), solo.polls()), (2, 2, 5));
    }

    #[test]
    fn unhealthy_members_and_summaries_stay_polled() {
        let mut row = HubProject {
            health: "unreachable".into(),
            summary: Some(serde_json::json!({"counts": {"running": 1}})),
            ..Default::default()
        };
        assert_eq!(row.state(), None);
        row.health = "ok".into();
        assert_eq!(row.state().unwrap().running, 1);
        row.summary = None;
        assert_eq!(row.state(), None);
        let st = summary_state(&serde_json::json!({
            "counts": {"inputRequired": 1, "pendingInputResume": 2, "retrying": 1},
            "paused": true
        }))
        .unwrap();
        assert_eq!(st.waiting_for_input().count(), 1);
        assert_eq!(st.input_required.len(), 3);
        assert!(st.dispatch_paused);
        // Placeholders never notify: no identifier, no timestamp.
        let mut d = crate::events::EventDetector::new(0);
        assert!(d.observe("/w", "w", "http://x/", &st).is_empty());
        assert_eq!(
            hub_page_url("http://127.0.0.1:8089", HubPage::Phones),
            "http://127.0.0.1:8089/hub/?view=phones"
        );
        assert_eq!(
            hub_page_url("http://127.0.0.1:8089/", HubPage::Dashboard),
            "http://127.0.0.1:8089/hub/"
        );
        assert_eq!(
            hub_page_url("http://h:1", HubPage::Notifications),
            "http://h:1/hub/?view=notifications"
        );
    }
}
