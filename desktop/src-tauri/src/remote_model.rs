//! What the menu shows for the remote connections (#156), and how the
//! refresh loop reads remote daemons: through their tunnels, with the token
//! stored for the connection and daemon.

use std::collections::HashMap;

use crate::client::FetchError;
use crate::daemon_state::StateSummary;
use crate::dashboard::origin_of;
use crate::events::{Alert, EventDetector};
use crate::model::{FetchJob, Health};
use crate::remote::{token_key, Remotes};
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
}

impl RemoteDaemonView {
    pub fn needs_input(&self) -> usize {
        match &self.health {
            Health::Ok(s) => s.waiting_for_input().count(),
            _ => 0,
        }
    }

    /// The dashboard URL through the tunnel, while it is up.
    pub fn url(&self) -> Option<String> {
        self.tunnel
            .as_ref()
            .filter(|t| t.is_up())
            .map(TunnelStatus::base_url)
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
}

impl ConnectionView {
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
/// while the tunnel used another local port still opens the issue.
pub fn rebase_url(url: &str, base: &str) -> String {
    let rest = url
        .split_once("://")
        .and_then(|(_, r)| r.find('/').map(|i| &r[i + 1..]))
        .unwrap_or("");
    let mut b = base.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}{rest}")
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
                let key = token_key(&c.id, &d.workflow);
                let Some(t) = tunnels.iter().find(|t| t.id == key && t.is_up()) else {
                    continue;
                };
                let token = tokens.get(&key).ok().flatten();
                if d.token_required && token.is_none() {
                    continue;
                }
                jobs.push(FetchJob {
                    key,
                    url: t.base_url(),
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
        let base = self.daemon(key)?.url()?;
        let origin = origin_of(&base)?;
        Some(DashboardTarget {
            url: deep_link
                .map(|u| rebase_url(u, &base))
                .unwrap_or_else(|| base.clone()),
            origin,
            token: tokens.get(key).ok().flatten(),
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
                let tunnel = tunnels.iter().find(|t| t.id == key).cloned();
                let has_token = tokens.get(&key).ok().flatten().is_some();
                let up = tunnel.as_ref().is_some_and(TunnelStatus::is_up);
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
                if let (Health::Ok(s), Some(t)) = (&health, &tunnel) {
                    let label = format!("{name} ({})", c.name);
                    alerts.extend(self.detector.observe(&key, &label, &t.base_url(), s));
                }
                daemons.push(RemoteDaemonView {
                    key,
                    workflow: d.workflow.clone(),
                    name,
                    tunnel,
                    health,
                    has_token,
                });
            }
            views.push(ConnectionView {
                id: c.id.clone(),
                name: c.name.clone(),
                transport: c.transport.label(),
                daemons,
                error: self.errors.get(&c.id).cloned(),
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
