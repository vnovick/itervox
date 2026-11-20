//! Which terminal program "Open in Terminal" opens (#278): the user's own,
//! chosen with the Sessions window's "Terminal app" picker.
//!
//! The choice is **Automatic** (the default) or one of the programs found
//! on this machine — detected, never assumed:
//!
//! - macOS: the app bundles in `/Applications` and `~/Applications` (and
//!   Terminal in its system folder) whose `Contents/Info.plist` names the
//!   program's bundle id. Binary and XML plists both hold the id as plain
//!   ASCII, so a byte search reads either without a plist parser.
//! - Linux: `$TERMINAL` (one program, on PATH or an absolute path), then
//!   the known terminals on PATH.
//! - Windows: Windows Terminal, PowerShell 7, Windows PowerShell and
//!   Command Prompt, on PATH or where their installers put them.
//!
//! Automatic is: on macOS, Terminal (the default terminal app is kept by
//! LaunchServices in a binary plist, which the app does not parse); on
//! Linux `$TERMINAL`, else the first terminal found; on Windows, Windows
//! Terminal, else PowerShell (7, then Windows PowerShell), else Command
//! Prompt. A saved choice that is no longer found falls back to Automatic
//! and the picker says so. The choice is saved with the project list
//! (`projects.json`, [`crate::projects::SavedProjects::terminal_app`]).
//!
//! How each program is started is [`crate::terminal_launch`].

use std::path::{Path, PathBuf};

use serde::Serialize;

use crate::platform::Os;

/// The saved value, and the picker's id, of Automatic.
pub const AUTOMATIC: &str = "auto";

/// A terminal program the app knows how to start.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum App {
    /// macOS's Terminal.
    MacTerminal,
    ITerm2,
    Warp,
    Ghostty,
    WezTerm,
    Kitty,
    Alacritty,
    GnomeTerminal,
    Konsole,
    Foot,
    Xfce4Terminal,
    Tilix,
    Xterm,
    /// Debian's alternative for the default terminal.
    XTerminalEmulator,
    /// `$TERMINAL` naming none of the programs above: run as `<it> -e …`.
    Configured,
    WindowsTerminal,
    Pwsh,
    WindowsPowerShell,
    Cmd,
}

/// Every app, in the order the picker lists them on its platform.
pub const ALL: [App; 19] = [
    App::MacTerminal,
    App::ITerm2,
    App::Ghostty,
    App::WezTerm,
    App::Kitty,
    App::Alacritty,
    App::Warp,
    App::GnomeTerminal,
    App::Konsole,
    App::Foot,
    App::Xfce4Terminal,
    App::Tilix,
    App::Xterm,
    App::XTerminalEmulator,
    App::Configured,
    App::WindowsTerminal,
    App::Pwsh,
    App::WindowsPowerShell,
    App::Cmd,
];

impl App {
    /// The id the preference saves.
    pub fn id(self) -> &'static str {
        match self {
            App::MacTerminal => "terminal",
            App::ITerm2 => "iterm2",
            App::Warp => "warp",
            App::Ghostty => "ghostty",
            App::WezTerm => "wezterm",
            App::Kitty => "kitty",
            App::Alacritty => "alacritty",
            App::GnomeTerminal => "gnome-terminal",
            App::Konsole => "konsole",
            App::Foot => "foot",
            App::Xfce4Terminal => "xfce4-terminal",
            App::Tilix => "tilix",
            App::Xterm => "xterm",
            App::XTerminalEmulator => "x-terminal-emulator",
            App::Configured => "terminal-env",
            App::WindowsTerminal => "windows-terminal",
            App::Pwsh => "pwsh",
            App::WindowsPowerShell => "windows-powershell",
            App::Cmd => "cmd",
        }
    }

    pub fn from_id(id: &str) -> Option<App> {
        ALL.into_iter().find(|a| a.id() == id)
    }

    /// The picker's name for it.
    pub fn name(self) -> &'static str {
        match self {
            App::MacTerminal => "Terminal",
            App::ITerm2 => "iTerm2",
            App::Warp => "Warp",
            App::Ghostty => "Ghostty",
            App::WezTerm => "WezTerm",
            App::Kitty => "kitty",
            App::Alacritty => "Alacritty",
            App::GnomeTerminal => "GNOME Terminal",
            App::Konsole => "Konsole",
            App::Foot => "foot",
            App::Xfce4Terminal => "Xfce Terminal",
            App::Tilix => "Tilix",
            App::Xterm => "xterm",
            App::XTerminalEmulator => "Default terminal (x-terminal-emulator)",
            App::Configured => "$TERMINAL",
            App::WindowsTerminal => "Windows Terminal",
            App::Pwsh => "PowerShell 7",
            App::WindowsPowerShell => "Windows PowerShell",
            App::Cmd => "Command Prompt",
        }
    }

    /// The name on the button, "Open in <it>".
    fn short(self) -> &'static str {
        match self {
            App::ITerm2 => "iTerm",
            App::XTerminalEmulator => "Terminal",
            App::Pwsh | App::WindowsPowerShell => "PowerShell",
            a => a.name(),
        }
    }

    /// The program on PATH (Linux) that is this app.
    fn linux_program(self) -> Option<&'static str> {
        Some(match self {
            App::GnomeTerminal => "gnome-terminal",
            App::Konsole => "konsole",
            App::Kitty => "kitty",
            App::Alacritty => "alacritty",
            App::WezTerm => "wezterm",
            App::Ghostty => "ghostty",
            App::Foot => "foot",
            App::Xfce4Terminal => "xfce4-terminal",
            App::Tilix => "tilix",
            App::Xterm => "xterm",
            App::XTerminalEmulator => "x-terminal-emulator",
            _ => return None,
        })
    }

    /// The bundle names and bundle id of a macOS app.
    fn mac_bundle(self) -> Option<(&'static [&'static str], &'static str)> {
        Some(match self {
            App::MacTerminal => (&["Terminal.app"], "com.apple.Terminal"),
            App::ITerm2 => (&["iTerm.app", "iTerm2.app"], "com.googlecode.iterm2"),
            App::Ghostty => (&["Ghostty.app"], "com.mitchellh.ghostty"),
            App::WezTerm => (&["WezTerm.app"], "com.github.wez.wezterm"),
            App::Kitty => (&["kitty.app"], "net.kovidgoyal.kitty"),
            App::Alacritty => (&["Alacritty.app"], "org.alacritty"),
            App::Warp => (&["Warp.app"], "dev.warp.Warp-Stable"),
            _ => return None,
        })
    }
}

/// The Linux terminals looked for on PATH, in this order.
pub const LINUX_PATH_ORDER: [App; 11] = [
    App::GnomeTerminal,
    App::Konsole,
    App::Kitty,
    App::Alacritty,
    App::WezTerm,
    App::Ghostty,
    App::Foot,
    App::Xfce4Terminal,
    App::Tilix,
    App::Xterm,
    App::XTerminalEmulator,
];

/// A terminal found on this machine: the app and what starts it (a bundle
/// path on macOS, a program path on Linux and Windows).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Detected {
    pub app: App,
    pub program: PathBuf,
}

impl Detected {
    pub fn new(app: App, program: impl Into<PathBuf>) -> Self {
        Self {
            app,
            program: program.into(),
        }
    }

    /// Its name in the picker; `$TERMINAL`'s program by its file name.
    pub fn name(&self) -> String {
        match self.app {
            App::Configured => format!("{} ($TERMINAL)", self.file_name()),
            a => a.name().to_string(),
        }
    }

    fn file_name(&self) -> String {
        self.program
            .file_name()
            .map(|f| f.to_string_lossy().into_owned())
            .unwrap_or_else(|| self.program.to_string_lossy().into_owned())
    }

    /// "Open in iTerm", "Open in Ghostty", …
    pub fn open_label(&self) -> String {
        match self.app {
            App::Configured => format!("Open in {}", self.file_name()),
            a => format!("Open in {}", a.short()),
        }
    }
}

/// What detection may look at, so tests can fake the filesystem and PATH.
pub trait Probe {
    /// The bytes of a file (an `Info.plist`), if it can be read.
    fn read(&self, path: &Path) -> Option<Vec<u8>>;
    /// Whether `path` is a program this user can run.
    fn runnable(&self, path: &Path) -> bool;
    /// The program `name` on PATH.
    fn which(&self, name: &str) -> Option<PathBuf>;
    fn var(&self, name: &str) -> Option<String>;
    fn home(&self) -> Option<PathBuf>;
}

/// The real machine, with the PATH the app runs ssh and gcloud with.
pub struct SystemProbe<'a> {
    pub path_env: &'a std::ffi::OsStr,
}

impl Probe for SystemProbe<'_> {
    fn read(&self, path: &Path) -> Option<Vec<u8>> {
        std::fs::read(path).ok()
    }

    fn runnable(&self, path: &Path) -> bool {
        // Windows' app execution aliases (wt.exe in WindowsApps) are
        // reparse points that metadata cannot always follow.
        if cfg!(windows) {
            std::fs::symlink_metadata(path).is_ok()
        } else {
            crate::platform::is_executable(path)
        }
    }

    fn which(&self, name: &str) -> Option<PathBuf> {
        crate::shellpath::find_in_path(name, self.path_env)
    }

    fn var(&self, name: &str) -> Option<String> {
        std::env::var(name).ok()
    }

    fn home(&self) -> Option<PathBuf> {
        crate::platform::home_dir()
    }
}

/// The terminals found on this machine, in the picker's order, and a note
/// when `$TERMINAL` is set but unusable.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Detection {
    pub apps: Vec<Detected>,
    pub note: Option<String>,
}

/// Finds the terminals installed on `os`.
pub fn detect(os: Os, p: &dyn Probe) -> Detection {
    match os {
        Os::Mac => Detection {
            apps: detect_mac(p),
            note: None,
        },
        Os::Linux => detect_linux(p),
        Os::Windows => Detection {
            apps: detect_windows(p),
            note: None,
        },
    }
}

fn detect_mac(p: &dyn Probe) -> Vec<Detected> {
    let mut dirs = vec![PathBuf::from("/Applications")];
    if let Some(h) = p.home() {
        dirs.push(h.join("Applications"));
    }
    let system = [
        PathBuf::from("/System/Applications/Utilities"),
        PathBuf::from("/Applications/Utilities"),
    ];
    ALL.into_iter()
        .filter_map(|app| {
            let (names, id) = app.mac_bundle()?;
            let search: Vec<&PathBuf> = if app == App::MacTerminal {
                system.iter().collect()
            } else {
                dirs.iter().collect()
            };
            search
                .into_iter()
                .flat_map(|d| names.iter().map(move |n| d.join(n)))
                .find(|bundle| {
                    p.read(&bundle.join("Contents").join("Info.plist"))
                        .is_some_and(|plist| contains(&plist, id.as_bytes()))
                })
                .map(|bundle| Detected::new(app, bundle))
        })
        .collect()
}

fn contains(hay: &[u8], needle: &[u8]) -> bool {
    hay.windows(needle.len()).any(|w| w == needle)
}

fn detect_linux(p: &dyn Probe) -> Detection {
    let mut apps = Vec::new();
    let mut note = None;
    if let Some(t) = p.var("TERMINAL").map(|t| t.trim().to_string()) {
        if !t.is_empty() {
            match configured(&t, p) {
                Some(d) => apps.push(d),
                None => {
                    note = Some(format!(
                        "$TERMINAL ({t}) is not one terminal program on PATH, so it is not offered."
                    ))
                }
            }
        }
    }
    for app in LINUX_PATH_ORDER {
        if apps.iter().any(|d: &Detected| d.app == app) {
            continue;
        }
        if let Some(path) = app.linux_program().and_then(|n| p.which(n)) {
            apps.push(Detected::new(app, path));
        }
    }
    Detection { apps, note }
}

/// `$TERMINAL`: one word, not an option, an absolute path this user can
/// run or a program on PATH. A known terminal keeps its own flags.
fn configured(t: &str, p: &dyn Probe) -> Option<Detected> {
    if t.starts_with('-') || t.chars().any(|c| c.is_whitespace() || c.is_control()) {
        return None;
    }
    let path = if Path::new(t).is_absolute() {
        p.runnable(Path::new(t)).then(|| PathBuf::from(t))?
    } else if t.contains('/') {
        return None;
    } else {
        p.which(t)?
    };
    let base = path.file_name()?.to_string_lossy().into_owned();
    let app = LINUX_PATH_ORDER
        .into_iter()
        .find(|a| a.linux_program() == Some(base.as_str()))
        .unwrap_or(App::Configured);
    Some(Detected::new(app, path))
}

fn detect_windows(p: &dyn Probe) -> Vec<Detected> {
    let root = p
        .var("SystemRoot")
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| r"C:\Windows".into());
    let local = p.var("LOCALAPPDATA").filter(|s| !s.is_empty());
    let programs = p
        .var("ProgramFiles")
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| r"C:\Program Files".into());
    let known: [(App, &str, Vec<String>); 4] = [
        (
            App::WindowsTerminal,
            "wt",
            local
                .iter()
                .map(|l| format!(r"{l}\Microsoft\WindowsApps\wt.exe"))
                .collect(),
        ),
        (
            App::Pwsh,
            "pwsh",
            vec![format!(r"{programs}\PowerShell\7\pwsh.exe")],
        ),
        (
            App::WindowsPowerShell,
            "powershell",
            vec![format!(
                r"{root}\System32\WindowsPowerShell\v1.0\powershell.exe"
            )],
        ),
        (App::Cmd, "cmd", vec![format!(r"{root}\System32\cmd.exe")]),
    ];
    known
        .into_iter()
        .filter_map(|(app, name, places)| {
            p.which(name)
                .or_else(|| {
                    places
                        .into_iter()
                        .map(PathBuf::from)
                        .find(|f| p.runnable(f))
                })
                .map(|path| Detected::new(app, path))
        })
        .collect()
}

/// What Automatic opens on `os`, given what was found.
pub fn automatic(os: Os, found: &[Detected]) -> Detected {
    let first = |apps: &[App]| {
        apps.iter()
            .find_map(|a| found.iter().find(|d| d.app == *a).cloned())
    };
    match os {
        Os::Mac => first(&[App::MacTerminal])
            .unwrap_or_else(|| Detected::new(App::MacTerminal, "Terminal")),
        Os::Linux => found
            .first()
            .cloned()
            .unwrap_or_else(|| Detected::new(App::XTerminalEmulator, "x-terminal-emulator")),
        Os::Windows => first(&[
            App::WindowsTerminal,
            App::Pwsh,
            App::WindowsPowerShell,
            App::Cmd,
        ])
        .unwrap_or_else(|| Detected::new(App::WindowsTerminal, "wt.exe")),
    }
}

/// The terminal to open: the saved choice when it is still found, else
/// Automatic.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Resolved {
    pub app: Detected,
    /// Whether the saved choice is in use (false for Automatic).
    pub chosen: bool,
    /// Why the saved choice is not in use.
    pub note: Option<String>,
}

pub fn resolve(os: Os, choice: Option<&str>, found: &[Detected]) -> Resolved {
    let auto = automatic(os, found);
    let Some(id) = choice.filter(|c| *c != AUTOMATIC && !c.is_empty()) else {
        return Resolved {
            app: auto,
            chosen: false,
            note: None,
        };
    };
    if let Some(d) = found.iter().find(|d| d.app.id() == id) {
        return Resolved {
            app: d.clone(),
            chosen: true,
            note: None,
        };
    }
    let name = App::from_id(id).map_or("The chosen terminal app", App::name);
    Resolved {
        note: Some(format!(
            "{name} was not found on this machine: using Automatic ({}).",
            auto.name()
        )),
        app: auto,
        chosen: false,
    }
}

/// One choice of the picker.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct PickerOption {
    pub id: String,
    pub label: String,
}

/// The "Terminal app" picker as the Sessions window shows it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Picker {
    /// The selected option's id ([`AUTOMATIC`] while falling back).
    pub choice: String,
    pub options: Vec<PickerOption>,
    /// The label of "Open in <terminal>".
    pub open_label: String,
    /// A saved choice that is no longer found, or an unusable `$TERMINAL`.
    pub note: Option<String>,
}

pub fn picker(os: Os, choice: Option<&str>, found: &Detection) -> Picker {
    let r = resolve(os, choice, &found.apps);
    let auto = automatic(os, &found.apps);
    let mut options = vec![PickerOption {
        id: AUTOMATIC.into(),
        label: format!("Automatic ({})", auto.name()),
    }];
    options.extend(found.apps.iter().map(|d| PickerOption {
        id: d.app.id().into(),
        label: d.name(),
    }));
    let note = match (r.note, &found.note) {
        (Some(a), Some(b)) => Some(format!("{a} {b}")),
        (a, b) => a.or_else(|| b.clone()),
    };
    Picker {
        choice: if r.chosen {
            r.app.app.id().into()
        } else {
            AUTOMATIC.into()
        },
        options,
        open_label: r.app.open_label(),
        note,
    }
}

/// Whether `id` may be saved: Automatic, or a terminal found now.
pub fn may_save(id: &str, found: &Detection) -> bool {
    id == AUTOMATIC || found.apps.iter().any(|d| d.app.id() == id)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[derive(Default)]
    struct Fake {
        files: HashMap<PathBuf, Vec<u8>>,
        path: HashMap<String, PathBuf>,
        runnable: Vec<PathBuf>,
        vars: HashMap<String, String>,
        home: Option<PathBuf>,
    }

    impl Fake {
        fn bundle(mut self, dir: &str, name: &str, id: &str) -> Self {
            let plist = format!(
                "<?xml version=\"1.0\"?><plist><dict><key>CFBundleIdentifier</key><string>{id}</string></dict></plist>"
            );
            self.files.insert(
                Path::new(dir).join(name).join("Contents/Info.plist"),
                plist.into_bytes(),
            );
            self
        }
        fn on_path(mut self, name: &str, at: &str) -> Self {
            self.path.insert(name.into(), at.into());
            self
        }
        fn var(mut self, k: &str, v: &str) -> Self {
            self.vars.insert(k.into(), v.into());
            self
        }
    }

    impl Probe for Fake {
        fn read(&self, path: &Path) -> Option<Vec<u8>> {
            self.files.get(path).cloned()
        }
        fn runnable(&self, path: &Path) -> bool {
            self.runnable.iter().any(|p| p == path)
        }
        fn which(&self, name: &str) -> Option<PathBuf> {
            self.path.get(name).cloned()
        }
        fn var(&self, name: &str) -> Option<String> {
            self.vars.get(name).cloned()
        }
        fn home(&self) -> Option<PathBuf> {
            self.home.clone()
        }
    }

    fn ids(d: &Detection) -> Vec<&'static str> {
        d.apps.iter().map(|d| d.app.id()).collect()
    }

    /// macOS: bundles in /Applications and ~/Applications count only when
    /// their Info.plist names the program's bundle id.
    #[test]
    fn terminal_app_detects_mac_bundles_by_bundle_id() {
        let mut fake = Fake {
            home: Some("/Users/me".into()),
            ..Default::default()
        }
        .bundle(
            "/System/Applications/Utilities",
            "Terminal.app",
            "com.apple.Terminal",
        )
        .bundle("/Applications", "iTerm.app", "com.googlecode.iterm2")
        .bundle(
            "/Users/me/Applications",
            "Ghostty.app",
            "com.mitchellh.ghostty",
        )
        .bundle("/Applications", "kitty.app", "net.kovidgoyal.kitty")
        .bundle("/Applications", "Warp.app", "dev.warp.Warp-Stable")
        // Named like WezTerm, but some other program.
        .bundle("/Applications", "WezTerm.app", "com.example.other");
        // A binary plist holds the id as plain ASCII too.
        fake.files.insert(
            "/Applications/Alacritty.app/Contents/Info.plist".into(),
            b"bplist00\xd1\x01\x02_\x10\x12CFBundleIdentifier\\org.alacritty\x08".to_vec(),
        );
        let d = detect(Os::Mac, &fake);
        assert_eq!(
            ids(&d),
            [
                "terminal",
                "iterm2",
                "ghostty",
                "kitty",
                "alacritty",
                "warp"
            ]
        );
        assert_eq!(
            d.apps[2].program,
            PathBuf::from("/Users/me/Applications/Ghostty.app")
        );
        assert_eq!(d.apps[1].program, PathBuf::from("/Applications/iTerm.app"));
        assert!(detect(Os::Mac, &Fake::default()).apps.is_empty());
    }

    /// Linux: $TERMINAL first, then the known terminals on PATH; a known
    /// $TERMINAL keeps its own flags and is listed once.
    #[test]
    fn terminal_app_detects_linux_terminals_on_path() {
        let fake = Fake::default()
            .on_path("xterm", "/usr/bin/xterm")
            .on_path("kitty", "/usr/bin/kitty")
            .on_path("gnome-terminal", "/usr/bin/gnome-terminal")
            .on_path("foot", "/usr/bin/foot")
            .on_path("x-terminal-emulator", "/usr/bin/x-terminal-emulator");
        assert_eq!(
            ids(&detect(Os::Linux, &fake)),
            [
                "gnome-terminal",
                "kitty",
                "foot",
                "xterm",
                "x-terminal-emulator"
            ]
        );

        let d = detect(Os::Linux, &fake.var("TERMINAL", "kitty"));
        assert_eq!(
            ids(&d),
            [
                "kitty",
                "gnome-terminal",
                "foot",
                "xterm",
                "x-terminal-emulator"
            ]
        );
        assert_eq!(d.note, None);

        let mut odd = Fake::default()
            .on_path("xterm", "/usr/bin/xterm")
            .var("TERMINAL", "/opt/st/st");
        odd.runnable.push("/opt/st/st".into());
        let d = detect(Os::Linux, &odd);
        assert_eq!(d.apps[0], Detected::new(App::Configured, "/opt/st/st"));
        assert_eq!(d.apps[0].name(), "st ($TERMINAL)");
        assert_eq!(d.apps[0].open_label(), "Open in st");

        for bad in ["xterm -hold", "-e", "/opt/missing", "rel/st", "nowhere"] {
            let d = detect(
                Os::Linux,
                &Fake::default()
                    .on_path("xterm", "/usr/bin/xterm")
                    .var("TERMINAL", bad),
            );
            assert_eq!(ids(&d), ["xterm"], "{bad:?}");
            assert!(d.note.unwrap().contains("$TERMINAL"), "{bad:?}");
        }
    }

    /// Windows: on PATH, or where the installers put them.
    #[test]
    fn terminal_app_detects_windows_shells() {
        let mut fake = Fake::default()
            .var("SystemRoot", r"C:\Windows")
            .var("LOCALAPPDATA", r"C:\Users\me\AppData\Local")
            .on_path("pwsh", r"C:\Program Files\PowerShell\7\pwsh.exe");
        fake.runnable.extend([
            PathBuf::from(r"C:\Users\me\AppData\Local\Microsoft\WindowsApps\wt.exe"),
            PathBuf::from(r"C:\Windows\System32\cmd.exe"),
        ]);
        let d = detect(Os::Windows, &fake);
        assert_eq!(ids(&d), ["windows-terminal", "pwsh", "cmd"]);
        assert_eq!(
            d.apps[0].program,
            PathBuf::from(r"C:\Users\me\AppData\Local\Microsoft\WindowsApps\wt.exe")
        );
    }

    /// Automatic: Terminal on macOS; $TERMINAL, else the first found, on
    /// Linux; Windows Terminal, else PowerShell, on Windows.
    #[test]
    fn terminal_app_automatic_resolution_order() {
        let mac = [
            Detected::new(App::ITerm2, "/Applications/iTerm.app"),
            Detected::new(
                App::MacTerminal,
                "/System/Applications/Utilities/Terminal.app",
            ),
        ];
        assert_eq!(automatic(Os::Mac, &mac).app, App::MacTerminal);
        assert_eq!(
            automatic(Os::Mac, &[]),
            Detected::new(App::MacTerminal, "Terminal")
        );

        let linux = [
            Detected::new(App::Kitty, "/usr/bin/kitty"),
            Detected::new(App::Xterm, "/usr/bin/xterm"),
        ];
        assert_eq!(automatic(Os::Linux, &linux).app, App::Kitty);
        assert_eq!(
            automatic(Os::Linux, &[]),
            Detected::new(App::XTerminalEmulator, "x-terminal-emulator")
        );

        let pw = Detected::new(App::WindowsPowerShell, "powershell.exe");
        let p7 = Detected::new(App::Pwsh, "pwsh.exe");
        let wt = Detected::new(App::WindowsTerminal, "wt.exe");
        let cmd = Detected::new(App::Cmd, "cmd.exe");
        assert_eq!(
            automatic(
                Os::Windows,
                &[cmd.clone(), pw.clone(), p7.clone(), wt.clone()]
            )
            .app,
            App::WindowsTerminal
        );
        assert_eq!(
            automatic(Os::Windows, &[cmd.clone(), pw.clone(), p7]).app,
            App::Pwsh
        );
        assert_eq!(
            automatic(Os::Windows, &[cmd.clone(), pw]).app,
            App::WindowsPowerShell
        );
        assert_eq!(automatic(Os::Windows, &[cmd]).app, App::Cmd);
        assert_eq!(automatic(Os::Windows, &[]), wt);
    }

    /// A saved choice still installed is used; one that is gone falls back
    /// to Automatic, and the picker says so.
    #[test]
    fn terminal_app_missing_choice_falls_back_to_automatic() {
        let found = Detection {
            apps: vec![
                Detected::new(
                    App::MacTerminal,
                    "/System/Applications/Utilities/Terminal.app",
                ),
                Detected::new(App::ITerm2, "/Applications/iTerm.app"),
            ],
            note: None,
        };
        let r = resolve(Os::Mac, Some("iterm2"), &found.apps);
        assert!(r.chosen);
        assert_eq!(r.app.app, App::ITerm2);
        assert_eq!(r.note, None);
        for auto in [None, Some("auto"), Some("")] {
            let r = resolve(Os::Mac, auto, &found.apps);
            assert!(!r.chosen);
            assert_eq!(r.app.app, App::MacTerminal);
        }

        let gone = resolve(Os::Mac, Some("ghostty"), &found.apps);
        assert!(!gone.chosen);
        assert_eq!(gone.app.app, App::MacTerminal);
        assert_eq!(
            gone.note.as_deref(),
            Some("Ghostty was not found on this machine: using Automatic (Terminal).")
        );
        let p = picker(Os::Mac, Some("ghostty"), &found);
        assert_eq!(p.choice, AUTOMATIC);
        assert_eq!(p.open_label, "Open in Terminal");
        assert!(p.note.unwrap().contains("Ghostty was not found"));
        assert!(resolve(Os::Mac, Some("nonsense"), &found.apps)
            .note
            .unwrap()
            .starts_with("The chosen terminal app was not found"));

        assert!(may_save("auto", &found));
        assert!(may_save("iterm2", &found));
        assert!(!may_save("ghostty", &found));
        assert!(!may_save("../x", &found));
    }

    /// The picker lists Automatic (naming what it opens) and what was
    /// found; the button names the program.
    #[test]
    fn terminal_app_button_label_names_the_program() {
        let mac = Detection {
            apps: vec![
                Detected::new(
                    App::MacTerminal,
                    "/System/Applications/Utilities/Terminal.app",
                ),
                Detected::new(App::ITerm2, "/Applications/iTerm.app"),
                Detected::new(App::Ghostty, "/Applications/Ghostty.app"),
            ],
            note: None,
        };
        let p = picker(Os::Mac, Some("iterm2"), &mac);
        assert_eq!(p.choice, "iterm2");
        assert_eq!(p.open_label, "Open in iTerm");
        assert_eq!(
            p.options,
            vec![
                PickerOption {
                    id: "auto".into(),
                    label: "Automatic (Terminal)".into()
                },
                PickerOption {
                    id: "terminal".into(),
                    label: "Terminal".into()
                },
                PickerOption {
                    id: "iterm2".into(),
                    label: "iTerm2".into()
                },
                PickerOption {
                    id: "ghostty".into(),
                    label: "Ghostty".into()
                },
            ]
        );
        assert_eq!(
            picker(Os::Mac, Some("ghostty"), &mac).open_label,
            "Open in Ghostty"
        );
        assert_eq!(picker(Os::Mac, None, &mac).open_label, "Open in Terminal");

        let linux = Detection {
            apps: vec![Detected::new(App::WezTerm, "/usr/bin/wezterm")],
            note: Some(
                "$TERMINAL (x y) is not one terminal program on PATH, so it is not offered.".into(),
            ),
        };
        let p = picker(Os::Linux, None, &linux);
        assert_eq!(p.options[0].label, "Automatic (WezTerm)");
        assert_eq!(p.open_label, "Open in WezTerm");
        assert!(p.note.unwrap().starts_with("$TERMINAL"));
        assert_eq!(
            picker(Os::Linux, None, &Detection::default()).open_label,
            "Open in Terminal"
        );

        let win = Detection {
            apps: vec![Detected::new(App::Pwsh, "pwsh.exe")],
            note: None,
        };
        assert_eq!(
            picker(Os::Windows, None, &win).open_label,
            "Open in PowerShell"
        );
        assert_eq!(
            Detected::new(App::WindowsTerminal, "wt.exe").open_label(),
            "Open in Windows Terminal"
        );

        let js = serde_json::to_string(&picker(Os::Mac, None, &mac)).unwrap();
        assert!(js.contains(r#""openLabel":"Open in Terminal""#));
        assert!(!js.contains("/Applications"), "paths stay in the app");
        for a in ALL {
            assert_eq!(App::from_id(a.id()), Some(a));
        }
    }
}
