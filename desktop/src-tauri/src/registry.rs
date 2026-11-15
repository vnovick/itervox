//! Reading the host daemon registry (`~/.itervox/daemons/<pid>.json`, the
//! #119 supervisor contract) in-process (#212), instead of spawning
//! `itervox status --all --json` on every refresh.
//!
//! This mirrors `cmd/itervox/daemon_registry.go` (`readDaemonRegistry`): a
//! record is listed while its pid is alive, the project's
//! `.itervox/daemon.pid` names that pid and, for a lock-marked pid file, the
//! pid lock is held; records that fail the check, and unreadable ones, are
//! deleted, exactly as `status` deletes them. Both sides run the shared
//! fixture `cmd/itervox/testdata/daemon_registry_parity.json`.
//!
//! What it does not do is the process scan for daemons started by binaries
//! older than the registry; [`DaemonLister`] runs the sidecar for that now
//! and then, and whenever a record has a schema version this reader does not
//! know.

use std::io::ErrorKind;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use crate::status::{DaemonRecord, SOURCE_PROCESS_SCAN, SUPPORTED_SCHEMA_VERSION};
use crate::supervisor::process_alive;

/// The third field of a pid record written by a daemon holding the pid lock.
const PID_LOCK_MARKER: &str = "flock";

/// `source` of a record read from the registry.
pub const SOURCE_REGISTRY: &str = "registry";

/// How often the sidecar's full listing (with the process scan) runs at
/// most, when the menu opens. It also runs at app start.
pub const SWEEP_MIN_INTERVAL: Duration = Duration::from_secs(30);

/// Why the in-process reader cannot answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RegistryError {
    /// A record carries a schema version this app does not know: its fields
    /// may mean something else, so the bundled daemon decides.
    UnknownSchema(u64),
    /// The registry directory exists but cannot be read.
    Unreadable(String),
}

/// `~/.itervox/daemons`.
pub fn registry_dir() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(|h| PathBuf::from(h).join(".itervox").join("daemons"))
}

/// Whether a record's pid is still its project's daemon.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Ownership {
    Owned,
    /// Gone: the record may be deleted.
    Stale,
    /// The pid lock could not be probed: skip the record, keep the file.
    Unknown,
}

/// `<dir of workflow, symlinks resolved>/.itervox/daemon.pid`, like Go's
/// `pidFilePath`.
fn pid_file_path(workflow: &Path) -> PathBuf {
    let dir = workflow.parent().unwrap_or(Path::new("/"));
    let dir = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf());
    dir.join(".itervox").join("daemon.pid")
}

/// A parsed pid file: `<pid>\t<workflow>[\tflock]`.
fn parse_pid_record(data: &str) -> Option<(i32, bool)> {
    let line = data.trim();
    let parts: Vec<&str> = line.split('\t').collect();
    let pid = parts.first()?.parse::<i32>().ok()?;
    let locked = parts.len() >= 3 && parts.last() == Some(&PID_LOCK_MARKER);
    Some((pid, locked))
}

/// Lock state of `.itervox/runtime/daemon.pid.lock`, as `itervox stop`
/// probes it: a missing runtime directory means no lock-holding daemon ever
/// ran there. Probing a free lock holds it for an instant.
fn probe_pid_lock(pid_path: &Path) -> Ownership {
    let runtime = pid_path.parent().unwrap_or(Path::new("/")).join("runtime");
    if matches!(std::fs::symlink_metadata(&runtime), Err(e) if e.kind() == ErrorKind::NotFound) {
        return Ownership::Stale;
    }
    let file = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(runtime.join("daemon.pid.lock"));
    let Ok(file) = file else {
        return Ownership::Unknown;
    };
    match file.try_lock() {
        Ok(()) => Ownership::Stale, // released when `file` drops
        Err(std::fs::TryLockError::WouldBlock) => Ownership::Owned,
        Err(_) => Ownership::Unknown,
    }
}

/// Go's `daemonPIDOwnership`.
fn ownership(pid: i32, workflow: &str) -> Ownership {
    if pid <= 0 || workflow.is_empty() || !process_alive(pid) {
        return Ownership::Stale;
    }
    let pid_path = pid_file_path(Path::new(workflow));
    let Ok(data) = std::fs::read_to_string(&pid_path) else {
        return Ownership::Stale;
    };
    match parse_pid_record(&data) {
        Some((p, _)) if p != pid => Ownership::Stale,
        None => Ownership::Stale,
        Some((_, false)) => Ownership::Owned,
        Some((_, true)) => probe_pid_lock(&pid_path),
    }
}

/// Lists the live daemons recorded in `dir`, sorted by workflow then pid,
/// deleting stale and unreadable records — the rows `itervox status --all
/// --json` prints for them. A missing directory is no daemons.
pub fn read_registry(dir: &Path) -> Result<Vec<DaemonRecord>, RegistryError> {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(RegistryError::Unreadable(e.to_string())),
    };
    let mut files: Vec<PathBuf> = entries
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| !t.is_dir()))
        .map(|e| e.path())
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    files.sort();
    // Check every version before deleting anything: a record of an unknown
    // version is left to the bundled daemon, whose rules may differ.
    let mut parsed = Vec::new();
    for path in files {
        let Ok(data) = std::fs::read(&path) else {
            continue;
        };
        let value = serde_json::from_slice::<serde_json::Value>(&data)
            .ok()
            .filter(serde_json::Value::is_object);
        if let Some(v) = &value {
            let version = v
                .get("schema_version")
                .and_then(serde_json::Value::as_u64)
                .unwrap_or(0);
            if version != u64::from(SUPPORTED_SCHEMA_VERSION) {
                return Err(RegistryError::UnknownSchema(version));
            }
        }
        parsed.push((path, value.and_then(|v| serde_json::from_value(v).ok())));
    }
    let mut out = Vec::new();
    for (path, rec) in parsed {
        let Some(mut rec): Option<DaemonRecord> = rec else {
            let _ = std::fs::remove_file(&path);
            continue;
        };
        match ownership(rec.pid, &rec.workflow) {
            Ownership::Stale => {
                let _ = std::fs::remove_file(&path);
                continue;
            }
            Ownership::Unknown => continue,
            Ownership::Owned => {}
        }
        rec.source = SOURCE_REGISTRY.into();
        out.push(rec);
    }
    sort_records(&mut out);
    Ok(out)
}

/// `status` order: workflow (byte order, like Go's strings.Compare), then pid.
fn sort_records(recs: &mut [DaemonRecord]) {
    recs.sort_by(|a, b| a.workflow.cmp(&b.workflow).then(a.pid.cmp(&b.pid)));
}

/// Lists daemons for the refresh loop: the registry in-process, and the
/// sidecar's `status --all --json` at start, on request (menu open, at most
/// every [`SWEEP_MIN_INTERVAL`]) and whenever the registry cannot be read
/// in-process. Daemons the sidecar found by process scan (started by a
/// binary older than the registry) are kept between sweeps while their pid
/// lives.
#[derive(Default)]
pub struct DaemonLister {
    last_sweep: Option<Instant>,
    scanned: Vec<DaemonRecord>,
}

impl DaemonLister {
    /// One listing. `sweep` asks for the sidecar's full listing (honoured
    /// at most every [`SWEEP_MIN_INTERVAL`], always on the first call);
    /// `sidecar` runs it.
    pub fn list(
        &mut self,
        dir: Option<&Path>,
        sweep: bool,
        now: Instant,
        sidecar: impl FnOnce() -> Result<Vec<DaemonRecord>, String>,
    ) -> Result<Vec<DaemonRecord>, String> {
        let due = match self.last_sweep {
            None => true,
            Some(t) => sweep && now.duration_since(t) >= SWEEP_MIN_INTERVAL,
        };
        let registry = match dir {
            Some(d) if !due => read_registry(d),
            Some(_) => Err(RegistryError::Unreadable("sweep".into())),
            None => Err(RegistryError::Unreadable("no home directory".into())),
        };
        match registry {
            Ok(mut recs) => {
                self.scanned.retain(|s| process_alive(s.pid));
                for s in &self.scanned {
                    if !recs.iter().any(|r| r.pid == s.pid) {
                        recs.push(s.clone());
                    }
                }
                sort_records(&mut recs);
                Ok(recs)
            }
            Err(_) => {
                let recs = sidecar()?;
                self.last_sweep = Some(now);
                self.scanned = recs
                    .iter()
                    .filter(|r| r.source == SOURCE_PROCESS_SCAN)
                    .cloned()
                    .collect();
                Ok(recs)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::status::parse_status;
    use std::cell::Cell;

    const FIXTURE: &str = include_str!("../../../cmd/itervox/testdata/daemon_registry_parity.json");

    /// The pid of a child that has exited and been reaped.
    fn reaped_pid() -> i32 {
        let mut c = std::process::Command::new("true").spawn().unwrap();
        let pid = c.id() as i32;
        c.wait().unwrap();
        pid
    }

    /// Lays the shared fixture out under `root`; returns it parsed and the
    /// lock files held for its `hold_lock` projects.
    fn lay_out(root: &Path) -> (serde_json::Value, Vec<std::fs::File>) {
        let text = FIXTURE
            .replace("__ROOT__", &root.display().to_string())
            .replace("__LIVE_PID__", &std::process::id().to_string())
            .replace("__DEAD_PID__", &reaped_pid().to_string());
        let fx: serde_json::Value = serde_json::from_str(&text).unwrap();
        let mut held = Vec::new();
        for p in fx["projects"].as_array().unwrap() {
            let dir = root.join(p["dir"].as_str().unwrap()).join(".itervox");
            std::fs::create_dir_all(&dir).unwrap();
            if let Some(pf) = p["pid_file"].as_str() {
                std::fs::write(dir.join("daemon.pid"), pf).unwrap();
            }
            let hold = p["hold_lock"].as_bool().unwrap_or(false);
            if hold || p["runtime_dir"].as_bool().unwrap_or(false) {
                std::fs::create_dir_all(dir.join("runtime")).unwrap();
            }
            if hold {
                let f = std::fs::File::create(dir.join("runtime").join("daemon.pid.lock")).unwrap();
                f.try_lock().unwrap();
                held.push(f);
            }
        }
        let reg = root.join("daemons");
        std::fs::create_dir_all(&reg).unwrap();
        for (name, body) in fx["registry"].as_object().unwrap() {
            let content = match body {
                serde_json::Value::String(s) => s.clone(),
                other => other.to_string(),
            };
            std::fs::write(reg.join(name), content).unwrap();
        }
        (fx, held)
    }

    #[test]
    fn registry_read_in_process_matches_status_json() {
        let root = tempfile::tempdir().unwrap();
        let (fx, _held) = lay_out(root.path());
        let got = read_registry(&root.path().join("daemons")).unwrap();
        // `expected` is what `itervox status --all --json` prints for this
        // registry (TestDaemonRegistryParityFixture in cmd/itervox).
        let want = parse_status(fx["expected"].to_string().as_bytes()).unwrap();
        assert_eq!(got, want);
        let mut left: Vec<String> = std::fs::read_dir(root.path().join("daemons"))
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        left.sort();
        let remaining: Vec<String> = serde_json::from_value(fx["remaining"].clone()).unwrap();
        assert_eq!(left, remaining, "stale and unreadable records are pruned");
    }

    #[test]
    fn unknown_schema_version_is_left_to_the_sidecar() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(
            dir.path().join("1.json"),
            r#"{"schema_version":2,"pid":1,"workflow":"/a/WORKFLOW.md"}"#,
        )
        .unwrap();
        assert_eq!(
            read_registry(dir.path()),
            Err(RegistryError::UnknownSchema(2))
        );
        assert!(dir.path().join("1.json").exists(), "nothing deleted");

        let mut lister = DaemonLister::default();
        let t0 = Instant::now();
        let ran = Cell::new(0);
        let sidecar = || {
            ran.set(ran.get() + 1);
            Ok(vec![])
        };
        // The start-up sweep, then the sidecar again (unknown version) even
        // without a sweep request.
        lister.list(Some(dir.path()), false, t0, sidecar).unwrap();
        lister.list(Some(dir.path()), false, t0, sidecar).unwrap();
        assert_eq!(ran.get(), 2);
    }

    #[test]
    fn missing_registry_is_no_daemons() {
        let dir = tempfile::tempdir().unwrap();
        assert_eq!(read_registry(&dir.path().join("nope")), Ok(vec![]));
    }

    #[test]
    fn lister_sweeps_at_start_and_on_request_and_keeps_scanned_daemons() {
        let dir = tempfile::tempdir().unwrap();
        let mut lister = DaemonLister::default();
        let t0 = Instant::now();
        let scanned = DaemonRecord {
            schema_version: 1,
            pid: std::process::id() as i32,
            workflow: "/old/WORKFLOW.md".into(),
            source: SOURCE_PROCESS_SCAN.into(),
            ..Default::default()
        };
        let dead = DaemonRecord {
            pid: reaped_pid(),
            ..scanned.clone()
        };
        let sweep_result = vec![scanned.clone(), dead];
        let ran = Cell::new(0);
        let sidecar = || {
            ran.set(ran.get() + 1);
            Ok(sweep_result.clone())
        };
        let d = Some(dir.path());
        assert_eq!(lister.list(d, false, t0, sidecar).unwrap().len(), 2);
        // In-process from now on; the scanned daemon that still lives stays.
        let early = t0 + Duration::from_secs(5);
        assert_eq!(
            lister.list(d, true, early, sidecar).unwrap(),
            vec![scanned.clone()],
            "a sweep request inside the minimum interval is not honoured"
        );
        let late = t0 + SWEEP_MIN_INTERVAL * 2;
        assert_eq!(lister.list(d, false, late, sidecar).unwrap(), vec![scanned]);
        assert_eq!(ran.get(), 1);
        lister.list(d, true, late, sidecar).unwrap();
        assert_eq!(
            ran.get(),
            2,
            "a menu-open sweep after the interval runs the sidecar"
        );
    }

    #[test]
    fn pid_record_shapes() {
        assert_eq!(
            parse_pid_record("42\t/a/WORKFLOW.md\tflock\n"),
            Some((42, true))
        );
        assert_eq!(parse_pid_record("42\t/a/WORKFLOW.md\n"), Some((42, false)));
        assert_eq!(parse_pid_record("42"), Some((42, false)));
        assert_eq!(parse_pid_record("x\t/a"), None);
    }
}
