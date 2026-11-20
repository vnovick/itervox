//! "Open in Terminal" (#278 phase 1): the Sessions window opens the
//! system terminal attached to a daemon's tmux session.
//!
//! The app attaches; it never starts an agent CLI. The command the terminal
//! runs is the operator's own: `tmux attach -t <name>` for a local daemon,
//! `ssh -t <destination> tmux attach -t <name>` for a remote daemon reached
//! over SSH (the connection's own destination, so `~/.ssh/config` applies),
//! and `gcloud compute ssh <vm> --tunnel-through-iap --command=… -- -t` for
//! a GCP VM reached by SSH through IAP. The daemon keeps owning the session:
//! closing the terminal detaches, and tmux keeps the session running.
//!
//! The terminal is the user's own program, picked with "Terminal app"
//! ([`crate::terminal_app`]); how each one is started is
//! [`crate::terminal_launch`].
//!
//! Safety: every part of the command is checked here and handed to the
//! terminal as an argument vector — Linux's terminals and Windows
//! Terminal get one argument per word, no shell line. On macOS the app
//! writes a one-shot `.command` script (mode 0700, created new, deleting
//! itself when it runs) whose words are each shell-quoted
//! ([`command_file`]), and the terminal gets only its path. A session
//! name must be a plain tmux name (`[A-Za-z0-9_-]`, which is what the
//! daemon's `tmuxctl.ValidSessionName` allows; itervox's own are
//! `itervox-<6 hex>`) and a host a single word that cannot be read as an
//! option. Over SSH the remote login shell reads the command line, which is
//! why nothing but those checked words reaches it.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use crate::platform::Os;
use crate::remote::{shell_quote, IapMode, Transport};
use crate::shellpath::Tools;
use crate::terminal_app::{App, Detected};
use crate::terminal_launch::launch_in;

/// Why a local daemon's sessions cannot be opened on Windows.
pub const WINDOWS_LOCAL_NOTE: &str =
    "Open in Terminal needs tmux, which Windows does not have: run itervox in WSL and add it as an SSH remote.";
/// Why a daemon reached through a plain IAP port tunnel has no terminal.
pub const IAP_PORT_NOTE: &str = "Open in Terminal needs SSH to the VM: this connection uses a plain IAP port tunnel. Switch it to SSH through IAP, or attach from your own terminal.";

/// Keep-alives for the interactive SSH session (the tunnel's own, without
/// `BatchMode`, so ssh may ask for a passphrase or host key in the
/// terminal).
const SSH_KEEPALIVE: [&str; 4] = [
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=3",
];

/// Where a daemon's tmux sessions are attached from.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AttachHost {
    /// This machine: `tmux attach`.
    Local,
    /// `ssh -t <destination>`.
    Ssh { destination: String },
    /// `gcloud compute ssh <instance> --tunnel-through-iap`.
    Iap {
        instance: String,
        zone: String,
        project: String,
    },
}

/// Where the sessions of a daemon reached over `transport` (`None` for a
/// local daemon) are attached from on `os`, or why they cannot be.
pub fn attach_host(os: Os, transport: Option<&Transport>) -> Result<AttachHost, String> {
    match transport {
        None if os == Os::Windows => Err(WINDOWS_LOCAL_NOTE.into()),
        None => Ok(AttachHost::Local),
        Some(Transport::Ssh { destination }) => {
            check_word("the SSH destination", destination, b"@._-:[]%/")?;
            Ok(AttachHost::Ssh {
                destination: destination.clone(),
            })
        }
        Some(Transport::Iap {
            mode: IapMode::Port,
            ..
        }) => Err(IAP_PORT_NOTE.into()),
        Some(Transport::Iap {
            instance,
            zone,
            project,
            mode: IapMode::Ssh,
        }) => {
            check_word("the VM name", instance, b"._-")?;
            for (what, v) in [("the zone", zone), ("the project", project)] {
                if !v.is_empty() {
                    check_word(what, v, b"._-:")?;
                }
            }
            Ok(AttachHost::Iap {
                instance: instance.clone(),
                zone: zone.clone(),
                project: project.clone(),
            })
        }
    }
}

/// `v` is one non-empty word of ASCII letters, digits and `extra`, not
/// starting with `-` (so neither ssh nor gcloud reads it as an option and
/// no shell splits or expands it).
fn check_word(what: &str, v: &str, extra: &[u8]) -> Result<(), String> {
    let ok = !v.is_empty()
        && !v.starts_with('-')
        && v.len() <= 255
        && v.bytes()
            .all(|b| b.is_ascii_alphanumeric() || extra.contains(&b));
    if ok {
        Ok(())
    } else {
        Err(format!("{what} cannot be used in a terminal command."))
    }
}

/// A tmux session name the app may put in an attach command: 1–64 of
/// `[A-Za-z0-9_-]` (the daemon's `tmuxctl.ValidSessionName`), not starting
/// with `-`.
pub fn valid_session_name(name: &str) -> bool {
    (1..=64).contains(&name.len())
        && !name.starts_with('-')
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// A tmux session itervox started ("New session"): `itervox-<6 hex>`.
pub fn is_itervox_session(id: &str) -> bool {
    id.strip_prefix("itervox-").is_some_and(|h| {
        h.len() == 6
            && h.bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    })
}

/// One session row of `GET /api/v1/sessions`, as far as attaching goes.
#[derive(Debug, Clone, Copy, Default)]
pub struct AttachFacts<'a> {
    pub id: &'a str,
    pub owner: &'a str,
    pub kind: &'a str,
    /// The daemon may control it: tmux is available, and for a session
    /// started by hand `sessions.tmux_control` is on and it is in a pane.
    pub controllable: bool,
    /// The pane of a session started by hand, `session:window.pane`.
    pub pane_label: &'a str,
    /// What the daemon says attaches to its own tmux session.
    pub attach_command: &'a str,
}

/// The tmux session the app may attach to for a session row, or `None`.
///
/// - A session itervox started in tmux (`kind: tmux`, `owner: itervox`)
///   whose id is `itervox-<6 hex>`, while the daemon has tmux. When the
///   daemon names its attach command, it must be the plain
///   `tmux attach -t <id>` (a daemon on its own tmux socket is left out).
/// - A session started by hand only when the daemon lets the dashboard
///   control it (`controllable`, which needs `sessions.tmux_control`), by
///   its pane's tmux session, when that name is a plain one.
/// - Nothing else: an ordinary run has no tmux session to attach to.
pub fn attach_name(f: &AttachFacts) -> Option<String> {
    if !f.controllable {
        return None;
    }
    match (f.owner, f.kind) {
        ("itervox", "tmux") => {
            let plain = format!("tmux attach -t {}", f.id);
            (is_itervox_session(f.id) && (f.attach_command.is_empty() || f.attach_command == plain))
                .then(|| f.id.to_string())
        }
        ("external", _) => {
            let (session, _) = f.pane_label.split_once(':')?;
            valid_session_name(session).then(|| session.to_string())
        }
        _ => None,
    }
}

/// The argument vector that attaches to tmux session `name` on `host`.
/// `ssh` / `gcloud` are the programs the app resolved (`Tools`).
pub fn attach_argv(
    host: &AttachHost,
    name: &str,
    ssh: Option<&str>,
    gcloud: Option<&str>,
) -> Result<Vec<String>, String> {
    if !valid_session_name(name) {
        return Err("That tmux session name cannot be used in a terminal command.".into());
    }
    let tmux = ["tmux", "attach", "-t", name].map(String::from);
    Ok(match host {
        AttachHost::Local => tmux.to_vec(),
        AttachHost::Ssh { destination } => {
            check_word("the SSH destination", destination, b"@._-:[]%/")?;
            let ssh = ssh.ok_or("ssh was not found on PATH or in the login shell's PATH")?;
            let mut a = vec![ssh.to_string(), "-t".into()];
            a.extend(SSH_KEEPALIVE.map(String::from));
            a.extend(["--".into(), destination.clone()]);
            a.extend(tmux);
            a
        }
        AttachHost::Iap {
            instance,
            zone,
            project,
        } => {
            check_word("the VM name", instance, b"._-")?;
            let gcloud =
                gcloud.ok_or("gcloud was not found on PATH or in the login shell's PATH")?;
            let mut a = [gcloud, "compute", "ssh", instance, "--tunnel-through-iap"]
                .map(String::from)
                .to_vec();
            if !zone.is_empty() {
                check_word("the zone", zone, b"._-:")?;
                a.push(format!("--zone={zone}"));
            }
            if !project.is_empty() {
                check_word("the project", project, b"._-:")?;
                a.push(format!("--project={project}"));
            }
            a.push(format!("--command={}", tmux.join(" ")));
            a.extend(["--".into(), "-t".into()]);
            a.extend(SSH_KEEPALIVE.map(String::from));
            a
        }
    })
}

/// What starts the terminal: a program and its arguments, and on macOS the
/// `.command` file to write first (path, contents). Built per terminal
/// program by [`crate::terminal_launch::launch_in`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Launch {
    pub program: String,
    pub args: Vec<String>,
    pub script: Option<(PathBuf, String)>,
    /// Windows: start it in a new console (PowerShell, Command Prompt).
    pub new_console: bool,
    /// Windows: the command line after `args`, passed as is (Command
    /// Prompt's quoting, [`crate::terminal_launch::cmd_line`]).
    pub raw_tail: Option<String>,
}

/// The one-shot script the macOS terminals run: it deletes itself, then
/// `exec`s `argv`, each word shell-quoted.
pub fn command_file(argv: &[String]) -> String {
    let words: Vec<String> = argv.iter().map(|w| shell_quote(w)).collect();
    format!(
        "#!/bin/sh\n# Itervox: Open in Terminal. Deletes itself, then attaches.\nrm -f -- \"$0\"\nexec {}\n",
        words.join(" ")
    )
}

/// A fresh path for the macOS `.command` file, in the per-user temp dir.
fn script_path() -> PathBuf {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    std::env::temp_dir().join(format!(
        "itervox-attach-{}-{nanos}.command",
        std::process::id()
    ))
}

/// Writes `body` to a new file at `path` that only this user may read or
/// run (it never replaces an existing file).
fn write_script(path: &Path, body: &str) -> Result<(), String> {
    let mut o = std::fs::OpenOptions::new();
    o.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o700);
    }
    let mut f = o
        .open(path)
        .map_err(|e| format!("Could not write the terminal command file: {e}"))?;
    f.write_all(body.as_bytes())
        .map_err(|e| format!("Could not write the terminal command file: {e}"))
}

/// Opens `app` (the user's terminal program, [`crate::terminal_app`])
/// attached to tmux session `name` on `host`, without waiting for it. Runs
/// ssh/gcloud from `tools`, with its PATH.
pub fn open(host: &AttachHost, name: &str, tools: &Tools, app: &Detected) -> Result<(), String> {
    let os = Os::current();
    let lossy = |p: &Option<PathBuf>| p.as_ref().map(|p| p.to_string_lossy().into_owned());
    let argv = attach_argv(
        host,
        name,
        lossy(&tools.ssh).as_deref(),
        lossy(&tools.gcloud).as_deref(),
    )?;
    let script = (os == Os::Mac).then(script_path);
    let l = launch_in(os, app, &argv, script.as_deref())?;
    #[cfg(not(windows))]
    if l.new_console || l.raw_tail.is_some() {
        return Err(format!(
            "{} is not a terminal on this platform.",
            app.name()
        ));
    }
    if let Some((path, body)) = &l.script {
        write_script(path, body)?;
    }
    let mut c = Command::new(&l.program);
    c.args(&l.args)
        .env("PATH", &tools.path_env)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        /// `CREATE_NEW_CONSOLE`: PowerShell and Command Prompt get a window.
        const CREATE_NEW_CONSOLE: u32 = 0x0000_0010;
        if l.new_console {
            c.creation_flags(CREATE_NEW_CONSOLE);
        }
        if let Some(tail) = &l.raw_tail {
            c.raw_arg(tail);
        }
    }
    match c.spawn() {
        Ok(mut child) => {
            // Reap it; the terminal outlives this call.
            std::thread::spawn(move || {
                let _ = child.wait();
            });
            Ok(())
        }
        Err(_) => {
            if let Some((path, _)) = &l.script {
                let _ = std::fs::remove_file(path);
            }
            Err(match (os, app.app) {
                (Os::Linux, _) => format!(
                    "Could not start {} ({}) — choose another Terminal app in the Sessions window, or set $TERMINAL to your terminal program.",
                    app.name(),
                    l.program
                ),
                (Os::Windows, App::WindowsTerminal) => "Could not start Windows Terminal (wt.exe). Install it from the Microsoft Store, or choose another Terminal app in the Sessions window.".into(),
                _ => format!("Could not open {}.", app.name()),
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(words: &[&str]) -> Vec<String> {
        words.iter().map(|s| s.to_string()).collect()
    }

    /// The terminal each platform opens when nothing is chosen or found:
    /// Terminal, `$TERMINAL` or `x-terminal-emulator`, Windows Terminal.
    fn launch(
        os: Os,
        argv: &[String],
        script: Option<&Path>,
        configured: Option<&str>,
    ) -> Result<Launch, String> {
        let app = match os {
            Os::Mac => Detected::new(App::MacTerminal, "Terminal"),
            Os::Linux => match configured.map(str::trim).filter(|t| !t.is_empty()) {
                Some(t) if t.starts_with('-') || t.contains(char::is_whitespace) => {
                    return Err("$TERMINAL must name one terminal program.".into())
                }
                Some(t) => Detected::new(App::Configured, t),
                None => Detected::new(App::XTerminalEmulator, "x-terminal-emulator"),
            },
            Os::Windows => Detected::new(App::WindowsTerminal, "wt.exe"),
        };
        launch_in(os, &app, argv, script)
    }

    fn ssh(dest: &str) -> Transport {
        Transport::Ssh {
            destination: dest.into(),
        }
    }

    /// #278: the command each platform runs for a local daemon's session.
    #[test]
    fn open_in_terminal_builds_each_platforms_command() {
        let argv = attach_argv(&AttachHost::Local, "itervox-abc123", None, None).unwrap();
        assert_eq!(argv, v(&["tmux", "attach", "-t", "itervox-abc123"]));

        let script = PathBuf::from("/tmp/itervox-attach-1-2.command");
        let mac = launch(Os::Mac, &argv, Some(&script), None).unwrap();
        assert_eq!(mac.program, "/usr/bin/open");
        assert_eq!(
            mac.args,
            v(&["-a", "Terminal", "/tmp/itervox-attach-1-2.command"])
        );
        let (path, body) = mac.script.unwrap();
        assert_eq!(path, script);
        assert_eq!(
            body,
            "#!/bin/sh\n# Itervox: Open in Terminal. Deletes itself, then attaches.\nrm -f -- \"$0\"\nexec tmux attach -t itervox-abc123\n"
        );
        assert!(launch(Os::Mac, &argv, None, None).is_err());

        let linux = launch(Os::Linux, &argv, None, None).unwrap();
        assert_eq!(linux.program, "x-terminal-emulator");
        assert_eq!(
            linux.args,
            v(&["-e", "tmux", "attach", "-t", "itervox-abc123"])
        );
        assert_eq!(linux.script, None);
        let kitty = launch(Os::Linux, &argv, None, Some("/usr/bin/alacritty")).unwrap();
        assert_eq!(kitty.program, "/usr/bin/alacritty");
        assert_eq!(kitty.args[0], "-e");
        assert!(launch(Os::Linux, &argv, None, Some("xterm -hold")).is_err());
        assert!(launch(Os::Linux, &argv, None, Some("-e")).is_err());
        assert_eq!(
            launch(Os::Linux, &argv, None, Some("  ")).unwrap().program,
            "x-terminal-emulator"
        );

        let win = launch(Os::Windows, &argv, None, None).unwrap();
        assert_eq!(win.program, "wt.exe");
        assert_eq!(
            win.args,
            v(&["new-tab", "tmux", "attach", "-t", "itervox-abc123"])
        );

        // Local tmux needs a Unix host: Windows has none.
        assert_eq!(attach_host(Os::Mac, None), Ok(AttachHost::Local));
        assert_eq!(attach_host(Os::Linux, None), Ok(AttachHost::Local));
        assert_eq!(
            attach_host(Os::Windows, None),
            Err(WINDOWS_LOCAL_NOTE.to_string())
        );
    }

    /// #278: a remote daemon reached through the app's SSH tunnel is
    /// attached with `ssh -t <destination> tmux attach -t <id>`, on every
    /// platform; SSH through IAP with gcloud; a plain IAP port tunnel has
    /// no SSH and says so.
    #[test]
    fn open_in_terminal_attaches_a_tunnelled_remote_over_ssh() {
        let host = attach_host(Os::Mac, Some(&ssh("me@vm-1"))).unwrap();
        assert_eq!(
            host,
            AttachHost::Ssh {
                destination: "me@vm-1".into()
            }
        );
        let argv = attach_argv(&host, "itervox-0f9e8d", Some("/usr/bin/ssh"), None).unwrap();
        assert_eq!(
            argv,
            v(&[
                "/usr/bin/ssh",
                "-t",
                "-o",
                "ServerAliveInterval=15",
                "-o",
                "ServerAliveCountMax=3",
                "--",
                "me@vm-1",
                "tmux",
                "attach",
                "-t",
                "itervox-0f9e8d",
            ])
        );
        assert!(
            !argv.iter().any(|a| a.contains("BatchMode")),
            "the terminal may ask for a passphrase"
        );
        assert!(attach_argv(&host, "itervox-0f9e8d", None, None)
            .unwrap_err()
            .contains("ssh was not found"));

        let mac = launch(Os::Mac, &argv, Some(Path::new("/t/x.command")), None).unwrap();
        assert!(mac.script.unwrap().1.ends_with(
            "exec /usr/bin/ssh -t -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -- me@vm-1 tmux attach -t itervox-0f9e8d\n"
        ));
        let linux = launch(Os::Linux, &argv, None, None).unwrap();
        assert_eq!(&linux.args[..3], &v(&["-e", "/usr/bin/ssh", "-t"])[..]);
        assert_eq!(linux.args.len(), argv.len() + 1);
        let win_argv = attach_argv(
            &host,
            "itervox-0f9e8d",
            Some(r"C:\Windows\System32\OpenSSH\ssh.exe"),
            None,
        )
        .unwrap();
        let win = launch(Os::Windows, &win_argv, None, None).unwrap();
        assert_eq!(win.program, "wt.exe");
        assert_eq!(win.args[0], "new-tab");
        assert_eq!(win.args[1], r"C:\Windows\System32\OpenSSH\ssh.exe");
        assert_eq!(win.args.last().unwrap(), "itervox-0f9e8d");
        // A remote works from Windows, where local tmux does not.
        assert!(attach_host(Os::Windows, Some(&ssh("box"))).is_ok());

        let iap = Transport::Iap {
            instance: "itervox-vm".into(),
            zone: "europe-west1-b".into(),
            project: "acme-prod".into(),
            mode: IapMode::Ssh,
        };
        let host = attach_host(Os::Linux, Some(&iap)).unwrap();
        assert_eq!(
            attach_argv(&host, "itervox-abc123", None, Some("/opt/gcloud")).unwrap(),
            v(&[
                "/opt/gcloud",
                "compute",
                "ssh",
                "itervox-vm",
                "--tunnel-through-iap",
                "--zone=europe-west1-b",
                "--project=acme-prod",
                "--command=tmux attach -t itervox-abc123",
                "--",
                "-t",
                "-o",
                "ServerAliveInterval=15",
                "-o",
                "ServerAliveCountMax=3",
            ])
        );
        let port = Transport::Iap {
            instance: "itervox-vm".into(),
            zone: String::new(),
            project: String::new(),
            mode: IapMode::Port,
        };
        assert_eq!(
            attach_host(Os::Mac, Some(&port)),
            Err(IAP_PORT_NOTE.to_string())
        );
    }

    /// #278: untrusted strings never reach a shell or an option slot.
    #[test]
    fn open_in_terminal_refuses_bad_ids_and_hosts() {
        for bad in [
            "",
            "-x",
            "itervox-abc123; rm -rf ~",
            "a b",
            "$(id)",
            "`id`",
            "x'y",
            "work:0",
            "a\nb",
            &"a".repeat(65),
        ] {
            assert!(!valid_session_name(bad), "{bad:?}");
            assert!(
                attach_argv(&AttachHost::Local, bad, None, None).is_err(),
                "{bad:?}"
            );
        }
        assert!(valid_session_name("itervox-abc123"));
        assert!(valid_session_name("work_2"));
        assert!(is_itervox_session("itervox-abc123"));
        for bad in ["itervox-ABC123", "itervox-abc12", "itervox-abc1234", "work"] {
            assert!(!is_itervox_session(bad), "{bad:?}");
        }

        for bad in [
            "-oProxyCommand=sh",
            "me@host;id",
            "me@host id",
            "$(id)@h",
            "h\n",
            "h`x`",
            "",
        ] {
            assert!(attach_host(Os::Linux, Some(&ssh(bad))).is_err(), "{bad:?}");
            let host = AttachHost::Ssh {
                destination: bad.into(),
            };
            assert!(
                attach_argv(&host, "itervox-abc123", Some("ssh"), None).is_err(),
                "{bad:?}"
            );
        }
        for good in [
            "me@vm-1",
            "vm.example.com",
            "alias",
            "me@[::1]",
            "ssh://me@h:22",
        ] {
            assert!(attach_host(Os::Linux, Some(&ssh(good))).is_ok(), "{good:?}");
        }
        let iap = |instance: &str, zone: &str| Transport::Iap {
            instance: instance.into(),
            zone: zone.into(),
            project: String::new(),
            mode: IapMode::Ssh,
        };
        assert!(attach_host(Os::Mac, Some(&iap("--flag", ""))).is_err());
        assert!(attach_host(Os::Mac, Some(&iap("vm", "z;id"))).is_err());
        assert!(attach_host(Os::Mac, Some(&iap("vm", ""))).is_ok());

        // Windows Terminal splits its command line at ';'.
        assert!(launch(Os::Windows, &v(&["ssh", "a;b"]), None, None).is_err());
        assert!(launch(Os::Linux, &v(&["tmux", "a\u{7}"]), None, None).is_err());
        assert!(launch(Os::Linux, &v(&["-e", "x"]), None, None).is_err());
        assert!(launch(Os::Linux, &[], None, None).is_err());
        // Anything unusual is quoted in the macOS script.
        assert!(
            command_file(&v(&["/Apps/My SSH/ssh", "x"])).contains("exec '/Apps/My SSH/ssh' x\n")
        );
    }

    /// #278: only sessions the app may attach to have Open in Terminal —
    /// itervox's own tmux sessions while the daemon has tmux, and sessions
    /// started by hand only under `sessions.tmux_control` (controllable).
    #[test]
    fn open_in_terminal_only_for_attachable_sessions() {
        let own = AttachFacts {
            id: "itervox-abc123",
            owner: "itervox",
            kind: "tmux",
            controllable: true,
            pane_label: "",
            attach_command: "tmux attach -t itervox-abc123",
        };
        assert_eq!(attach_name(&own).as_deref(), Some("itervox-abc123"));
        assert_eq!(
            attach_name(&AttachFacts {
                attach_command: "",
                ..own
            })
            .as_deref(),
            Some("itervox-abc123"),
            "an older daemon names no attach command"
        );
        for not in [
            AttachFacts {
                controllable: false,
                ..own
            },
            AttachFacts {
                attach_command: "tmux -S '/run/x.sock' attach -t itervox-abc123",
                ..own
            },
            AttachFacts {
                id: "itervox-zz; id",
                attach_command: "",
                ..own
            },
            AttachFacts { kind: "", ..own },
        ] {
            assert_eq!(attach_name(&not), None, "{not:?}");
        }

        let hand = AttachFacts {
            id: "hand-1",
            owner: "external",
            kind: "",
            controllable: true,
            pane_label: "work:0.1",
            attach_command: "",
        };
        assert_eq!(attach_name(&hand).as_deref(), Some("work"));
        // Without sessions.tmux_control the daemon reports it read-only.
        assert_eq!(
            attach_name(&AttachFacts {
                controllable: false,
                ..hand
            }),
            None
        );
        assert_eq!(
            attach_name(&AttachFacts {
                pane_label: "",
                ..hand
            }),
            None
        );
        assert_eq!(
            attach_name(&AttachFacts {
                pane_label: "my work;id:0.0",
                ..hand
            }),
            None
        );
        // A dispatched run has no tmux session of its own.
        assert_eq!(
            attach_name(&AttachFacts {
                id: "sess-1",
                owner: "itervox",
                kind: "",
                controllable: true,
                ..Default::default()
            }),
            None
        );
    }
}
