//! Remote connections (#156) over stub tunnels.
//!
//! `ssh` and `gcloud` are a stand-in (tests/support/stub_remote.py) found on
//! PATH: the "remote host" is this machine, a forward is a local TCP relay
//! process, and a remote command runs here. So the app's own code runs end
//! to end — PATH resolution, discovery over the transport, reading the
//! token file over it, the tunnel processes, health checks, restarts — with
//! only the network hop replaced.
//!
//! - With a real daemon (ITERVOX_E2E_DAEMON, as in daemon_e2e.rs): adding an
//!   SSH or IAP remote lists the host's daemons, reads their tokens, and
//!   opens their dashboards through the tunnels; a killed tunnel recovers.
//! - With an in-process fake daemon (always): a killed tunnel process is
//!   restarted; a stale tunnel recovers after a simulated wake or network
//!   change; two remotes never share a local port or a token.
//!
//! The stub needs python3 (part of the Xcode Command Line Tools on a Mac).

use std::collections::{HashMap, HashSet};
use std::ffi::OsString;
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use itervox_desktop::client::fetch_state;
use itervox_desktop::dashboard::token_init_script;
use itervox_desktop::model::Health;
use itervox_desktop::remote::{token_key, Connection, IapMode, RemoteDaemon, Remotes, Transport};
use itervox_desktop::remote_model::RemoteCore;
use itervox_desktop::remote_ops::{add_connection, tunnel_specs, AddRequest};
use itervox_desktop::shellpath::{find_in_path, resolve_tools, Tools};
use itervox_desktop::status::DaemonRecord;
use itervox_desktop::supervisor::send_sigterm;
use itervox_desktop::sysevents::{ManualEvents, SystemEvent, SystemEventSource};
use itervox_desktop::token::{MemoryTokenStore, TokenStore};
use itervox_desktop::tunnel::{health_probe, TunnelManager, TunnelPhase, TunnelTiming};

const STUB: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/support/stub_remote.py");

fn require() -> bool {
    std::env::var_os("ITERVOX_E2E_REQUIRE").is_some_and(|v| v == "1")
}

/// python3 for the stub; without it the tests skip (fail under
/// ITERVOX_E2E_REQUIRE=1).
fn have_python() -> bool {
    let ok = find_in_path("python3", &std::env::var_os("PATH").unwrap_or_default()).is_some();
    assert!(ok || !require(), "python3 is needed for the stub tunnels");
    if !ok {
        eprintln!("python3 not found; skipping");
    }
    ok
}

fn daemon_bin() -> Option<PathBuf> {
    match std::env::var_os("ITERVOX_E2E_DAEMON") {
        Some(p) => Some(PathBuf::from(p)),
        None => {
            assert!(
                !require(),
                "ITERVOX_E2E_REQUIRE=1 but ITERVOX_E2E_DAEMON is not set"
            );
            eprintln!("ITERVOX_E2E_DAEMON not set; skipping");
            None
        }
    }
}

fn exe(path: &Path, body: &str) {
    use std::os::unix::fs::PermissionsExt;
    std::fs::write(path, body).unwrap();
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
}

/// A directory with the stub as `ssh` and `gcloud`, plus `itervox` running
/// `daemon` with `home` as HOME (the "remote host"'s itervox).
struct Stubs {
    dir: PathBuf,
    log: PathBuf,
}

impl Stubs {
    fn new(root: &Path, daemon: Option<(&Path, &Path)>) -> Self {
        let dir = root.join("stub-bin");
        std::fs::create_dir_all(&dir).unwrap();
        let src = std::fs::read_to_string(STUB).unwrap();
        exe(&dir.join("ssh"), &src);
        exe(&dir.join("gcloud"), &src);
        if let Some((bin, home)) = daemon {
            exe(
                &dir.join("itervox"),
                &format!(
                    "#!/bin/sh\nHOME='{}' exec '{}' \"$@\"\n",
                    home.display(),
                    bin.display()
                ),
            );
        }
        let log = dir.join("calls.log");
        Stubs { dir, log }
    }

    /// The tools, resolved the app's way from a PATH that starts with the
    /// stubs.
    fn tools(&self) -> Tools {
        let mut p = OsString::from(self.dir.as_os_str());
        p.push(":");
        p.push(std::env::var_os("PATH").unwrap_or_default());
        let t = resolve_tools(&p, None, Duration::from_secs(1));
        assert_eq!(t.ssh.as_deref(), Some(self.dir.join("ssh").as_path()));
        assert_eq!(t.gcloud.as_deref(), Some(self.dir.join("gcloud").as_path()));
        t
    }

    fn calls(&self) -> String {
        std::fs::read_to_string(&self.log).unwrap_or_default()
    }
}

fn fast_timing() -> TunnelTiming {
    TunnelTiming {
        backoff_base: Duration::from_millis(100),
        backoff_max: Duration::from_millis(400),
        startup_grace: Duration::from_secs(10),
        max_health_failures: 2,
    }
}

/// Ticks until `done`, at most 20 s.
fn tick_until(mgr: &mut TunnelManager, what: &str, mut done: impl FnMut(&TunnelManager) -> bool) {
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        mgr.tick(Instant::now(), &health_probe);
        if done(mgr) {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "timed out waiting for {what}: {:?}",
            mgr.statuses(Instant::now())
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn all_up(mgr: &TunnelManager) -> bool {
    let s = mgr.statuses(Instant::now());
    !s.is_empty() && s.iter().all(|t| t.is_up())
}

fn kill9(pid: u32) {
    // SAFETY: a pid of a tunnel process this test started.
    unsafe {
        libc::kill(pid as i32, libc::SIGKILL);
    }
}

/// A fake daemon: `/api/v1/health` open, `/api/v1/state` behind `token`,
/// everything else the dashboard page.
fn fake_daemon(token: &'static str) -> u16 {
    let l = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = l.local_addr().unwrap().port();
    std::thread::spawn(move || {
        for s in l.incoming() {
            let Ok(mut s) = s else { continue };
            std::thread::spawn(move || {
                let mut r = BufReader::new(s.try_clone().unwrap());
                let mut first = String::new();
                if r.read_line(&mut first).is_err() {
                    return;
                }
                let mut auth = String::new();
                loop {
                    let mut line = String::new();
                    if r.read_line(&mut line).unwrap_or(0) == 0 || line == "\r\n" {
                        break;
                    }
                    if line.to_ascii_lowercase().starts_with("authorization:") {
                        auth = line["authorization:".len()..].trim().to_string();
                    }
                }
                let path = first.split_whitespace().nth(1).unwrap_or("/");
                let (status, body) = if path == "/api/v1/health" {
                    ("200 OK", r#"{"status":"ok"}"#.to_string())
                } else if path == "/api/v1/state" {
                    if auth == format!("Bearer {token}") {
                        (
                            "200 OK",
                            r#"{"projectName":"fake","counts":{"running":1}}"#.into(),
                        )
                    } else {
                        ("401 Unauthorized", "{}".into())
                    }
                } else {
                    ("200 OK", r#"<div id="root"></div>"#.into())
                };
                let _ = write!(
                    s,
                    "HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
            });
        }
    });
    port
}

fn ssh_conn(id: &str, port: u16, local_port: u16) -> Connection {
    let mut c = Connection::new(
        id.into(),
        id.into(),
        Transport::Ssh {
            destination: format!("me@{id}"),
        },
    );
    c.daemons.push(RemoteDaemon {
        workflow: "/srv/app/WORKFLOW.md".into(),
        remote_host: "127.0.0.1".into(),
        remote_port: port,
        token_required: true,
        token_source: "env".into(),
        token_file: String::new(),
        local_port,
    });
    c
}

fn manager(stubs: &Stubs, root: &Path, remotes: &Remotes, timing: TunnelTiming) -> TunnelManager {
    let mut mgr = TunnelManager::new(stubs.tools(), root.join("tunnel-logs"), timing);
    for s in tunnel_specs(remotes) {
        mgr.ensure(s, Instant::now());
    }
    mgr
}

/// Reads every connected remote daemon once, as the app's refresh does.
fn refresh(core: &mut RemoteCore, remotes: &Remotes, mgr: &TunnelManager, tokens: &dyn TokenStore) {
    let tunnels = mgr.statuses(Instant::now());
    let states: HashMap<_, _> = RemoteCore::fetch_jobs(remotes, &tunnels, tokens)
        .into_iter()
        .map(|j| (j.key.clone(), fetch_state(&j.url, j.token.as_deref())))
        .collect();
    core.apply(remotes, &tunnels, tokens, &states);
}

fn http_get(url: &str, token: Option<&str>) -> (u16, String) {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .proxy(None)
        .http_status_as_error(false)
        .build()
        .into();
    let mut req = agent.get(url);
    if let Some(t) = token {
        req = req.header("Authorization", &format!("Bearer {t}"));
    }
    let mut resp = req.call().unwrap();
    let code = resp.status().as_u16();
    (code, resp.body_mut().read_to_string().unwrap_or_default())
}

// ── Real daemon ─────────────────────────────────────────────────────────

/// SIGTERMs the daemons when a test ends.
struct Daemons(Vec<i32>);
impl Drop for Daemons {
    fn drop(&mut self) {
        for p in &self.0 {
            let _ = send_sigterm(*p);
        }
    }
}

/// A "remote host": `n` local-tracker projects with daemons started the
/// way a VM's service would (no supervisor token, so each auto-generates
/// its token into its `token_file`), all under one HOME.
fn host_with_daemons(bin: &Path, root: &Path, n: usize) -> (PathBuf, Vec<PathBuf>, Daemons) {
    let home = root.join("host-home");
    std::fs::create_dir_all(&home).unwrap();
    let mut workflows = Vec::new();
    let mut pids = Daemons(Vec::new());
    // `itervox init` and the daemon check that the agent CLI runs; this
    // test needs no real one, so a fake answers from the front of PATH.
    let fake_dir = root.join("fake-claude");
    std::fs::create_dir_all(&fake_dir).unwrap();
    exe(
        &fake_dir.join("claude"),
        "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"2.0.0 (Claude Code)\"; exit 0; fi\ncat >/dev/null\n",
    );
    let path = std::env::join_paths(
        std::iter::once(fake_dir).chain(
            std::env::var_os("PATH")
                .map(|p| std::env::split_paths(&p).collect::<Vec<_>>())
                .unwrap_or_default(),
        ),
    )
    .unwrap();
    for i in 0..n {
        let proj = root.join(format!("proj{i}"));
        std::fs::create_dir_all(&proj).unwrap();
        let out = Command::new(bin)
            .args(["init", "--tracker", "local"])
            .current_dir(&proj)
            .env("HOME", &home)
            .env("PATH", &path)
            .stdin(Stdio::null())
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "{}",
            String::from_utf8_lossy(&out.stderr)
        );
        let wf = proj.join("WORKFLOW.md");
        let log = std::fs::File::create(root.join(format!("daemon{i}.log"))).unwrap();
        let mut child = Command::new(bin)
            .args(["--workflow", &wf.to_string_lossy()])
            .current_dir(&proj)
            .env("HOME", &home)
            .env("PATH", &path)
            .env_remove("ITERVOX_API_TOKEN")
            .env_remove("ITERVOX_SUPERVISOR_TOKEN")
            .stdin(Stdio::null())
            .stdout(log.try_clone().unwrap())
            .stderr(log)
            .spawn()
            .unwrap();
        pids.0.push(child.id() as i32);
        std::thread::spawn(move || child.wait());
        workflows.push(wf);
    }
    // Wait until every daemon is registered with its port.
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        let out = Command::new(bin)
            .args(["status", "--all", "--json"])
            .env("HOME", &home)
            .output()
            .unwrap();
        let recs: Vec<DaemonRecord> =
            itervox_desktop::status::parse_status(&out.stdout).unwrap_or_default();
        let ready = workflows.iter().all(|w| {
            recs.iter()
                .any(|r| r.workflow == w.to_string_lossy() && r.port > 0)
        });
        if ready {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "daemons did not register: {recs:?}"
        );
        std::thread::sleep(Duration::from_millis(200));
    }
    (home, workflows, pids)
}

fn add_and_open(transport: Transport, n: usize) {
    if !have_python() {
        return;
    }
    let Some(bin) = daemon_bin() else { return };
    let tmp = tempfile::tempdir().unwrap();
    let root = tmp.path();
    let (home, workflows, _daemons) = host_with_daemons(&bin, root, n);
    let stubs = Stubs::new(root, Some((&bin, &home)));
    let tools = stubs.tools();
    let tokens = MemoryTokenStore::default();

    // Add remote: discovery lists every daemon on the host.
    let ours: Vec<String> = workflows
        .iter()
        .map(|w| w.to_string_lossy().into_owned())
        .collect();
    let mut listed = 0;
    let added = add_connection(
        &Remotes::default(),
        AddRequest {
            name: "Build box".into(),
            transport: transport.clone(),
            itervox_command: None,
        },
        &tools,
        &tokens,
        &mut |recs| {
            // The host's own daemons; daemons of tests running in parallel
            // can show up through the process scan and are left out.
            let pick: Vec<usize> = recs
                .iter()
                .enumerate()
                .filter(|(_, r)| ours.contains(&r.workflow))
                .map(|(i, _)| i)
                .collect();
            listed = pick.len();
            Some(pick)
        },
        &mut |_, why| panic!("the token file should have been read: {why:?}"),
    )
    .unwrap();
    assert!(
        added.token_problems.is_empty(),
        "{:?}",
        added.token_problems
    );
    let conn = added.connection;
    assert_eq!(conn.id, "build-box");
    if listed > 0 {
        assert_eq!(listed, n, "the chooser was offered every daemon");
    }
    let mut found: Vec<String> = conn.daemons.iter().map(|d| d.workflow.clone()).collect();
    found.sort();
    let mut want: Vec<String> = workflows
        .iter()
        .map(|w| w.to_string_lossy().into_owned())
        .collect();
    want.sort();
    assert_eq!(found, want);
    let calls = stubs.calls();
    assert!(calls.contains("status --all --json"), "{calls}");
    // Each daemon's token was read over the transport and stored under the
    // connection+daemon key.
    for d in &conn.daemons {
        let on_host = std::fs::read_to_string(&d.token_file).unwrap();
        let key = token_key(&conn.id, &d.workflow);
        assert_eq!(tokens.get(&key).unwrap().as_deref(), Some(on_host.trim()));
    }

    // Tunnels: one per daemon, each on its own port.
    let remotes = Remotes {
        connections: vec![conn.clone()],
    };
    let mut mgr = manager(&stubs, root, &remotes, fast_timing());
    tick_until(&mut mgr, "every tunnel up", all_up);
    let ports: HashSet<u16> = mgr
        .statuses(Instant::now())
        .iter()
        .map(|s| s.local_port)
        .collect();
    assert_eq!(ports.len(), n);
    let calls = stubs.calls();
    match &transport {
        Transport::Ssh { .. } => assert!(calls.contains("-o ExitOnForwardFailure=yes"), "{calls}"),
        Transport::Iap {
            mode: IapMode::Port,
            ..
        } => assert!(calls.contains("compute start-iap-tunnel"), "{calls}"),
        Transport::Iap { .. } => assert!(calls.contains("--tunnel-through-iap"), "{calls}"),
    }

    // The refresh reads each daemon through its tunnel with its token.
    let mut core = RemoteCore::new(0);
    refresh(&mut core, &remotes, &mgr, &tokens);
    for d in &core.views()[0].daemons {
        assert!(
            matches!(d.health, Health::Ok(_)),
            "{}: {:?}",
            d.key,
            d.health
        );
    }
    assert_eq!(core.views()[0].status_line(), format!("{n}/{n} connected"));

    // Open Dashboard: the tunnel URL, the token handed over on its origin.
    for d in &conn.daemons {
        let key = token_key(&conn.id, &d.workflow);
        let t = core.dashboard_target(&key, &tokens, None).unwrap();
        let st = mgr.status(&key, Instant::now()).unwrap();
        assert_eq!(t.url, format!("http://127.0.0.1:{}/", st.local_port));
        assert_eq!(t.origin, format!("http://127.0.0.1:{}", st.local_port));
        let tok = t.token.clone().unwrap();
        assert!(token_init_script(&t.origin, &tok).contains(&t.origin));
        let (code, page) = http_get(&t.url, None);
        assert_eq!(code, 200);
        assert!(page.contains("id=\"root\""), "{page}");
        assert_eq!(http_get(&format!("{}api/v1/state", t.url), None).0, 401);
        assert_eq!(
            http_get(&format!("{}api/v1/state", t.url), Some(&tok)).0,
            200
        );
    }

    // Killing a tunnel process: restarted on the same port, readable again.
    let key = token_key(&conn.id, &conn.daemons[0].workflow);
    let before = mgr.status(&key, Instant::now()).unwrap();
    kill9(before.pid.unwrap());
    tick_until(&mut mgr, "the killed tunnel to come back", |m| {
        m.status(&key, Instant::now())
            .is_some_and(|s| s.is_up() && s.pid != before.pid)
    });
    assert_eq!(
        mgr.status(&key, Instant::now()).unwrap().local_port,
        before.local_port
    );
    refresh(&mut core, &remotes, &mgr, &tokens);
    assert!(matches!(core.daemon(&key).unwrap().health, Health::Ok(_)));
}

#[test]
fn ssh_remote_lists_its_daemons_and_opens_dashboards_through_tunnels() {
    add_and_open(
        Transport::Ssh {
            destination: "me@build-box".into(),
        },
        2,
    );
}

#[test]
fn iap_remote_lists_its_daemons_and_opens_dashboards_through_tunnels() {
    add_and_open(
        Transport::Iap {
            instance: "itervox".into(),
            zone: "europe-west1-b".into(),
            project: "acme".into(),
            mode: IapMode::Ssh,
        },
        1,
    );
}

#[test]
fn iap_port_tunnel_opens_the_dashboard() {
    add_and_open(
        Transport::Iap {
            instance: "itervox".into(),
            zone: "europe-west1-b".into(),
            project: String::new(),
            mode: IapMode::Port,
        },
        1,
    );
}

// ── Fake daemon ─────────────────────────────────────────────────────────

#[test]
fn killed_tunnel_process_is_restarted_and_the_connection_recovers() {
    if !have_python() {
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let stubs = Stubs::new(tmp.path(), None);
    let port = fake_daemon("fake-token-1");
    let remotes = Remotes {
        connections: vec![ssh_conn("prod", port, 0)],
    };
    let key = token_key("prod", "/srv/app/WORKFLOW.md");
    let tokens = MemoryTokenStore::default();
    tokens.set(&key, "fake-token-1").unwrap();
    let mut mgr = manager(&stubs, tmp.path(), &remotes, fast_timing());
    tick_until(&mut mgr, "the tunnel up", all_up);
    let first = mgr.status(&key, Instant::now()).unwrap();
    let mut core = RemoteCore::new(0);

    for round in 0..2 {
        let before = mgr.status(&key, Instant::now()).unwrap();
        kill9(before.pid.unwrap());
        // The next tick sees the exit and schedules a restart.
        std::thread::sleep(Duration::from_millis(100));
        mgr.tick(Instant::now(), &health_probe);
        let s = mgr.status(&key, Instant::now()).unwrap();
        assert!(
            matches!(s.phase, TunnelPhase::Retrying { .. }),
            "round {round}: {s:?}"
        );
        refresh(&mut core, &remotes, &mgr, &tokens);
        assert!(core.views()[0].status_line().starts_with("reconnecting"));
        // No user action: ticking alone brings it back.
        tick_until(&mut mgr, "the tunnel restarted", |m| {
            m.status(&key, Instant::now())
                .is_some_and(|s| s.is_up() && s.pid != before.pid)
        });
        refresh(&mut core, &remotes, &mgr, &tokens);
        assert!(matches!(core.daemon(&key).unwrap().health, Health::Ok(_)));
    }
    let last = mgr.status(&key, Instant::now()).unwrap();
    assert_eq!(
        last.local_port, first.local_port,
        "same port after restarts"
    );
    assert_eq!(last.starts, 3);
}

#[test]
fn wake_or_network_change_reprobes_and_recovers_a_stale_tunnel() {
    if !have_python() {
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let stubs = Stubs::new(tmp.path(), None);
    let port = fake_daemon("fake-token-2");
    let remotes = Remotes {
        connections: vec![ssh_conn("prod", port, 0)],
    };
    let key = token_key("prod", "/srv/app/WORKFLOW.md");
    // Patient health checks and a long backoff, so only the events can
    // explain a quick recovery.
    let timing = TunnelTiming {
        backoff_base: Duration::from_secs(3600),
        backoff_max: Duration::from_secs(3600),
        startup_grace: Duration::from_secs(10),
        max_health_failures: 1000,
    };
    let mut mgr = manager(&stubs, tmp.path(), &remotes, timing);
    let (fire, mut events) = ManualEvents::new();
    let mut tick = |mgr: &mut TunnelManager| {
        for e in events.poll() {
            mgr.on_event(e, Instant::now());
        }
        mgr.tick(Instant::now(), &health_probe)
    };
    let deadline = Instant::now() + Duration::from_secs(20);
    while !all_up(&mgr) {
        tick(&mut mgr);
        assert!(Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(100));
    }

    // A tunnel that went stale while the Mac slept: the process lives but
    // forwards nothing (stopped).
    let stale = mgr.status(&key, Instant::now()).unwrap().pid.unwrap();
    // SAFETY: the tunnel process this test started.
    unsafe {
        libc::kill(stale as i32, libc::SIGSTOP);
    }
    for _ in 0..3 {
        tick(&mut mgr);
    }
    assert_eq!(
        mgr.status(&key, Instant::now()).unwrap().pid,
        Some(stale),
        "without an event, failed probes are tolerated"
    );
    fire.send(SystemEvent::Wake).unwrap();
    tick(&mut mgr);
    let after = mgr.status(&key, Instant::now()).unwrap();
    assert_ne!(
        after.pid,
        Some(stale),
        "the wake restarted the stale tunnel"
    );
    let deadline = Instant::now() + Duration::from_secs(20);
    while !all_up(&mgr) {
        tick(&mut mgr);
        assert!(
            Instant::now() < deadline,
            "{:?}",
            mgr.statuses(Instant::now())
        );
        std::thread::sleep(Duration::from_millis(100));
    }

    // A tunnel waiting out an hour's backoff (the network was down when it
    // died) retries as soon as the network changes.
    let pid = mgr.status(&key, Instant::now()).unwrap().pid.unwrap();
    kill9(pid);
    std::thread::sleep(Duration::from_millis(100));
    tick(&mut mgr);
    tick(&mut mgr);
    let s = mgr.status(&key, Instant::now()).unwrap();
    assert!(
        matches!(s.phase, TunnelPhase::Retrying { retry_in, .. } if retry_in > Duration::from_secs(60)),
        "{s:?}"
    );
    fire.send(SystemEvent::NetworkChanged).unwrap();
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        tick(&mut mgr);
        if all_up(&mgr) {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "{:?}",
            mgr.statuses(Instant::now())
        );
        std::thread::sleep(Duration::from_millis(100));
    }
    let tokens = MemoryTokenStore::default();
    tokens.set(&key, "fake-token-2").unwrap();
    let mut core = RemoteCore::new(0);
    refresh(&mut core, &remotes, &mgr, &tokens);
    assert!(matches!(core.daemon(&key).unwrap().health, Health::Ok(_)));
}

#[test]
fn two_remotes_never_share_a_local_port_or_token_storage() {
    if !have_python() {
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let stubs = Stubs::new(tmp.path(), None);
    let pa = fake_daemon("token-for-a");
    let pb = fake_daemon("token-for-b");
    // Both remotes have a daemon at the same remote path and both remember
    // the same local port.
    let wanted = itervox_desktop::tunnel::os_free_port().unwrap();
    let remotes = Remotes {
        connections: vec![ssh_conn("alpha", pa, wanted), ssh_conn("beta", pb, wanted)],
    };
    let ka = token_key("alpha", "/srv/app/WORKFLOW.md");
    let kb = token_key("beta", "/srv/app/WORKFLOW.md");
    assert_ne!(ka, kb);
    let tokens = MemoryTokenStore::default();
    tokens.set(&ka, "token-for-a").unwrap();
    let mut mgr = manager(&stubs, tmp.path(), &remotes, fast_timing());
    tick_until(&mut mgr, "both tunnels up", all_up);
    let a = mgr.status(&ka, Instant::now()).unwrap();
    let b = mgr.status(&kb, Instant::now()).unwrap();
    assert_ne!(a.local_port, b.local_port);
    assert!(a.local_port == wanted || b.local_port == wanted);

    let mut core = RemoteCore::new(0);
    refresh(&mut core, &remotes, &mgr, &tokens);
    assert!(matches!(core.daemon(&ka).unwrap().health, Health::Ok(_)));
    // beta's token was never stored: alpha's is not used for it.
    assert_eq!(core.daemon(&kb).unwrap().health, Health::TokenRequired);
    assert!(!core.daemon(&kb).unwrap().has_token);
    tokens.set(&kb, "token-for-b").unwrap();
    refresh(&mut core, &remotes, &mgr, &tokens);
    assert!(matches!(core.daemon(&kb).unwrap().health, Health::Ok(_)));
    assert_eq!(tokens.get(&ka).unwrap().as_deref(), Some("token-for-a"));
    let ta = core.dashboard_target(&ka, &tokens, None).unwrap();
    let tb = core.dashboard_target(&kb, &tokens, None).unwrap();
    assert_ne!(ta.origin, tb.origin);
    assert_ne!(ta.token, tb.token);

    // While beta is down, its port stays reserved: a third tunnel added now
    // still gets a port of its own.
    kill9(b.pid.unwrap());
    std::thread::sleep(Duration::from_millis(100));
    mgr.tick(Instant::now(), &health_probe);
    let mut third = ssh_conn("gamma", pa, b.local_port);
    third.daemons[0].workflow = "/srv/other/WORKFLOW.md".into();
    for s in tunnel_specs(&Remotes {
        connections: vec![third],
    }) {
        mgr.ensure(s, Instant::now());
    }
    tick_until(&mut mgr, "all three up", |m| {
        m.statuses(Instant::now()).len() == 3 && all_up(m)
    });
    let ports: HashSet<u16> = mgr
        .statuses(Instant::now())
        .iter()
        .map(|s| s.local_port)
        .collect();
    assert_eq!(ports.len(), 3, "{:?}", mgr.statuses(Instant::now()));
    assert_eq!(
        mgr.status(&kb, Instant::now()).unwrap().local_port,
        b.local_port,
        "beta got its own port back"
    );
}
