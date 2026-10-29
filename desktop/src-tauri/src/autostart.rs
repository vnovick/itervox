//! Start at Login on Linux and Windows (#260).
//!
//! - **Linux**: an XDG autostart entry, `$XDG_CONFIG_HOME/autostart/`
//!   (default `~/.config/autostart/`) `itervox-desktop.desktop`, which every
//!   freedesktop session (GNOME, KDE, Xfce, …) runs at login. From an
//!   AppImage it runs the AppImage file (`$APPIMAGE`), not the temporary
//!   mount the app runs from.
//! - **Windows**: the value `Itervox` under the registry Run key
//!   `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, written and
//!   removed by the app (`app_update.rs`). The value is the executable's
//!   path in double quotes ([`run_value`]): unquoted, a path with spaces
//!   (`C:\Program Files\…`, where the MSI installs) would make Windows try
//!   to run `C:\Program` at login.
//! - **macOS**: not on this platform; the Mac uses System Settings → General
//!   → Login Items, and the tray shows no item for it.
//!
//! The plan is plain data so every platform's entry is tested from any one;
//! the Linux file is written here, the Windows value in `app_update.rs`.

use std::path::{Path, PathBuf};

use crate::platform::Os;

/// The autostart entry's file name on Linux.
pub const XDG_FILE: &str = "itervox-desktop.desktop";

/// The Run key value name on Windows (the plugin's `app_name`).
pub const RUN_VALUE: &str = "Itervox";

/// The Run key, under `HKEY_CURRENT_USER`.
pub const RUN_KEY: &str = r"Software\Microsoft\Windows\CurrentVersion\Run";

/// The tray item, by state.
pub const MENU_OFF: &str = "Start Itervox at Login";
pub const MENU_ON: &str = "✓ Start Itervox at Login";

/// How the app starts at login on a platform.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Plan {
    /// Linux: this file with these contents.
    XdgEntry { path: PathBuf, contents: String },
    /// Windows: the value `name` under `HKCU\{key}` holding `command`.
    RunKey {
        key: &'static str,
        name: &'static str,
        command: String,
    },
    /// No login item from the app on this platform, and why.
    Unsupported(&'static str),
}

/// Quotes one argument of a desktop entry's `Exec` key: in double quotes,
/// with `"`, `` ` ``, `$` and `\` escaped, then the string-value escape
/// that doubles every backslash, and `%` doubled (field codes).
pub fn exec_quote(arg: &str) -> String {
    let mut quoted = String::with_capacity(arg.len() + 2);
    for c in arg.chars() {
        match c {
            '"' | '`' | '$' | '\\' => {
                quoted.push('\\');
                quoted.push(c);
            }
            c => quoted.push(c),
        }
    }
    let string_escaped = quoted.replace('\\', "\\\\").replace('%', "%%");
    format!("\"{string_escaped}\"")
}

/// The Run key value that starts `exe` with `args`: the path always in
/// double quotes (Windows paths cannot contain `"`), then each argument,
/// quoted when it has a space or tab.
pub fn run_value(exe: &Path, args: &[&str]) -> String {
    let mut v = format!("\"{}\"", exe.display());
    for a in args {
        if a.is_empty() || a.contains([' ', '\t']) {
            v.push_str(&format!(" \"{a}\""));
        } else {
            v.push(' ');
            v.push_str(a);
        }
    }
    v
}

/// The XDG autostart entry that runs `exe`.
pub fn xdg_entry(exe: &Path) -> String {
    format!(
        "[Desktop Entry]\nType=Application\nName=Itervox\nComment=Tray app for the itervox daemons\nExec={}\nIcon=itervox-desktop\nTerminal=false\nX-GNOME-Autostart-enabled=true\n",
        exec_quote(&exe.to_string_lossy())
    )
}

/// The plan for `os`. `config_home` is `$XDG_CONFIG_HOME` (or
/// `~/.config`), `exe` the app's executable and `appimage` the AppImage
/// file it runs from, if any.
pub fn plan(os: Os, config_home: Option<&Path>, exe: &Path, appimage: Option<&Path>) -> Plan {
    match os {
        Os::Mac => Plan::Unsupported("use System Settings → General → Login Items on macOS"),
        Os::Linux => match config_home {
            Some(dir) => {
                let target = appimage
                    .filter(|p| !p.as_os_str().is_empty())
                    .unwrap_or(exe);
                Plan::XdgEntry {
                    path: dir.join("autostart").join(XDG_FILE),
                    contents: xdg_entry(target),
                }
            }
            None => Plan::Unsupported("no home directory to write ~/.config/autostart into"),
        },
        Os::Windows => Plan::RunKey {
            key: RUN_KEY,
            name: RUN_VALUE,
            command: run_value(exe, &[]),
        },
    }
}

/// `$XDG_CONFIG_HOME`, or `~/.config` when it is unset or relative (the
/// XDG rule).
pub fn config_home(xdg: Option<&Path>, home: Option<&Path>) -> Option<PathBuf> {
    match xdg {
        Some(p) if p.is_absolute() => Some(p.to_path_buf()),
        _ => home.map(|h| h.join(".config")),
    }
}

/// Whether the Linux entry is installed: the file exists.
pub fn xdg_enabled(path: &Path) -> bool {
    path.is_file()
}

/// Writes (atomically) or removes the Linux entry.
pub fn xdg_set(path: &Path, contents: &str, enabled: bool) -> Result<(), String> {
    if !enabled {
        return match std::fs::remove_file(path) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => {
                Err(format!("{}: {e}", path.display()))
            }
            _ => Ok(()),
        };
    }
    let dir = path.parent().unwrap_or(Path::new("."));
    std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let tmp = dir.join(format!(".{XDG_FILE}.tmp-{}", std::process::id()));
    std::fs::write(&tmp, contents)
        .and_then(|()| std::fs::rename(&tmp, path))
        .map_err(|e| {
            let _ = std::fs::remove_file(&tmp);
            format!("{}: {e}", path.display())
        })
}

/// The tray item's label for the current state.
pub fn menu_label(enabled: bool) -> &'static str {
    if enabled {
        MENU_ON
    } else {
        MENU_OFF
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// #260: the login entry on each platform.
    #[test]
    fn autostart_entry_per_os() {
        let exe = Path::new("/usr/bin/itervox-desktop");
        let cfg = Path::new("/home/me/.config");
        match plan(Os::Linux, Some(cfg), exe, None) {
            Plan::XdgEntry { path, contents } => {
                assert_eq!(
                    path,
                    PathBuf::from("/home/me/.config/autostart/itervox-desktop.desktop")
                );
                assert!(contents.starts_with("[Desktop Entry]\nType=Application\n"));
                assert!(contents.contains("\nExec=\"/usr/bin/itervox-desktop\"\n"));
                assert!(contents.contains("\nX-GNOME-Autostart-enabled=true\n"));
            }
            other => panic!("{other:?}"),
        }
        // From an AppImage the entry runs the AppImage file, not the mount.
        let mount_exe = Path::new("/tmp/.mount_ItervoX/usr/bin/itervox-desktop");
        let appimage = Path::new("/home/me/Apps/Itervox_0.3.1_amd64.AppImage");
        match plan(Os::Linux, Some(cfg), mount_exe, Some(appimage)) {
            Plan::XdgEntry { contents, .. } => assert!(
                contents.contains("\nExec=\"/home/me/Apps/Itervox_0.3.1_amd64.AppImage\"\n"),
                "{contents}"
            ),
            other => panic!("{other:?}"),
        }
        let win_exe = Path::new(r"C:\Users\me\AppData\Local\Itervox\itervox-desktop.exe");
        assert_eq!(
            plan(Os::Windows, None, win_exe, None),
            Plan::RunKey {
                key: r"Software\Microsoft\Windows\CurrentVersion\Run",
                name: "Itervox",
                command: format!("\"{}\"", win_exe.display()),
            }
        );
        assert!(matches!(
            plan(Os::Mac, Some(cfg), exe, None),
            Plan::Unsupported(m) if m.contains("Login Items")
        ));
        assert!(matches!(
            plan(Os::Linux, None, exe, None),
            Plan::Unsupported(_)
        ));
    }

    /// #260 follow-up: an unquoted Run value with a space in the path makes
    /// Windows run `C:\Program` at login.
    #[test]
    fn windows_run_value_quotes_the_exe_path() {
        let exe = Path::new(r"C:\Program Files\Itervox\Itervox.exe");
        assert_eq!(
            run_value(exe, &[]),
            r#""C:\Program Files\Itervox\Itervox.exe""#
        );
        assert_eq!(
            run_value(exe, &["--minimized", "a b"]),
            r#""C:\Program Files\Itervox\Itervox.exe" --minimized "a b""#
        );
        match plan(Os::Windows, None, exe, None) {
            Plan::RunKey { command, .. } => {
                assert_eq!(command, r#""C:\Program Files\Itervox\Itervox.exe""#)
            }
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn exec_arguments_are_quoted_per_the_desktop_entry_spec() {
        assert_eq!(exec_quote("/opt/My Apps/it"), "\"/opt/My Apps/it\"");
        assert_eq!(exec_quote("/a/$x\"y"), r#""/a/\\$x\\"y""#);
        assert_eq!(exec_quote(r"/a\b"), r#""/a\\\\b""#);
        assert_eq!(exec_quote("/a/100%"), "\"/a/100%%\"");
    }

    /// An absolute path on the host: `p` itself on Unix, `C:` plus `p` with
    /// backslashes on Windows (where `/xdg` is not absolute).
    fn abs(p: &str) -> PathBuf {
        if cfg!(windows) {
            PathBuf::from(format!("C:{}", p.replace('/', "\\")))
        } else {
            PathBuf::from(p)
        }
    }

    #[test]
    fn config_home_follows_xdg() {
        let home = abs("/home/me");
        let xdg = abs("/xdg");
        assert_eq!(config_home(Some(&xdg), Some(&home)), Some(xdg.clone()));
        assert_eq!(
            config_home(Some(Path::new("rel")), Some(&home)),
            Some(home.join(".config"))
        );
        assert_eq!(config_home(None, None), None);
    }

    #[test]
    fn the_linux_entry_is_written_and_removed() {
        let tmp = tempfile::tempdir().unwrap();
        let Plan::XdgEntry { path, contents } = plan(
            Os::Linux,
            Some(tmp.path()),
            Path::new("/usr/bin/itervox-desktop"),
            None,
        ) else {
            panic!("no entry");
        };
        assert!(!xdg_enabled(&path));
        assert_eq!(menu_label(xdg_enabled(&path)), MENU_OFF);
        xdg_set(&path, &contents, true).unwrap();
        assert!(xdg_enabled(&path));
        assert_eq!(std::fs::read_to_string(&path).unwrap(), contents);
        assert_eq!(menu_label(xdg_enabled(&path)), MENU_ON);
        // No temporary file is left behind.
        assert_eq!(
            std::fs::read_dir(path.parent().unwrap()).unwrap().count(),
            1
        );
        xdg_set(&path, &contents, false).unwrap();
        assert!(!xdg_enabled(&path));
        xdg_set(&path, &contents, false).unwrap();
    }
}
