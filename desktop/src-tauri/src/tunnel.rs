//! The tunnels to remote daemons (#156): one process per daemon (ssh or
//! gcloud), each on its own local port, health-checked and restarted.
//!
//! [`TunnelManager`] is owned by one thread (the app's tunnel thread, or a
//! test) and driven by [`TunnelManager::tick`]:
//!
//! - a tunnel whose process exited is restarted after a backoff (1 s,
//!   doubling to 30 s), on the same local port when it is still free, so an
//!   open dashboard window keeps its address;
//! - a running tunnel is probed every tick — a TCP connect to its local port
//!   and `GET /api/v1/health` through it — and restarted after
//!   [`TunnelTiming::max_health_failures`] failed probes in a row (after a
//!   start-up grace period, since an IAP handshake takes seconds);
//! - after a [`SystemEvent`] (wake from sleep, network change) every tunnel
//!   is probed at once, one that fails is restarted at once instead of
//!   waiting out its failures, and one waiting in backoff retries at once.
//!
//! Local ports come from a [`PortAllocator`] shared by every tunnel, so two
//! tunnels never get the same port even while one of them is restarting.

use std::collections::{BTreeMap, HashSet};
use std::net::{Ipv4Addr, SocketAddr, TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use crate::remote::{tunnel_argv, Transport};
use crate::shellpath::{kill_group, Tools};
use crate::sysevents::SystemEvent;

/// What a tunnel forwards.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TunnelSpec {
    /// The remote daemon's key ([`crate::remote::token_key`]).
    pub id: String,
    pub transport: Transport,
    /// Remote side of the forward (`127.0.0.1` for a loopback daemon).
    pub daemon_host: String,
    pub remote_port: u16,
    /// The local port to try first (the last one used), 0 for any.
    pub preferred_port: u16,
}

/// Where a tunnel is.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TunnelPhase {
    /// Process started, first health probe not passed yet.
    Connecting,
    /// Forwarding, and the daemon answers its health check through it.
    Up,
    /// Down; the next start is at `retry_in` from the status snapshot.
    Retrying { attempt: u32, retry_in: Duration },
}

/// A snapshot of one tunnel, for the menu and the refresh loop.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TunnelStatus {
    pub id: String,
    pub local_port: u16,
    pub phase: TunnelPhase,
    /// The tunnel process, while one runs.
    pub pid: Option<u32>,
    /// How often the process was (re)started.
    pub starts: u32,
    pub last_error: Option<String>,
}

impl TunnelStatus {
    pub fn is_up(&self) -> bool {
        self.phase == TunnelPhase::Up
    }

    /// The daemon's base URL through the tunnel.
    pub fn base_url(&self) -> String {
        format!("http://127.0.0.1:{}/", self.local_port)
    }

    /// Short text for the menu.
    pub fn phase_line(&self) -> String {
        match &self.phase {
            TunnelPhase::Up => format!("connected · localhost:{}", self.local_port),
            TunnelPhase::Connecting => "connecting…".into(),
            TunnelPhase::Retrying { attempt, retry_in } => {
                let mut s = format!("reconnecting in {}s", retry_in.as_secs().max(1));
                if *attempt > 1 {
                    s.push_str(&format!(" (attempt {attempt})"));
                }
                if let Some(e) = &self.last_error {
                    s.push_str(" · ");
                    s.push_str(e);
                }
                s
            }
        }
    }
}

/// Timing knobs; tests shorten them.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TunnelTiming {
    pub backoff_base: Duration,
    pub backoff_max: Duration,
    /// After a start, failed probes do not count for this long.
    pub startup_grace: Duration,
    /// Failed probes in a row that restart a running tunnel.
    pub max_health_failures: u32,
}

impl Default for TunnelTiming {
    fn default() -> Self {
        TunnelTiming {
            backoff_base: Duration::from_secs(1),
            backoff_max: Duration::from_secs(30),
            startup_grace: Duration::from_secs(20),
            max_health_failures: 3,
        }
    }
}

impl TunnelTiming {
    /// The wait before restart number `failures` (1-based).
    pub fn backoff(&self, failures: u32) -> Duration {
        let shift = failures.saturating_sub(1).min(16);
        self.backoff_base
            .saturating_mul(1u32 << shift)
            .min(self.backoff_max)
    }
}

/// Hands out local ports. A port stays reserved for its tunnel until the
/// tunnel is removed, also while its process is down, so no other tunnel
/// can take it.
#[derive(Debug, Default)]
pub struct PortAllocator {
    reserved: HashSet<u16>,
}

impl PortAllocator {
    /// A port for a tunnel: `preferred` when it is not reserved and
    /// `is_free` says it can be bound, else one the OS picks.
    pub fn allocate(&mut self, preferred: u16, is_free: &dyn Fn(u16) -> bool) -> Option<u16> {
        if preferred != 0 && !self.reserved.contains(&preferred) && is_free(preferred) {
            self.reserved.insert(preferred);
            return Some(preferred);
        }
        for _ in 0..64 {
            let p = os_free_port()?;
            if !self.reserved.contains(&p) {
                self.reserved.insert(p);
                return Some(p);
            }
        }
        None
    }

    pub fn release(&mut self, port: u16) {
        self.reserved.remove(&port);
    }

    pub fn is_reserved(&self, port: u16) -> bool {
        self.reserved.contains(&port)
    }
}

/// A free loopback port, chosen by the OS.
pub fn os_free_port() -> Option<u16> {
    TcpListener::bind((Ipv4Addr::LOCALHOST, 0))
        .and_then(|l| l.local_addr())
        .map(|a| a.port())
        .ok()
}

/// Whether nothing listens on loopback `port`.
pub fn port_is_free(port: u16) -> bool {
    TcpListener::bind((Ipv4Addr::LOCALHOST, port)).is_ok()
}

/// How a probe is done; the default is [`health_probe`].
pub type Probe = dyn Fn(u16) -> Result<(), String> + Sync;

/// Per-probe bound.
pub const PROBE_TIMEOUT: Duration = Duration::from_secs(3);

/// TCP connect to `127.0.0.1:port`, then `GET /api/v1/health` through it
/// (no token needed). A tunnel can accept connections while its far end is
/// gone; only the HTTP answer proves the daemon is reachable.
pub fn health_probe(port: u16) -> Result<(), String> {
    let addr = SocketAddr::from((Ipv4Addr::LOCALHOST, port));
    TcpStream::connect_timeout(&addr, PROBE_TIMEOUT).map_err(|e| format!("port closed: {e}"))?;
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(PROBE_TIMEOUT))
        .http_status_as_error(false)
        .proxy(None)
        .build()
        .into();
    let resp = agent
        .get(&format!("http://127.0.0.1:{port}/api/v1/health"))
        .call()
        .map_err(|e| format!("daemon unreachable: {e}"))?;
    match resp.status().as_u16() {
        200 => Ok(()),
        s => Err(format!("health check answered HTTP {s}")),
    }
}

struct Tunnel {
    spec: TunnelSpec,
    port: u16,
    child: Option<Child>,
    started: Option<Instant>,
    phase: TunnelPhase,
    failures: u32,
    health_failures: u32,
    next_attempt: Instant,
    starts: u32,
    last_error: Option<String>,
    /// Probe at once and restart on failure (after a system event).
    urgent: bool,
}

/// The tunnels and their processes. Dropping it stops every tunnel.
pub struct TunnelManager {
    tools: Tools,
    log_dir: PathBuf,
    timing: TunnelTiming,
    ports: PortAllocator,
    tunnels: BTreeMap<String, Tunnel>,
}

impl TunnelManager {
    /// `log_dir` receives each tunnel process's stderr.
    pub fn new(tools: Tools, log_dir: PathBuf, timing: TunnelTiming) -> Self {
        TunnelManager {
            tools,
            log_dir,
            timing,
            ports: PortAllocator::default(),
            tunnels: BTreeMap::new(),
        }
    }

    pub fn set_tools(&mut self, tools: Tools) {
        self.tools = tools;
    }

    /// Adds a tunnel, or updates one whose spec changed (restarting it).
    pub fn ensure(&mut self, spec: TunnelSpec, now: Instant) {
        if let Some(t) = self.tunnels.get_mut(&spec.id) {
            let same = t.spec.transport == spec.transport
                && t.spec.daemon_host == spec.daemon_host
                && t.spec.remote_port == spec.remote_port;
            t.spec.preferred_port = spec.preferred_port;
            if !same {
                t.spec = spec;
                stop_child(t);
                t.next_attempt = now;
                t.failures = 0;
            }
            return;
        }
        self.tunnels.insert(
            spec.id.clone(),
            Tunnel {
                spec,
                port: 0,
                child: None,
                started: None,
                phase: TunnelPhase::Retrying {
                    attempt: 0,
                    retry_in: Duration::ZERO,
                },
                failures: 0,
                health_failures: 0,
                next_attempt: now,
                starts: 0,
                last_error: None,
                urgent: false,
            },
        );
    }

    /// Keeps only the tunnels in `ids`.
    pub fn retain(&mut self, ids: &HashSet<String>) {
        let gone: Vec<String> = self
            .tunnels
            .keys()
            .filter(|k| !ids.contains(*k))
            .cloned()
            .collect();
        for id in gone {
            self.remove(&id);
        }
    }

    /// Stops a tunnel and frees its port.
    pub fn remove(&mut self, id: &str) {
        if let Some(mut t) = self.tunnels.remove(id) {
            stop_child(&mut t);
            if t.port != 0 {
                self.ports.release(t.port);
            }
        }
    }

    /// The user's Reconnect: restart now, whatever the state.
    pub fn restart(&mut self, id: &str, now: Instant) {
        if let Some(t) = self.tunnels.get_mut(id) {
            stop_child(t);
            t.failures = 0;
            t.next_attempt = now;
        }
    }

    /// Wake or network change: every tunnel is suspect.
    pub fn on_event(&mut self, _ev: SystemEvent, now: Instant) {
        for t in self.tunnels.values_mut() {
            t.urgent = true;
            if t.child.is_none() {
                t.next_attempt = now;
                t.failures = 0;
            }
        }
    }

    /// One pass: reap exited processes, probe running ones, start due ones.
    /// Returns whether any status changed.
    pub fn tick(&mut self, now: Instant, probe: &Probe) -> bool {
        let before = self.statuses(now);
        // Reap.
        for t in self.tunnels.values_mut() {
            let exited = match t.child.as_mut().map(Child::try_wait) {
                Some(Ok(Some(status))) => Some(format!("tunnel exited ({status})")),
                Some(Err(e)) => Some(e.to_string()),
                _ => None,
            };
            if let Some(why) = exited {
                t.child = None;
                let detail = last_stderr_line(&self.log_dir, &t.spec.id);
                t.last_error = Some(if detail.is_empty() { why } else { detail });
                schedule_restart(t, &self.timing, now, false);
            }
        }
        // Probe running tunnels in parallel.
        let targets: Vec<(String, u16)> = self
            .tunnels
            .iter()
            .filter(|(_, t)| t.child.is_some())
            .map(|(k, t)| (k.clone(), t.port))
            .collect();
        let results: Vec<(String, Result<(), String>)> = std::thread::scope(|s| {
            let hs: Vec<_> = targets
                .iter()
                .map(|(k, p)| s.spawn(move || (k.clone(), probe(*p))))
                .collect();
            hs.into_iter().filter_map(|h| h.join().ok()).collect()
        });
        for (id, res) in results {
            let Some(t) = self.tunnels.get_mut(&id) else {
                continue;
            };
            let urgent = std::mem::take(&mut t.urgent);
            match res {
                Ok(()) => {
                    t.phase = TunnelPhase::Up;
                    t.failures = 0;
                    t.health_failures = 0;
                    t.last_error = None;
                }
                Err(e) => {
                    let in_grace = t.phase == TunnelPhase::Connecting
                        && t.started
                            .is_some_and(|s| now.duration_since(s) < self.timing.startup_grace);
                    if in_grace && !urgent {
                        continue;
                    }
                    t.health_failures += 1;
                    t.last_error = Some(e);
                    if urgent || t.health_failures >= self.timing.max_health_failures {
                        stop_child(t);
                        schedule_restart(t, &self.timing, now, urgent);
                    }
                }
            }
        }
        // Start due tunnels.
        let ids: Vec<String> = self.tunnels.keys().cloned().collect();
        for id in ids {
            let due = self
                .tunnels
                .get(&id)
                .is_some_and(|t| t.child.is_none() && now >= t.next_attempt);
            if due {
                self.start(&id, now);
            }
        }
        before != self.statuses(now)
    }

    fn start(&mut self, id: &str, now: Instant) {
        let Some(t) = self.tunnels.get_mut(id) else {
            return;
        };
        t.urgent = false;
        // Keep the port when it is still free; otherwise trade it for a new
        // one (something else took it while the tunnel was down).
        if t.port == 0 || !port_is_free(t.port) {
            if t.port != 0 {
                self.ports.release(t.port);
            }
            let preferred = if t.port == 0 {
                t.spec.preferred_port
            } else {
                0
            };
            match self.ports.allocate(preferred, &port_is_free) {
                Some(p) => t.port = p,
                None => {
                    t.last_error = Some("no free local port".into());
                    schedule_restart(t, &self.timing, now, false);
                    return;
                }
            }
        }
        let spawned = tunnel_argv(
            &t.spec.transport,
            &t.spec.daemon_host,
            t.spec.remote_port,
            t.port,
            &self.tools,
        )
        .and_then(|(prog, args)| spawn_tunnel(&prog, &args, &self.tools, &self.log_dir, id));
        match spawned {
            Ok(child) => {
                t.child = Some(child);
                t.started = Some(now);
                t.starts += 1;
                t.health_failures = 0;
                t.phase = TunnelPhase::Connecting;
            }
            Err(e) => {
                t.last_error = Some(e);
                schedule_restart(t, &self.timing, now, false);
            }
        }
    }

    pub fn statuses(&self, now: Instant) -> Vec<TunnelStatus> {
        self.tunnels
            .values()
            .map(|t| TunnelStatus {
                id: t.spec.id.clone(),
                local_port: t.port,
                phase: match t.phase {
                    TunnelPhase::Retrying { attempt, .. } => TunnelPhase::Retrying {
                        attempt,
                        retry_in: t.next_attempt.saturating_duration_since(now),
                    },
                    ref p => p.clone(),
                },
                pid: t.child.as_ref().map(Child::id),
                starts: t.starts,
                last_error: t.last_error.clone(),
            })
            .collect()
    }

    pub fn status(&self, id: &str, now: Instant) -> Option<TunnelStatus> {
        self.statuses(now).into_iter().find(|s| s.id == id)
    }

    /// Stops every tunnel (app quit).
    pub fn shutdown(&mut self) {
        let ids: Vec<String> = self.tunnels.keys().cloned().collect();
        for id in ids {
            self.remove(&id);
        }
    }
}

impl Drop for TunnelManager {
    fn drop(&mut self) {
        self.shutdown();
    }
}

fn schedule_restart(t: &mut Tunnel, timing: &TunnelTiming, now: Instant, immediately: bool) {
    t.failures += 1;
    t.next_attempt = if immediately {
        now
    } else {
        now + timing.backoff(t.failures)
    };
    t.phase = TunnelPhase::Retrying {
        attempt: t.failures,
        retry_in: t.next_attempt.saturating_duration_since(now),
    };
}

fn stop_child(t: &mut Tunnel) {
    if let Some(mut c) = t.child.take() {
        // The whole group: gcloud runs ssh, which runs gcloud's IAP helper.
        kill_group(c.id());
        let _ = c.kill();
        let _ = c.wait();
    }
}

/// A file-name-safe log name for a tunnel id.
pub fn log_name(id: &str) -> String {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in id.bytes() {
        h ^= u64::from(b);
        h = h.wrapping_mul(0x100000001b3);
    }
    format!("tunnel-{h:016x}.log")
}

fn last_stderr_line(dir: &Path, id: &str) -> String {
    let s = std::fs::read_to_string(dir.join(log_name(id))).unwrap_or_default();
    crate::supervisor::last_line(&s)
}

fn spawn_tunnel(
    program: &Path,
    args: &[String],
    tools: &Tools,
    log_dir: &Path,
    id: &str,
) -> Result<Child, String> {
    use std::os::unix::process::CommandExt;
    std::fs::create_dir_all(log_dir).map_err(|e| format!("{}: {e}", log_dir.display()))?;
    let log = std::fs::File::create(log_dir.join(log_name(id))).map_err(|e| e.to_string())?;
    Command::new(program)
        .args(args)
        .env("PATH", &tools.path_env)
        .stdin(Stdio::null())
        .stdout(log.try_clone().map_err(|e| e.to_string())?)
        .stderr(log)
        .process_group(0)
        .spawn()
        .map_err(|e| format!("could not run {}: {e}", program.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_doubles_up_to_the_cap() {
        let t = TunnelTiming::default();
        assert_eq!(t.backoff(1), Duration::from_secs(1));
        assert_eq!(t.backoff(2), Duration::from_secs(2));
        assert_eq!(t.backoff(4), Duration::from_secs(8));
        assert_eq!(t.backoff(30), Duration::from_secs(30));
    }

    /// Two tunnels never share a local port, even when both ask for the same
    /// one and one of them is down at the time.
    #[test]
    fn allocator_never_hands_out_a_reserved_port() {
        let mut a = PortAllocator::default();
        let always_free = |_: u16| true;
        let p1 = a.allocate(50555, &always_free).unwrap();
        assert_eq!(p1, 50555);
        let p2 = a.allocate(50555, &always_free).unwrap();
        assert_ne!(p2, p1);
        assert!(a.is_reserved(p1) && a.is_reserved(p2));
        a.release(p1);
        assert_eq!(a.allocate(50555, &always_free), Some(50555));
        let never_free = |_: u16| false;
        let p3 = a.allocate(40000, &never_free).unwrap();
        assert_ne!(p3, 40000);
    }

    #[test]
    fn phase_lines_read_well() {
        let mut s = TunnelStatus {
            id: "x".into(),
            local_port: 50001,
            phase: TunnelPhase::Up,
            pid: Some(1),
            starts: 1,
            last_error: None,
        };
        assert_eq!(s.phase_line(), "connected · localhost:50001");
        assert_eq!(s.base_url(), "http://127.0.0.1:50001/");
        s.phase = TunnelPhase::Retrying {
            attempt: 3,
            retry_in: Duration::from_secs(4),
        };
        s.last_error = Some("Connection refused".into());
        assert_eq!(
            s.phase_line(),
            "reconnecting in 4s (attempt 3) · Connection refused"
        );
    }
}
