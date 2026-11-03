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

use std::io::ErrorKind;
use std::path::{Path, PathBuf};

/// The name of the link the app installs.
pub const LINK_NAME: &str = "itervox";

/// Where the link may go, in order of preference.
pub fn link_dirs(home: Option<&Path>) -> Vec<PathBuf> {
    let mut dirs = vec![PathBuf::from("/usr/local/bin")];
    if let Some(h) = home {
        dirs.push(h.join(".local/bin"));
    }
    dirs
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

/// Whether `target` is the bundled daemon of some `.app`.
fn is_bundle_sidecar(target: &Path) -> bool {
    target.ends_with(Path::new("Contents/MacOS").join(LINK_NAME))
        && target
            .ancestors()
            .nth(3)
            .and_then(Path::extension)
            .is_some_and(|e| e == "app")
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

/// Points `link` at `target` atomically (a temporary link renamed over it).
fn replace_symlink(link: &Path, target: &Path) -> std::io::Result<()> {
    let tmp = link.with_file_name(format!(".{LINK_NAME}.tmp-{}", std::process::id()));
    match std::fs::remove_file(&tmp) {
        Err(e) if e.kind() != ErrorKind::NotFound => return Err(e),
        _ => {}
    }
    std::os::unix::fs::symlink(target, &tmp)?;
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

#[cfg(test)]
mod tests {
    use super::*;

    struct Fixture {
        _tmp: tempfile::TempDir,
        dirs: Vec<PathBuf>,
        sidecar: PathBuf,
    }

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
}
