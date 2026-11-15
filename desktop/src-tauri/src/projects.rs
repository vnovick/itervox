//! The projects the user added in the app, persisted as JSON in the app's
//! config directory (`projects.json`). Daemons found by `status --all --json`
//! are listed whether or not they were added; an added project is listed
//! also while its daemon is stopped, so it can be started. The project
//! rail's order, pins and collapsed state (#244) are kept in the same file.

use std::io::Write;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// File name inside the app config dir.
pub const PROJECTS_FILE: &str = "projects.json";

const FILE_VERSION: u32 = 1;

/// One added project.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SavedProject {
    /// Absolute path of the project's WORKFLOW.md; the project key.
    pub workflow: String,
}

/// How the user arranged the project rail of the dashboard windows (#244).
/// Keys are project keys: a WORKFLOW.md path, or a remote daemon's key.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RailPrefs {
    /// The rows' order inside their groups, as the user dragged them; a
    /// project not listed keeps its default place after the listed ones.
    pub order: Vec<String>,
    /// Pinned projects, top of the rail, in this order.
    pub pinned: Vec<String>,
    /// The rail shows icons only.
    pub collapsed: bool,
}

impl RailPrefs {
    pub fn is_pinned(&self, key: &str) -> bool {
        self.pinned.iter().any(|k| k == key)
    }

    /// Pins (or unpins) a project; false when nothing changed.
    pub fn set_pinned(&mut self, key: &str, pinned: bool) -> bool {
        if pinned == self.is_pinned(key) {
            return false;
        }
        if pinned {
            self.pinned.push(key.to_string());
        } else {
            self.pinned.retain(|k| k != key);
        }
        true
    }

    /// Forgets a project.
    pub fn forget(&mut self, key: &str) {
        self.pinned.retain(|k| k != key);
        self.order.retain(|k| k != key);
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
struct FileShape {
    version: u32,
    projects: Vec<SavedProject>,
    /// Absent in files written before #244; ignored by older apps.
    #[serde(default, skip_serializing_if = "is_default_rail")]
    rail: RailPrefs,
}

fn is_default_rail(r: &RailPrefs) -> bool {
    *r == RailPrefs::default()
}

/// The added projects, in the order they were added, and the rail layout.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SavedProjects {
    pub projects: Vec<SavedProject>,
    pub rail: RailPrefs,
}

/// The project key for a WORKFLOW.md path: absolute, `.` and `..` resolved
/// lexically (no symlink resolution, matching the daemon's
/// `filepath.Abs`).
pub fn project_key(path: &Path, cwd: &Path) -> String {
    let abs = if path.is_absolute() {
        path.to_path_buf()
    } else {
        cwd.join(path)
    };
    let mut out = PathBuf::new();
    for c in abs.components() {
        match c {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                out.pop();
            }
            other => out.push(other.as_os_str()),
        }
    }
    out.to_string_lossy().into_owned()
}

impl SavedProjects {
    /// Loads the file; a missing file is an empty list. A corrupt file is an
    /// error so the caller can keep it instead of overwriting the user's list.
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
                    projects: shape.projects,
                    rail: shape.rail,
                })
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Self::default()),
            Err(e) => Err(format!("{}: {e}", path.display())),
        }
    }

    /// Writes the file atomically (temp file + rename), creating the dir.
    pub fn save(&self, path: &Path) -> Result<(), String> {
        let dir = path
            .parent()
            .ok_or_else(|| format!("{}: no parent directory", path.display()))?;
        std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
        let data = serde_json::to_vec_pretty(&FileShape {
            version: FILE_VERSION,
            projects: self.projects.clone(),
            rail: self.rail.clone(),
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

    pub fn contains(&self, key: &str) -> bool {
        self.projects.iter().any(|p| p.workflow == key)
    }

    /// Adds a project; false when it was already there.
    pub fn add(&mut self, key: &str) -> bool {
        if self.contains(key) {
            return false;
        }
        self.projects.push(SavedProject {
            workflow: key.to_string(),
        });
        true
    }

    /// Removes a project (and its place in the rail); false when it was
    /// not there.
    pub fn remove(&mut self, key: &str) -> bool {
        let before = self.projects.len();
        self.projects.retain(|p| p.workflow != key);
        self.rail.forget(key);
        before != self.projects.len()
    }
}

/// Checks that `path` is a WORKFLOW file the daemon can be started with.
pub fn validate_workflow_file(path: &Path) -> Result<(), String> {
    let meta = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    if !meta.is_file() {
        return Err(format!("{} is not a file", path.display()));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_file_is_empty_and_round_trip_persists() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sub").join(PROJECTS_FILE);
        let mut s = SavedProjects::load(&path).unwrap();
        assert!(s.projects.is_empty());
        assert!(s.add("/a/WORKFLOW.md"));
        assert!(!s.add("/a/WORKFLOW.md"), "no duplicates");
        assert!(s.add("/b/WORKFLOW.md"));
        s.save(&path).unwrap();
        let back = SavedProjects::load(&path).unwrap();
        assert_eq!(back, s);
        let mut back = back;
        assert!(back.remove("/a/WORKFLOW.md"));
        assert!(!back.remove("/a/WORKFLOW.md"));
        back.save(&path).unwrap();
        assert_eq!(
            SavedProjects::load(&path).unwrap().projects,
            vec![SavedProject {
                workflow: "/b/WORKFLOW.md".into()
            }]
        );
        assert!(!path.with_extension("json.tmp").exists());
    }

    #[test]
    fn corrupt_file_is_an_error_not_an_empty_list() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join(PROJECTS_FILE);
        std::fs::write(&path, b"{oops").unwrap();
        assert!(SavedProjects::load(&path).is_err());
        std::fs::write(&path, br#"{"version": 9, "projects": []}"#).unwrap();
        assert!(SavedProjects::load(&path).is_err());
    }

    #[test]
    fn project_key_is_absolute_and_lexically_clean() {
        let cwd = Path::new("/home/me");
        assert_eq!(
            project_key(Path::new("proj/./WORKFLOW.md"), cwd),
            "/home/me/proj/WORKFLOW.md"
        );
        assert_eq!(
            project_key(Path::new("/x/y/../WORKFLOW.md"), cwd),
            "/x/WORKFLOW.md"
        );
    }

    #[test]
    fn validate_workflow_file_rejects_dirs_and_missing() {
        let dir = tempfile::tempdir().unwrap();
        assert!(validate_workflow_file(dir.path()).is_err());
        assert!(validate_workflow_file(&dir.path().join("nope.md")).is_err());
        let wf = dir.path().join("WORKFLOW.md");
        std::fs::write(&wf, "---\n").unwrap();
        assert!(validate_workflow_file(&wf).is_ok());
    }
}
