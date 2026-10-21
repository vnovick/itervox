//! Tauri-side glue for the remote connections (#156): the tunnel thread,
//! the remote half of the refresh, and the menu flows. The decisions live in
//! `remote`, `remote_ops`, `remote_model`, `tunnel` and `sysevents`.

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, MutexGuard, OnceLock, PoisonError};
use std::time::{Duration, Instant};

use crate::client;
use crate::events::Alert;
use crate::prompt::{ask_choice, ask_text};
use crate::remote::{token_key, IapMode, Remotes, Transport, REMOTES_FILE};
use crate::remote_model::{ConnectionView, DashboardTarget, RemoteCore};
use crate::remote_ops::{
    add_connection, forget_tokens, obtain_token, rediscover, tunnel_specs, AddRequest,
};
use crate::shellpath::{resolve_tools_for_app, Tools};
use crate::sysevents::{PollingSystemEvents, SystemEventSource};
use crate::token::{CachedTokenStore, KeyringTokenStore, MemoryTokenStore, TokenStore};
use crate::tunnel::{health_probe, TunnelManager, TunnelStatus, TunnelTiming};

/// Keychain service for remote daemons' tokens; the account is
/// [`token_key`] (connection id + remote WORKFLOW.md).
pub const REMOTE_KEYCHAIN_SERVICE: &str = "dev.itervox.desktop.remote-token";

/// How often the tunnel thread reaps, probes and polls for wake / network
/// changes.
pub const TUNNEL_TICK: Duration = Duration::from_secs(2);

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// Messages to the tunnel thread.
pub enum TunnelCmd {
    /// The connection list changed.
    Sync,
    /// Restart these tunnels now.
    Restart(Vec<String>),
    Quit,
}

pub struct RemoteShared {
    pub remotes: Mutex<Remotes>,
    remotes_file: PathBuf,
    core: Mutex<RemoteCore>,
    pub tokens: Box<dyn TokenStore>,
    tx: Mutex<Sender<TunnelCmd>>,
    statuses: Mutex<Vec<TunnelStatus>>,
    tools: OnceLock<Tools>,
    stopped: AtomicBool,
}

impl RemoteShared {
    /// Loads `remotes.json`; an unreadable file is moved aside and reported.
    pub fn load(config_dir: &Path, since: i64) -> (Arc<Self>, Receiver<TunnelCmd>, Option<String>) {
        let file = config_dir.join(REMOTES_FILE);
        let (remotes, err) = match Remotes::load(&file) {
            Ok(r) => (r, None),
            Err(e) => {
                let backup = file.with_extension("json.unreadable");
                let _ = std::fs::rename(&file, &backup);
                (
                    Remotes::default(),
                    Some(format!(
                        "The saved remote list could not be read ({e}). It was moved to {}.",
                        backup.display()
                    )),
                )
            }
        };
        let tokens: Box<dyn TokenStore> = if cfg!(target_os = "macos") {
            Box::new(CachedTokenStore::new(KeyringTokenStore::new(
                REMOTE_KEYCHAIN_SERVICE,
            )))
        } else {
            Box::new(MemoryTokenStore::default())
        };
        let (tx, rx) = mpsc::channel();
        let s = Arc::new(RemoteShared {
            remotes: Mutex::new(remotes),
            remotes_file: file,
            core: Mutex::new(RemoteCore::new(since)),
            tokens,
            tx: Mutex::new(tx),
            statuses: Mutex::default(),
            tools: OnceLock::new(),
            stopped: AtomicBool::new(false),
        });
        (s, rx, err)
    }

    /// `ssh` / `gcloud` and their PATH, resolved once (the login shell can
    /// take a few seconds, so never on the main thread).
    pub fn tools(&self) -> &Tools {
        self.tools.get_or_init(resolve_tools_for_app)
    }

    fn send(&self, c: TunnelCmd) {
        let _ = lock(&self.tx).send(c);
    }

    pub fn save(&self) -> Result<(), String> {
        let r = lock(&self.remotes).clone();
        r.save(&self.remotes_file)
    }

    /// Starts the tunnel thread. `on_change` runs (on that thread) whenever
    /// a tunnel's status changed.
    pub fn spawn_tunnel_thread(
        self: &Arc<Self>,
        log_dir: PathBuf,
        rx: Receiver<TunnelCmd>,
        on_change: impl Fn() + Send + 'static,
    ) -> std::io::Result<()> {
        let me = Arc::clone(self);
        std::thread::Builder::new()
            .name("itervox-tunnels".into())
            .spawn(move || {
                let tools = me.tools().clone();
                let mut mgr = TunnelManager::new(tools, log_dir, TunnelTiming::default());
                let mut events = PollingSystemEvents::new();
                me.sync(&mut mgr);
                loop {
                    match rx.recv_timeout(TUNNEL_TICK) {
                        Ok(TunnelCmd::Sync) => me.sync(&mut mgr),
                        Ok(TunnelCmd::Restart(ids)) => {
                            for id in ids {
                                mgr.restart(&id, Instant::now());
                            }
                        }
                        Ok(TunnelCmd::Quit) | Err(RecvTimeoutError::Disconnected) => {
                            mgr.shutdown();
                            me.stopped.store(true, Ordering::SeqCst);
                            return;
                        }
                        Err(RecvTimeoutError::Timeout) => {}
                    }
                    let now = Instant::now();
                    for ev in events.poll() {
                        if std::env::var_os("ITERVOX_DESKTOP_DEBUG").is_some_and(|v| v == "1") {
                            eprintln!("itervox desktop: {ev:?}: re-probing every tunnel");
                        }
                        mgr.on_event(ev, now);
                    }
                    let changed = mgr.tick(now, &health_probe);
                    let statuses = mgr.statuses(now);
                    me.remember_ports(&statuses);
                    *lock(&me.statuses) = statuses;
                    if changed {
                        on_change();
                    }
                }
            })
            .map(|_| ())
    }

    fn sync(&self, mgr: &mut TunnelManager) {
        let specs = tunnel_specs(&lock(&self.remotes));
        let ids: HashSet<String> = specs.iter().map(|s| s.id.clone()).collect();
        mgr.retain(&ids);
        let now = Instant::now();
        for s in specs {
            mgr.ensure(s, now);
        }
    }

    /// Keeps each daemon's local port in remotes.json, so the next start of
    /// the app tries the same port first.
    fn remember_ports(&self, statuses: &[TunnelStatus]) {
        let mut changed = false;
        {
            let mut r = lock(&self.remotes);
            for s in statuses.iter().filter(|s| s.local_port != 0) {
                changed |= r.set_local_port(&s.id, s.local_port);
            }
        }
        if changed {
            let _ = self.save();
        }
    }

    /// Stops every tunnel (app quit), waiting up to 2 s.
    pub fn shutdown(&self) {
        self.send(TunnelCmd::Quit);
        let deadline = Instant::now() + Duration::from_secs(2);
        while !self.stopped.load(Ordering::SeqCst) && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    /// The remote half of a refresh: reads every connected daemon through its
    /// tunnel. Returns the alerts and whether the menu needs rebuilding.
    pub fn refresh(&self) -> (Vec<Alert>, bool) {
        let remotes = lock(&self.remotes).clone();
        let tunnels = lock(&self.statuses).clone();
        let jobs = RemoteCore::fetch_jobs(&remotes, &tunnels, self.tokens.as_ref());
        let states: HashMap<_, _> = std::thread::scope(|s| {
            let hs: Vec<_> = jobs
                .iter()
                .map(|j| {
                    s.spawn(move || {
                        (
                            j.key.clone(),
                            client::fetch_state(&j.url, j.token.as_deref()),
                        )
                    })
                })
                .collect();
            hs.into_iter().filter_map(|h| h.join().ok()).collect()
        });
        let mut core = lock(&self.core);
        let before = core.views().to_vec();
        let alerts = core.apply(&remotes, &tunnels, self.tokens.as_ref(), &states);
        let changed = before != core.views();
        (alerts, changed)
    }

    pub fn views(&self) -> Vec<ConnectionView> {
        lock(&self.core).views().to_vec()
    }

    /// Where a remote daemon's dashboard opens, and its window title.
    pub fn dashboard(
        &self,
        key: &str,
        deep_link: Option<&str>,
    ) -> Result<(DashboardTarget, String), String> {
        let core = lock(&self.core);
        let title = core
            .daemon(key)
            .map(|d| d.name.clone())
            .unwrap_or_else(|| "remote".into());
        let conn = lock(&self.remotes)
            .daemon(key)
            .map(|(c, _)| c.name.clone())
            .unwrap_or_default();
        core.dashboard_target(key, self.tokens.as_ref(), deep_link)
            .map(|t| (t, format!("{title} ({conn}) — Itervox")))
            .ok_or_else(|| "That remote daemon is not connected yet.".into())
    }

    fn after_change(&self, conn: &str, err: Option<String>) {
        lock(&self.core).set_error(conn, err);
        self.send(TunnelCmd::Sync);
    }

    /// Menu: Add Remote…. `Ok(None)` when the user cancelled.
    pub fn add_flow(&self) -> Result<Option<String>, String> {
        let kinds = [
            "SSH — user@host or a ~/.ssh/config alias".to_string(),
            "GCP IAP — SSH through IAP (the deploy kit's setup)".to_string(),
            "GCP IAP — direct port tunnel (start-iap-tunnel)".to_string(),
        ];
        let Some(kind) = ask_choice("How does this Mac reach the host?", &kinds, false, false)?
        else {
            return Ok(None);
        };
        let transport = match kind.first() {
            Some(0) => {
                let Some(destination) = ask_text(
                    "SSH destination (user@host or a ~/.ssh/config alias). Your SSH key or agent must log in without a prompt.",
                    "",
                    false,
                )?
                else {
                    return Ok(None);
                };
                Transport::Ssh { destination }
            }
            Some(i) => {
                let Some(instance) = ask_text("VM instance name", "itervox", false)? else {
                    return Ok(None);
                };
                let Some(zone) = ask_text("Zone (empty: gcloud's default)", "", false)? else {
                    return Ok(None);
                };
                let Some(project) = ask_text("Project (empty: gcloud's default)", "", false)?
                else {
                    return Ok(None);
                };
                Transport::Iap {
                    instance,
                    zone,
                    project,
                    mode: if *i == 1 { IapMode::Ssh } else { IapMode::Port },
                }
            }
            None => return Ok(None),
        };
        transport.validate()?;
        let Some(name) = ask_text("A name for this remote", &transport.label(), false)? else {
            return Ok(None);
        };
        let Some(cmd) = ask_text(
            "The command that runs itervox on the host",
            "itervox",
            false,
        )?
        else {
            return Ok(None);
        };
        let host = transport.label();
        let snapshot = lock(&self.remotes).clone();
        let mut choose = |recs: &[crate::status::DaemonRecord]| {
            let items: Vec<String> = recs
                .iter()
                .map(|r| format!("{} (port {})", r.project_dir, r.port))
                .collect();
            ask_choice(
                &format!("Daemons on {host}: choose the ones to add"),
                &items,
                true,
                true,
            )
            .ok()
            .flatten()
        };
        let mut ask = |d: &crate::remote::RemoteDaemon, why: Option<&str>| {
            let why = why.map(|w| format!(" ({w})")).unwrap_or_default();
            ask_text(
                &format!(
                    "API token for {} on {host}{why}. It is stored in your Keychain.",
                    d.name()
                ),
                "",
                true,
            )
            .ok()
            .flatten()
            .filter(|t| !t.is_empty())
        };
        let added = add_connection(
            &snapshot,
            AddRequest {
                name,
                transport,
                itervox_command: Some(cmd),
            },
            self.tools(),
            self.tokens.as_ref(),
            &mut choose,
            &mut ask,
        )?;
        let id = added.connection.id.clone();
        let n = added.connection.daemons.len();
        lock(&self.remotes).upsert(added.connection);
        let saved = self.save();
        let problems = added
            .token_problems
            .iter()
            .map(|(_, e)| e.clone())
            .collect::<Vec<_>>();
        self.after_change(&id, problems.first().cloned());
        saved?;
        Ok(Some(format!(
            "Added {n} daemon{} on {host}.",
            if n == 1 { "" } else { "s" }
        )))
    }

    /// Menu: Enter / Update Token….
    pub fn token_flow(&self, key: &str) -> Result<(), String> {
        let found = lock(&self.remotes)
            .daemon(key)
            .map(|(c, d)| (c.clone(), d.clone()));
        let Some((conn, mut daemon)) = found else {
            return Ok(());
        };
        // A token typed for a daemon whose API is open still helps if it
        // later gets one; ask whenever there is no file to read.
        daemon.token_required = true;
        let mut ask = |d: &crate::remote::RemoteDaemon, why: Option<&str>| {
            let why = why.map(|w| format!(" ({w})")).unwrap_or_default();
            ask_text(
                &format!("API token for {} on {}{why}", d.name(), conn.name),
                "",
                true,
            )
            .ok()
            .flatten()
            .filter(|t| !t.is_empty())
        };
        let res = obtain_token(&conn, &daemon, self.tools(), self.tokens.as_ref(), &mut ask);
        self.after_change(&conn.id, res.as_ref().err().cloned());
        res.map(|_| ())
    }

    /// Menu: Refresh Daemon List.
    pub fn rediscover_flow(&self, conn_id: &str) -> Result<(), String> {
        let Some(mut conn) = lock(&self.remotes).get(conn_id).cloned() else {
            return Ok(());
        };
        let mut ask = |d: &crate::remote::RemoteDaemon, _: Option<&str>| {
            ask_text(&format!("API token for {}", d.name()), "", true)
                .ok()
                .flatten()
                .filter(|t| !t.is_empty())
        };
        let res = rediscover(&mut conn, self.tools(), self.tokens.as_ref(), &mut ask);
        match res {
            Ok(problems) => {
                lock(&self.remotes).upsert(conn);
                self.after_change(conn_id, problems.first().map(|(_, e)| e.clone()));
                self.save()
            }
            Err(e) => {
                self.after_change(conn_id, Some(e.clone()));
                Err(e)
            }
        }
    }

    /// Menu: Reconnect.
    pub fn reconnect(&self, conn_id: &str) {
        let ids: Vec<String> = lock(&self.remotes)
            .get(conn_id)
            .map(|c| {
                c.daemons
                    .iter()
                    .map(|d| token_key(&c.id, &d.workflow))
                    .collect()
            })
            .unwrap_or_default();
        self.send(TunnelCmd::Restart(ids));
    }

    /// Menu: Remove Remote.
    pub fn remove(&self, conn_id: &str) -> Result<(), String> {
        let removed = lock(&self.remotes).remove(conn_id);
        if let Some(c) = removed {
            forget_tokens(&c, self.tokens.as_ref());
        }
        self.after_change(conn_id, None);
        self.save()
    }
}
