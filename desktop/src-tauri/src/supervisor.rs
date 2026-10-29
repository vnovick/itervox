//! Finding the bundled daemon and starting, stopping and listing daemons.
//!
//! The daemon is the `itervox` binary bundled as a Tauri sidecar
//! (`externalBin`): in the app bundle it sits next to the app executable in
//! `Contents/MacOS/`. The app never searches PATH for it, so it works the
//! same from Finder, launchd or a terminal.
//!
//! Started daemons are detached: they get their own process group (and so
//! no SIGHUP/SIGINT from the app's terminal, if any) and keep running when
//! the app quits. On its next start the app finds them again through
//! `itervox status --all --json` and still holds their token in the
//! Keychain. Quitting the app therefore never stops agents mid-run.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use crate::platform::Os;
use crate::status::{parse_status, DaemonRecord};

/// Development override: an absolute path to an `itervox` binary to use
/// instead of the bundled sidecar.
pub const DAEMON_OVERRIDE_ENV: &str = "ITERVOX_DESKTOP_DAEMON";

/// The environment variable the daemon takes its supervisor token from.
pub const SUPERVISOR_TOKEN_ENV: &str = "ITERVOX_SUPERVISOR_TOKEN";

/// Name of the sidecar binary inside the bundle (`itervox.exe` on
/// Windows, where Tauri keeps the extension).
pub const SIDECAR_NAME: &str = if cfg!(windows) {
    "itervox.exe"
} else {
    "itervox"
};

/// How long `status --json` may take.
pub const STATUS_TIMEOUT: Duration = Duration::from_secs(10);

/// How long a stop waits for the daemon to exit after SIGTERM. The daemon
/// drains in-flight agent turns for up to `--shutdown-grace` (30 s by
/// default) first.
pub const STOP_TIMEOUT: Duration = Duration::from_secs(45);

/// Candidate sidecar paths, in order: the override, then next to the app
/// executable (the bundle's `Contents/MacOS`, or `target/<profile>` under
/// `tauri dev`, where Tauri copies the sidecar too).
pub fn sidecar_candidates(exe: Option<&Path>, override_path: Option<OsString>) -> Vec<PathBuf> {
    let mut out = Vec::new();
    if let Some(p) = override_path.filter(|p| !p.is_empty()) {
        let p = PathBuf::from(p);
        if p.is_absolute() {
            out.push(p);
        }
    }
    if let Some(dir) = exe.and_then(Path::parent) {
        out.push(dir.join(SIDECAR_NAME));
    }
    out
}

/// The first candidate that is an executable file.
pub fn resolve_sidecar(candidates: &[PathBuf]) -> Result<PathBuf, String> {
    for c in candidates {
        if is_executable_file(c) {
            return Ok(c.clone());
        }
    }
    Err(missing_sidecar_message(Os::current(), candidates))
}

/// The full listing when there is no daemon binary to run it: an error,
/// except on Windows without `itervox.exe` (#259), where no local daemon is
/// expected and the registry alone is the listing (the tray shows the WSL
/// notice instead of an error line).
pub fn list_without_sidecar(os: Os, err: &str) -> Result<Vec<DaemonRecord>, String> {
    if os == Os::Windows {
        Ok(Vec::new())
    } else {
        Err(err.to_string())
    }
}

/// Why there is no daemon to run. On Windows the native `itervox.exe` is
/// not bundled yet (#259): say what works instead.
pub fn missing_sidecar_message(os: Os, candidates: &[PathBuf]) -> String {
    if os == Os::Windows {
        return crate::platform::NO_LOCAL_DAEMON_MESSAGE.to_string();
    }
    format!(
        "the bundled itervox daemon was not found (looked at {})",
        candidates
            .iter()
            .map(|c| c.display().to_string())
            .collect::<Vec<_>>()
            .join(", ")
    )
}

fn is_executable_file(p: &Path) -> bool {
    crate::platform::is_executable(p)
}

/// The command that lists every daemon on the host.
pub fn status_command(sidecar: &Path) -> Command {
    let mut c = Command::new(sidecar);
    c.args(["status", "--all", "--json"])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    c
}

/// Runs `status --all --json` with a timeout and parses it.
pub fn list_daemons(sidecar: &Path) -> Result<Vec<DaemonRecord>, String> {
    let child = status_command(sidecar)
        .spawn()
        .map_err(|e| format!("could not run {}: {e}", sidecar.display()))?;
    let out = wait_output(child, STATUS_TIMEOUT)?;
    if !out.status.success() {
        return Err(format!(
            "itervox status failed ({}): {}",
            out.status,
            last_line(&String::from_utf8_lossy(&out.stderr))
        ));
    }
    parse_status(&out.stdout).map_err(|e| e.to_string())
}

pub(crate) fn wait_output(
    mut child: std::process::Child,
    timeout: Duration,
) -> Result<std::process::Output, String> {
    let deadline = Instant::now() + timeout;
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return child.wait_with_output().map_err(|e| e.to_string()),
            Ok(None) if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!("itervox status did not finish within {timeout:?}"));
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(25)),
            Err(e) => return Err(e.to_string()),
        }
    }
}

/// The last non-empty line of `s`, trimmed.
pub fn last_line(s: &str) -> String {
    s.lines()
        .rev()
        .map(str::trim)
        .find(|l| !l.is_empty())
        .unwrap_or("")
        .to_string()
}

/// Everything needed to start one daemon.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StartSpec {
    pub program: PathBuf,
    pub args: Vec<String>,
    pub cwd: PathBuf,
    /// Environment variables to set (the token among them).
    pub env: Vec<(String, String)>,
    /// Environment variables to remove from the inherited environment.
    pub env_remove: Vec<String>,
    /// Where the daemon's stdout/stderr go (startup errors before its own
    /// log file is open land here).
    pub output_log: PathBuf,
}

/// Builds the start command for `workflow`: headless (stdin is /dev/null, so
/// the terminal UI never starts), in the project directory, with the
/// supervisor token in the environment and a pinned API token removed.
pub fn start_spec(
    sidecar: &Path,
    workflow: &Path,
    token: &str,
    output_log: &Path,
) -> Result<StartSpec, String> {
    crate::token::validate_token(token)?;
    if !workflow.is_absolute() {
        return Err(format!("{} is not an absolute path", workflow.display()));
    }
    let cwd = workflow
        .parent()
        .ok_or_else(|| format!("{} has no parent directory", workflow.display()))?
        .to_path_buf();
    Ok(StartSpec {
        program: sidecar.to_path_buf(),
        args: vec!["--workflow".into(), workflow.to_string_lossy().into_owned()],
        cwd,
        env: vec![(SUPERVISOR_TOKEN_ENV.into(), token.into())],
        env_remove: vec!["ITERVOX_API_TOKEN".into()],
        output_log: output_log.to_path_buf(),
    })
}

impl StartSpec {
    /// The detached command: its own process group, no stdin, output to
    /// `output_log` (truncated per start).
    pub fn command(&self) -> std::io::Result<Command> {
        if let Some(dir) = self.output_log.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let log = std::fs::File::create(&self.output_log)?;
        let mut c = Command::new(&self.program);
        c.args(&self.args)
            .current_dir(&self.cwd)
            .stdin(Stdio::null())
            .stdout(log.try_clone()?)
            .stderr(log);
        crate::platform::own_process_group(&mut c);
        for k in &self.env_remove {
            c.env_remove(k);
        }
        for (k, v) in &self.env {
            c.env(k, v);
        }
        Ok(c)
    }
}

/// A file-name-safe name for a project's output log.
pub fn output_log_name(workflow: &str) -> String {
    let dir = Path::new(workflow)
        .parent()
        .and_then(Path::file_name)
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| "project".into());
    let safe: String = dir
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                c
            } else {
                '_'
            }
        })
        .collect();
    // FNV-1a of the full path keeps two same-named directories apart.
    let mut h: u32 = 0x811c9dc5;
    for b in workflow.bytes() {
        h ^= u32::from(b);
        h = h.wrapping_mul(0x01000193);
    }
    format!("{safe}-{h:08x}.log")
}

/// Whether a process with `pid` exists (signal 0 on Unix).
pub fn process_alive(pid: i32) -> bool {
    crate::platform::process_alive(pid)
}

/// Sends SIGTERM to `pid`, which starts the daemon's graceful drain (Unix;
/// Windows stops through [`stop_command`]).
pub fn send_sigterm(pid: i32) -> Result<(), String> {
    crate::platform::send_sigterm(pid)
}

/// `itervox stop --workflow <workflow>`: how the app asks a daemon to stop
/// where there is no SIGTERM (Windows). The daemon's own `stop` command
/// knows how its platform drains it.
pub fn stop_command(sidecar: &Path, workflow: &Path) -> Command {
    let mut c = Command::new(sidecar);
    c.arg("stop")
        .arg("--workflow")
        .arg(workflow)
        .current_dir(workflow.parent().unwrap_or(Path::new(".")))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    crate::platform::no_console(&mut c);
    c
}

/// Asks the daemon `pid` serving `workflow` to drain and exit, without
/// waiting: SIGTERM on Unix, `itervox stop` on Windows.
pub fn request_stop(sidecar: Option<&Path>, pid: i32, workflow: &str) -> Result<(), String> {
    if Os::current() != Os::Windows {
        return send_sigterm(pid);
    }
    let sidecar = sidecar.ok_or(crate::platform::NO_LOCAL_DAEMON_MESSAGE)?;
    stop_command(sidecar, Path::new(workflow))
        .spawn()
        .map(drop)
        .map_err(|e| format!("could not run {} stop: {e}", sidecar.display()))
}

/// Polls `alive` until it reports false or `timeout` passes; true when the
/// process is gone.
pub fn wait_for_exit(alive: impl Fn() -> bool, timeout: Duration, step: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    loop {
        if !alive() {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(step);
    }
}

/// Picks the record to stop for `workflow` from a fresh `status` listing, so
/// a pid is only signalled while the registry still ties it to that project.
pub fn record_for<'a>(records: &'a [DaemonRecord], workflow: &str) -> Option<&'a DaemonRecord> {
    records.iter().find(|r| r.workflow == workflow)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(unix)]
    use std::os::unix::fs::PermissionsExt;

    #[cfg(unix)]
    #[test]
    fn sidecar_is_found_next_to_the_app_not_on_path() {
        let dir = tempfile::tempdir().unwrap();
        let exe = dir.path().join("itervox-desktop");
        let side = dir.path().join(SIDECAR_NAME);
        std::fs::write(&side, "#!/bin/sh\n").unwrap();
        std::fs::set_permissions(&side, std::fs::Permissions::from_mode(0o755)).unwrap();
        let c = sidecar_candidates(Some(&exe), None);
        assert_eq!(c, vec![side.clone()]);
        assert_eq!(resolve_sidecar(&c).unwrap(), side);
    }

    #[cfg(unix)]
    #[test]
    fn override_must_be_absolute_and_comes_first() {
        let exe = Path::new("/Applications/Itervox.app/Contents/MacOS/itervox-desktop");
        let c = sidecar_candidates(Some(exe), Some("/opt/itervox".into()));
        assert_eq!(c[0], PathBuf::from("/opt/itervox"));
        assert_eq!(
            c[1],
            PathBuf::from("/Applications/Itervox.app/Contents/MacOS/itervox")
        );
        let rel = sidecar_candidates(Some(exe), Some("itervox".into()));
        assert_eq!(rel.len(), 1, "a relative override would search cwd/PATH");
    }

    #[cfg(unix)]
    #[test]
    fn non_executable_sidecar_is_an_error() {
        let dir = tempfile::tempdir().unwrap();
        let side = dir.path().join(SIDECAR_NAME);
        std::fs::write(&side, "x").unwrap();
        std::fs::set_permissions(&side, std::fs::Permissions::from_mode(0o644)).unwrap();
        let err = resolve_sidecar(std::slice::from_ref(&side)).unwrap_err();
        assert!(err.contains("not found"), "{err}");
    }

    #[cfg(unix)]
    #[test]
    fn start_spec_runs_the_workflow_headless_with_the_token_in_env() {
        let tok = "0123456789abcdef0123";
        let spec = start_spec(
            Path::new("/App/itervox"),
            Path::new("/Users/me/proj/WORKFLOW.md"),
            tok,
            Path::new("/logs/proj.log"),
        )
        .unwrap();
        assert_eq!(spec.args, vec!["--workflow", "/Users/me/proj/WORKFLOW.md"]);
        assert_eq!(spec.cwd, PathBuf::from("/Users/me/proj"));
        assert_eq!(
            spec.env,
            vec![(SUPERVISOR_TOKEN_ENV.to_string(), tok.to_string())]
        );
        assert!(spec.env_remove.contains(&"ITERVOX_API_TOKEN".to_string()));
        assert!(
            !spec.args.iter().any(|a| a.contains(tok)),
            "the token never goes on the command line (visible in ps)"
        );
    }

    #[test]
    fn start_spec_rejects_bad_input() {
        let p = Path::new("/App/itervox");
        let l = Path::new("/l.log");
        assert!(start_spec(p, Path::new("WORKFLOW.md"), &"a".repeat(20), l).is_err());
        assert!(start_spec(p, Path::new("/x/WORKFLOW.md"), "short", l).is_err());
    }

    #[cfg(unix)]
    #[test]
    fn detached_command_survives_in_its_own_group_and_logs_output() {
        let dir = tempfile::tempdir().unwrap();
        let script = dir.path().join("fake-itervox");
        std::fs::write(
            &script,
            "#!/bin/sh\necho \"args=$*\"\necho \"tok=${ITERVOX_SUPERVISOR_TOKEN:+set} api=${ITERVOX_API_TOKEN:-unset}\"\necho \"pgid=$(ps -o pgid= -p $$ | tr -d ' ') pid=$$\"\n",
        )
        .unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
        let wf = dir.path().join("WORKFLOW.md");
        std::fs::write(&wf, "---\n").unwrap();
        let log = dir.path().join("out").join("p.log");
        let spec = start_spec(&script, &wf, &"t".repeat(32), &log).unwrap();
        let mut cmd = spec.command().unwrap();
        let envs: Vec<_> = cmd.get_envs().collect();
        assert!(envs.contains(&(std::ffi::OsStr::new("ITERVOX_API_TOKEN"), None)));
        assert!(envs
            .iter()
            .any(|(k, v)| *k == SUPERVISOR_TOKEN_ENV && v.is_some()));
        let status = cmd.status().unwrap();
        assert!(status.success());
        let out = std::fs::read_to_string(&log).unwrap();
        assert!(
            out.contains(&format!("args=--workflow {}", wf.display())),
            "{out}"
        );
        assert!(out.contains("tok=set api=unset"), "{out}");
        let line = out.lines().find(|l| l.starts_with("pgid=")).unwrap();
        let (pg, pid) = line.split_once(' ').unwrap();
        assert_eq!(
            pg.trim_start_matches("pgid="),
            pid.trim_start_matches("pid="),
            "the daemon leads its own process group"
        );
    }

    #[cfg(unix)]
    #[test]
    fn stop_signals_and_waits_for_exit() {
        let mut child = Command::new("sleep").arg("30").spawn().unwrap();
        let pid = child.id() as i32;
        assert!(process_alive(pid));
        send_sigterm(pid).unwrap();
        let _ = child.wait();
        assert!(wait_for_exit(
            || process_alive(pid),
            Duration::from_secs(5),
            Duration::from_millis(10)
        ));
        assert!(send_sigterm(1).is_err());
        assert!(!process_alive(0));
    }

    #[test]
    fn wait_for_exit_times_out() {
        assert!(!wait_for_exit(
            || true,
            Duration::from_millis(30),
            Duration::from_millis(5)
        ));
    }

    #[cfg(unix)]
    #[test]
    fn list_daemons_runs_status_all_json() {
        let dir = tempfile::tempdir().unwrap();
        let script = dir.path().join("itervox");
        std::fs::write(
            &script,
            "#!/bin/sh\nif [ \"$*\" = \"status --all --json\" ]; then echo '[{\"schema_version\":1,\"pid\":7,\"workflow\":\"/p/WORKFLOW.md\"}]'; else exit 3; fi\n",
        )
        .unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
        let recs = list_daemons(&script).unwrap();
        assert_eq!(recs.len(), 1);
        assert_eq!(recs[0].pid, 7);
        assert!(record_for(&recs, "/p/WORKFLOW.md").is_some());
        assert!(record_for(&recs, "/q/WORKFLOW.md").is_none());

        std::fs::write(&script, "#!/bin/sh\necho boom >&2\nexit 1\n").unwrap();
        let err = list_daemons(&script).unwrap_err();
        assert!(err.contains("boom"), "{err}");
    }

    #[test]
    fn output_log_name_is_safe_and_distinct() {
        let a = output_log_name("/a/my proj/WORKFLOW.md");
        let b = output_log_name("/b/my proj/WORKFLOW.md");
        assert!(a.starts_with("my_proj-") && a.ends_with(".log"), "{a}");
        assert_ne!(a, b);
    }

    /// #260: Windows without itervox.exe says what works instead; the
    /// stop there goes through the daemon's own `stop` command.
    #[test]
    fn windows_no_local_daemon_message_and_stop_command() {
        let c = vec![PathBuf::from("/A/itervox")];
        let win = missing_sidecar_message(Os::Windows, &c);
        assert!(
            win.contains("Local projects need WSL or a remote daemon"),
            "{win}"
        );
        let mac = missing_sidecar_message(Os::Mac, &c);
        assert!(
            mac.contains("was not found (looked at /A/itervox)"),
            "{mac}"
        );
        assert_eq!(missing_sidecar_message(Os::Linux, &c), mac);
        // Without itervox.exe the tray lists no local daemons, without an
        // error line; elsewhere a missing daemon is an error.
        assert_eq!(list_without_sidecar(Os::Windows, &win), Ok(Vec::new()));
        assert_eq!(list_without_sidecar(Os::Linux, &mac), Err(mac.clone()));
        let cmd = stop_command(Path::new("/A/itervox"), Path::new("/p/WORKFLOW.md"));
        let args: Vec<_> = cmd.get_args().map(|a| a.to_string_lossy()).collect();
        assert_eq!(args, ["stop", "--workflow", "/p/WORKFLOW.md"]);
        assert_eq!(cmd.get_current_dir(), Some(Path::new("/p")));
    }

    #[test]
    fn last_line_skips_blank_lines() {
        assert_eq!(last_line("a\nb error  \n\n"), "b error");
        assert_eq!(last_line(""), "");
    }
}
