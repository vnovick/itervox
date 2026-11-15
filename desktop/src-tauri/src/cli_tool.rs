//! The command-line tool the app manages (#130).
//!
//! **Install Command-Line Tool…** puts a symlink named `itervox` into
//! `/usr/local/bin` (when the user can write there) or else
//! `~/.local/bin`, pointing at the daemon bundled in the app
//! (`Itervox.app/Contents/MacOS/itervox`). That symlink is what "the CLI the
//! app manages" means: an app update replaces the bundle in place, so the
//! symlink, and every `itervox` command run through it, follows the update
//! with nothing to do. A file the app did not create (a Homebrew or
//! hand-copied `itervox`) is never touched.
//!
//! **Linux (#260)** links `~/.local/bin/itervox` only (no root), and says so
//! when that directory is not on PATH. From a `.deb` the link points at the
//! installed `/usr/bin/itervox`. An AppImage mounts itself at a new
//! temporary path on every launch, so a link into it would break: the app
//! copies the daemon to `~/.local/share/itervox/bin/itervox`, links that,
//! and refreshes the copy when a newer AppImage starts.
//!
//! **Windows (#260)** has no symlinks for this: the app adds its install
//! directory, where `itervox.exe` sits, to the user's PATH instead.

use std::io::ErrorKind;
use std::path::{Path, PathBuf};

use crate::platform::Os;

/// The name of the link the app installs.
pub const LINK_NAME: &str = "itervox";

/// Where the link may go on the Mac, in order of preference.
pub fn link_dirs(home: Option<&Path>) -> Vec<PathBuf> {
    let mut dirs = vec![PathBuf::from("/usr/local/bin")];
    if let Some(h) = home {
        dirs.push(h.join(".local/bin"));
    }
    dirs
}

/// How the command-line tool is installed on a platform.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CliPlan {
    /// A symlink named `itervox` in the first usable of `dirs`, pointing at
    /// `target` (the Mac, Linux).
    Link { dirs: Vec<PathBuf>, target: PathBuf },
    /// `dir` (where `itervox.exe` is) added to the user's PATH (Windows).
    UserPath { dir: PathBuf },
}

/// The AppImage's stable copy of the daemon: the link target on Linux when
/// the app runs from an AppImage.
pub fn appimage_copy(home: &Path) -> PathBuf {
    home.join(".local/share/itervox/bin").join(LINK_NAME)
}

/// Whether `sidecar` lies inside the running AppImage's mount (`$APPDIR`).
pub fn in_appimage(sidecar: &Path, appdir: Option<&Path>) -> bool {
    appdir.is_some_and(|d| !d.as_os_str().is_empty() && sidecar.starts_with(d))
}

/// The plan for `os`: the Mac as before; Linux `~/.local/bin` (pointing at
/// the AppImage copy when `appdir` holds the sidecar); Windows the user
/// PATH. `None` when there is no home directory to install into.
pub fn plan(os: Os, home: Option<&Path>, sidecar: &Path, appdir: Option<&Path>) -> Option<CliPlan> {
    match os {
        Os::Mac => Some(CliPlan::Link {
            dirs: link_dirs(home),
            target: sidecar.to_path_buf(),
        }),
        Os::Linux => {
            let home = home?;
            let target = if in_appimage(sidecar, appdir) {
                appimage_copy(home)
            } else {
                sidecar.to_path_buf()
            };
            Some(CliPlan::Link {
                dirs: vec![home.join(".local/bin")],
                target,
            })
        }
        Os::Windows => sidecar
            .parent()
            .map(|d| CliPlan::UserPath { dir: d.into() }),
    }
}

/// What is at a link location.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LinkState {
    Absent,
    /// A symlink to this app's bundled daemon.
    Managed,
    /// A symlink the app installed earlier, pointing into another (moved,
    /// deleted or second) copy of the app.
    Stale(PathBuf),
    /// Anything else: not ours, never replaced.
    Foreign,
}

/// Whether `target` is the bundled daemon of some `.app`, or of some
/// AppImage mount (`/tmp/.mount_Itervo…/usr/bin/itervox`, a link an older
/// build may have made).
fn is_bundle_sidecar(target: &Path) -> bool {
    let app = target.ends_with(Path::new("Contents/MacOS").join(LINK_NAME))
        && target
            .ancestors()
            .nth(3)
            .and_then(Path::extension)
            .is_some_and(|e| e == "app");
    let appimage = target.ends_with(Path::new("usr/bin").join(LINK_NAME))
        && target
            .ancestors()
            .nth(3)
            .and_then(Path::file_name)
            .is_some_and(|n| n.to_string_lossy().starts_with(".mount_"));
    app || appimage
}

pub fn inspect(link: &Path, sidecar: &Path) -> LinkState {
    let Ok(meta) = std::fs::symlink_metadata(link) else {
        return LinkState::Absent;
    };
    if !meta.file_type().is_symlink() {
        return LinkState::Foreign;
    }
    match std::fs::read_link(link) {
        Ok(t) if t == sidecar => LinkState::Managed,
        Ok(t) if is_bundle_sidecar(&t) => LinkState::Stale(t),
        _ => LinkState::Foreign,
    }
}

/// The state of the managed command-line tool across `dirs`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CliStatus {
    /// Not installed (or only someone else's `itervox` exists).
    NotInstalled,
    Installed(PathBuf),
    /// A link of ours points into another copy of the app.
    NeedsRepair(PathBuf),
}

pub fn status(dirs: &[PathBuf], sidecar: &Path) -> CliStatus {
    let mut stale = None;
    for d in dirs {
        let link = d.join(LINK_NAME);
        match inspect(&link, sidecar) {
            LinkState::Managed => return CliStatus::Installed(link),
            LinkState::Stale(_) if stale.is_none() => stale = Some(link),
            _ => {}
        }
    }
    stale.map_or(CliStatus::NotInstalled, CliStatus::NeedsRepair)
}

/// The menu item for `status` on `os`: Windows says PATH, not a link.
pub fn menu_item_for(os: Os, status: &CliStatus) -> (String, bool) {
    if os != Os::Windows {
        return menu_item(status);
    }
    match status {
        CliStatus::NotInstalled | CliStatus::NeedsRepair(_) => {
            ("Add Command-Line Tool to PATH…".into(), true)
        }
        CliStatus::Installed(p) => (format!("Command-line tool: {} on PATH", p.display()), false),
    }
}

/// The menu item for `status`: label and whether it is clickable.
pub fn menu_item(status: &CliStatus) -> (String, bool) {
    match status {
        CliStatus::NotInstalled => ("Install Command-Line Tool…".into(), true),
        CliStatus::Installed(p) => (format!("Command-line tool: {}", p.display()), false),
        CliStatus::NeedsRepair(_) => ("Repair Command-Line Tool…".into(), true),
    }
}

/// Whether the app runs from a translocated (read-only, randomised) path:
/// Gatekeeper does that to an app opened from its download location. Links
/// into it would break, and the app cannot update itself there.
pub fn is_translocated(path: &Path) -> bool {
    path.components()
        .any(|c| c.as_os_str() == "AppTranslocation")
}

#[cfg(unix)]
fn symlink(target: &Path, link: &Path) -> std::io::Result<()> {
    std::os::unix::fs::symlink(target, link)
}

/// Windows installs through the user PATH, never a link.
#[cfg(not(unix))]
fn symlink(_target: &Path, _link: &Path) -> std::io::Result<()> {
    Err(std::io::Error::new(
        ErrorKind::Unsupported,
        "symbolic links are not used on this platform",
    ))
}

/// Points `link` at `target` atomically (a temporary link renamed over it).
fn replace_symlink(link: &Path, target: &Path) -> std::io::Result<()> {
    let tmp = link.with_file_name(format!(".{LINK_NAME}.tmp-{}", std::process::id()));
    match std::fs::remove_file(&tmp) {
        Err(e) if e.kind() != ErrorKind::NotFound => return Err(e),
        _ => {}
    }
    symlink(target, &tmp)?;
    std::fs::rename(&tmp, link).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

/// Installs (or repairs) the link: the first of `dirs` where it is already
/// ours, or else the first that is free or ours-but-stale and writable.
/// Returns the link path.
pub fn install(dirs: &[PathBuf], sidecar: &Path) -> Result<PathBuf, String> {
    if is_translocated(sidecar) {
        return Err("Move Itervox to the Applications folder and open it from there first.".into());
    }
    if let CliStatus::Installed(p) = status(dirs, sidecar) {
        return Ok(p);
    }
    let mut problems = Vec::new();
    for d in dirs {
        let link = d.join(LINK_NAME);
        match inspect(&link, sidecar) {
            LinkState::Managed => return Ok(link),
            LinkState::Foreign => {
                problems.push(format!(
                    "{} exists and is not managed by Itervox",
                    link.display()
                ));
                continue;
            }
            LinkState::Absent | LinkState::Stale(_) => {}
        }
        let res = std::fs::create_dir_all(d).and_then(|()| replace_symlink(&link, sidecar));
        match res {
            Ok(()) => return Ok(link),
            Err(e) => problems.push(format!("{}: {e}", d.display())),
        }
    }
    Err(format!(
        "Could not install the command-line tool ({}).",
        problems.join("; ")
    ))
}

/// Whether `dir` is on the `PATH`-style list `path`.
pub fn on_path(dir: &Path, path: &str) -> bool {
    std::env::split_paths(path).any(|p| p == dir)
}

/// What to tell the user after installing `link` on `os`, given the app's
/// `PATH`. Linux names `~/.local/bin` when it is not on PATH.
pub fn installed_message(os: Os, link: &Path, target: &Path, path: &str) -> String {
    let dir = link.parent().unwrap_or(Path::new("/"));
    let mut msg = format!(
        "Installed {} → {}. It updates together with the app.",
        link.display(),
        target.display()
    );
    if on_path(dir, path) {
        return msg;
    }
    match os {
        Os::Mac if dir == Path::new("/usr/local/bin") => {}
        Os::Mac => msg.push_str(&format!(
            " If a new terminal does not find `itervox`, add {} to your PATH.",
            dir.display()
        )),
        Os::Linux => msg.push_str(&format!(
            " {} is not on your PATH: add it in your shell profile (for example `export PATH=\"$HOME/.local/bin:$PATH\"` in ~/.profile), then open a new terminal.",
            dir.display()
        )),
        Os::Windows => {}
    }
    msg
}

/// Copies the daemon to the AppImage's stable location `copy` (Linux),
/// atomically and executable, when it differs from `sidecar`. Returns
/// whether it wrote.
pub fn refresh_copy(sidecar: &Path, copy: &Path) -> std::io::Result<bool> {
    let new = std::fs::read(sidecar)?;
    if std::fs::read(copy).is_ok_and(|old| old == new) {
        return Ok(false);
    }
    let dir = copy.parent().unwrap_or(Path::new("."));
    std::fs::create_dir_all(dir)?;
    let tmp = dir.join(format!(".{LINK_NAME}.tmp-{}", std::process::id()));
    std::fs::write(&tmp, &new)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o755))?;
    }
    std::fs::rename(&tmp, copy).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })?;
    Ok(true)
}

/// Linux AppImage: installs the stable copy, then links it.
pub fn install_with_copy(dirs: &[PathBuf], sidecar: &Path, copy: &Path) -> Result<PathBuf, String> {
    refresh_copy(sidecar, copy).map_err(|e| format!("{}: {e}", copy.display()))?;
    install(dirs, copy)
}

/// Normalises a PATH entry for comparison on Windows: no trailing
/// separator, case-insensitive.
fn windows_path_key(entry: &str) -> String {
    entry
        .trim()
        .trim_end_matches(['\\', '/'])
        .to_ascii_lowercase()
}

/// Whether the Windows PATH value `path` (`;`-separated) holds `dir`.
pub fn windows_path_has(path: &str, dir: &Path) -> bool {
    let want = windows_path_key(&dir.to_string_lossy());
    path.split(';').any(|e| windows_path_key(e) == want)
}

/// The user PATH value with `dir` appended, or `None` when it is there
/// already. Other entries, their order and spelling are kept.
pub fn windows_path_with(path: &str, dir: &Path) -> Option<String> {
    if windows_path_has(path, dir) {
        return None;
    }
    let d = dir.to_string_lossy();
    let base = path.trim_end_matches(';');
    Some(if base.is_empty() {
        d.into_owned()
    } else {
        format!("{base};{d}")
    })
}

/// The state of the Windows tool: installed when the user PATH holds the
/// directory.
pub fn windows_status(user_path: &str, dir: &Path) -> CliStatus {
    if windows_path_has(user_path, dir) {
        CliStatus::Installed(dir.to_path_buf())
    } else {
        CliStatus::NotInstalled
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg_attr(not(unix), allow(dead_code))]
    struct Fixture {
        _tmp: tempfile::TempDir,
        dirs: Vec<PathBuf>,
        sidecar: PathBuf,
    }

    #[cfg_attr(not(unix), allow(dead_code))]
    fn fixture() -> Fixture {
        let tmp = tempfile::tempdir().unwrap();
        let app = tmp.path().join("Applications/Itervox.app/Contents/MacOS");
        std::fs::create_dir_all(&app).unwrap();
        let sidecar = app.join("itervox");
        std::fs::write(&sidecar, "#!/bin/sh\n").unwrap();
        let dirs = vec![
            tmp.path().join("usr-local-bin"),
            tmp.path().join("home/.local/bin"),
        ];
        Fixture {
            _tmp: tmp,
            dirs,
            sidecar,
        }
    }

    #[cfg(unix)]
    #[test]
    fn install_links_the_bundled_daemon_and_is_idempotent() {
        let f = fixture();
        std::fs::create_dir_all(&f.dirs[0]).unwrap();
        assert_eq!(status(&f.dirs, &f.sidecar), CliStatus::NotInstalled);
        let link = install(&f.dirs, &f.sidecar).unwrap();
        assert_eq!(link, f.dirs[0].join("itervox"));
        assert_eq!(std::fs::read_link(&link).unwrap(), f.sidecar);
        assert_eq!(
            status(&f.dirs, &f.sidecar),
            CliStatus::Installed(link.clone())
        );
        assert_eq!(install(&f.dirs, &f.sidecar).unwrap(), link);
        assert_eq!(
            menu_item(&status(&f.dirs, &f.sidecar)),
            (format!("Command-line tool: {}", link.display()), false)
        );
    }

    #[cfg(unix)]
    #[test]
    fn the_link_follows_an_in_place_app_update() {
        let f = fixture();
        let link = install(&f.dirs, &f.sidecar).unwrap();
        // The updater swaps the bundle at the same path.
        std::fs::write(&f.sidecar, "#!/bin/sh\necho new\n").unwrap();
        assert_eq!(
            std::fs::read_to_string(&link).unwrap(),
            "#!/bin/sh\necho new\n"
        );
        assert_eq!(status(&f.dirs, &f.sidecar), CliStatus::Installed(link));
    }

    #[cfg(unix)]
    #[test]
    fn a_foreign_itervox_is_never_replaced() {
        let f = fixture();
        std::fs::create_dir_all(&f.dirs[0]).unwrap();
        let brew = f.dirs[0].join("itervox");
        std::fs::write(&brew, "brew").unwrap();
        // Falls through to ~/.local/bin, creating it.
        let link = install(&f.dirs, &f.sidecar).unwrap();
        assert_eq!(link, f.dirs[1].join("itervox"));
        assert_eq!(std::fs::read_to_string(&brew).unwrap(), "brew");
        // A foreign symlink is not ours either.
        std::os::unix::fs::symlink("/opt/homebrew/bin/itervox", f.dirs[1].join("x")).unwrap();
        std::fs::rename(f.dirs[1].join("x"), &link).unwrap();
        assert_eq!(inspect(&link, &f.sidecar), LinkState::Foreign);
        let err = install(&f.dirs, &f.sidecar).unwrap_err();
        assert!(err.contains("not managed by Itervox"), "{err}");
    }

    #[cfg(unix)]
    #[test]
    fn a_link_into_a_moved_app_is_repaired() {
        let f = fixture();
        std::fs::create_dir_all(&f.dirs[0]).unwrap();
        let old = PathBuf::from("/Users/me/Downloads/Itervox.app/Contents/MacOS/itervox");
        let link = f.dirs[0].join("itervox");
        std::os::unix::fs::symlink(&old, &link).unwrap();
        assert_eq!(inspect(&link, &f.sidecar), LinkState::Stale(old));
        assert_eq!(
            status(&f.dirs, &f.sidecar),
            CliStatus::NeedsRepair(link.clone())
        );
        assert_eq!(
            menu_item(&status(&f.dirs, &f.sidecar)).0,
            "Repair Command-Line Tool…"
        );
        assert_eq!(install(&f.dirs, &f.sidecar).unwrap(), link);
        assert_eq!(std::fs::read_link(&link).unwrap(), f.sidecar);
        // No temporary link is left behind.
        assert_eq!(std::fs::read_dir(&f.dirs[0]).unwrap().count(), 1);
    }

    #[cfg(unix)]
    #[test]
    fn an_unusable_location_falls_through_and_errors_name_each() {
        let f = fixture();
        // The first "directory" is a file: create_dir_all fails.
        std::fs::create_dir_all(f.dirs[0].parent().unwrap()).unwrap();
        std::fs::write(&f.dirs[0], "").unwrap();
        assert_eq!(
            install(&f.dirs, &f.sidecar).unwrap(),
            f.dirs[1].join("itervox")
        );
        std::fs::remove_file(f.dirs[1].join("itervox")).unwrap();
        std::fs::write(f.dirs[1].join("itervox"), "mine").unwrap();
        let err = install(&f.dirs, &f.sidecar).unwrap_err();
        assert!(err.contains(&f.dirs[0].display().to_string()), "{err}");
        assert!(err.contains("not managed by Itervox"), "{err}");
    }

    #[test]
    fn a_translocated_app_does_not_install() {
        let p = Path::new(
            "/private/var/folders/x/AppTranslocation/ABC/d/Itervox.app/Contents/MacOS/itervox",
        );
        assert!(is_translocated(p));
        assert!(!is_translocated(Path::new("/Applications/Itervox.app")));
        let err = install(&[PathBuf::from("/nonexistent")], p).unwrap_err();
        assert!(err.contains("Applications folder"), "{err}");
    }

    #[cfg(unix)]
    #[test]
    fn bundle_sidecar_detection_and_path_lookup() {
        assert!(is_bundle_sidecar(Path::new(
            "/A/Itervox.app/Contents/MacOS/itervox"
        )));
        assert!(!is_bundle_sidecar(Path::new(
            "/A/Itervox/Contents/MacOS/itervox"
        )));
        assert!(!is_bundle_sidecar(Path::new("/opt/homebrew/bin/itervox")));
        assert_eq!(
            link_dirs(Some(Path::new("/Users/me"))),
            vec![
                PathBuf::from("/usr/local/bin"),
                PathBuf::from("/Users/me/.local/bin")
            ]
        );
        assert!(on_path(
            Path::new("/usr/local/bin"),
            "/usr/bin:/usr/local/bin"
        ));
        assert!(!on_path(Path::new("/Users/me/.local/bin"), "/usr/bin:/bin"));
    }

    /// #260: where the command-line tool goes on each platform.
    #[test]
    fn cli_install_path_per_os() {
        let home = Path::new("/home/me");
        let mac_sidecar = Path::new("/Applications/Itervox.app/Contents/MacOS/itervox");
        assert_eq!(
            plan(Os::Mac, Some(home), mac_sidecar, None),
            Some(CliPlan::Link {
                dirs: vec![
                    PathBuf::from("/usr/local/bin"),
                    PathBuf::from("/home/me/.local/bin")
                ],
                target: mac_sidecar.into(),
            })
        );
        // Linux, .deb: ~/.local/bin only, pointing at the installed daemon.
        let deb = Path::new("/usr/bin/itervox");
        assert_eq!(
            plan(Os::Linux, Some(home), deb, None),
            Some(CliPlan::Link {
                dirs: vec![PathBuf::from("/home/me/.local/bin")],
                target: deb.into(),
            })
        );
        // Linux, AppImage: the mount moves every launch; link the copy.
        let mount = Path::new("/tmp/.mount_ItervoAbC");
        let in_mount = mount.join("usr/bin/itervox");
        assert_eq!(
            plan(Os::Linux, Some(home), &in_mount, Some(mount)),
            Some(CliPlan::Link {
                dirs: vec![PathBuf::from("/home/me/.local/bin")],
                target: PathBuf::from("/home/me/.local/share/itervox/bin/itervox"),
            })
        );
        assert_eq!(plan(Os::Linux, None, deb, None), None);
        // Windows: the install directory goes on the user PATH.
        let exe = Path::new(r"C:\Users\me\AppData\Local\Itervox\itervox.exe");
        match plan(Os::Windows, Some(home), exe, None) {
            Some(CliPlan::UserPath { dir }) => assert_eq!(dir, exe.parent().unwrap()),
            other => panic!("{other:?}"),
        }
        assert_eq!(
            menu_item_for(Os::Windows, &CliStatus::NotInstalled).0,
            "Add Command-Line Tool to PATH…"
        );
        assert_eq!(
            menu_item_for(Os::Linux, &CliStatus::NotInstalled).0,
            "Install Command-Line Tool…"
        );
    }

    #[test]
    fn windows_user_path_gains_the_install_dir_once() {
        let dir = Path::new(r"C:\Users\me\AppData\Local\Itervox");
        assert_eq!(
            windows_path_with(r"C:\bin;%USERPROFILE%\go\bin", dir).as_deref(),
            Some(r"C:\bin;%USERPROFILE%\go\bin;C:\Users\me\AppData\Local\Itervox")
        );
        assert_eq!(
            windows_path_with("", dir).as_deref(),
            Some(r"C:\Users\me\AppData\Local\Itervox")
        );
        assert_eq!(
            windows_path_with(r"C:\bin;", dir).as_deref(),
            Some(r"C:\bin;C:\Users\me\AppData\Local\Itervox")
        );
        // Already there, in any case or with a trailing separator.
        assert_eq!(
            windows_path_with(r"c:\users\ME\appdata\local\itervox\;C:\bin", dir),
            None
        );
        assert_eq!(windows_status(r"C:\bin", dir), CliStatus::NotInstalled);
        assert_eq!(
            windows_status(r"C:\bin;C:\Users\me\AppData\Local\Itervox", dir),
            CliStatus::Installed(dir.into())
        );
    }

    #[cfg(unix)]
    #[test]
    fn linux_links_local_bin_and_says_when_it_is_not_on_path() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path().join("home");
        let sidecar = tmp.path().join("usr/bin/itervox");
        std::fs::create_dir_all(sidecar.parent().unwrap()).unwrap();
        std::fs::write(&sidecar, "#!/bin/sh\n").unwrap();
        let Some(CliPlan::Link { dirs, target }) = plan(Os::Linux, Some(&home), &sidecar, None)
        else {
            panic!("no link plan");
        };
        let link = install(&dirs, &target).unwrap();
        assert_eq!(link, home.join(".local/bin/itervox"));
        assert_eq!(std::fs::read_link(&link).unwrap(), sidecar);
        let msg = installed_message(Os::Linux, &link, &target, "/usr/bin:/bin");
        assert!(
            msg.contains(&format!(
                "{} is not on your PATH",
                home.join(".local/bin").display()
            )),
            "{msg}"
        );
        let on = format!("/usr/bin:{}", home.join(".local/bin").display());
        let msg = installed_message(Os::Linux, &link, &target, &on);
        assert!(!msg.contains("not on your PATH"), "{msg}");
    }

    #[cfg(unix)]
    #[test]
    fn appimage_install_links_a_copy_that_follows_a_newer_appimage() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path().join("home");
        let mount = tmp.path().join(".mount_ItervoX1");
        let sidecar = mount.join("usr/bin/itervox");
        std::fs::create_dir_all(sidecar.parent().unwrap()).unwrap();
        std::fs::write(&sidecar, "v1").unwrap();
        let Some(CliPlan::Link { dirs, target }) =
            plan(Os::Linux, Some(&home), &sidecar, Some(&mount))
        else {
            panic!("no link plan");
        };
        let link = install_with_copy(&dirs, &sidecar, &target).unwrap();
        assert_eq!(std::fs::read_link(&link).unwrap(), target);
        assert_eq!(std::fs::read_to_string(&link).unwrap(), "v1");
        assert_eq!(status(&dirs, &target), CliStatus::Installed(link.clone()));
        // The next AppImage carries a new daemon: the copy is refreshed.
        std::fs::write(&sidecar, "v2").unwrap();
        assert!(refresh_copy(&sidecar, &target).unwrap());
        assert!(!refresh_copy(&sidecar, &target).unwrap());
        assert_eq!(std::fs::read_to_string(&link).unwrap(), "v2");
        // A link straight into an AppImage mount is ours, and stale.
        let old = PathBuf::from("/tmp/.mount_ItervoZZ/usr/bin/itervox");
        std::fs::remove_file(&link).unwrap();
        std::os::unix::fs::symlink(&old, &link).unwrap();
        assert_eq!(inspect(&link, &target), LinkState::Stale(old));
        assert_eq!(install(&dirs, &target).unwrap(), link);
        assert_eq!(std::fs::read_link(&link).unwrap(), target);
    }
}
