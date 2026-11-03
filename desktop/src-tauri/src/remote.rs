//! Remote connections (#156): daemons on another machine, reached through a
//! tunnel the app runs — SSH, or GCP IAP.
//!
//! A connection is a host (an SSH destination, or a GCP VM with its zone and
//! project) and the daemons found there by `itervox status --all --json`,
//! run on the host over the same transport. Each daemon gets its own tunnel
//! and its own local port ([`crate::tunnel`]); its token is stored under a
//! key made of the connection and the daemon's remote WORKFLOW.md
//! ([`token_key`]), never the local port, so two remotes never share one.
//! Agent credentials stay on the host: the app only reads the daemon's API
//! token (its `token_file`, read over the transport, or typed in).
//!
//! The connections are kept in `remotes.json` in the app config directory.

use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};

use crate::shellpath::Tools;
use crate::status::{parse_status, DaemonRecord};

/// File name inside the app config dir.
pub const REMOTES_FILE: &str = "remotes.json";

const FILE_VERSION: u32 = 1;

/// The daemon port when a record names none (`server.port` default).
pub const DEFAULT_REMOTE_PORT: u16 = 8090;

/// How long discovery or a token read over the transport may take (an IAP
/// SSH handshake alone can take several seconds).
pub const REMOTE_EXEC_TIMEOUT: Duration = Duration::from_secs(45);

/// Prefix of every remote daemon key, so it never equals a local project key
/// (an absolute path).
pub const REMOTE_KEY_PREFIX: &str = "remote/";

/// How a GCP IAP connection reaches the daemon.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum IapMode {
    /// SSH through IAP (`gcloud compute ssh --tunnel-through-iap -- -N -L`):
    /// reaches a daemon bound to the VM's loopback and needs only the SSH
    /// firewall rule the deploy kit creates. The default.
    #[default]
    Ssh,
    /// A plain IAP TCP tunnel to the daemon port
    /// (`gcloud compute start-iap-tunnel <vm> <port>`): needs a daemon bound
    /// to the VM's network interface and an IAP firewall rule for the port.
    Port,
}

/// How the app reaches the host.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Transport {
    /// `ssh <destination>`: `user@host`, a host, or a `~/.ssh/config` alias.
    Ssh { destination: String },
    /// A GCP VM reached through Identity-Aware Proxy.
    Iap {
        instance: String,
        #[serde(default)]
        zone: String,
        #[serde(default)]
        project: String,
        #[serde(default)]
        mode: IapMode,
    },
}

impl Transport {
    /// Rejects values that would be read as options by ssh or gcloud, or
    /// split into several arguments.
    pub fn validate(&self) -> Result<(), String> {
        let check = |what: &str, v: &str, required: bool| -> Result<(), String> {
            if v.is_empty() {
                return if required {
                    Err(format!("{what} is required"))
                } else {
                    Ok(())
                };
            }
            if v.starts_with('-') {
                return Err(format!("{what} must not start with '-'"));
            }
            if v.chars().any(|c| c.is_whitespace() || c.is_control()) {
                return Err(format!("{what} must not contain spaces"));
            }
            Ok(())
        };
        match self {
            Transport::Ssh { destination } => check("the SSH destination", destination, true),
            Transport::Iap {
                instance,
                zone,
                project,
                ..
            } => {
                check("the VM name", instance, true)?;
                check("the zone", zone, false)?;
                check("the project", project, false)
            }
        }
    }

    /// Short description for the menu, e.g. `ssh me@box` or `iap vm (zone)`.
    pub fn label(&self) -> String {
        match self {
            Transport::Ssh { destination } => format!("ssh {destination}"),
            Transport::Iap { instance, zone, .. } if !zone.is_empty() => {
                format!("IAP {instance} ({zone})")
            }
            Transport::Iap { instance, .. } => format!("IAP {instance}"),
        }
    }

    fn iap_scope(zone: &str, project: &str) -> Vec<String> {
        let mut a = Vec::new();
        if !zone.is_empty() {
            a.push(format!("--zone={zone}"));
        }
        if !project.is_empty() {
            a.push(format!("--project={project}"));
        }
        a
    }
}

/// SSH options for every connection: no prompts (there is no terminal to
/// answer them), bounded connects, and a dead connection noticed within
/// about 45 s.
pub const SSH_COMMON_OPTS: &[&str] = &[
    "-o",
    "BatchMode=yes",
    "-o",
    "ConnectTimeout=15",
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=3",
];

/// One daemon on a remote host.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RemoteDaemon {
    /// The daemon's WORKFLOW.md path on the host; with the connection id, the
    /// daemon's identity.
    pub workflow: String,
    /// The address the daemon listens on, as seen on the host.
    #[serde(default)]
    pub remote_host: String,
    pub remote_port: u16,
    #[serde(default)]
    pub token_required: bool,
    #[serde(default)]
    pub token_source: String,
    /// The daemon's auto-generated token file on the host, if any.
    #[serde(default)]
    pub token_file: String,
    /// The local port it was last forwarded to; reused when free, so an open
    /// dashboard window keeps its address across reconnects.
    #[serde(default)]
    pub local_port: u16,
}

impl RemoteDaemon {
    /// Display name: the project directory's name.
    pub fn name(&self) -> String {
        Path::new(&self.workflow)
            .parent()
            .and_then(Path::file_name)
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_else(|| self.workflow.clone())
    }

    /// The host the tunnel forwards to on the remote side: loopback for a
    /// daemon bound to loopback or every interface, else its address.
    pub fn forward_host(&self) -> String {
        let h = self.remote_host.trim();
        match h {
            "" | "0.0.0.0" | "localhost" | "::" | "[::]" => "127.0.0.1".into(),
            h if h.starts_with("127.") => h.into(),
            h if h.contains(':') && !h.starts_with('[') => format!("[{h}]"),
            h => h.into(),
        }
    }
}

/// The daemons of a `status --all --json` listing. A record without a port
/// (a daemon from an older release, found by process scan) is assumed on
/// the default port [`DEFAULT_REMOTE_PORT`] unless another daemon already
/// has it.
pub fn daemons_from_records(records: &[DaemonRecord]) -> Vec<RemoteDaemon> {
    let mut out: Vec<RemoteDaemon> = Vec::new();
    let mut sorted: Vec<&DaemonRecord> = records.iter().collect();
    // Daemons with a known port first, so a guess never takes a real one.
    sorted.sort_by_key(|r| r.port == 0);
    for r in sorted {
        let port = if r.port == 0 {
            DEFAULT_REMOTE_PORT
        } else {
            r.port
        };
        if out
            .iter()
            .any(|d| d.workflow == r.workflow || d.remote_port == port)
        {
            continue;
        }
        out.push(RemoteDaemon {
            workflow: r.workflow.clone(),
            remote_host: r.host.clone(),
            remote_port: port,
            token_required: r.token_required,
            token_source: r.token_source.clone(),
            token_file: r.token_file.clone(),
            local_port: 0,
        });
    }
    out
}

/// One remote host.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Connection {
    /// Stable, unique, `[a-z0-9-]`; part of every key of its daemons.
    pub id: String,
    pub name: String,
    pub transport: Transport,
    /// How to run itervox on the host (e.g. `itervox`,
    /// `/usr/local/bin/itervox`, `sudo -u itervox itervox`).
    #[serde(default = "default_itervox_command")]
    pub itervox_command: String,
    #[serde(default)]
    pub daemons: Vec<RemoteDaemon>,
}

fn default_itervox_command() -> String {
    "itervox".into()
}

impl Connection {
    pub fn new(id: String, name: String, transport: Transport) -> Self {
        Connection {
            id,
            name,
            transport,
            itervox_command: default_itervox_command(),
            daemons: Vec::new(),
        }
    }

    /// Replaces the daemon list with a fresh discovery, keeping each known
    /// daemon's local port.
    pub fn set_daemons(&mut self, found: Vec<RemoteDaemon>) {
        let old = std::mem::take(&mut self.daemons);
        self.daemons = found
            .into_iter()
            .map(|mut d| {
                if let Some(o) = old.iter().find(|o| o.workflow == d.workflow) {
                    d.local_port = o.local_port;
                }
                d
            })
            .collect();
    }
}

/// The key of a remote daemon: its token's Keychain account, its tunnel id
/// and its row in the menu. Made of the connection id and the remote
/// WORKFLOW.md path — never the local port, which can change.
pub fn token_key(connection_id: &str, workflow: &str) -> String {
    format!("{REMOTE_KEY_PREFIX}{connection_id}/{workflow}")
}

/// Whether `key` names a remote daemon.
pub fn is_remote_key(key: &str) -> bool {
    key.starts_with(REMOTE_KEY_PREFIX)
}

/// A connection id from a name, unique among `taken`.
pub fn connection_id(name: &str, taken: &[&str]) -> String {
    let mut base: String = name
        .to_ascii_lowercase()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect();
    base = base
        .split('-')
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>()
        .join("-");
    if base.is_empty() {
        base = "remote".into();
    }
    base.truncate(40);
    let mut id = base.clone();
    let mut n = 2;
    while taken.contains(&id.as_str()) {
        id = format!("{base}-{n}");
        n += 1;
    }
    id
}

#[derive(Serialize, Deserialize)]
struct FileShape {
    version: u32,
    connections: Vec<Connection>,
}

/// The saved connections.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Remotes {
    pub connections: Vec<Connection>,
}

impl Remotes {
    /// Loads the file; missing is empty, corrupt is an error (kept by the
    /// caller rather than overwritten).
    pub fn load(path: &Path) -> Result<Self, String> {
        match std::fs::read(path) {
            Ok(bytes) => {
                let shape: FileShape = serde_json::from_slice(&bytes)
                    .map_err(|e| format!("{}: {e}", path.display()))?;
                if shape.version != FILE_VERSION {
                    return Err(format!(
                        "{}: unsupported version {}",
                        path.display(),
                        shape.version
                    ));
                }
                Ok(Self {
                    connections: shape.connections,
                })
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Self::default()),
            Err(e) => Err(format!("{}: {e}", path.display())),
        }
    }

    /// Writes the file atomically.
    pub fn save(&self, path: &Path) -> Result<(), String> {
        let dir = path
            .parent()
            .ok_or_else(|| format!("{}: no parent directory", path.display()))?;
        std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
        let data = serde_json::to_vec_pretty(&FileShape {
            version: FILE_VERSION,
            connections: self.connections.clone(),
        })
        .map_err(|e| e.to_string())?;
        let tmp = path.with_extension("json.tmp");
        {
            let mut f =
                std::fs::File::create(&tmp).map_err(|e| format!("{}: {e}", tmp.display()))?;
            f.write_all(&data)
                .and_then(|_| f.write_all(b"\n"))
                .and_then(|_| f.sync_all())
                .map_err(|e| format!("{}: {e}", tmp.display()))?;
        }
        std::fs::rename(&tmp, path).map_err(|e| format!("{}: {e}", path.display()))
    }

    pub fn get(&self, id: &str) -> Option<&Connection> {
        self.connections.iter().find(|c| c.id == id)
    }

    pub fn get_mut(&mut self, id: &str) -> Option<&mut Connection> {
        self.connections.iter_mut().find(|c| c.id == id)
    }

    pub fn ids(&self) -> Vec<&str> {
        self.connections.iter().map(|c| c.id.as_str()).collect()
    }

    /// Adds a connection, or replaces the one with its id.
    pub fn upsert(&mut self, c: Connection) {
        match self.get_mut(&c.id) {
            Some(old) => *old = c,
            None => self.connections.push(c),
        }
    }

    pub fn remove(&mut self, id: &str) -> Option<Connection> {
        let i = self.connections.iter().position(|c| c.id == id)?;
        Some(self.connections.remove(i))
    }

    /// The connection and daemon behind a remote key.
    pub fn daemon(&self, key: &str) -> Option<(&Connection, &RemoteDaemon)> {
        self.connections.iter().find_map(|c| {
            c.daemons
                .iter()
                .find(|d| token_key(&c.id, &d.workflow) == key)
                .map(|d| (c, d))
        })
    }

    /// Records the local port a daemon's tunnel uses; true when it changed.
    pub fn set_local_port(&mut self, key: &str, port: u16) -> bool {
        for c in &mut self.connections {
            let id = c.id.clone();
            for d in &mut c.daemons {
                if token_key(&id, &d.workflow) == key && d.local_port != port {
                    d.local_port = port;
                    return true;
                }
            }
        }
        false
    }
}

/// Quotes `s` for a POSIX shell (the remote login shell runs the command
/// line ssh sends).
pub fn shell_quote(s: &str) -> String {
    if !s.is_empty()
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"/._-+=:@,".contains(&b))
    {
        return s.to_string();
    }
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// The program and arguments that run `remote_command` (a shell command
/// line) on the host.
pub fn exec_argv(
    transport: &Transport,
    remote_command: &str,
    tools: &Tools,
) -> Result<(std::path::PathBuf, Vec<String>), String> {
    transport.validate()?;
    match transport {
        Transport::Ssh { destination } => {
            let mut a: Vec<String> = SSH_COMMON_OPTS.iter().map(|s| s.to_string()).collect();
            a.extend(["-T".into(), "--".into(), destination.clone()]);
            a.push(remote_command.into());
            Ok((tools.ssh()?.to_path_buf(), a))
        }
        Transport::Iap {
            instance,
            zone,
            project,
            ..
        } => {
            let mut a = vec![
                "compute".to_string(),
                "ssh".into(),
                instance.clone(),
                "--tunnel-through-iap".into(),
                "--quiet".into(),
            ];
            a.extend(Transport::iap_scope(zone, project));
            a.push(format!("--command={remote_command}"));
            a.push("--".into());
            a.extend(SSH_COMMON_OPTS.iter().map(|s| s.to_string()));
            a.push("-T".into());
            Ok((tools.gcloud()?.to_path_buf(), a))
        }
    }
}

/// The program and arguments of the tunnel that forwards
/// `127.0.0.1:<local_port>` to the daemon.
pub fn tunnel_argv(
    transport: &Transport,
    daemon_host: &str,
    remote_port: u16,
    local_port: u16,
    tools: &Tools,
) -> Result<(std::path::PathBuf, Vec<String>), String> {
    transport.validate()?;
    let forward = format!("127.0.0.1:{local_port}:{daemon_host}:{remote_port}");
    let ssh_forward_opts = |a: &mut Vec<String>| {
        a.extend(SSH_COMMON_OPTS.iter().map(|s| s.to_string()));
        a.extend([
            "-o".into(),
            "ExitOnForwardFailure=yes".into(),
            "-N".into(),
            "-T".into(),
            "-L".into(),
            forward.clone(),
        ]);
    };
    match transport {
        Transport::Ssh { destination } => {
            let mut a = Vec::new();
            ssh_forward_opts(&mut a);
            a.extend(["--".into(), destination.clone()]);
            Ok((tools.ssh()?.to_path_buf(), a))
        }
        Transport::Iap {
            instance,
            zone,
            project,
            mode: IapMode::Ssh,
        } => {
            let mut a = vec![
                "compute".to_string(),
                "ssh".into(),
                instance.clone(),
                "--tunnel-through-iap".into(),
                "--quiet".into(),
            ];
            a.extend(Transport::iap_scope(zone, project));
            a.push("--".into());
            ssh_forward_opts(&mut a);
            Ok((tools.gcloud()?.to_path_buf(), a))
        }
        Transport::Iap {
            instance,
            zone,
            project,
            mode: IapMode::Port,
        } => {
            let mut a = vec![
                "compute".to_string(),
                "start-iap-tunnel".into(),
                instance.clone(),
                remote_port.to_string(),
                format!("--local-host-port=localhost:{local_port}"),
            ];
            a.extend(Transport::iap_scope(zone, project));
            Ok((tools.gcloud()?.to_path_buf(), a))
        }
    }
}

/// Runs a command with the tools' PATH, no stdin, its own process group, and
/// a timeout; returns stdout, or the last line of stderr on failure.
pub fn run_bounded(
    program: &Path,
    args: &[String],
    tools: &Tools,
    timeout: Duration,
) -> Result<Vec<u8>, String> {
    use std::io::Read;
    use std::os::unix::process::CommandExt;
    let mut child = Command::new(program)
        .args(args)
        .env("PATH", &tools.path_env)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .spawn()
        .map_err(|e| format!("could not run {}: {e}", program.display()))?;
    let mut out = child.stdout.take().ok_or("no stdout")?;
    let mut err = child.stderr.take().ok_or("no stderr")?;
    let out_t = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = (&mut out).take(16 << 20).read_to_end(&mut b);
        b
    });
    let err_t = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = (&mut err).take(1 << 20).read_to_end(&mut b);
        b
    });
    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(s)) => break s,
            Ok(None) if Instant::now() >= deadline => {
                crate::shellpath::kill_group(child.id());
                let _ = child.wait();
                return Err(format!(
                    "{} did not finish within {timeout:?}",
                    program.display()
                ));
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(20)),
            Err(e) => return Err(e.to_string()),
        }
    };
    let stdout = out_t.join().unwrap_or_default();
    let stderr = err_t.join().unwrap_or_default();
    if !status.success() {
        let line = crate::supervisor::last_line(&String::from_utf8_lossy(&stderr));
        return Err(if line.is_empty() {
            format!("{} failed ({status})", program.display())
        } else {
            line
        });
    }
    Ok(stdout)
}

/// "Add remote": lists the daemons on the host by running
/// `itervox status --all --json` there over the connection's transport.
pub fn discover(conn: &Connection, tools: &Tools) -> Result<Vec<DaemonRecord>, String> {
    if conn.itervox_command.trim().is_empty() {
        return Err("the itervox command for this host is empty".into());
    }
    let cmd = format!("{} status --all --json", conn.itervox_command.trim());
    let (prog, args) = exec_argv(&conn.transport, &cmd, tools)?;
    let out = run_bounded(&prog, &args, tools, REMOTE_EXEC_TIMEOUT).map_err(|e| {
        format!(
            "listing the daemons on {} failed: {e}",
            conn.transport.label()
        )
    })?;
    parse_status(&out).map_err(|e| e.to_string())
}

/// Reads a remote daemon's auto-generated token from its `token_file` over
/// the transport. The token is returned, never logged.
pub fn read_remote_token(
    conn: &Connection,
    daemon: &RemoteDaemon,
    tools: &Tools,
) -> Result<String, String> {
    if daemon.token_file.is_empty() {
        return Err("this daemon has no token file; enter its token".into());
    }
    let cmd = format!("cat -- {}", shell_quote(&daemon.token_file));
    let (prog, args) = exec_argv(&conn.transport, &cmd, tools)?;
    let out = run_bounded(&prog, &args, tools, REMOTE_EXEC_TIMEOUT).map_err(|e| {
        format!(
            "reading the token on {} failed: {e}",
            conn.transport.label()
        )
    })?;
    let tok = String::from_utf8_lossy(&out).trim().to_string();
    validate_api_token(&tok)?;
    Ok(tok)
}

/// A token the app will send as a bearer token: non-empty printable ASCII
/// without spaces, at most 512 characters.
pub fn validate_api_token(tok: &str) -> Result<(), String> {
    if tok.is_empty() || tok.len() > crate::token::MAX_TOKEN_LEN {
        return Err("the token is empty or too long".into());
    }
    if !tok.bytes().all(|c| (0x21..=0x7e).contains(&c)) {
        return Err("the token must be printable ASCII without spaces".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::OsString;
    use std::path::PathBuf;

    fn tools() -> Tools {
        Tools {
            path_env: OsString::from("/usr/bin"),
            ssh: Some(PathBuf::from("/usr/bin/ssh")),
            gcloud: Some(PathBuf::from("/sdk/gcloud")),
            path_source: "inherited",
        }
    }

    fn ssh(dest: &str) -> Transport {
        Transport::Ssh {
            destination: dest.into(),
        }
    }

    #[test]
    fn ssh_tunnel_forwards_a_loopback_port_and_exits_on_forward_failure() {
        let (p, a) = tunnel_argv(&ssh("me@box"), "127.0.0.1", 8090, 51234, &tools()).unwrap();
        assert_eq!(p, PathBuf::from("/usr/bin/ssh"));
        let s = a.join(" ");
        assert!(s.contains("-o ExitOnForwardFailure=yes"), "{s}");
        assert!(s.contains("-o ServerAliveInterval=15"), "{s}");
        assert!(s.contains("-o BatchMode=yes"), "{s}");
        assert!(s.contains("-N"), "{s}");
        assert!(
            s.ends_with("-L 127.0.0.1:51234:127.0.0.1:8090 -- me@box"),
            "{s}"
        );
    }

    #[test]
    fn iap_tunnels_use_gcloud_with_zone_and_project() {
        let iap = |mode| Transport::Iap {
            instance: "itervox".into(),
            zone: "us-central1-a".into(),
            project: "acme".into(),
            mode,
        };
        let (p, a) = tunnel_argv(&iap(IapMode::Port), "127.0.0.1", 8090, 50001, &tools()).unwrap();
        assert_eq!(p, PathBuf::from("/sdk/gcloud"));
        assert_eq!(
            a,
            vec![
                "compute",
                "start-iap-tunnel",
                "itervox",
                "8090",
                "--local-host-port=localhost:50001",
                "--zone=us-central1-a",
                "--project=acme"
            ]
        );
        let (_, a) = tunnel_argv(&iap(IapMode::Ssh), "127.0.0.1", 8090, 50001, &tools()).unwrap();
        let s = a.join(" ");
        assert!(
            s.starts_with("compute ssh itervox --tunnel-through-iap --quiet --zone=us-central1-a --project=acme -- "),
            "{s}"
        );
        assert!(s.ends_with("-L 127.0.0.1:50001:127.0.0.1:8090"), "{s}");
        let (_, a) =
            exec_argv(&iap(IapMode::Ssh), "itervox status --all --json", &tools()).unwrap();
        assert!(a.contains(&"--command=itervox status --all --json".to_string()));
    }

    #[test]
    fn option_injection_and_missing_tools_are_refused() {
        assert!(tunnel_argv(&ssh("-oProxyCommand=x"), "127.0.0.1", 1, 2, &tools()).is_err());
        assert!(tunnel_argv(&ssh("a b"), "127.0.0.1", 1, 2, &tools()).is_err());
        assert!(tunnel_argv(&ssh(""), "127.0.0.1", 1, 2, &tools()).is_err());
        let bad = Transport::Iap {
            instance: "vm".into(),
            zone: "--zone=x".into(),
            project: String::new(),
            mode: IapMode::Ssh,
        };
        assert!(bad.validate().is_err());
        let mut t = tools();
        t.gcloud = None;
        let iap = Transport::Iap {
            instance: "vm".into(),
            zone: String::new(),
            project: String::new(),
            mode: IapMode::Port,
        };
        assert!(tunnel_argv(&iap, "127.0.0.1", 1, 2, &t)
            .unwrap_err()
            .contains("gcloud"));
    }

    #[test]
    fn forward_host_maps_wildcards_to_loopback() {
        let mut d = RemoteDaemon {
            workflow: "/srv/app/WORKFLOW.md".into(),
            remote_host: "0.0.0.0".into(),
            remote_port: 8090,
            token_required: true,
            token_source: "generated".into(),
            token_file: String::new(),
            local_port: 0,
        };
        assert_eq!(d.forward_host(), "127.0.0.1");
        d.remote_host = "10.0.0.5".into();
        assert_eq!(d.forward_host(), "10.0.0.5");
        d.remote_host = "::1".into();
        assert_eq!(d.forward_host(), "[::1]");
        assert_eq!(d.name(), "app");
    }

    #[test]
    fn shell_quote_survives_quotes_and_spaces() {
        assert_eq!(shell_quote("/a/b-c.txt"), "/a/b-c.txt");
        assert_eq!(shell_quote("/a b/it's"), r"'/a b/it'\''s'");
        assert_eq!(shell_quote(""), "''");
        assert_eq!(shell_quote("$(rm -rf /)"), "'$(rm -rf /)'");
    }

    /// Two remotes never share token storage: the key names the connection
    /// and the remote WORKFLOW.md, and never collides with a local project.
    #[test]
    fn token_keys_are_per_connection_and_daemon() {
        let a = token_key("prod", "/srv/app/WORKFLOW.md");
        let b = token_key("staging", "/srv/app/WORKFLOW.md");
        assert_ne!(a, b);
        assert!(is_remote_key(&a) && !is_remote_key("/srv/app/WORKFLOW.md"));
        assert_ne!(
            token_key("prod", "/srv/app/WORKFLOW.md"),
            token_key("prod", "/srv/other/WORKFLOW.md")
        );
    }

    #[test]
    fn connection_ids_are_slugs_and_unique() {
        assert_eq!(connection_id("Prod VM (EU)", &[]), "prod-vm-eu");
        assert_eq!(
            connection_id("Prod VM (EU)", &["prod-vm-eu"]),
            "prod-vm-eu-2"
        );
        assert_eq!(connection_id("!!!", &[]), "remote");
    }

    #[test]
    fn remotes_round_trip_and_keep_local_ports_on_rediscovery() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join(REMOTES_FILE);
        assert_eq!(Remotes::load(&path).unwrap(), Remotes::default());
        let rec = |wf: &str, port| DaemonRecord {
            schema_version: 1,
            pid: 1,
            workflow: wf.into(),
            host: "127.0.0.1".into(),
            port,
            token_required: true,
            token_source: "generated".into(),
            token_file: "/home/x/.itervox/logs/a/api-token".into(),
            ..Default::default()
        };
        let mut c = Connection::new("prod".into(), "Prod".into(), ssh("me@box"));
        c.set_daemons(daemons_from_records(&[
            rec("/b/WORKFLOW.md", 0),
            rec("/a/WORKFLOW.md", 8090),
        ]));
        assert_eq!(
            c.daemons.len(),
            1,
            "a portless record does not take a known daemon's port"
        );
        assert_eq!(c.daemons[0].workflow, "/a/WORKFLOW.md");
        assert_eq!(
            daemons_from_records(&[rec("/old/WORKFLOW.md", 0)])[0].remote_port,
            DEFAULT_REMOTE_PORT
        );
        let mut r = Remotes::default();
        r.upsert(c);
        assert!(r.set_local_port(&token_key("prod", "/a/WORKFLOW.md"), 50123));
        r.save(&path).unwrap();
        let mut back = Remotes::load(&path).unwrap();
        assert_eq!(back, r);
        let c = back.get_mut("prod").unwrap();
        c.set_daemons(daemons_from_records(&[
            rec("/a/WORKFLOW.md", 8091),
            rec("/c/WORKFLOW.md", 8092),
        ]));
        assert_eq!(c.daemons[0].local_port, 50123);
        assert_eq!(c.daemons[0].remote_port, 8091);
        assert_eq!(c.daemons[1].local_port, 0);
        std::fs::write(&path, b"{oops").unwrap();
        assert!(Remotes::load(&path).is_err());
    }

    #[test]
    fn api_tokens_are_validated() {
        assert!(validate_api_token("").is_err());
        assert!(validate_api_token("a b").is_err());
        assert!(validate_api_token(&"x".repeat(513)).is_err());
        assert!(validate_api_token("abc123").is_ok());
    }
}
