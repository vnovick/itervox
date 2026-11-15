//! Finding `ssh` and `gcloud` for the remote connections (#156).
//!
//! A Mac app started from Finder or at login gets launchd's minimal PATH
//! (`/usr/bin:/bin:/usr/sbin:/sbin`), which has `ssh` but not `gcloud`
//! installed through Homebrew or the Cloud SDK installer. Like the daemon
//! does for its agent CLIs (#119, cmd/itervox/login_path.go), the app asks
//! the user's login shell for its PATH, once, bounded in time, and merges it
//! after the inherited one. The merged PATH is also what the tunnel and
//! discovery processes run with: `gcloud` is a script that needs it to find
//! its Python.
//!
//! On Linux (#260) an app started from the desktop's launcher inherits the
//! session's PATH, which may also lack them; the same lookup applies, with
//! bash, zsh and fish as login shells. Windows has no login shell to ask:
//! the app inherits the user's PATH and finds `ssh.exe` and `gcloud.cmd`
//! there or in the Cloud SDK's install directories.

use std::ffi::{OsStr, OsString};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use crate::platform::Os;

/// Bounds the whole login-shell lookup, rc files included.
pub const LOGIN_SHELL_TIMEOUT: Duration = Duration::from_secs(5);

/// Brackets the PATH in the shell's output, so whatever an interactive rc
/// file prints (banners, prompts) is ignored.
pub const PATH_MARKER: &str = "__ITERVOX_DESKTOP_PATH__";

/// Most of the shell's stdout that is kept.
const MAX_SHELL_OUTPUT: usize = 256 << 10;

/// Where `gcloud` commonly lives when no PATH names it, searched last.
pub const FALLBACK_DIRS: &[&str] = &[
    "/opt/homebrew/bin",
    "/usr/local/bin",
    "/opt/homebrew/share/google-cloud-sdk/bin",
    "/usr/local/share/google-cloud-sdk/bin",
];

/// The executables the remote connections run, and the PATH they run with.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Tools {
    /// PATH for the spawned processes.
    pub path_env: OsString,
    pub ssh: Option<PathBuf>,
    pub gcloud: Option<PathBuf>,
    /// Where PATH came from: `inherited` or `login_shell`.
    pub path_source: &'static str,
}

impl Tools {
    pub fn ssh(&self) -> Result<&Path, String> {
        self.ssh
            .as_deref()
            .ok_or_else(|| "ssh was not found on PATH or in the login shell's PATH".to_string())
    }

    pub fn gcloud(&self) -> Result<&Path, String> {
        self.gcloud.as_deref().ok_or_else(|| {
            "gcloud was not found on PATH or in the login shell's PATH — install the Google Cloud CLI"
                .to_string()
        })
    }
}

/// The first executable named `name` in `path` (a PATH value). On
/// Windows the name is tried with the extensions a command runs with
/// (`ssh.exe`, `gcloud.cmd`).
pub fn find_in_path(name: &str, path: &OsStr) -> Option<PathBuf> {
    let names = executable_names(Os::current(), name);
    std::env::split_paths(path)
        .filter(|d| d.is_absolute())
        .flat_map(|d| names.iter().map(move |n| d.join(n)))
        .find(|p| is_executable(p))
}

/// The file names `name` may have as a command on `os`.
pub fn executable_names(os: Os, name: &str) -> Vec<String> {
    match os {
        Os::Windows => ["exe", "cmd", "bat"]
            .iter()
            .map(|ext| format!("{name}.{ext}"))
            .collect(),
        Os::Mac | Os::Linux => vec![name.to_string()],
    }
}

/// Where `gcloud` commonly lives on `os` when no PATH names it, searched
/// last: [`FALLBACK_DIRS`] on the Mac; on Linux the snap and the Cloud
/// SDK's own installer; on Windows the Cloud SDK installer's per-user and
/// per-machine locations.
pub fn fallback_dirs(os: Os, home: Option<&Path>) -> Vec<PathBuf> {
    match os {
        Os::Mac => FALLBACK_DIRS.iter().map(PathBuf::from).collect(),
        Os::Linux => {
            let mut v = vec![PathBuf::from("/snap/bin"), PathBuf::from("/usr/local/bin")];
            if let Some(h) = home {
                v.push(h.join("google-cloud-sdk/bin"));
            }
            v
        }
        Os::Windows => {
            let sdk = Path::new("Google")
                .join("Cloud SDK")
                .join("google-cloud-sdk")
                .join("bin");
            let mut v = Vec::new();
            if let Some(h) = home {
                v.push(h.join("AppData").join("Local").join(&sdk));
            }
            v.push(Path::new(r"C:\Program Files (x86)").join(&sdk));
            v.push(Path::new(r"C:\Program Files").join(&sdk));
            v
        }
    }
}

fn is_executable(p: &Path) -> bool {
    crate::platform::is_executable(p)
}

/// `a` followed by the directories of `b` not already in `a`.
pub fn merge_paths(a: &OsStr, b: &OsStr) -> OsString {
    let mut dirs: Vec<PathBuf> = std::env::split_paths(a).collect();
    for d in std::env::split_paths(b) {
        if !d.as_os_str().is_empty() && !dirs.contains(&d) {
            dirs.push(d);
        }
    }
    std::env::join_paths(dirs).unwrap_or_else(|_| a.to_os_string())
}

/// The arguments that make `shell` print its login PATH between markers.
/// csh and tcsh take `-l` only alone, so they source the login files
/// themselves. fish keeps PATH as a list: `string join` prints it with
/// colons whatever the fish version (bash and zsh print `$PATH` as is).
pub fn login_shell_args(shell: &Path) -> Vec<String> {
    let print = format!("printf '%s%s%s' {PATH_MARKER} \"$PATH\" {PATH_MARKER}");
    let base = shell
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    if base == "fish" {
        let fish_print =
            format!("printf '%s%s%s' {PATH_MARKER} (string join : $PATH) {PATH_MARKER}");
        return vec!["-l".into(), "-i".into(), "-c".into(), fish_print];
    }
    if base == "csh" || base == "tcsh" {
        vec![
            "-c".into(),
            format!(
                "if ( -r /etc/csh.login ) source /etc/csh.login >& /dev/null; if ( -r ~/.login ) source ~/.login >& /dev/null; {print}"
            ),
        ]
    } else {
        vec!["-l".into(), "-i".into(), "-c".into(), print]
    }
}

/// The PATH between the markers of `out`.
pub fn parse_marked_path(out: &[u8]) -> Option<String> {
    let s = String::from_utf8_lossy(out);
    let start = s.find(PATH_MARKER)? + PATH_MARKER.len();
    let rest = &s[start..];
    let end = rest.find(PATH_MARKER)?;
    let p = rest[..end].trim();
    (!p.is_empty()).then(|| p.to_string())
}

/// Asks `shell` for its login PATH, within `timeout`. The shell gets no
/// terminal and its own process group, so an rc file that waits for input
/// cannot hang the app.
pub fn login_shell_path(shell: &Path, timeout: Duration) -> Result<String, String> {
    use std::io::Read;
    let mut cmd = Command::new(shell);
    cmd.args(login_shell_args(shell))
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    crate::platform::own_process_group(&mut cmd);
    let mut child = cmd
        .spawn()
        .map_err(|e| format!("{}: {e}", shell.display()))?;
    let mut stdout = child.stdout.take().ok_or("no stdout")?;
    let reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = (&mut stdout)
            .take(MAX_SHELL_OUTPUT as u64)
            .read_to_end(&mut buf);
        buf
    });
    let deadline = Instant::now() + timeout;
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) if Instant::now() >= deadline => {
                kill_group(child.id());
                let _ = child.wait();
                return Err(format!(
                    "{} did not answer within {timeout:?}",
                    shell.display()
                ));
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(20)),
            Err(e) => return Err(e.to_string()),
        }
    }
    let out = reader.join().unwrap_or_default();
    parse_marked_path(&out).ok_or_else(|| format!("{} printed no PATH", shell.display()))
}

/// SIGKILLs the process group led by `pid` (the process tree on Windows).
pub use crate::platform::kill_group;

/// Resolves the tools: the inherited PATH first; when `ssh` or `gcloud` is
/// missing from it, the login shell's PATH merged after it; the fallback
/// directories last.
pub fn resolve_tools(inherited: &OsStr, shell: Option<&Path>, timeout: Duration) -> Tools {
    let mut path_env = inherited.to_os_string();
    let mut path_source = "inherited";
    let missing =
        |p: &OsStr| find_in_path("ssh", p).is_none() || find_in_path("gcloud", p).is_none();
    if missing(&path_env) {
        if let Some(sh) = shell.filter(|s| s.is_absolute()) {
            if let Ok(login) = login_shell_path(sh, timeout) {
                let merged = merge_paths(&path_env, OsStr::new(&login));
                if merged != path_env {
                    path_env = merged;
                    path_source = "login_shell";
                }
            }
        }
    }
    let fallback = std::env::join_paths(fallback_dirs(
        Os::current(),
        crate::platform::home_dir().as_deref(),
    ))
    .unwrap_or_default();
    let search = merge_paths(&path_env, &fallback);
    Tools {
        ssh: find_in_path("ssh", &search),
        gcloud: find_in_path("gcloud", &search),
        path_env: search,
        path_source,
    }
}

/// [`resolve_tools`] for this process: its PATH and `$SHELL` (Windows has
/// no login shell to ask; the app inherits the user's PATH there).
pub fn resolve_tools_for_app() -> Tools {
    let inherited = std::env::var_os("PATH").unwrap_or_else(|| "/usr/bin:/bin".into());
    let shell = std::env::var_os("SHELL").map(PathBuf::from);
    resolve_tools(&inherited, shell.as_deref(), LOGIN_SHELL_TIMEOUT)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(unix)]
    use std::os::unix::fs::PermissionsExt;

    #[cfg(unix)]
    fn exe(path: &Path, body: &str) {
        std::fs::write(path, body).unwrap();
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }

    /// #260: the login-shell lookup speaks bash, zsh and fish.
    #[test]
    fn login_shell_path_works_with_bash_zsh_and_fish() {
        for sh in ["/bin/bash", "/usr/bin/zsh"] {
            let a = login_shell_args(Path::new(sh));
            assert_eq!(&a[..3], &["-l", "-i", "-c"], "{sh}");
            assert!(a[3].contains("\"$PATH\""), "{sh}: {}", a[3]);
        }
        let f = login_shell_args(Path::new("/usr/bin/fish"));
        assert_eq!(&f[..3], &["-l", "-i", "-c"]);
        assert!(f[3].contains("(string join : $PATH)"), "{}", f[3]);
        assert!(!f[3].contains("\"$PATH\""), "{}", f[3]);
    }

    /// The real shells, where installed: each prints its login PATH
    /// between the markers (a missing shell is skipped).
    #[cfg(unix)]
    #[test]
    fn real_bash_zsh_and_fish_print_their_login_path() {
        let path = std::env::var_os("PATH").unwrap_or_default();
        let mut ran = 0;
        for name in ["bash", "zsh", "fish"] {
            let Some(sh) = find_in_path(name, &path) else {
                continue;
            };
            let p = login_shell_path(&sh, Duration::from_secs(20))
                .unwrap_or_else(|e| panic!("{name}: {e}"));
            assert!(
                std::env::split_paths(&p).any(|d| d.is_absolute()),
                "{name}: {p}"
            );
            // A list joined with spaces (fish's `$PATH` in old versions)
            // would read as one bogus directory.
            assert!(!p.contains(" /"), "{name}: {p}");
            ran += 1;
        }
        assert!(ran >= 1, "no bash, zsh or fish on PATH");
    }

    #[test]
    fn tools_are_found_with_each_platforms_names_and_fallbacks() {
        assert_eq!(executable_names(Os::Linux, "ssh"), ["ssh"]);
        assert_eq!(executable_names(Os::Mac, "gcloud"), ["gcloud"]);
        assert_eq!(
            executable_names(Os::Windows, "gcloud"),
            ["gcloud.exe", "gcloud.cmd", "gcloud.bat"]
        );
        let mac: Vec<PathBuf> = FALLBACK_DIRS.iter().map(PathBuf::from).collect();
        assert_eq!(fallback_dirs(Os::Mac, Some(Path::new("/Users/me"))), mac);
        let linux = fallback_dirs(Os::Linux, Some(Path::new("/home/me")));
        assert!(linux.contains(&PathBuf::from("/snap/bin")));
        assert!(linux.contains(&PathBuf::from("/home/me/google-cloud-sdk/bin")));
        let win = fallback_dirs(Os::Windows, Some(Path::new(r"C:\Users\me")));
        assert!(win[0].ends_with("AppData/Local/Google/Cloud SDK/google-cloud-sdk/bin"));
    }

    #[test]
    fn parse_marked_path_ignores_rc_noise() {
        let out = format!("Welcome!\n{PATH_MARKER}/a:/b{PATH_MARKER}\nbye");
        assert_eq!(parse_marked_path(out.as_bytes()).as_deref(), Some("/a:/b"));
        assert_eq!(parse_marked_path(b"no markers"), None);
    }

    #[cfg(unix)]
    #[test]
    fn merge_keeps_order_and_drops_duplicates() {
        let m = merge_paths(OsStr::new("/usr/bin:/bin"), OsStr::new("/opt/x:/bin"));
        assert_eq!(m, OsString::from("/usr/bin:/bin:/opt/x"));
    }

    #[test]
    fn csh_sources_login_files_instead_of_dash_l() {
        let a = login_shell_args(Path::new("/bin/tcsh"));
        assert_eq!(a[0], "-c");
        assert!(a[1].contains("/etc/csh.login"));
        let z = login_shell_args(Path::new("/bin/zsh"));
        assert_eq!(&z[..3], &["-l", "-i", "-c"]);
    }

    /// Launched from Finder: launchd's PATH has no gcloud; the login shell's
    /// profile adds its directory, so it is found and the tunnels run with
    /// that PATH.
    #[cfg(unix)]
    #[test]
    fn tools_come_from_the_login_shell_path_when_launched_from_finder() {
        let dir = tempfile::tempdir().unwrap();
        let sdk = dir.path().join("sdk-bin");
        let sys = dir.path().join("sys-bin");
        std::fs::create_dir_all(&sdk).unwrap();
        std::fs::create_dir_all(&sys).unwrap();
        exe(&sdk.join("gcloud"), "#!/bin/sh\n");
        exe(&sys.join("ssh"), "#!/bin/sh\n");
        let shell = dir.path().join("fake-zsh");
        exe(
            &shell,
            &format!(
                "#!/bin/sh\nPATH=\"{}:$PATH\"; export PATH\necho 'rc banner'\nwhile [ $# -gt 1 ]; do shift; done\nexec /bin/sh -c \"$1\"\n",
                sdk.display()
            ),
        );
        let inherited = OsString::from(sys.as_os_str());
        let t = resolve_tools(&inherited, Some(&shell), Duration::from_secs(5));
        assert_eq!(t.ssh.as_deref(), Some(sys.join("ssh").as_path()));
        assert_eq!(t.gcloud.as_deref(), Some(sdk.join("gcloud").as_path()));
        assert_eq!(t.path_source, "login_shell");
        assert!(std::env::split_paths(&t.path_env).any(|d| d == sdk));

        // Without a usable login shell, gcloud stays missing and says so.
        let t = resolve_tools(&inherited, None, Duration::from_secs(5));
        assert_eq!(t.path_source, "inherited");
        if t.gcloud.is_none() {
            assert!(t.gcloud().unwrap_err().contains("gcloud was not found"));
        }
    }

    #[cfg(unix)]
    #[test]
    fn a_hanging_login_shell_is_bounded() {
        let dir = tempfile::tempdir().unwrap();
        let shell = dir.path().join("hang-sh");
        exe(&shell, "#!/bin/sh\nsleep 30\n");
        let t0 = Instant::now();
        assert!(login_shell_path(&shell, Duration::from_millis(300)).is_err());
        assert!(t0.elapsed() < Duration::from_secs(5));
    }
}
