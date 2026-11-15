//! Model servers and the GPU, machine-wide (the desktop app only).
//!
//! A profile can point its agent at a model server on this machine
//! (`ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude`). Each daemon lists
//! the ones its profiles use at `GET /api/v1/model-servers` and never
//! probes them; the app merges the lists of every local daemon by URL,
//! probes each loopback server itself at most every [`PROBE_INTERVAL`], and
//! shows the result in the tray menu ("Model servers"), in the project rail
//! and, when a server that was answering stops (two failed probes in a row)
//! or comes back, in a notification. Everything here is plain data; the
//! threads and HTTP are in `app_machine.rs` and `client.rs`.

use std::collections::{BTreeMap, HashMap};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};

use crate::gpu::{gpu_lines, GpuStatus};

/// How often each server is probed, at most.
pub const PROBE_INTERVAL: Duration = Duration::from_secs(30);
/// Bounds one probe.
pub const PROBE_TIMEOUT: Duration = Duration::from_secs(3);
/// How often the daemons' lists are read again, at most.
pub const LIST_INTERVAL: Duration = Duration::from_secs(5);
/// Failed probes in a row before an answering server counts as down.
pub const DOWN_AFTER: u32 = 2;

/// One row of a daemon's `GET /api/v1/model-servers`.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct DaemonModelServer {
    #[serde(default)]
    pub var: String,
    pub url: String,
    #[serde(default)]
    pub profiles: Vec<String>,
    #[serde(default)]
    pub pools: Vec<String>,
    /// The daemon runs agents on SSH worker hosts.
    #[serde(default)]
    pub remote: bool,
    /// The URL comes from a shell variable.
    #[serde(default)]
    pub unchecked: bool,
}

#[derive(Deserialize)]
struct RawList {
    #[serde(default)]
    servers: Option<Vec<DaemonModelServer>>,
}

/// Parses a `GET /api/v1/model-servers` body; rows without a URL are
/// skipped.
pub fn parse_model_servers(body: &[u8]) -> Result<Vec<DaemonModelServer>, String> {
    let raw: RawList = serde_json::from_slice(body).map_err(|e| e.to_string())?;
    Ok(raw
        .servers
        .unwrap_or_default()
        .into_iter()
        .filter(|s| !s.url.trim().is_empty())
        .collect())
}

/// Whether `url` is an http(s) URL on this machine: `127.0.0.1`,
/// `localhost` or `::1`. Nothing else is ever probed.
pub fn is_loopback_url(url: &str) -> bool {
    host_port(url).is_some_and(|(host, _)| {
        host.eq_ignore_ascii_case("localhost") || host == "127.0.0.1" || host == "::1"
    })
}

/// The host and port of an http(s) URL (userinfo dropped, brackets of an
/// IPv6 literal removed, the scheme's port when none is given).
pub fn host_port(url: &str) -> Option<(String, u16)> {
    let lower = url.trim().to_ascii_lowercase();
    let (rest, default_port) = if let Some(r) = lower.strip_prefix("http://") {
        (r, 80)
    } else {
        (lower.strip_prefix("https://")?, 443)
    };
    let authority = rest.split(['/', '?', '#']).next().unwrap_or("");
    let hostport = authority.rsplit_once('@').map_or(authority, |(_, h)| h);
    let (host, port) = if let Some(v6) = hostport.strip_prefix('[') {
        let (h, after) = v6.split_once(']')?;
        (h, after.strip_prefix(':'))
    } else {
        match hostport.split_once(':') {
            Some((h, p)) => (h, Some(p)),
            None => (hostport, None),
        }
    };
    if host.is_empty() {
        return None;
    }
    let port = match port {
        None | Some("") => default_port,
        Some(p) => p.parse().ok()?,
    };
    Some((host.to_string(), port))
}

/// `host:port` as the menu shows it (`127.0.0.1:8000`, `[::1]:8000`), or
/// the URL itself when it is not an http(s) URL.
pub fn display_address(url: &str) -> String {
    match host_port(url) {
        Some((h, p)) if h.contains(':') => format!("[{h}]:{p}"),
        Some((h, p)) => format!("{h}:{p}"),
        None => url.to_string(),
    }
}

/// Which projects use a server, and through which profiles.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ServerUse {
    pub project: String,
    pub profiles: Vec<String>,
}

/// Why a server is not probed.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NotChecked {
    /// The URL is a `$VAR` the agent's shell expands.
    FromVariable,
    /// Not `127.0.0.1`, `localhost` or `::1`.
    NotLoopback,
    /// Every project using it runs agents on SSH worker hosts.
    Remote,
}

impl NotChecked {
    pub fn reason(self) -> &'static str {
        match self {
            NotChecked::FromVariable => "set from a variable",
            NotChecked::NotLoopback => "not on this machine",
            NotChecked::Remote => "runs go to SSH worker hosts",
        }
    }
}

/// One model server, merged across projects.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MergedServer {
    pub url: String,
    /// The variables that name it (usually one).
    pub vars: Vec<String>,
    /// Per project, sorted by project name.
    pub uses: Vec<ServerUse>,
    pub pools: Vec<String>,
    /// `None` when it is probed.
    pub not_checked: Option<NotChecked>,
}

impl MergedServer {
    /// The name the menu gives it: its pool(s), else "model server".
    pub fn name(&self) -> String {
        if self.pools.is_empty() {
            "model server".into()
        } else {
            self.pools.join("/")
        }
    }
}

/// The key servers are merged under: the URL without trailing slashes.
fn merge_key(url: &str) -> String {
    url.trim().trim_end_matches('/').to_string()
}

fn push_unique(v: &mut Vec<String>, s: &str) {
    if !v.iter().any(|x| x == s) {
        v.push(s.to_string());
    }
}

/// Merges the lists of every project (`(project name, rows)`) by URL,
/// recording which projects and profiles use each. A server is probed when
/// it is a loopback URL, not from a variable, and at least one project runs
/// it on this machine. Sorted by URL.
pub fn merge_servers(lists: &[(String, Vec<DaemonModelServer>)]) -> Vec<MergedServer> {
    struct Acc {
        url: String,
        vars: Vec<String>,
        uses: BTreeMap<String, Vec<String>>,
        pools: Vec<String>,
        unchecked: bool,
        local: bool,
    }
    let mut by_url: BTreeMap<String, Acc> = BTreeMap::new();
    for (project, rows) in lists {
        for r in rows {
            let key = merge_key(&r.url);
            let acc = by_url.entry(key.clone()).or_insert_with(|| Acc {
                url: key,
                vars: Vec::new(),
                uses: BTreeMap::new(),
                pools: Vec::new(),
                unchecked: false,
                local: false,
            });
            if !r.var.is_empty() {
                push_unique(&mut acc.vars, &r.var);
            }
            let profiles = acc.uses.entry(project.clone()).or_default();
            for p in &r.profiles {
                push_unique(profiles, p);
            }
            for p in &r.pools {
                push_unique(&mut acc.pools, p);
            }
            acc.unchecked |= r.unchecked || r.url.contains('$');
            acc.local |= !r.remote;
        }
    }
    by_url
        .into_values()
        .map(|mut a| {
            a.vars.sort();
            a.pools.sort();
            let not_checked = if a.unchecked {
                Some(NotChecked::FromVariable)
            } else if !is_loopback_url(&a.url) {
                Some(NotChecked::NotLoopback)
            } else if !a.local {
                Some(NotChecked::Remote)
            } else {
                None
            };
            MergedServer {
                url: a.url,
                vars: a.vars,
                uses: a
                    .uses
                    .into_iter()
                    .map(|(project, mut profiles)| {
                        profiles.sort();
                        ServerUse { project, profiles }
                    })
                    .collect(),
                pools: a.pools,
                not_checked,
            }
        })
        .collect()
}

/// What the probes say about one server.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ProbeHealth {
    /// Not probed yet.
    Checking,
    Answering,
    NotAnswering,
}

/// A change worth a notification.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Transition {
    /// It was answering and missed [`DOWN_AFTER`] probes in a row.
    WentDown,
    /// It was down and answered again.
    Recovered,
}

/// One server's probe history. An answering server needs [`DOWN_AFTER`]
/// failures in a row before it is down, so one slow probe does not
/// notify; a server never seen answering is down after its first failure,
/// without a notification (there was nothing to lose).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProbeTracker {
    pub health: ProbeHealth,
    fails: u32,
    last: Option<Instant>,
}

impl Default for ProbeTracker {
    fn default() -> Self {
        ProbeTracker {
            health: ProbeHealth::Checking,
            fails: 0,
            last: None,
        }
    }
}

impl ProbeTracker {
    /// Whether a probe is due at `now`.
    pub fn due(&self, now: Instant) -> bool {
        self.last
            .is_none_or(|t| now.saturating_duration_since(t) >= PROBE_INTERVAL)
    }

    /// Records a probe's result; returns the transition to notify, if any.
    pub fn record(&mut self, answered: bool, now: Instant) -> Option<Transition> {
        self.last = Some(now);
        if answered {
            self.fails = 0;
            let was = self.health;
            self.health = ProbeHealth::Answering;
            return (was == ProbeHealth::NotAnswering).then_some(Transition::Recovered);
        }
        self.fails = self.fails.saturating_add(1);
        match self.health {
            ProbeHealth::Checking => {
                self.health = ProbeHealth::NotAnswering;
                None
            }
            ProbeHealth::Answering if self.fails >= DOWN_AFTER => {
                self.health = ProbeHealth::NotAnswering;
                Some(Transition::WentDown)
            }
            _ => None,
        }
    }
}

/// A notification to show.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HealthAlert {
    pub title: String,
    pub body: String,
}

fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

fn alert_for(s: &MergedServer, t: Transition) -> HealthAlert {
    let who = s
        .uses
        .iter()
        .map(|u| u.project.as_str())
        .collect::<Vec<_>>()
        .join(", ");
    let addr = display_address(&s.url);
    match t {
        Transition::WentDown => HealthAlert {
            title: format!("Model server {addr} stopped answering"),
            body: format!(
                "{} {addr} is not answering; runs of {who} that use it will fail until it is back.",
                s.name()
            ),
        },
        Transition::Recovered => HealthAlert {
            title: format!("Model server {addr} is answering again"),
            body: format!("{} {addr} is back for {who}.", s.name()),
        },
    }
}

/// One server as the menu and the rail show it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ServerLine {
    /// The menu line: `local 127.0.0.1:8000 — answering · used by 2 projects`.
    pub label: String,
    pub name: String,
    pub address: String,
    /// `answering`, `not_answering`, `checking` or `not_checked`.
    pub state: &'static str,
    /// The state as words (`not checked: set from a variable`).
    pub state_text: String,
    pub uses: Vec<ServerUse>,
}

/// The rail's "Model servers" panel.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MachinePanel {
    pub servers: Vec<ServerLine>,
    pub gpu: Vec<String>,
}

/// The menu and rail line of one server.
pub fn server_line(s: &MergedServer, health: Option<ProbeHealth>) -> ServerLine {
    let (state, state_text) = match (s.not_checked, health) {
        (Some(n), _) => ("not_checked", format!("not checked: {}", n.reason())),
        (None, Some(ProbeHealth::Answering)) => ("answering", "answering".to_string()),
        (None, Some(ProbeHealth::NotAnswering)) => ("not_answering", "not answering".to_string()),
        (None, Some(ProbeHealth::Checking) | None) => ("checking", "checking…".to_string()),
    };
    let name = s.name();
    let address = display_address(&s.url);
    let label = format!(
        "{name} {address} — {state_text} · used by {}",
        plural(s.uses.len(), "project", "projects")
    );
    ServerLine {
        label,
        name,
        address,
        state,
        state_text,
        uses: s.uses.clone(),
    }
}

/// The last list read from each project, kept across a failed read so a
/// daemon that misses one read does not reset its servers' history.
#[derive(Debug, Clone, Default)]
struct ProjectList {
    name: String,
    rows: Vec<DaemonModelServer>,
}

/// The read of one project's list.
pub struct ListRead {
    pub key: String,
    pub name: String,
    /// `Err` keeps the project's previous list.
    pub rows: Result<Vec<DaemonModelServer>, String>,
}

/// The whole machine-health state: lists, merged servers, probe history and
/// the GPU.
#[derive(Debug, Default)]
pub struct MachineHealth {
    lists: HashMap<String, ProjectList>,
    servers: Vec<MergedServer>,
    probes: HashMap<String, ProbeTracker>,
    pub gpu: GpuStatus,
    gpu_read: Option<Instant>,
}

impl MachineHealth {
    /// Applies one round of list reads: projects not in `reads` are gone;
    /// a failed read keeps the project's last list. Probe history of a
    /// server no project lists any more is dropped.
    pub fn apply_lists(&mut self, reads: Vec<ListRead>) {
        let mut next = HashMap::new();
        for r in reads {
            let rows = match r.rows {
                Ok(rows) => rows,
                Err(_) => self
                    .lists
                    .get(&r.key)
                    .map(|l| l.rows.clone())
                    .unwrap_or_default(),
            };
            next.insert(r.key, ProjectList { name: r.name, rows });
        }
        self.lists = next;
        let mut lists: Vec<(String, Vec<DaemonModelServer>)> = self
            .lists
            .values()
            .map(|l| (l.name.clone(), l.rows.clone()))
            .collect();
        lists.sort_by(|a, b| a.0.cmp(&b.0));
        self.servers = merge_servers(&lists);
        let probed: Vec<&str> = self
            .servers
            .iter()
            .filter(|s| s.not_checked.is_none())
            .map(|s| s.url.as_str())
            .collect();
        self.probes.retain(|url, _| probed.contains(&url.as_str()));
    }

    pub fn servers(&self) -> &[MergedServer] {
        &self.servers
    }

    /// The URLs to probe at `now`.
    pub fn due_probes(&self, now: Instant) -> Vec<String> {
        self.servers
            .iter()
            .filter(|s| s.not_checked.is_none())
            .filter(|s| self.probes.get(&s.url).is_none_or(|p| p.due(now)))
            .map(|s| s.url.clone())
            .collect()
    }

    /// Records a probe of `url`; returns the notification to show, if any.
    pub fn apply_probe(&mut self, url: &str, answered: bool, now: Instant) -> Option<HealthAlert> {
        let server = self
            .servers
            .iter()
            .find(|s| s.url == url && s.not_checked.is_none())?;
        let t = self
            .probes
            .entry(url.to_string())
            .or_default()
            .record(answered, now)?;
        Some(alert_for(server, t))
    }

    /// Whether the GPU should be read at `now`: only while some server is
    /// listed (no subprocess for a machine without local models), at most
    /// every [`PROBE_INTERVAL`].
    pub fn gpu_due(&self, now: Instant) -> bool {
        !self.servers.is_empty()
            && self
                .gpu_read
                .is_none_or(|t| now.saturating_duration_since(t) >= PROBE_INTERVAL)
    }

    pub fn set_gpu(&mut self, gpu: GpuStatus, now: Instant) {
        self.gpu = gpu;
        self.gpu_read = Some(now);
    }

    /// The server lines, in URL order.
    pub fn lines(&self) -> Vec<ServerLine> {
        self.servers
            .iter()
            .map(|s| server_line(s, self.probes.get(&s.url).map(|p| p.health)))
            .collect()
    }

    /// The rail's panel; `None` when no project uses a model server.
    pub fn panel(&self) -> Option<MachinePanel> {
        if self.servers.is_empty() {
            return None;
        }
        Some(MachinePanel {
            servers: self.lines(),
            gpu: gpu_lines(&self.gpu),
        })
    }

    /// The menu's "Model servers" lines: one per server, then the GPU
    /// line(s). Empty when no project uses a model server.
    pub fn menu_lines(&self) -> Vec<String> {
        match self.panel() {
            None => Vec::new(),
            Some(p) => p
                .servers
                .into_iter()
                .map(|s| s.label)
                .chain(p.gpu)
                .collect(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(var: &str, url: &str, profiles: &[&str], pools: &[&str]) -> DaemonModelServer {
        DaemonModelServer {
            var: var.into(),
            url: url.into(),
            profiles: profiles.iter().map(|s| s.to_string()).collect(),
            pools: pools.iter().map(|s| s.to_string()).collect(),
            remote: false,
            unchecked: false,
        }
    }

    #[test]
    fn parses_the_daemon_list() {
        let body = br#"{"servers":[
            {"var":"ANTHROPIC_BASE_URL","url":"http://127.0.0.1:8000","profiles":["(default)","coder-local"],"pools":["local"],"remote":false},
            {"var":"OPENAI_BASE_URL","url":"$LOCAL","profiles":["x"],"pools":[],"remote":false,"unchecked":true},
            {"var":"OPENAI_API_BASE","url":"  "},
            {"url":"http://localhost:9000","future":"ignored"}
        ]}"#;
        let got = parse_model_servers(body).unwrap();
        assert_eq!(got.len(), 3, "a row without a URL is skipped");
        assert_eq!(
            got[0],
            row(
                "ANTHROPIC_BASE_URL",
                "http://127.0.0.1:8000",
                &["(default)", "coder-local"],
                &["local"]
            )
        );
        assert!(got[1].unchecked);
        assert_eq!(got[2].var, "");
        assert!(parse_model_servers(b"{}").unwrap().is_empty());
        assert!(parse_model_servers(br#"{"servers":null}"#)
            .unwrap()
            .is_empty());
        assert!(parse_model_servers(b"<html>").is_err());
    }

    #[test]
    fn loopback_filter_allows_only_this_machine() {
        for url in [
            "http://127.0.0.1:8000",
            "http://localhost:8000/v1",
            "https://LOCALHOST",
            "http://[::1]:8000",
            "http://user:pw@127.0.0.1:8000",
            "http://%5Bredacted%5D@localhost:9000/v1",
        ] {
            assert!(is_loopback_url(url), "{url}");
        }
        for url in [
            "http://127.0.0.2:8000",
            "http://10.0.0.5:8000",
            "http://localhost.evil.com:8000",
            "http://evil.com/@127.0.0.1",
            "http://127.0.0.1.nip.io",
            "ftp://127.0.0.1",
            "127.0.0.1:8000",
            "$LOCAL_URL",
            "http://[::2]:1",
            "http://127.0.0.1:notaport",
            "http://",
        ] {
            assert!(!is_loopback_url(url), "{url}");
        }
        assert_eq!(
            display_address("http://127.0.0.1:8000/v1"),
            "127.0.0.1:8000"
        );
        assert_eq!(display_address("http://localhost"), "localhost:80");
        assert_eq!(display_address("http://[::1]:8000"), "[::1]:8000");
        assert_eq!(display_address("$X"), "$X");
    }

    #[test]
    fn merges_across_projects_by_url() {
        let mut remote = row("ANTHROPIC_BASE_URL", "http://127.0.0.1:9000", &["r"], &[]);
        remote.remote = true;
        let mut var = row("OPENAI_BASE_URL", "$LOCAL", &["v"], &[]);
        var.unchecked = true;
        let lists = vec![
            (
                "web".to_string(),
                vec![
                    row(
                        "ANTHROPIC_BASE_URL",
                        "http://127.0.0.1:8000",
                        &["(default)", "coder-local"],
                        &["local"],
                    ),
                    row("OPENAI_BASE_URL", "http://10.0.0.5:8000/v1", &["far"], &[]),
                    remote.clone(),
                    var,
                ],
            ),
            (
                "api".to_string(),
                vec![
                    // Same server, trailing slash, another variable and pool.
                    row(
                        "OPENAI_BASE_URL",
                        "http://127.0.0.1:8000/",
                        &["coder-local"],
                        &["box"],
                    ),
                ],
            ),
        ];
        let merged = merge_servers(&lists);
        let urls: Vec<&str> = merged.iter().map(|s| s.url.as_str()).collect();
        assert_eq!(
            urls,
            vec![
                "$LOCAL",
                "http://10.0.0.5:8000/v1",
                "http://127.0.0.1:8000",
                "http://127.0.0.1:9000"
            ]
        );
        let colibri = &merged[2];
        assert_eq!(colibri.vars, vec!["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"]);
        assert_eq!(colibri.pools, vec!["box", "local"]);
        assert_eq!(colibri.name(), "box/local");
        assert_eq!(
            colibri.uses,
            vec![
                ServerUse {
                    project: "api".into(),
                    profiles: vec!["coder-local".into()]
                },
                ServerUse {
                    project: "web".into(),
                    profiles: vec!["(default)".into(), "coder-local".into()]
                },
            ]
        );
        assert_eq!(colibri.not_checked, None);
        assert_eq!(merged[0].not_checked, Some(NotChecked::FromVariable));
        assert_eq!(merged[1].not_checked, Some(NotChecked::NotLoopback));
        assert_eq!(merged[3].not_checked, Some(NotChecked::Remote));

        // One project running it locally is enough to probe it.
        let mut both = lists.clone();
        both.push((
            "local".into(),
            vec![row(
                "ANTHROPIC_BASE_URL",
                "http://127.0.0.1:9000",
                &["l"],
                &[],
            )],
        ));
        assert_eq!(merge_servers(&both)[3].not_checked, None);
        assert!(merge_servers(&[]).is_empty());
    }

    #[test]
    fn an_answering_server_is_down_after_two_failed_probes() {
        let t0 = Instant::now();
        let mut p = ProbeTracker::default();
        assert!(p.due(t0));
        assert_eq!(p.record(true, t0), None, "first answer: no notification");
        assert_eq!(p.health, ProbeHealth::Answering);
        assert!(!p.due(t0 + Duration::from_secs(29)));
        assert!(p.due(t0 + PROBE_INTERVAL));

        assert_eq!(p.record(false, t0), None, "one failure is debounced");
        assert_eq!(p.health, ProbeHealth::Answering);
        assert_eq!(p.record(false, t0), Some(Transition::WentDown));
        assert_eq!(p.health, ProbeHealth::NotAnswering);
        assert_eq!(
            p.record(false, t0),
            None,
            "notified once, on the transition"
        );
        assert_eq!(p.record(false, t0), None);
        assert_eq!(p.record(true, t0), Some(Transition::Recovered));
        assert_eq!(p.record(true, t0), None);

        // A failure between two answers resets the count.
        assert_eq!(p.record(false, t0), None);
        assert_eq!(
            p.record(true, t0),
            None,
            "never went down: nothing recovered"
        );
        assert_eq!(p.record(false, t0), None);
        assert_eq!(p.health, ProbeHealth::Answering);
    }

    #[test]
    fn a_server_never_seen_answering_does_not_notify() {
        let t0 = Instant::now();
        let mut p = ProbeTracker::default();
        assert_eq!(p.record(false, t0), None);
        assert_eq!(p.health, ProbeHealth::NotAnswering);
        assert_eq!(p.record(false, t0), None);
        assert_eq!(
            p.record(true, t0),
            Some(Transition::Recovered),
            "started later: that is worth knowing"
        );
    }

    type Read<'a> = (&'a str, &'a str, Result<Vec<DaemonModelServer>, String>);

    fn reads(lists: &[Read<'_>]) -> Vec<ListRead> {
        lists
            .iter()
            .map(|(k, n, r)| ListRead {
                key: k.to_string(),
                name: n.to_string(),
                rows: r.clone(),
            })
            .collect()
    }

    #[test]
    fn machine_health_probes_notifies_and_keeps_history_across_a_failed_read() {
        let t0 = Instant::now();
        let mut m = MachineHealth::default();
        assert!(m.menu_lines().is_empty(), "nothing configured: no section");
        assert!(m.panel().is_none());
        assert!(!m.gpu_due(t0), "no server: no nvidia-smi");

        let local = row(
            "ANTHROPIC_BASE_URL",
            "http://127.0.0.1:8000",
            &["coder-local"],
            &["local"],
        );
        let far = row("ANTHROPIC_BASE_URL", "http://10.0.0.5:8000", &["far"], &[]);
        m.apply_lists(reads(&[
            ("/a/WORKFLOW.md", "web", Ok(vec![local.clone(), far])),
            ("/b/WORKFLOW.md", "api", Ok(vec![local.clone()])),
        ]));
        assert_eq!(
            m.due_probes(t0),
            vec!["http://127.0.0.1:8000"],
            "loopback only"
        );
        assert!(m.gpu_due(t0));
        assert_eq!(
            m.apply_probe("http://10.0.0.5:8000", true, t0),
            None,
            "never probed"
        );

        assert_eq!(m.apply_probe("http://127.0.0.1:8000", true, t0), None);
        assert!(m.due_probes(t0 + Duration::from_secs(1)).is_empty());
        // A failed read of one project keeps its list and the history.
        m.apply_lists(reads(&[
            ("/a/WORKFLOW.md", "web", Err("HTTP 500".into())),
            ("/b/WORKFLOW.md", "api", Ok(vec![local.clone()])),
        ]));
        assert_eq!(m.servers().len(), 2);
        assert_eq!(m.lines()[1].state, "answering");

        assert_eq!(m.apply_probe("http://127.0.0.1:8000", false, t0), None);
        let down = m.apply_probe("http://127.0.0.1:8000", false, t0).unwrap();
        assert_eq!(down.title, "Model server 127.0.0.1:8000 stopped answering");
        assert!(down.body.contains("api, web"), "{}", down.body);
        let back = m.apply_probe("http://127.0.0.1:8000", true, t0).unwrap();
        assert_eq!(back.title, "Model server 127.0.0.1:8000 is answering again");

        // Gone from every project: its history goes too.
        m.apply_lists(reads(&[("/b/WORKFLOW.md", "api", Ok(vec![]))]));
        assert!(m.servers().is_empty());
        m.apply_lists(reads(&[("/b/WORKFLOW.md", "api", Ok(vec![local]))]));
        assert_eq!(
            m.lines()[0].state,
            "checking",
            "a server listed again starts over"
        );
    }

    #[test]
    fn menu_labels_name_the_server_its_state_and_its_projects() {
        let t0 = Instant::now();
        let mut m = MachineHealth::default();
        let mut var = row("OPENAI_BASE_URL", "$LOCAL", &["v"], &[]);
        var.unchecked = true;
        let colibri = row(
            "ANTHROPIC_BASE_URL",
            "http://127.0.0.1:8000",
            &["coder-local"],
            &["colibri"],
        );
        m.apply_lists(reads(&[
            ("/a", "web", Ok(vec![colibri.clone(), var])),
            ("/b", "api", Ok(vec![colibri])),
            (
                "/c",
                "docs",
                Ok(vec![row(
                    "OPENAI_BASE_URL",
                    "http://localhost:9000/v1",
                    &["x"],
                    &[],
                )]),
            ),
        ]));
        assert_eq!(
            m.menu_lines(),
            vec![
                "model server $LOCAL — not checked: set from a variable · used by 1 project",
                "colibri 127.0.0.1:8000 — checking… · used by 2 projects",
                "model server localhost:9000 — checking… · used by 1 project",
                "GPU: checking…",
            ]
        );
        m.apply_probe("http://127.0.0.1:8000", true, t0);
        m.apply_probe("http://localhost:9000/v1", false, t0);
        m.set_gpu(crate::gpu::status_without_nvidia_smi(false), t0);
        assert!(!m.gpu_due(t0 + Duration::from_secs(1)));
        assert_eq!(
            m.menu_lines()[1..],
            [
                "colibri 127.0.0.1:8000 — answering · used by 2 projects".to_string(),
                "model server localhost:9000 — not answering · used by 1 project".to_string(),
                "GPU: not reported".to_string(),
            ]
        );
        let json = serde_json::to_value(m.panel().unwrap()).unwrap();
        assert_eq!(json["servers"][1]["state"], "answering");
        assert_eq!(json["servers"][1]["stateText"], "answering");
        assert_eq!(json["servers"][1]["uses"][0]["project"], "api");
        assert_eq!(json["gpu"][0], "GPU: not reported");
    }
}
