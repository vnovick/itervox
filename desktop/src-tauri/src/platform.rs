//! What differs between macOS, Linux and Windows (#260), in one place.
//!
//! Two kinds of thing live here:
//!
//! - **Decisions as plain data**, keyed by [`Os`] so the unit tests cover
//!   every platform from any one of them: menu accelerators, how a path or
//!   URL is opened, and what the app says on Windows while no native daemon
//!   is bundled.
//! - **Process primitives** with one `cfg` per platform: whether a file is
//!   executable, a child in its own process group, killing that group,
//!   whether a pid is alive, the graceful stop signal and the home
//!   directory. The rest of the app calls these and stays platform-neutral.
//!
//! Each macOS-only integration in the app (the activation policy, the app
//! menu, the menu-bar title, `osascript` prompts, the Keychain, the
//! updater) has its Linux and Windows counterpart, or its "not on this
//! platform", listed in `site/src/content/docs/guides/desktop-app.mdx`.

use std::path::{Path, PathBuf};
use std::process::Command;

/// The desktop platforms the app is built for.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Os {
    Mac,
    Linux,
    Windows,
}

impl Os {
    /// The platform this binary was built for (other Unixes count as Linux).
    pub const fn current() -> Os {
        if cfg!(target_os = "macos") {
            Os::Mac
        } else if cfg!(windows) {
            Os::Windows
        } else {
            Os::Linux
        }
    }
}

/// The desktop-app guide's section on Windows without a native daemon.
pub const WINDOWS_DAEMON_GUIDE: &str =
    "https://itervox.dev/guides/desktop-app/#windows-local-projects";

/// What the app says on Windows while no native `itervox.exe` is bundled
/// (#259): local projects cannot run, remote daemons and the hub can.
pub const NO_LOCAL_DAEMON_MESSAGE: &str = "Local projects need WSL or a remote daemon on Windows: this build has no native itervox daemon. Run itervox inside WSL and add it as a remote (ssh), or connect to a remote daemon or hub.";

/// The tray notice about local daemons, when there is one to show: on
/// Windows without a bundled daemon. `(label, guide URL)`.
pub fn no_local_daemon_notice(os: Os, sidecar_found: bool) -> Option<(&'static str, &'static str)> {
    (os == Os::Windows && !sidecar_found).then_some((
        "Local projects need WSL or a remote daemon — Read the Guide…",
        WINDOWS_DAEMON_GUIDE,
    ))
}

/// A menu accelerator as `os` shows it: Tauri's `CmdOrCtrl` is ⌘ on macOS
/// and Ctrl on Linux and Windows; elsewhere the key list is kept. Linux and
/// Windows get the explicit `Ctrl` so the menu reads the same as the
/// accelerator the window system registers.
pub fn accelerator(os: Os, accel: &str) -> String {
    let modifier = match os {
        Os::Mac => "Cmd",
        Os::Linux | Os::Windows => "Ctrl",
    };
    accel
        .split('+')
        .map(|k| match k {
            "CmdOrCtrl" | "CommandOrControl" => modifier,
            k => k,
        })
        .collect::<Vec<_>>()
        .join("+")
}

/// The program and arguments that open `target` (a file or an `http(s)`
/// URL) with the user's default application.
pub fn open_command(os: Os, target: &str) -> (String, Vec<String>) {
    match os {
        Os::Mac => ("/usr/bin/open".into(), vec![target.into()]),
        Os::Linux => ("xdg-open".into(), vec![target.into()]),
        // explorer.exe hands a URL to the default browser and a file to
        // its associated program, with no console window.
        Os::Windows => ("explorer.exe".into(), vec![target.into()]),
    }
}

/// Opens `target` with the default application, without waiting.
pub fn open(target: &str) {
    let (program, args) = open_command(Os::current(), target);
    let mut c = Command::new(program);
    c.args(args);
    no_console(&mut c);
    let _ = c.spawn();
}

/// The user's home directory: `$HOME` on Unix, `%USERPROFILE%` on Windows
/// (what Go's `os.UserHomeDir` reads, so the app and the daemon agree on
/// `~/.itervox`).
pub fn home_dir() -> Option<PathBuf> {
    let var = if cfg!(windows) { "USERPROFILE" } else { "HOME" };
    std::env::var_os(var)
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
}

/// Whether `p` is a file this user may run: an executable mode bit on Unix;
/// on Windows, which has no such bit, any file.
pub fn is_executable(p: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::metadata(p)
            .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
            .unwrap_or(false)
    }
    #[cfg(not(unix))]
    {
        p.is_file()
    }
}

/// Windows: `CREATE_NO_WINDOW`, so a console program started from the tray
/// app does not flash a console window.
#[cfg(windows)]
const CREATE_NO_WINDOW: u32 = 0x0800_0000;
/// Windows: the child leads a new process group (no Ctrl+C from ours).
#[cfg(windows)]
const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;

/// Starts the child in its own process group, detached from the app's
/// terminal signals; on Windows a new process group without a console
/// window. [`kill_group`] stops the whole group.
pub fn own_process_group(c: &mut Command) {
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        c.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW);
    }
}

/// Windows: no console window for a short-lived helper; elsewhere nothing.
pub fn no_console(c: &mut Command) {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(CREATE_NO_WINDOW);
    }
    #[cfg(not(windows))]
    let _ = c;
}

/// Kills the process group led by `pid` (SIGKILL); on Windows the process
/// tree (`taskkill /T /F`).
pub fn kill_group(pid: u32) {
    #[cfg(unix)]
    {
        let Ok(pid) = i32::try_from(pid) else { return };
        if pid > 1 {
            // SAFETY: killpg on a group this app created with process_group(0).
            unsafe {
                libc::killpg(pid, libc::SIGKILL);
            }
        }
    }
    #[cfg(windows)]
    {
        if pid == 0 {
            return;
        }
        let mut c = Command::new("taskkill");
        c.args(["/PID", &pid.to_string(), "/T", "/F"])
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null());
        no_console(&mut c);
        let _ = c.status();
    }
}

/// Whether a process with `pid` exists: `kill(pid, 0)` on Unix; on Windows
/// a process handle that has not exited.
pub fn process_alive(pid: i32) -> bool {
    if pid <= 0 {
        return false;
    }
    #[cfg(unix)]
    {
        // SAFETY: kill(2) with signal 0 only checks for existence/permission.
        let rc = unsafe { libc::kill(pid, 0) };
        rc == 0 || std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
    }
    #[cfg(windows)]
    {
        use windows_sys::Win32::Foundation::{CloseHandle, STILL_ACTIVE};
        use windows_sys::Win32::System::Threading::{
            GetExitCodeProcess, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
        };
        // SAFETY: OpenProcess returns null or a handle closed below; the
        // exit code is written into a local.
        unsafe {
            let h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid as u32);
            if h.is_null() {
                // Access denied still means the process exists.
                return std::io::Error::last_os_error().raw_os_error() == Some(5);
            }
            let mut code = 0u32;
            let ok = GetExitCodeProcess(h, &mut code) != 0;
            CloseHandle(h);
            ok && code == STILL_ACTIVE as u32
        }
    }
}

/// Sends SIGTERM to `pid`, which starts the daemon's graceful drain. On
/// Windows there is no signal to send to a process without a console: the
/// app runs the daemon's own `itervox stop` instead
/// (`supervisor::stop_command`).
pub fn send_sigterm(pid: i32) -> Result<(), String> {
    if pid <= 1 {
        return Err(format!("refusing to signal pid {pid}"));
    }
    #[cfg(unix)]
    {
        // SAFETY: plain kill(2) on a positive pid taken from the daemon registry.
        let rc = unsafe { libc::kill(pid, libc::SIGTERM) };
        if rc == 0 {
            Ok(())
        } else {
            let err = std::io::Error::last_os_error();
            if err.raw_os_error() == Some(libc::ESRCH) {
                Ok(()) // already gone
            } else {
                Err(format!("could not stop pid {pid}: {err}"))
            }
        }
    }
    #[cfg(not(unix))]
    {
        Err(format!(
            "pid {pid}: Windows has no SIGTERM; stop the daemon with `itervox stop`"
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accelerators_use_cmd_on_mac_and_ctrl_on_linux_and_windows() {
        assert_eq!(accelerator(Os::Mac, "CmdOrCtrl+1"), "Cmd+1");
        assert_eq!(accelerator(Os::Linux, "CmdOrCtrl+1"), "Ctrl+1");
        assert_eq!(accelerator(Os::Windows, "CmdOrCtrl+1"), "Ctrl+1");
        assert_eq!(accelerator(Os::Linux, "CmdOrCtrl+Shift+]"), "Ctrl+Shift+]");
        assert_eq!(accelerator(Os::Windows, "CmdOrCtrl+Alt+S"), "Ctrl+Alt+S");
        // Every rail shortcut becomes a Ctrl shortcut off the Mac.
        for (_, _, accel) in crate::rail::menu_items() {
            for os in [Os::Linux, Os::Windows] {
                let a = accelerator(os, &accel);
                assert!(a.starts_with("Ctrl+"), "{os:?}: {a}");
                assert!(!a.contains("Cmd"), "{os:?}: {a}");
            }
            assert!(accelerator(Os::Mac, &accel).starts_with("Cmd+"));
        }
    }

    #[test]
    fn windows_without_a_bundled_daemon_says_local_projects_need_wsl() {
        let (label, url) = no_local_daemon_notice(Os::Windows, false).unwrap();
        assert!(label.contains("WSL or a remote daemon"), "{label}");
        assert!(url.ends_with("/guides/desktop-app/#windows-local-projects"));
        assert!(NO_LOCAL_DAEMON_MESSAGE.contains("Local projects need WSL or a remote daemon"));
        // A bundled itervox.exe (#259) makes local projects work: no notice.
        assert_eq!(no_local_daemon_notice(Os::Windows, true), None);
        // The Mac and Linux always bundle their daemon.
        assert_eq!(no_local_daemon_notice(Os::Mac, false), None);
        assert_eq!(no_local_daemon_notice(Os::Linux, false), None);
    }

    #[test]
    fn paths_and_urls_open_with_each_platforms_opener() {
        assert_eq!(open_command(Os::Mac, "/x").0, "/usr/bin/open");
        assert_eq!(open_command(Os::Linux, "/x").0, "xdg-open");
        let (p, a) = open_command(Os::Windows, "https://example.com/");
        assert_eq!(p, "explorer.exe");
        assert_eq!(a, vec!["https://example.com/".to_string()]);
    }

    #[test]
    fn current_matches_the_build_target() {
        let os = Os::current();
        if cfg!(target_os = "macos") {
            assert_eq!(os, Os::Mac);
        } else if cfg!(windows) {
            assert_eq!(os, Os::Windows);
        } else {
            assert_eq!(os, Os::Linux);
        }
        assert!(!process_alive(0));
        assert!(send_sigterm(1).is_err());
    }
}
