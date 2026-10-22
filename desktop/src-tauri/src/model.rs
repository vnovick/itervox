//! The project list: merging the daemon registry with the added projects,
//! the per-project health, pending start/stop operations, and the refresh
//! cycle that diffs one poll against the previous one.

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::path::Path;
use std::time::{Duration, Instant};

use crate::client::FetchError;
use crate::daemon_state::StateSummary;
use crate::events::{Alert, EventDetector};
use crate::projects::SavedProjects;
use crate::sessions::SessionsJob;
use crate::status::{
    DaemonRecord, SOURCE_PROCESS_SCAN, TOKEN_SOURCE_GENERATED, TOKEN_SOURCE_SUPERVISOR,
};
use crate::token::TokenStore;

/// How often the app reads the daemon registry and the daemons while any
/// of them has an agent running, retrying or waiting for input, or an
/// operation the app started is pending. The acceptance bar is "at least
/// every 5 s" (#128).
pub const BUSY_REFRESH_INTERVAL: Duration = Duration::from_secs(3);

/// The interval while everything is idle (#212): the V3 target is near-zero CPU with no
/// agents running. Opening the menu or focusing a window refreshes at once.
pub const IDLE_REFRESH_INTERVAL: Duration = Duration::from_secs(15);

/// The wait before the next refresh.
pub fn refresh_interval(busy: bool) -> Duration {
    if busy {
        BUSY_REFRESH_INTERVAL
    } else {
        IDLE_REFRESH_INTERVAL
    }
}

/// A pending operation shows in the menu until the next poll confirms it,
/// or this long at most.
pub const PENDING_TIMEOUT: Duration = Duration::from_secs(60);

/// How many recent alerts the tray menu lists.
pub const MAX_RECENT_ALERTS: usize = 8;

/// An operation the app started on a project.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PendingOp {
    Starting,
    Stopping,
    Restarting,
}

/// What the app knows about a project's daemon.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Health {
    /// No daemon runs for the project.
    Stopped,
    /// Running, state read.
    Ok(StateSummary),
    /// Running; its API needs a token the app does not hold.
    TokenRequired,
    /// Running, but the state could not be read.
    Unreachable(String),
}

/// One row of the menu.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProjectView {
    /// Absolute WORKFLOW.md path.
    pub key: String,
    pub name: String,
    pub record: Option<DaemonRecord>,
    /// Added in the app (as opposed to only found in the registry).
    pub added: bool,
    /// The app started this daemon and holds its token.
    pub managed: bool,
    pub health: Health,
    pub pending: Option<PendingOp>,
    /// The last start/stop error, shown until the next operation.
    pub error: Option<String>,
}

impl Health {
    /// Whether the daemon has agent work in flight: running or retrying
    /// agents, or a question waiting for (or resuming after) an answer.
    pub fn busy(&self) -> bool {
        match self {
            Health::Ok(s) => s.running > 0 || s.retrying > 0 || !s.input_required.is_empty(),
            _ => false,
        }
    }
}

impl ProjectView {
    pub fn running(&self) -> bool {
        self.record.is_some()
    }

    pub fn needs_input(&self) -> usize {
        match &self.health {
            Health::Ok(s) => s.waiting_for_input().count(),
            _ => 0,
        }
    }

    /// One-line status, e.g. "running · 2 agents · 1 needs input".
    pub fn status_line(&self) -> String {
        if let Some(op) = self.pending {
            return match op {
                PendingOp::Starting => "starting…",
                PendingOp::Stopping => "stopping…",
                PendingOp::Restarting => "restarting…",
            }
            .into();
        }
        let mut s = match &self.health {
            Health::Stopped => "stopped".to_string(),
            Health::TokenRequired => "running · token required".to_string(),
            Health::Unreachable(e) => format!("running · {e}"),
            Health::Ok(st) => {
                let mut parts = vec![format!(
                    "running · {} agent{}",
                    st.running,
                    if st.running == 1 { "" } else { "s" }
                )];
                let waiting = st.waiting_for_input().count();
                if waiting > 0 {
                    parts.push(format!(
                        "{waiting} need{} input",
                        if waiting == 1 { "s" } else { "" }
                    ));
                }
                if st.retrying > 0 {
                    parts.push(format!("{} retrying", st.retrying));
                }
                parts.join(" · ")
            }
        };
        if let Some(e) = &self.error {
            s.push_str(" · ");
            s.push_str(e);
        }
        s
    }
}

/// Display name: the daemon's project name, else the directory name.
pub fn display_name(key: &str, state: Option<&StateSummary>) -> String {
    if let Some(n) = state
        .map(|s| s.project_name.trim())
        .filter(|n| !n.is_empty())
    {
        return n.to_string();
    }
    Path::new(key)
        .parent()
        .and_then(Path::file_name)
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| key.to_string())
}

/// How the app reads one daemon's API.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FetchJob {
    pub key: String,
    pub url: String,
    pub token: Option<String>,
}

/// The token for a daemon: the supervisor token the app holds for one it
/// started, an auto-generated token from the daemon's `token_file` (same
/// user, mode 0600), or none when the API is open. `None` when the app has
/// no way in ("token required").
pub fn token_for(
    rec: &DaemonRecord,
    tokens: &dyn TokenStore,
    read_file: &dyn Fn(&str) -> Option<String>,
) -> Option<Option<String>> {
    if rec.open_api() {
        return Some(None);
    }
    match rec.token_source.as_str() {
        TOKEN_SOURCE_SUPERVISOR => tokens.get(&rec.workflow).ok().flatten().map(Some),
        TOKEN_SOURCE_GENERATED if !rec.token_file.is_empty() => read_file(&rec.token_file)
            .map(|t| t.trim().to_string())
            .filter(|t| !t.is_empty())
            .map(Some),
        _ => None,
    }
}

/// The added changes between two polls, used to decide whether the tray
/// menu needs rebuilding and to log what happened.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct RefreshDiff {
    pub appeared: Vec<String>,
    pub disappeared: Vec<String>,
    pub changed: Vec<String>,
}

impl RefreshDiff {
    pub fn is_empty(&self) -> bool {
        self.appeared.is_empty() && self.disappeared.is_empty() && self.changed.is_empty()
    }
}

/// What a menu row shows; two polls with equal signatures need no rebuild.
fn signature(v: &ProjectView) -> (String, String, bool, bool, Option<i32>) {
    (
        v.name.clone(),
        v.status_line(),
        v.added,
        v.managed,
        v.record.as_ref().map(|r| r.pid),
    )
}

pub fn diff_views(prev: &[ProjectView], next: &[ProjectView]) -> RefreshDiff {
    let p: HashMap<&str, &ProjectView> = prev.iter().map(|v| (v.key.as_str(), v)).collect();
    let n: HashMap<&str, &ProjectView> = next.iter().map(|v| (v.key.as_str(), v)).collect();
    let mut d = RefreshDiff::default();
    for v in next {
        match p.get(v.key.as_str()) {
            None => d.appeared.push(v.key.clone()),
            Some(old) if signature(old) != signature(v) => d.changed.push(v.key.clone()),
            _ => {}
        }
    }
    for v in prev {
        if !n.contains_key(v.key.as_str()) {
            d.disappeared.push(v.key.clone());
        }
    }
    d
}

/// The result of one refresh.
#[derive(Debug, Default)]
pub struct RefreshOutcome {
    pub diff: RefreshDiff,
    pub alerts: Vec<Alert>,
    /// Set when `status --json` failed; the previous list is kept.
    pub status_error: Option<String>,
}

#[derive(Debug, Clone)]
struct Pending {
    op: PendingOp,
    since: Instant,
    /// The pid running when the op started (a restart is done once a
    /// different pid runs).
    old_pid: Option<i32>,
}

/// The app's model. Network and process I/O happen outside it: the caller
/// lists daemons, asks [`Core::fetch_jobs`] what to read, reads, and hands
/// the results to [`Core::apply`].
pub struct Core {
    pub saved: SavedProjects,
    views: Vec<ProjectView>,
    pending: HashMap<String, Pending>,
    errors: HashMap<String, String>,
    detector: EventDetector,
    recent: VecDeque<Alert>,
    last_records: Vec<DaemonRecord>,
}

impl Core {
    /// A model whose notifications cover what happens from now on.
    pub fn new(saved: SavedProjects) -> Self {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        Self::with_since(saved, now)
    }

    /// A model that notifies about entries from `since` (Unix seconds) on.
    pub fn with_since(saved: SavedProjects, since: i64) -> Self {
        let mut c = Core {
            saved,
            views: Vec::new(),
            pending: HashMap::new(),
            errors: HashMap::new(),
            detector: EventDetector::new(since),
            recent: VecDeque::new(),
            last_records: Vec::new(),
        };
        c.views = c.build_views(&[], &HashMap::new());
        c
    }

    pub fn views(&self) -> &[ProjectView] {
        &self.views
    }

    pub fn view(&self, key: &str) -> Option<&ProjectView> {
        self.views.iter().find(|v| v.key == key)
    }

    /// Most recent first.
    pub fn recent_alerts(&self) -> impl Iterator<Item = &Alert> {
        self.recent.iter()
    }

    /// Whether the next refresh should come soon: a daemon is busy or an
    /// operation the app started is pending.
    pub fn busy(&self) -> bool {
        !self.pending.is_empty() || self.views.iter().any(|v| v.health.busy())
    }

    pub fn last_records(&self) -> &[DaemonRecord] {
        &self.last_records
    }

    /// The reads this poll needs, given a fresh registry listing.
    pub fn fetch_jobs(
        &self,
        records: &[DaemonRecord],
        tokens: &dyn TokenStore,
        read_file: &dyn Fn(&str) -> Option<String>,
    ) -> Vec<FetchJob> {
        records
            .iter()
            .filter_map(|r| {
                let token = token_for(r, tokens, read_file)?;
                let url = r.base_url();
                (!url.is_empty()).then(|| FetchJob {
                    key: r.workflow.clone(),
                    url,
                    token,
                })
            })
            .collect()
    }

    /// The Sessions window's reads of the local daemons (#178): the same
    /// reads as [`Core::fetch_jobs`], named as the menu names them.
    pub fn session_jobs(
        &self,
        records: &[DaemonRecord],
        tokens: &dyn TokenStore,
        read_file: &dyn Fn(&str) -> Option<String>,
    ) -> Vec<SessionsJob> {
        self.fetch_jobs(records, tokens, read_file)
            .into_iter()
            .map(|j| SessionsJob {
                name: self
                    .view(&j.key)
                    .map(|v| v.name.clone())
                    .unwrap_or_else(|| display_name(&j.key, None)),
                remote: None,
                key: j.key,
                url: j.url,
                token: j.token,
            })
            .collect()
    }

    /// Marks an operation as started; the menu shows it at once.
    pub fn begin(&mut self, key: &str, op: PendingOp, now: Instant) {
        let old_pid = self
            .view(key)
            .and_then(|v| v.record.as_ref())
            .map(|r| r.pid);
        self.errors.remove(key);
        self.pending.insert(
            key.to_string(),
            Pending {
                op,
                since: now,
                old_pid,
            },
        );
        self.rebuild();
    }

    /// Records a failed operation.
    pub fn fail(&mut self, key: &str, error: String) {
        self.pending.remove(key);
        self.errors.insert(key.to_string(), error);
        self.rebuild();
    }

    /// Whether the op on `key` is confirmed by `rec` (the record for that
    /// project in the latest listing, if any).
    fn settled(p: &Pending, rec: Option<&DaemonRecord>) -> bool {
        // A daemon the app started is ready once its registry record exists
        // (its socket is bound); before that `status` may already find it
        // by process scan, without URL or token source.
        let ready = |r: &&DaemonRecord| r.source != SOURCE_PROCESS_SCAN;
        match p.op {
            PendingOp::Starting => rec.filter(ready).is_some(),
            PendingOp::Stopping => rec.is_none(),
            PendingOp::Restarting => rec.filter(ready).is_some_and(|r| Some(r.pid) != p.old_pid),
        }
    }

    /// Applies one poll: the registry listing (or the error running it) and
    /// the state reads. Returns what changed and the alerts to raise.
    pub fn apply(
        &mut self,
        records: Result<Vec<DaemonRecord>, String>,
        states: HashMap<String, Result<StateSummary, FetchError>>,
        now: Instant,
    ) -> RefreshOutcome {
        let mut outcome = RefreshOutcome::default();
        let records = match records {
            Ok(r) => r,
            Err(e) => {
                outcome.status_error = Some(e);
                return outcome;
            }
        };
        // Settle or expire pending operations.
        let by_key: HashMap<&str, &DaemonRecord> =
            records.iter().map(|r| (r.workflow.as_str(), r)).collect();
        let mut expired = Vec::new();
        self.pending.retain(|k, p| {
            if Self::settled(p, by_key.get(k.as_str()).copied()) {
                return false;
            }
            if now.duration_since(p.since) > PENDING_TIMEOUT {
                expired.push((k.clone(), p.op));
                return false;
            }
            true
        });
        for (k, op) in expired {
            let what = match op {
                PendingOp::Starting => "did not start in time — see the daemon output log",
                PendingOp::Stopping => "did not stop in time",
                PendingOp::Restarting => "did not restart in time",
            };
            self.errors.insert(k, what.into());
        }
        // Alerts, per daemon whose state was read.
        for r in &records {
            if let Some(Ok(st)) = states.get(&r.workflow) {
                let name = display_name(&r.workflow, Some(st));
                let alerts = self.detector.observe(&r.workflow, &name, &r.base_url(), st);
                outcome.alerts.extend(alerts);
            }
        }
        for a in &outcome.alerts {
            self.recent.push_front(a.clone());
        }
        self.recent.truncate(MAX_RECENT_ALERTS);

        let prev = std::mem::take(&mut self.views);
        self.views = self.build_views(&records, &states);
        self.last_records = records;
        outcome.diff = diff_views(&prev, &self.views);
        outcome
    }

    /// Rebuilds the views from the last poll (after begin/fail/add/remove).
    pub fn rebuild(&mut self) {
        let records = self.last_records.clone();
        let states: HashMap<String, Result<StateSummary, FetchError>> = self
            .views
            .iter()
            .filter_map(|v| match &v.health {
                Health::Ok(s) => Some((v.key.clone(), Ok(s.clone()))),
                Health::TokenRequired => Some((v.key.clone(), Err(FetchError::Unauthorized))),
                Health::Unreachable(e) => {
                    Some((v.key.clone(), Err(FetchError::Transport(e.clone()))))
                }
                Health::Stopped => None,
            })
            .collect();
        self.views = self.build_views(&records, &states);
    }

    fn build_views(
        &self,
        records: &[DaemonRecord],
        states: &HashMap<String, Result<StateSummary, FetchError>>,
    ) -> Vec<ProjectView> {
        // BTreeMap: one row per project key; a second daemon on the same
        // workflow (should not happen) keeps the first record.
        let mut rows: BTreeMap<String, ProjectView> = BTreeMap::new();
        for r in records {
            if rows.contains_key(&r.workflow) {
                continue;
            }
            let health = match states.get(&r.workflow) {
                Some(Ok(s)) => Health::Ok(s.clone()),
                Some(Err(FetchError::Unauthorized)) | None => Health::TokenRequired,
                Some(Err(e)) => Health::Unreachable(e.to_string()),
            };
            let state = match &health {
                Health::Ok(s) => Some(s),
                _ => None,
            };
            rows.insert(
                r.workflow.clone(),
                ProjectView {
                    key: r.workflow.clone(),
                    name: display_name(&r.workflow, state),
                    record: Some(r.clone()),
                    added: self.saved.contains(&r.workflow),
                    // Started by a supervisor whose token the app holds.
                    managed: r.token_source == TOKEN_SOURCE_SUPERVISOR
                        && !matches!(health, Health::TokenRequired),
                    health,
                    pending: None,
                    error: None,
                },
            );
        }
        for p in &self.saved.projects {
            rows.entry(p.workflow.clone())
                .or_insert_with(|| ProjectView {
                    key: p.workflow.clone(),
                    name: display_name(&p.workflow, None),
                    record: None,
                    added: true,
                    managed: false,
                    health: Health::Stopped,
                    pending: None,
                    error: None,
                });
        }
        // A project being started may be neither added nor running yet.
        for k in self.pending.keys() {
            rows.entry(k.clone()).or_insert_with(|| ProjectView {
                key: k.clone(),
                name: display_name(k, None),
                record: None,
                added: false,
                managed: false,
                health: Health::Stopped,
                pending: None,
                error: None,
            });
        }
        let mut out: Vec<ProjectView> = rows
            .into_values()
            .map(|mut v| {
                v.pending = self.pending.get(&v.key).map(|p| p.op);
                v.error = self.errors.get(&v.key).cloned();
                v
            })
            .collect();
        out.sort_by(|a, b| {
            a.name
                .to_lowercase()
                .cmp(&b.name.to_lowercase())
                .then_with(|| a.key.cmp(&b.key))
        });
        out
    }
}

/// Bounded map from notification id to what clicking it opens.
#[derive(Debug, Default)]
pub struct NotificationTargets {
    next_id: i32,
    order: VecDeque<i32>,
    targets: HashMap<i32, (String, String)>,
}

/// How many notification targets are remembered.
pub const MAX_NOTIFICATION_TARGETS: usize = 64;

impl NotificationTargets {
    /// Registers a target (project key, URL) and returns the id to post the
    /// notification with.
    pub fn register(&mut self, project: &str, url: &str) -> i32 {
        self.next_id = self.next_id.wrapping_add(1).max(1);
        let id = self.next_id;
        self.targets
            .insert(id, (project.to_string(), url.to_string()));
        self.order.push_back(id);
        while self.order.len() > MAX_NOTIFICATION_TARGETS {
            if let Some(old) = self.order.pop_front() {
                self.targets.remove(&old);
            }
        }
        id
    }

    pub fn get(&self, id: i32) -> Option<&(String, String)> {
        self.targets.get(&id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::InputRequired;
    use crate::token::MemoryTokenStore;

    fn rec(workflow: &str, pid: i32, source: &str) -> DaemonRecord {
        DaemonRecord {
            schema_version: 1,
            pid,
            workflow: workflow.into(),
            url: format!("http://127.0.0.1:{}/", 8000 + pid),
            token_required: true,
            token_source: source.into(),
            ..Default::default()
        }
    }

    fn no_file(_: &str) -> Option<String> {
        None
    }

    #[test]
    fn refresh_interval_is_long_when_idle_and_short_when_busy() {
        assert!(BUSY_REFRESH_INTERVAL <= Duration::from_secs(5));
        assert!(IDLE_REFRESH_INTERVAL >= Duration::from_secs(15));
        let mut core = Core::new(SavedProjects::default());
        assert!(!core.busy(), "nothing running");
        assert_eq!(refresh_interval(core.busy()), IDLE_REFRESH_INTERVAL);

        let r = rec("/a/WORKFLOW.md", 1, "supervisor");
        let poll = |core: &mut Core, s: StateSummary| {
            let mut m = HashMap::new();
            m.insert("/a/WORKFLOW.md".to_string(), Ok(s));
            core.apply(Ok(vec![r.clone()]), m, Instant::now());
            refresh_interval(core.busy())
        };
        assert_eq!(
            poll(&mut core, StateSummary::default()),
            IDLE_REFRESH_INTERVAL,
            "a running daemon with no agents is idle"
        );
        let running = StateSummary {
            running: 1,
            ..Default::default()
        };
        assert_eq!(poll(&mut core, running), BUSY_REFRESH_INTERVAL);
        let retrying = StateSummary {
            retrying: 1,
            ..Default::default()
        };
        assert_eq!(poll(&mut core, retrying), BUSY_REFRESH_INTERVAL);
        let mut waiting = StateSummary::default();
        waiting.input_required.push(InputRequired {
            identifier: "ENG-1".into(),
            state: "input_required".into(),
            queued_at: String::new(),
            context: String::new(),
        });
        assert_eq!(poll(&mut core, waiting), BUSY_REFRESH_INTERVAL);
        assert_eq!(
            poll(&mut core, StateSummary::default()),
            IDLE_REFRESH_INTERVAL
        );

        // A pending start or stop polls fast until it settles.
        core.begin("/b/WORKFLOW.md", PendingOp::Starting, Instant::now());
        assert_eq!(refresh_interval(core.busy()), BUSY_REFRESH_INTERVAL);
        // A daemon whose state cannot be read is not busy.
        let mut m = HashMap::new();
        m.insert("/a/WORKFLOW.md".to_string(), Err(FetchError::Unauthorized));
        core.fail("/b/WORKFLOW.md", "x".into());
        core.apply(Ok(vec![r.clone()]), m, Instant::now());
        assert!(!core.busy());
    }

    #[test]
    fn lists_every_registry_daemon_plus_added_projects() {
        let mut saved = SavedProjects::default();
        saved.add("/added/WORKFLOW.md");
        saved.add("/a/WORKFLOW.md");
        let mut core = Core::new(saved);
        assert_eq!(core.views().len(), 2, "added projects show before any poll");
        let recs = vec![
            rec("/a/WORKFLOW.md", 1, "supervisor"),
            rec("/b/WORKFLOW.md", 2, "env"),
        ];
        let mut states = HashMap::new();
        states.insert(
            "/a/WORKFLOW.md".to_string(),
            Ok(StateSummary {
                project_name: "alpha".into(),
                running: 2,
                ..Default::default()
            }),
        );
        let out = core.apply(Ok(recs), states, Instant::now());
        let names: Vec<_> = core.views().iter().map(|v| v.name.as_str()).collect();
        assert_eq!(names, vec!["added", "alpha", "b"]);
        let alpha = core.view("/a/WORKFLOW.md").unwrap();
        assert!(alpha.added && alpha.managed && alpha.running());
        assert_eq!(alpha.status_line(), "running · 2 agents");
        let b = core.view("/b/WORKFLOW.md").unwrap();
        assert_eq!(b.status_line(), "running · token required");
        assert!(!b.added);
        assert_eq!(
            core.view("/added/WORKFLOW.md").unwrap().status_line(),
            "stopped"
        );
        assert_eq!(out.diff.appeared, vec!["/b/WORKFLOW.md".to_string()]);
        assert_eq!(
            out.diff.changed.len(),
            1,
            "alpha went from stopped to running"
        );
    }

    #[test]
    fn status_failure_keeps_the_previous_list() {
        let mut core = Core::new(SavedProjects::default());
        core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 1, "env")]),
            HashMap::new(),
            Instant::now(),
        );
        let out = core.apply(Err("boom".into()), HashMap::new(), Instant::now());
        assert_eq!(out.status_error.as_deref(), Some("boom"));
        assert_eq!(core.views().len(), 1);
    }

    #[test]
    fn start_is_pending_until_the_next_poll_shows_the_daemon() {
        let mut core = Core::new(SavedProjects::default());
        let t0 = Instant::now();
        core.begin("/a/WORKFLOW.md", PendingOp::Starting, t0);
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().status_line(),
            "starting…"
        );
        core.apply(Ok(vec![]), HashMap::new(), t0 + Duration::from_secs(1));
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().pending,
            Some(PendingOp::Starting)
        );
        // Found by process scan before its socket is bound: still starting.
        let mut early = rec("/a/WORKFLOW.md", 5, "unknown");
        early.source = SOURCE_PROCESS_SCAN.into();
        core.apply(Ok(vec![early]), HashMap::new(), t0 + Duration::from_secs(1));
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().pending,
            Some(PendingOp::Starting)
        );
        let out = core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 5, "supervisor")]),
            HashMap::new(),
            t0 + Duration::from_secs(2),
        );
        let v = core.view("/a/WORKFLOW.md").unwrap();
        assert_eq!(v.pending, None);
        assert!(v.running());
        assert!(!out.diff.is_empty());
    }

    #[test]
    fn stop_and_restart_settle_on_the_registry() {
        let mut core = Core::new(SavedProjects::default());
        let t0 = Instant::now();
        core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 5, "supervisor")]),
            HashMap::new(),
            t0,
        );
        core.begin("/a/WORKFLOW.md", PendingOp::Restarting, t0);
        // Same pid: not restarted yet.
        core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 5, "supervisor")]),
            HashMap::new(),
            t0,
        );
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().pending,
            Some(PendingOp::Restarting)
        );
        core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 6, "supervisor")]),
            HashMap::new(),
            t0,
        );
        assert_eq!(core.view("/a/WORKFLOW.md").unwrap().pending, None);

        core.begin("/a/WORKFLOW.md", PendingOp::Stopping, t0);
        // Still draining: listed, so still stopping.
        core.apply(
            Ok(vec![rec("/a/WORKFLOW.md", 6, "supervisor")]),
            HashMap::new(),
            t0,
        );
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().pending,
            Some(PendingOp::Stopping)
        );
        core.apply(Ok(vec![]), HashMap::new(), t0);
        let gone = core.view("/a/WORKFLOW.md");
        assert!(
            gone.is_none(),
            "a stopped, never-added project leaves the list"
        );
    }

    #[test]
    fn pending_ops_expire_with_an_error_and_failures_show() {
        let mut core = Core::new(SavedProjects::default());
        let t0 = Instant::now();
        core.begin("/a/WORKFLOW.md", PendingOp::Starting, t0);
        core.apply(Ok(vec![]), HashMap::new(), t0 + PENDING_TIMEOUT * 2);
        assert!(core.view("/a/WORKFLOW.md").is_none());
        let mut saved = SavedProjects::default();
        saved.add("/b/WORKFLOW.md");
        let mut core = Core::new(saved);
        core.begin("/b/WORKFLOW.md", PendingOp::Starting, t0);
        core.fail("/b/WORKFLOW.md", "config invalid".into());
        assert_eq!(
            core.view("/b/WORKFLOW.md").unwrap().status_line(),
            "stopped · config invalid"
        );
    }

    #[test]
    fn input_required_on_a_managed_daemon_becomes_an_alert() {
        let mut core = Core::with_since(
            SavedProjects::default(),
            crate::events::rfc3339_unix("2026-10-14T10:00:00Z").unwrap(),
        );
        let r = rec("/a/WORKFLOW.md", 1, "supervisor");
        let mut s = StateSummary {
            project_name: "alpha".into(),
            ..Default::default()
        };
        let one = |s: &StateSummary| {
            let mut m = HashMap::new();
            m.insert("/a/WORKFLOW.md".to_string(), Ok(s.clone()));
            m
        };
        assert!(core
            .apply(Ok(vec![r.clone()]), one(&s), Instant::now())
            .alerts
            .is_empty());
        s.input_required.push(InputRequired {
            identifier: "ENG-9".into(),
            state: "input_required".into(),
            queued_at: "2026-10-14T10:00:05Z".into(),
            context: "Need a decision".into(),
        });
        let out = core.apply(Ok(vec![r]), one(&s), Instant::now());
        assert_eq!(out.alerts.len(), 1);
        assert_eq!(out.alerts[0].url, "http://127.0.0.1:8001/?issue=ENG-9");
        assert_eq!(core.recent_alerts().count(), 1);
        assert_eq!(
            core.view("/a/WORKFLOW.md").unwrap().status_line(),
            "running · 0 agents · 1 needs input"
        );
    }

    #[test]
    fn fetch_jobs_use_held_generated_or_no_token() {
        let core = Core::new(SavedProjects::default());
        let tokens = MemoryTokenStore::default();
        tokens
            .set("/sup/WORKFLOW.md", "held-token-1234567")
            .unwrap();
        let mut open = rec("/open/WORKFLOW.md", 3, "none");
        open.token_required = false;
        let mut generated = rec("/gen/WORKFLOW.md", 4, "generated");
        generated.token_file = "/logs/api-token".into();
        let recs = vec![
            rec("/sup/WORKFLOW.md", 1, "supervisor"),
            rec("/foreign/WORKFLOW.md", 2, "supervisor"),
            open,
            generated,
            rec("/env/WORKFLOW.md", 5, "env"),
        ];
        let read = |p: &str| (p == "/logs/api-token").then(|| "gen-token\n".to_string());
        let jobs = core.fetch_jobs(&recs, &tokens, &read);
        let got: Vec<(&str, Option<&str>)> = jobs
            .iter()
            .map(|j| (j.key.as_str(), j.token.as_deref()))
            .collect();
        assert_eq!(
            got,
            vec![
                ("/sup/WORKFLOW.md", Some("held-token-1234567")),
                ("/open/WORKFLOW.md", None),
                ("/gen/WORKFLOW.md", Some("gen-token")),
            ]
        );
        assert!(core.fetch_jobs(&recs, &tokens, &no_file).len() == 2);
    }

    #[test]
    fn diff_ignores_unchanged_rows() {
        let mut core = Core::new(SavedProjects::default());
        let recs = vec![rec("/a/WORKFLOW.md", 1, "env")];
        core.apply(Ok(recs.clone()), HashMap::new(), Instant::now());
        let out = core.apply(Ok(recs), HashMap::new(), Instant::now());
        assert!(out.diff.is_empty());
        let out = core.apply(Ok(vec![]), HashMap::new(), Instant::now());
        assert_eq!(out.diff.disappeared, vec!["/a/WORKFLOW.md".to_string()]);
    }

    #[test]
    fn notification_targets_are_bounded() {
        let mut t = NotificationTargets::default();
        let first = t.register("/a", "http://x/?issue=A");
        assert_eq!(t.get(first).unwrap().1, "http://x/?issue=A");
        for _ in 0..MAX_NOTIFICATION_TARGETS {
            t.register("/a", "u");
        }
        assert!(t.get(first).is_none());
    }
}
