//! What the menu shows for the remote connections (#156), and how the
//! refresh loop reads remote daemons: through their tunnels, with the token
//! stored for the connection and daemon.

use std::collections::{HashMap, HashSet};

use crate::client::FetchError;
use crate::daemon_state::StateSummary;
use crate::dashboard::origin_of;
use crate::events::{Alert, EventDetector};
use crate::model::{FetchJob, Health};
use crate::remote::{hub_key, token_key, Connection, RemoteDaemon, Remotes};
use crate::sessions::SessionsJob;
use crate::token::TokenStore;
use crate::tunnel::{TunnelPhase, TunnelStatus};

/// One remote daemon in the menu.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RemoteDaemonView {
    /// [`token_key`]: connection id + remote WORKFLOW.md.
    pub key: String,
    pub workflow: String,
    pub name: String,
    pub tunnel: Option<TunnelStatus>,
    /// The daemon's state, read through the tunnel. `Stopped` while the
    /// tunnel is not up.
    pub health: Health,
    pub has_token: bool,
    /// Reached through the host's hub (#232): the member's path on the
    /// hub's tunnel, `hub/p/{name}/`.
    pub hub_path: Option<String>,
}

impl RemoteDaemonView {
    pub fn needs_input(&self) -> usize {
        match &self.health {
            Health::Ok(s) => s.waiting_for_input().count(),
            _ => 0,
        }
    }

    /// The dashboard URL through the tunnel, while it is up: the member's
    /// dashboard on the hub for a daemon reached through the hub.
    pub fn url(&self) -> Option<String> {
        let base = self
            .tunnel
            .as_ref()
            .filter(|t| t.is_up())
            .map(TunnelStatus::base_url)?;
        Some(match &self.hub_path {
            Some(p) => format!("{base}{p}"),
            None => base,
        })
    }

    pub fn status_line(&self) -> String {
        let Some(t) = &self.tunnel else {
            return "not connected".into();
        };
        if !t.is_up() {
            return t.phase_line();
        }
        match &self.health {
            Health::Ok(st) => {
                let mut parts = vec![format!(
                    "{} agent{}",
                    st.running,
                    if st.running == 1 { "" } else { "s" }
                )];
                let w = st.waiting_for_input().count();
                if w > 0 {
                    parts.push(format!("{w} need{} input", if w == 1 { "s" } else { "" }));
                }
                if self.hub_path.is_some() {
                    parts.push("via hub".into());
                }
                parts.push(format!("localhost:{}", t.local_port));
                parts.join(" · ")
            }
            Health::TokenRequired => format!("token required · localhost:{}", t.local_port),
            Health::Unreachable(e) => e.clone(),
            Health::Stopped => t.phase_line(),
        }
    }
}

/// One connection in the menu.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConnectionView {
    pub id: String,
    pub name: String,
    pub transport: String,
    pub daemons: Vec<RemoteDaemonView>,
    /// The last discovery or token error, shown until the next success.
    pub error: Option<String>,
    /// The host's hub (#232), e.g. "hub · 3 projects through one tunnel".
    pub hub: Option<String>,
}

impl ConnectionView {
    /// Whether any of its daemons has agent work in flight (#212).
    pub fn busy(&self) -> bool {
        self.daemons.iter().any(|d| d.health.busy())
    }

    /// e.g. "2/2 connected · 1 needs input" or "reconnecting…".
    pub fn status_line(&self) -> String {
        if self.daemons.is_empty() {
            return self
                .error
                .clone()
                .unwrap_or_else(|| "no daemons found".into());
        }
        let up = self
            .daemons
            .iter()
            .filter(|d| d.tunnel.as_ref().is_some_and(TunnelStatus::is_up))
            .count();
        let mut s = if up == self.daemons.len() {
            format!("{up}/{} connected", self.daemons.len())
        } else if self.daemons.iter().any(|d| {
            d.tunnel
                .as_ref()
                .is_some_and(|t| t.phase == TunnelPhase::Connecting)
        }) {
            format!("connecting… ({up}/{} connected)", self.daemons.len())
        } else {
            format!("reconnecting… ({up}/{} connected)", self.daemons.len())
        };
        let w: usize = self.daemons.iter().map(RemoteDaemonView::needs_input).sum();
        if w > 0 {
            s.push_str(&format!(
                " · {w} need{} input",
                if w == 1 { "s" } else { "" }
            ));
        }
        s
    }
}

/// What opening a remote dashboard does: the tunnel URL, and the token
/// handed to the window exactly as for a local daemon.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DashboardTarget {
    pub url: String,
    pub origin: String,
    pub token: Option<String>,
}

/// `url` moved to `base` (same path and query): a notification link made
/// while the tunnel used another local port still opens the issue. A link
/// made through a host's hub (`hub/p/{name}/...`, #232) keeps only its
/// dashboard part, so it opens on the daemon's own tunnel or the hub's.
pub fn rebase_url(url: &str, base: &str) -> String {
    let mut rest = url
        .split_once("://")
        .and_then(|(_, r)| r.find('/').map(|i| &r[i + 1..]))
        .unwrap_or("");
    if let Some(after) = rest.strip_prefix("hub/p/") {
        rest = after.find('/').map_or("", |i| &after[i + 1..]);
    }
    let mut b = base.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}{rest}")
}

/// Whether a connection's hub stopped answering (#232): its tunnel failed
/// since it was last up. A hub tunnel still starting is not down.
pub fn hub_down(c: &Connection, tunnels: &[TunnelStatus]) -> bool {
    let key = hub_key(&c.id);
    c.hub.is_some()
        && tunnels
            .iter()
            .find(|t| t.id == key)
            .is_some_and(|t| !t.is_up() && t.last_error.is_some())
}

/// The connections whose hub is down: their members get their own tunnels
/// ([`crate::remote_ops::tunnel_specs_with`]).
pub fn hubs_down(remotes: &Remotes, tunnels: &[TunnelStatus]) -> HashSet<String> {
    remotes
        .connections
        .iter()
        .filter(|c| hub_down(c, tunnels))
        .map(|c| c.id.clone())
        .collect()
}

/// How the app reaches one remote daemon this refresh.
struct Route {
    tunnel: Option<TunnelStatus>,
    /// The token's key: the hub's for a member read through the hub.
    token_key: String,
    hub_path: Option<String>,
}

/// A hub project name as one URL path segment.
fn path_segment(name: &str) -> String {
    name.bytes()
        .map(|b| {
            if b.is_ascii_alphanumeric() || b"-._~".contains(&b) {
                (b as char).to_string()
            } else {
                format!("%{b:02X}")
            }
        })
        .collect()
}

/// Through the host's hub while it serves and the daemon is a member, else
/// through the daemon's own tunnel.
fn route(c: &Connection, d: &RemoteDaemon, tunnels: &[TunnelStatus]) -> Route {
    if let Some(m) = c.hub.as_ref().and_then(|h| h.member(&d.workflow)) {
        if !hub_down(c, tunnels) {
            let key = hub_key(&c.id);
            return Route {
                tunnel: tunnels.iter().find(|t| t.id == key).cloned(),
                token_key: key,
                hub_path: Some(format!("hub/p/{}/", path_segment(&m.name))),
            };
        }
    }
    let key = token_key(&c.id, &d.workflow);
    Route {
        tunnel: tunnels.iter().find(|t| t.id == key).cloned(),
        token_key: key,
        hub_path: None,
    }
}

impl Route {
    fn base(&self) -> Option<String> {
        let base = self
            .tunnel
            .as_ref()
            .filter(|t| t.is_up())
            .map(TunnelStatus::base_url)?;
        Some(format!("{base}{}", self.hub_path.as_deref().unwrap_or("")))
    }
}

/// The connection's hub line for the menu.
fn hub_line(c: &Connection, tunnels: &[TunnelStatus]) -> Option<String> {
    let h = c.hub.as_ref()?;
    let n = h.members.len();
    let projects = format!("{n} project{}", if n == 1 { "" } else { "s" });
    Some(if hub_down(c, tunnels) {
        format!("hub not answering — {projects} on their own tunnels")
    } else {
        format!("hub · {projects} through one tunnel")
    })
}

/// The remote side of the model.
pub struct RemoteCore {
    detector: EventDetector,
    views: Vec<ConnectionView>,
    errors: HashMap<String, String>,
}

impl RemoteCore {
    pub fn new(since: i64) -> Self {
        RemoteCore {
            detector: EventDetector::new(since),
            views: Vec::new(),
            errors: HashMap::new(),
        }
    }

    pub fn views(&self) -> &[ConnectionView] {
        &self.views
    }

    pub fn daemon(&self, key: &str) -> Option<&RemoteDaemonView> {
        self.views
            .iter()
            .flat_map(|c| c.daemons.iter())
            .find(|d| d.key == key)
    }

    pub fn set_error(&mut self, conn: &str, e: Option<String>) {
        match e {
            Some(e) => self.errors.insert(conn.to_string(), e),
            None => self.errors.remove(conn),
        };
    }

    /// The reads this refresh needs: every daemon whose tunnel is up, with
    /// its stored token (none stored → no read, shown as token required).
    pub fn fetch_jobs(
        remotes: &Remotes,
        tunnels: &[TunnelStatus],
        tokens: &dyn TokenStore,
    ) -> Vec<FetchJob> {
        let mut jobs = Vec::new();
        for c in &remotes.connections {
            for d in &c.daemons {
                let r = route(c, d, tunnels);
                let Some(url) = r.base() else {
                    continue;
                };
                let token = tokens.get(&r.token_key).ok().flatten();
                if (d.token_required || r.hub_path.is_some()) && token.is_none() {
                    continue;
                }
                jobs.push(FetchJob {
                    key: token_key(&c.id, &d.workflow),
                    url,
                    token,
                });
            }
        }
        jobs
    }

    /// The Sessions window's reads (#178): every daemon `fetch_jobs` reads,
    /// named as the menu names it, with its connection's name.
    pub fn session_jobs(
        &self,
        remotes: &Remotes,
        tunnels: &[TunnelStatus],
        tokens: &dyn TokenStore,
    ) -> Vec<SessionsJob> {
        Self::fetch_jobs(remotes, tunnels, tokens)
            .into_iter()
            .map(|j| {
                let conn = remotes
                    .daemon(&j.key)
                    .map(|(c, d)| (c.name.clone(), d.name()));
                let name = self
                    .daemon(&j.key)
                    .map(|d| d.name.clone())
                    .or_else(|| conn.as_ref().map(|(_, n)| n.clone()))
                    .unwrap_or_else(|| j.key.clone());
                SessionsJob {
                    name,
                    remote: Some(conn.map(|(c, _)| c).unwrap_or_default()),
                    key: j.key,
                    url: j.url,
                    token: j.token,
                }
            })
            .collect()
    }

    /// The dashboard target for a remote daemon, while its tunnel is up.
    pub fn dashboard_target(
        &self,
        key: &str,
        tokens: &dyn TokenStore,
        deep_link: Option<&str>,
    ) -> Option<DashboardTarget> {
        let d = self.daemon(key)?;
        let base = d.url()?;
        let origin = origin_of(&base)?;
        // A member opened through the host's hub gets the hub token.
        let conn_id = key
            .strip_prefix(crate::remote::REMOTE_KEY_PREFIX)
            .and_then(|k| k.split_once('/'))
            .map(|(id, _)| id);
        let token_key = match (&d.hub_path, conn_id) {
            (Some(_), Some(id)) => hub_key(id),
            _ => key.to_string(),
        };
        Some(DashboardTarget {
            url: deep_link
                .map(|u| rebase_url(u, &base))
                .unwrap_or_else(|| base.clone()),
            origin,
            token: tokens.get(&token_key).ok().flatten(),
        })
    }

    /// Applies one refresh; returns the alerts to raise.
    pub fn apply(
        &mut self,
        remotes: &Remotes,
        tunnels: &[TunnelStatus],
        tokens: &dyn TokenStore,
        states: &HashMap<String, Result<StateSummary, FetchError>>,
    ) -> Vec<Alert> {
        let mut alerts = Vec::new();
        let mut views = Vec::new();
        for c in &remotes.connections {
            let mut daemons = Vec::new();
            for d in &c.daemons {
                let key = token_key(&c.id, &d.workflow);
                let r = route(c, d, tunnels);
                let base = r.base();
                let (tunnel, hub_path) = (r.tunnel, r.hub_path);
                let has_token = tokens.get(&key).ok().flatten().is_some();
                let up = base.is_some();
                let health = match states.get(&key) {
                    _ if !up => Health::Stopped,
                    Some(Ok(s)) => Health::Ok(s.clone()),
                    Some(Err(FetchError::Unauthorized)) | None => Health::TokenRequired,
                    Some(Err(e)) => Health::Unreachable(e.to_string()),
                };
                let name = match &health {
                    Health::Ok(s) if !s.project_name.trim().is_empty() => {
                        s.project_name.trim().to_string()
                    }
                    _ => d.name(),
                };
                if let (Health::Ok(s), Some(base)) = (&health, &base) {
                    let label = format!("{name} ({})", c.name);
                    alerts.extend(self.detector.observe(&key, &label, base, s));
                }
                daemons.push(RemoteDaemonView {
                    key,
                    workflow: d.workflow.clone(),
                    name,
                    tunnel,
                    health,
                    has_token,
                    hub_path,
                });
            }
            views.push(ConnectionView {
                id: c.id.clone(),
                name: c.name.clone(),
                transport: c.transport.label(),
                daemons,
                error: self.errors.get(&c.id).cloned(),
                hub: hub_line(c, tunnels),
            });
        }
        self.views = views;
        alerts
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::remote::{Connection, RemoteDaemon, Transport};
    use crate::token::MemoryTokenStore;
    use std::time::Duration;

    /// #232 — a remote machine with a hub is reached through ONE tunnel to
    /// that hub: `itervox hub --json` on the host names the members, the
    /// tunnel list holds the hub's tunnel (probed on the hub's health
    /// route) and a tunnel only for the daemon the hub does not cover, every
    /// member is read through the hub's proxy with the hub token, its
    /// alerts and dashboard open on the hub, and when the hub stops each
    /// member gets its own tunnel again.
    #[test]
    fn remote_hub_reached_through_one_tunnel() {
        use crate::client::{fetch_all, ClientPool};
        use crate::hub_source::tests::{fake_daemon, FakeServer};
        use crate::remote::HubListing;
        use crate::remote_ops::{tunnel_specs, tunnel_specs_with};
        use crate::tunnel::{TunnelPhase, HUB_HEALTH_PATH};

        let daemon = |name: &str, port: u16, token_required: bool| RemoteDaemon {
            workflow: format!("/srv/{name}/WORKFLOW.md"),
            remote_host: "127.0.0.1".into(),
            remote_port: port,
            token_required,
            token_source: "generated".into(),
            token_file: format!("/srv/{name}/.itervox/api-token"),
            local_port: 0,
        };
        let mut c = Connection::new(
            "prod".into(),
            "Prod".into(),
            Transport::Ssh {
                destination: "me@box".into(),
            },
        );
        c.daemons = vec![
            daemon("alpha", 8090, true),
            daemon("beta", 8091, true),
            daemon("gamma", 8092, true),
            daemon("solo", 8093, true),
        ];
        // `itervox hub --json` on the host: solo runs with --no-hub.
        let listing: HubListing = serde_json::from_str(
            r#"{"hub": {"schema_version": 1, "pid": 4, "url": "http://127.0.0.1:8089/hub/",
                 "host": "127.0.0.1", "port": 8089, "version": "v0.2.9",
                 "workflow": "/srv/alpha/WORKFLOW.md", "started_at": ""},
               "members": [
                 {"name": "alpha", "workflow": "/srv/alpha/WORKFLOW.md", "url": "", "pid": 1, "version": ""},
                 {"name": "beta", "workflow": "/srv/beta/WORKFLOW.md", "url": "", "pid": 2, "version": ""},
                 {"name": "gamma", "workflow": "/srv/gamma/WORKFLOW.md", "url": "", "pid": 3, "version": ""}]}"#,
        )
        .unwrap();
        c.hub = listing.hub_for(&c.daemons);
        let remotes = Remotes {
            connections: vec![c],
        };

        // One tunnel covers the three members; solo keeps its own.
        let specs = tunnel_specs(&remotes);
        let ids: Vec<&str> = specs.iter().map(|s| s.id.as_str()).collect();
        assert_eq!(
            ids,
            ["remote/prod/hub", "remote/prod//srv/solo/WORKFLOW.md"]
        );
        assert_eq!(
            (specs[0].remote_port, specs[0].health_path.as_str()),
            (8089, HUB_HEALTH_PATH)
        );

        // The hub (the hub tunnel's local end) proxies each member's API.
        let hub = FakeServer::start(|path| {
            let name = path
                .trim_start_matches("/hub/p/")
                .split('/')
                .next()
                .unwrap();
            if path.ends_with("/api/v1/summary") {
                return (404, String::new());
            }
            let input = if name == "alpha" {
                r#","inputRequired":[{"identifier":"ENG-1","state":"input_required","queuedAt":"2026-10-22T09:00:00Z"}]"#
            } else {
                ""
            };
            let running = name.len();
            (
                200,
                format!(r#"{{"projectName":"{name}","counts":{{"running":{running}}}{input}}}"#),
            )
        });
        let solo = fake_daemon(1);
        let port = |origin: &str| -> u16 { origin.rsplit(':').next().unwrap().parse().unwrap() };
        let mut tunnels = vec![
            up("remote/prod/hub", port(&hub.origin)),
            up("remote/prod//srv/solo/WORKFLOW.md", port(&solo.origin)),
        ];
        let tokens = MemoryTokenStore::default();
        let hub_token = ["hub", "operator", "fixture"].join("-");
        tokens.set("remote/prod/hub", &hub_token).unwrap();
        for n in ["alpha", "beta", "gamma", "solo"] {
            tokens
                .set(
                    &format!("remote/prod//srv/{n}/WORKFLOW.md"),
                    &format!("{n}-fixture"),
                )
                .unwrap();
        }

        let jobs = RemoteCore::fetch_jobs(&remotes, &tunnels, &tokens);
        let hub_base = format!("{}/", hub.origin);
        let urls: Vec<(String, Option<String>)> = jobs
            .iter()
            .map(|j| (j.url.clone(), j.token.clone()))
            .collect();
        assert_eq!(
            urls,
            [
                (format!("{hub_base}hub/p/alpha/"), Some(hub_token.clone())),
                (format!("{hub_base}hub/p/beta/"), Some(hub_token.clone())),
                (format!("{hub_base}hub/p/gamma/"), Some(hub_token.clone())),
                (format!("{}/", solo.origin), Some("solo-fixture".into())),
            ]
        );
        let pool = ClientPool::default();
        let states = fetch_all(&pool, &jobs);
        let mut core = RemoteCore::new(0);
        let alerts = core.apply(&remotes, &tunnels, &tokens, &states);
        let view = &core.views()[0];
        assert_eq!(
            view.hub.as_deref(),
            Some("hub · 3 projects through one tunnel")
        );
        assert_eq!(view.status_line(), "4/4 connected · 1 needs input");
        let running: Vec<(String, usize)> = view
            .daemons
            .iter()
            .map(|d| match &d.health {
                Health::Ok(s) => (d.name.clone(), s.running),
                other => panic!("{}: {other:?}", d.key),
            })
            .collect();
        assert_eq!(
            running,
            [
                ("alpha".to_string(), 5),
                ("beta".to_string(), 4),
                ("gamma".to_string(), 5),
                ("solo".to_string(), 1)
            ]
        );
        assert!(view.daemons[0].status_line().contains("via hub"));
        // Every member read went to the hub, with the hub token.
        let seen = hub.seen.lock().unwrap().clone();
        assert_eq!(seen.len(), 6, "summary + state per member: {seen:?}");
        assert!(seen
            .iter()
            .all(|(_, a)| *a == format!("Bearer {hub_token}")));
        // The alert and the dashboard open the member on the hub.
        assert_eq!(alerts.len(), 1);
        assert_eq!(alerts[0].url, format!("{hub_base}hub/p/alpha/?issue=ENG-1"));
        let key = "remote/prod//srv/alpha/WORKFLOW.md";
        let t = core
            .dashboard_target(key, &tokens, Some(&alerts[0].url))
            .unwrap();
        assert_eq!(t.url, format!("{hub_base}hub/p/alpha/?issue=ENG-1"));
        assert_eq!(t.origin, hub.origin);
        assert_eq!(t.token.as_deref(), Some(hub_token.as_str()));

        // The hub stops answering: each member gets its own tunnel again,
        // read with its own token; the deep link moves onto that tunnel.
        tunnels[0].phase = TunnelPhase::Retrying {
            attempt: 1,
            retry_in: Duration::from_secs(2),
        };
        tunnels[0].last_error = Some("health check answered HTTP 502".into());
        let down = hubs_down(&remotes, &tunnels);
        assert_eq!(down.iter().collect::<Vec<_>>(), ["prod"]);
        assert_eq!(tunnel_specs_with(&remotes, &down).len(), 5);
        let alpha = fake_daemon(9);
        tunnels.push(up(key, port(&alpha.origin)));
        let jobs = RemoteCore::fetch_jobs(&remotes, &tunnels, &tokens);
        assert_eq!(
            jobs.iter()
                .map(|j| (j.key.as_str(), j.token.as_deref()))
                .collect::<Vec<_>>(),
            [
                (key, Some("alpha-fixture")),
                ("remote/prod//srv/solo/WORKFLOW.md", Some("solo-fixture"))
            ]
        );
        core.apply(&remotes, &tunnels, &tokens, &fetch_all(&pool, &jobs));
        let view = &core.views()[0];
        assert_eq!(
            view.hub.as_deref(),
            Some("hub not answering — 3 projects on their own tunnels")
        );
        let t = core
            .dashboard_target(key, &tokens, Some(&alerts[0].url))
            .unwrap();
        assert_eq!(t.url, format!("{}/?issue=ENG-1", alpha.origin));
        assert_eq!(t.token.as_deref(), Some("alpha-fixture"));
    }

    fn remotes() -> Remotes {
        let mk = |id: &str| {
            let mut c = Connection::new(
                id.into(),
                id.to_uppercase(),
                Transport::Ssh {
                    destination: format!("{id}.example"),
                },
            );
            c.daemons.push(RemoteDaemon {
                workflow: "/srv/app/WORKFLOW.md".into(),
                remote_host: "127.0.0.1".into(),
                remote_port: 8090,
                token_required: true,
                token_source: "generated".into(),
                token_file: "/x/api-token".into(),
                local_port: 0,
            });
            c
        };
        Remotes {
            connections: vec![mk("prod"), mk("staging")],
        }
    }

    fn up(id: &str, port: u16) -> TunnelStatus {
        TunnelStatus {
            id: id.into(),
            local_port: port,
            phase: TunnelPhase::Up,
            pid: Some(1),
            starts: 1,
            last_error: None,
        }
    }

    #[test]
    fn jobs_go_through_each_tunnel_with_that_daemons_token() {
        let r = remotes();
        let kp = token_key("prod", "/srv/app/WORKFLOW.md");
        let ks = token_key("staging", "/srv/app/WORKFLOW.md");
        let tokens = MemoryTokenStore::default();
        tokens.set(&kp, "prod-token").unwrap();
        let tunnels = vec![up(&kp, 50001), up(&ks, 50002)];
        let jobs = RemoteCore::fetch_jobs(&r, &tunnels, &tokens);
        assert_eq!(jobs.len(), 1, "no token for staging: no read");
        assert_eq!(jobs[0].url, "http://127.0.0.1:50001/");
        assert_eq!(jobs[0].token.as_deref(), Some("prod-token"));

        let mut core = RemoteCore::new(0);
        let mut states = HashMap::new();
        states.insert(
            kp.clone(),
            Ok(StateSummary {
                project_name: "app".into(),
                running: 1,
                ..Default::default()
            }),
        );
        core.apply(&r, &tunnels, &tokens, &states);
        let v = &core.views()[0];
        assert_eq!(v.status_line(), "1/1 connected");
        assert_eq!(v.daemons[0].status_line(), "1 agent · localhost:50001");
        assert_eq!(
            core.daemon(&ks).unwrap().status_line(),
            "token required · localhost:50002"
        );
        let t = core.dashboard_target(&kp, &tokens, None).unwrap();
        assert_eq!(t.url, "http://127.0.0.1:50001/");
        assert_eq!(t.origin, "http://127.0.0.1:50001");
        assert_eq!(t.token.as_deref(), Some("prod-token"));
        let t = core
            .dashboard_target(&kp, &tokens, Some("http://127.0.0.1:49999/?issue=ENG-1"))
            .unwrap();
        assert_eq!(t.url, "http://127.0.0.1:50001/?issue=ENG-1");
    }

    /// #178: the Sessions window reads a remote daemon through its tunnel
    /// with that daemon's token, named after its project and connection.
    #[test]
    fn session_jobs_read_remote_daemons_through_their_tunnels() {
        let r = remotes();
        let kp = token_key("prod", "/srv/app/WORKFLOW.md");
        let tokens = MemoryTokenStore::default();
        tokens.set(&kp, "prod-token").unwrap();
        let tunnels = vec![up(&kp, 50001)];
        let mut core = RemoteCore::new(0);
        let mut states = HashMap::new();
        states.insert(
            kp.clone(),
            Ok(StateSummary {
                project_name: "app".into(),
                ..Default::default()
            }),
        );
        core.apply(&r, &tunnels, &tokens, &states);
        let jobs = core.session_jobs(&r, &tunnels, &tokens);
        assert_eq!(
            jobs,
            vec![SessionsJob {
                key: kp,
                url: "http://127.0.0.1:50001/".into(),
                token: Some("prod-token".into()),
                name: "app".into(),
                remote: Some("PROD".into()),
            }]
        );
    }

    #[test]
    fn a_down_tunnel_shows_its_retry_and_has_no_dashboard() {
        let r = remotes();
        let kp = token_key("prod", "/srv/app/WORKFLOW.md");
        let tokens = MemoryTokenStore::default();
        let mut t = up(&kp, 50001);
        t.phase = TunnelPhase::Retrying {
            attempt: 2,
            retry_in: Duration::from_secs(2),
        };
        let mut core = RemoteCore::new(0);
        core.apply(&r, &[t], &tokens, &HashMap::new());
        assert_eq!(
            core.views()[0].status_line(),
            "reconnecting… (0/1 connected)"
        );
        assert!(core.dashboard_target(&kp, &tokens, None).is_none());
        assert_eq!(core.views()[1].daemons[0].status_line(), "not connected");
    }

    #[test]
    fn rebase_keeps_path_and_query() {
        assert_eq!(
            rebase_url("http://127.0.0.1:1/?issue=A", "http://127.0.0.1:2/"),
            "http://127.0.0.1:2/?issue=A"
        );
        assert_eq!(
            rebase_url("http://127.0.0.1:1", "http://127.0.0.1:2"),
            "http://127.0.0.1:2/"
        );
    }
}
