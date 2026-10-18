//! End-to-end checks against a real `itervox` binary, driving the same code
//! the app's refresh loop runs (`Core::fetch_jobs` → `fetch_state` →
//! `Core::apply`) with the same start/stop commands the menu uses:
//!
//! - start a daemon detached with a supervisor token, see it listed by
//!   `status --all --json` and its state read with the token; restart it
//!   under a new pid; stop it with SIGTERM and see it shown as stopped;
//! - an agent run that asks for input raises an alert whose link opens
//!   that issue in the dashboard.
//!
//! Skipped unless ITERVOX_E2E_DAEMON names the binary, e.g.
//!   desktop/scripts/build-sidecar.sh --host
//!   ITERVOX_E2E_DAEMON=$PWD/desktop/src-tauri/binaries/itervox-$(rustc -vV | sed -n 's/^host: //p') \
//!     cargo test --manifest-path desktop/src-tauri/Cargo.toml --test daemon_e2e
//! `make desktop-test` does this. HOME is a temporary directory, so the
//! registry holds only this test's daemons (other daemons on the host can
//! still appear through the process scan; the tests ignore them). The agent
//! is a shell script standing in for the `claude` CLI.

use std::collections::HashMap;
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use itervox_desktop::client::{fetch_sessions, fetch_state, FetchError};
use itervox_desktop::events::AlertKind;
use itervox_desktop::model::{Core, Health, PendingOp, RefreshOutcome};
use itervox_desktop::projects::SavedProjects;
use itervox_desktop::sessions::{read_all, session_link};
use itervox_desktop::status::{parse_status, DaemonRecord};
use itervox_desktop::supervisor::{
    process_alive, record_for, send_sigterm, start_spec, status_command, wait_for_exit,
};
use itervox_desktop::token::{generate_token, MemoryTokenStore, TokenStore};

/// The daemon binary under test. Unset, the tests skip — unless
/// ITERVOX_E2E_REQUIRE=1 (set by `make desktop-test` and CI), where a skip
/// would hide a missing sidecar.
fn daemon() -> Option<PathBuf> {
    match std::env::var_os("ITERVOX_E2E_DAEMON") {
        Some(p) => {
            let p = PathBuf::from(p);
            assert!(
                p.is_file(),
                "ITERVOX_E2E_DAEMON={} is not a file",
                p.display()
            );
            Some(p)
        }
        None => {
            assert!(
                std::env::var_os("ITERVOX_E2E_REQUIRE").is_none_or(|v| v != "1"),
                "ITERVOX_E2E_REQUIRE=1 but ITERVOX_E2E_DAEMON is not set"
            );
            None
        }
    }
}

/// A fake `claude`: one turn that asks for input with the sentinel.
const FAKE_CLAUDE: &str = r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.0.0 (Claude Code)"; exit 0; fi
cat >/dev/null
cat <<'J'
{"type":"system","subtype":"init","session_id":"sess-e2e","model":"x"}
{"type":"result","subtype":"success","is_error":false,"result":"Which database should I use?\n<!-- itervox:needs-input -->","session_id":"sess-e2e","num_turns":1}
J
"#;

/// SIGTERMs a daemon if a test fails half-way.
struct Guard(Option<i32>);
impl Drop for Guard {
    fn drop(&mut self) {
        if let Some(pid) = self.0 {
            let _ = send_sigterm(pid);
        }
    }
}

struct Fixture {
    bin: PathBuf,
    _tmp: tempfile::TempDir,
    home: PathBuf,
    root: PathBuf,
    workflow: PathBuf,
    key: String,
    tokens: MemoryTokenStore,
    core: Core,
}

fn run(cmd: &mut Command) {
    let out = cmd.stdin(Stdio::null()).output().unwrap();
    assert!(
        out.status.success(),
        "{cmd:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
}

impl Fixture {
    /// A local-tracker project (`itervox init --tracker local`) in a git
    /// repo that is its own clone source, with the fake agent and a 2 s
    /// poll interval. With `bare_command` the workflow names the agent as
    /// plain `claude`, so the daemon must find it on PATH.
    fn new(bin: PathBuf, bare_command: bool) -> Self {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path().join("home");
        let proj = tmp.path().join("proj");
        std::fs::create_dir_all(&home).unwrap();
        std::fs::create_dir_all(&proj).unwrap();
        let fake = tmp.path().join("fake").join("claude");
        std::fs::create_dir_all(fake.parent().unwrap()).unwrap();
        std::fs::write(&fake, FAKE_CLAUDE).unwrap();
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&fake, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
        // `itervox init` checks that the claude CLI runs; CI runners have
        // none, so the fake answers its `--version` from the front of PATH.
        let init_path = std::env::join_paths(
            std::iter::once(fake.parent().unwrap().to_path_buf()).chain(
                std::env::var_os("PATH")
                    .map(|p| std::env::split_paths(&p).collect::<Vec<_>>())
                    .unwrap_or_default(),
            ),
        )
        .unwrap();
        run(Command::new(&bin)
            .args(["init", "--tracker", "local"])
            .current_dir(&proj)
            .env("HOME", &home)
            .env("PATH", &init_path));
        let workflow = proj.join("WORKFLOW.md");
        let wf = std::fs::read_to_string(&workflow).unwrap();
        let wf = wf
            .replace(
                "command: claude",
                &if bare_command {
                    "command: claude".to_string()
                } else {
                    format!("command: {}", fake.display())
                },
            )
            .replace("interval_ms: 60000", "interval_ms: 2000")
            .replace(
                "clone_url: git@github.com:owner/my-project.git",
                &format!("clone_url: {}", proj.display()),
            )
            .replace("base_branch: main", "base_branch: e2e");
        std::fs::write(&workflow, wf).unwrap();
        let git = |args: &[&str]| {
            run(Command::new("git")
                .args(args)
                .current_dir(&proj)
                .env("HOME", &home)
                .env("GIT_AUTHOR_NAME", "e2e")
                .env("GIT_AUTHOR_EMAIL", "e2e@example.invalid")
                .env("GIT_COMMITTER_NAME", "e2e")
                .env("GIT_COMMITTER_EMAIL", "e2e@example.invalid"));
        };
        git(&["init", "-q", "-b", "e2e"]);
        git(&["add", "-A"]);
        git(&["commit", "-qm", "init"]);
        let key = workflow.to_string_lossy().into_owned();
        let mut saved = SavedProjects::default();
        saved.add(&key);
        Fixture {
            bin,
            home,
            root: tmp.path().to_path_buf(),
            workflow,
            key,
            tokens: MemoryTokenStore::default(),
            core: Core::new(saved),
            _tmp: tmp,
        }
    }

    fn status(&self) -> Vec<DaemonRecord> {
        let out = status_command(&self.bin)
            .env("HOME", &self.home)
            .output()
            .expect("run status");
        assert!(
            out.status.success(),
            "{}",
            String::from_utf8_lossy(&out.stderr)
        );
        parse_status(&out.stdout).expect("parse status")
    }

    /// One refresh, as the app's refresh loop does it.
    fn refresh(&mut self) -> RefreshOutcome {
        let records = self.status();
        let jobs = self
            .core
            .fetch_jobs(&records, &self.tokens, &|p| std::fs::read_to_string(p).ok());
        let states: HashMap<String, Result<_, FetchError>> = jobs
            .iter()
            .map(|j| (j.key.clone(), fetch_state(&j.url, j.token.as_deref())))
            .collect();
        self.core.apply(Ok(records), states, Instant::now())
    }

    /// Refreshes until `done` holds, for at most 30 s.
    fn refresh_until(
        &mut self,
        what: &str,
        mut done: impl FnMut(&Core, &str, &RefreshOutcome) -> bool,
    ) -> RefreshOutcome {
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            let out = self.refresh();
            if done(&self.core, &self.key, &out) {
                return out;
            }
            assert!(
                Instant::now() < deadline,
                "timed out waiting for {what}; views: {:?}\noutput:\n{}",
                self.core.views(),
                self.log()
            );
            std::thread::sleep(Duration::from_millis(250));
        }
    }

    /// The menu's Start: a fresh token in the store, a detached daemon.
    fn start(&mut self) -> i32 {
        self.start_with(|_| {})
    }

    /// Start, with `env` adjusting the inherited environment first.
    fn start_with(&mut self, env: impl FnOnce(&mut Command)) -> i32 {
        let token = generate_token().unwrap();
        self.tokens.set(&self.key, &token).unwrap();
        let log = self.root.join("out").join("daemon.log");
        let spec = start_spec(&self.bin, &self.workflow, &token, &log).unwrap();
        let mut cmd = spec.command().unwrap();
        env(&mut cmd);
        for (k, v) in &spec.env {
            cmd.env(k, v);
        }
        let mut child = cmd.env("HOME", &self.home).spawn().unwrap();
        let pid = child.id() as i32;
        // Reap it like the app does, so a stopped daemon does not linger
        // as a zombie that still answers kill(pid, 0).
        std::thread::spawn(move || child.wait());
        pid
    }

    /// The menu's Stop: SIGTERM the registry pid, wait for the exit.
    fn stop(&self) {
        let recs = self.status();
        let pid = record_for(&recs, &self.key).expect("running").pid;
        send_sigterm(pid).unwrap();
        assert!(wait_for_exit(
            || process_alive(pid),
            Duration::from_secs(45),
            Duration::from_millis(100)
        ));
    }

    /// The daemon's stdout/stderr plus the tail of its own log file.
    fn log(&self) -> String {
        let mut out =
            std::fs::read_to_string(self.root.join("out").join("daemon.log")).unwrap_or_default();
        if let Some(rec) = self.core.view(&self.key).and_then(|v| v.record.as_ref()) {
            let own = std::fs::read_to_string(PathBuf::from(&rec.logs_dir).join("itervox.log"))
                .unwrap_or_default();
            let lines: Vec<&str> = own.lines().collect();
            out.push_str("\n--- itervox.log (tail) ---\n");
            out.push_str(&lines[lines.len().saturating_sub(40)..].join("\n"));
        }
        out
    }
}

fn settled_running(c: &Core, key: &str) -> bool {
    c.view(key)
        .is_some_and(|v| v.pending.is_none() && v.running())
}

#[test]
fn start_stop_restart_are_reflected_within_one_refresh() {
    let Some(bin) = daemon() else {
        eprintln!("ITERVOX_E2E_DAEMON not set; skipping");
        return;
    };
    let mut f = Fixture::new(bin, false);
    f.refresh();
    let v = f.core.view(&f.key).unwrap();
    assert_eq!(
        v.status_line(),
        "stopped",
        "an added project shows while stopped"
    );

    // Start.
    f.core.begin(&f.key, PendingOp::Starting, Instant::now());
    let pid = f.start();
    let mut guard = Guard(Some(pid));
    f.refresh_until("the daemon to be listed", |c, k, _| settled_running(c, k));
    let v = f.core.view(&f.key).unwrap();
    let rec = v.record.clone().unwrap();
    assert_eq!(rec.pid, pid);
    assert_eq!(rec.token_source, "supervisor");
    assert!(v.managed, "the app holds the token: {:?}", v.health);
    assert!(
        v.status_line().starts_with("running · 0 agents"),
        "{}",
        v.status_line()
    );
    // The API needs the token; the daemon never prints it.
    assert_eq!(
        fetch_state(&rec.base_url(), None),
        Err(FetchError::Unauthorized)
    );
    let token = f.tokens.get(&f.key).unwrap().unwrap();
    assert!(
        !f.log().contains(&token),
        "token leaked into the daemon output"
    );

    // Restart: stop, start again; the next refresh shows the new pid.
    f.core.begin(&f.key, PendingOp::Restarting, Instant::now());
    f.stop();
    let pid2 = f.start();
    guard.0 = Some(pid2);
    f.refresh_until("the restarted daemon", |c, k, _| settled_running(c, k));
    let v = f.core.view(&f.key).unwrap();
    assert_eq!(v.record.as_ref().unwrap().pid, pid2);
    assert_ne!(pid2, pid);

    // Stop: the very next refresh shows it stopped.
    f.core.begin(&f.key, PendingOp::Stopping, Instant::now());
    f.stop();
    guard.0 = None;
    f.refresh();
    let v = f.core.view(&f.key).unwrap();
    assert_eq!(v.pending, None);
    assert_eq!(v.status_line(), "stopped");
}

#[test]
fn input_required_run_raises_an_alert_that_opens_the_issue() {
    let Some(bin) = daemon() else {
        eprintln!("ITERVOX_E2E_DAEMON not set; skipping");
        return;
    };
    let mut f = Fixture::new(bin, false);
    let pid = f.start();
    let mut guard = Guard(Some(pid));
    // Baseline: running and readable, nothing waiting.
    f.refresh_until("the daemon's state", |c, k, _| {
        c.view(k).is_some_and(|v| matches!(v.health, Health::Ok(_)))
    });

    // An issue an agent picks up and asks a question on.
    let issue = f
        .workflow
        .parent()
        .unwrap()
        .join(".itervox/issues/ITX-7.md");
    std::fs::write(
        &issue,
        "---\ntitle: 'Pick a database'\nstate: Todo\ncreated: 2026-10-14T08:00:00Z\nupdated: 2026-10-14T08:00:00Z\n---\n\nChoose the database.\n",
    )
    .unwrap();

    let out = f.refresh_until("an input-required alert", |_, _, o| {
        o.alerts.iter().any(|a| a.kind == AlertKind::InputRequired)
    });
    let alert = out
        .alerts
        .iter()
        .find(|a| a.kind == AlertKind::InputRequired)
        .unwrap()
        .clone();
    let base = f
        .core
        .view(&f.key)
        .unwrap()
        .record
        .as_ref()
        .unwrap()
        .base_url();
    assert_eq!(alert.identifier, "ITX-7");
    assert_eq!(alert.url, format!("{base}?issue=ITX-7"));
    assert_eq!(alert.title(), "ITX-7 needs input");
    assert!(
        alert.body().ends_with("Which database should I use?"),
        "{}",
        alert.body()
    );
    assert!(f
        .core
        .view(&f.key)
        .unwrap()
        .status_line()
        .contains("1 needs input"));
    // The next refresh does not repeat it.
    assert!(f
        .refresh()
        .alerts
        .iter()
        .all(|a| a.kind != AlertKind::InputRequired));
    // The link is served by the daemon's dashboard.
    let agent: ureq::Agent = ureq::Agent::config_builder().proxy(None).build().into();
    let page = agent
        .get(&alert.url)
        .call()
        .unwrap()
        .body_mut()
        .read_to_string()
        .unwrap();
    assert!(page.contains("id=\"root\""), "{page}");

    // #178: the Sessions window lists the waiting session from the real
    // daemon's /api/v1/sessions (read with the supervisor token), badged
    // as waiting, and its dashboard route is served.
    let records = f.status();
    let jobs = f
        .core
        .session_jobs(&records, &f.tokens, &|p| std::fs::read_to_string(p).ok());
    let view = read_all(&jobs, fetch_sessions);
    let group = view
        .groups
        .iter()
        .find(|g| g.daemon == f.key)
        .expect("the daemon's group");
    assert_eq!(group.location, "local");
    let waiting = group
        .sessions
        .iter()
        .find(|s| s.identifier == "ITX-7")
        .unwrap_or_else(|| panic!("ITX-7 listed: {group:?}"));
    assert!(waiting.waiting(), "{waiting:?}");
    assert_eq!(waiting.key, "sess-e2e", "keyed by the CLI's session id");
    assert_eq!(waiting.owner, "itervox");
    assert!(view.waiting >= 1);
    let link = session_link(&base, &waiting.key);
    assert_eq!(link, format!("{base}sessions?id=sess-e2e"));
    let page = agent
        .get(&link)
        .call()
        .unwrap()
        .body_mut()
        .read_to_string()
        .unwrap();
    assert!(page.contains("id=\"root\""), "{page}");
    f.stop();
    guard.0 = None;
}

/// A stand-in for the user's login shell: its profile puts the agent CLI's
/// directory on PATH, the way nvm or Homebrew set-up in ~/.zshrc does.
fn fake_login_shell(dir: &std::path::Path, agent_dir: &std::path::Path) -> PathBuf {
    let sh = dir.join("fake-zsh");
    std::fs::write(
        &sh,
        format!(
            "#!/bin/sh\nPATH=\"{}:$PATH\"; export PATH\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in\n    -*c*) shift; exec /bin/sh -c \"$@\" ;;\n    -*) shift ;;\n    *) break ;;\n  esac\ndone\nexec /bin/sh \"$@\"\n",
            agent_dir.display()
        ),
    )
    .unwrap();
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&sh, std::fs::Permissions::from_mode(0o755)).unwrap();
    sh
}

/// Started the way Finder or launchd starts the app (no terminal, launchd's
/// minimal PATH, no SHLVL), the daemon takes PATH from the login shell
/// (#119) and its agent, reachable only through that PATH, runs.
#[test]
fn works_with_no_terminal_via_the_login_shell_path() {
    let Some(bin) = daemon() else {
        eprintln!("ITERVOX_E2E_DAEMON not set; skipping");
        return;
    };
    let mut f = Fixture::new(bin, true);
    let shell = fake_login_shell(&f.root, &f.root.join("fake"));
    let pid = f.start_with(|cmd| {
        cmd.env_clear()
            .env("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
            .env("SHELL", &shell)
            .env("USER", "e2e");
    });
    let mut guard = Guard(Some(pid));
    f.refresh_until("the daemon's state", |c, k, _| {
        c.view(k).is_some_and(|v| matches!(v.health, Health::Ok(_)))
    });
    let rec = f.core.view(&f.key).unwrap().record.clone().unwrap();
    assert_eq!(rec.path_source, "login_shell", "{}", f.log());

    let issue = f
        .workflow
        .parent()
        .unwrap()
        .join(".itervox/issues/ITX-8.md");
    std::fs::write(
        &issue,
        "---\ntitle: 'Pick a queue'\nstate: Todo\ncreated: 2026-10-14T08:00:00Z\nupdated: 2026-10-14T08:00:00Z\n---\n\nChoose the queue.\n",
    )
    .unwrap();
    let out = f.refresh_until("the agent found through the login-shell PATH", |_, _, o| {
        o.alerts.iter().any(|a| a.kind == AlertKind::InputRequired)
    });
    assert!(out.alerts.iter().any(|a| a.identifier == "ITX-8"));
    f.stop();
    guard.0 = None;
}
