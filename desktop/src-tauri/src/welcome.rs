//! The welcome window: what the app shows when there is nothing else to
//! show. A menu-bar app with no projects has only its menu bar icon, and on
//! a Mac with a notch a full menu bar can push that icon behind the notch,
//! so the app looks as if it never started. The app opens this window on
//! a first launch (no saved projects and no remote connections) and when
//! it is opened again while running with no project dashboard to show
//! (macOS `RunEvent::Reopen`: Finder, the Dock, Spotlight).
//!
//! The window is an app-local page (`desktop/dist/welcome.html`) with no
//! IPC and no network access: it says where the app lives, offers
//! **Add Project…**, and lists the projects the app knows with Start or
//! Open buttons. The app pushes the list with `eval` after each refresh;
//! the page asks for actions by navigating to app-internal paths
//! (`/__itervox/welcome/…`), which the window's navigation handler
//! intercepts ([`welcome_action`]) — the pattern of the tab strip
//! (`rail.rs`) and the Sessions window. Tokens never reach the page; an
//! action is honoured only for a project the pushed list offers it for
//! ([`allowed`]).

use serde::Serialize;

use crate::platform::Os;
use crate::rail::{RailHealth, RailView};
use crate::remote::is_remote_key;
use crate::sessions::percent_decode;

/// The welcome window's label.
pub const WELCOME_LABEL: &str = "welcome";

/// The page's file in the app's bundled frontend.
pub const WELCOME_PAGE: &str = "welcome.html";

/// The app-internal path prefix the page navigates to.
pub const WELCOME_PATH: &str = "/__itervox/welcome/";

/// The window the app opens at startup, besides its menu bar icon.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StartupWindow {
    Welcome,
}

/// What startup opens: the welcome window on a first launch — no saved
/// projects and no remote connections — else nothing (the menu bar icon
/// only, as before). Daemons started from a terminal are not counted: they
/// are not the user's choice of this app's projects, and the window lists
/// them once the first refresh finds them.
pub fn startup_window(saved_projects: usize, remote_connections: usize) -> Option<StartupWindow> {
    (saved_projects == 0 && remote_connections == 0).then_some(StartupWindow::Welcome)
}

/// What opening the app again while it runs does (macOS `Reopen`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ReopenPlan {
    /// A window is visible: macOS brings it forward itself.
    Nothing,
    /// Show and focus this (minimized or hidden) window.
    Focus(String),
    /// Open this project's dashboard.
    OpenDashboard(String),
    /// Show the welcome window.
    Welcome,
}

/// The plan for a reopen. `existing` is the window to bring back when no
/// window is visible (the dashboard window focused last, else any of the
/// app's windows); `rail` is the projects as the last refresh left them,
/// in tab order. Without a window, the first project with a dashboard to
/// show opens; with none (no projects, or none running), the welcome
/// window lists what there is.
pub fn reopen_plan(
    has_visible_windows: bool,
    existing: Option<&str>,
    rail: &RailView,
) -> ReopenPlan {
    if has_visible_windows {
        return ReopenPlan::Nothing;
    }
    if let Some(label) = existing {
        return ReopenPlan::Focus(label.to_string());
    }
    match rail.rows().find(|r| r.openable) {
        Some(r) => ReopenPlan::OpenDashboard(r.key.clone()),
        None => ReopenPlan::Welcome,
    }
}

/// What a navigation of the welcome page asks for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum WelcomeAction {
    /// The page loaded: push the list.
    Ready,
    /// The tray's "Add Project…".
    AddProject,
    /// Start this stopped local project's daemon.
    Start(String),
    /// Open this project's dashboard.
    Open(String),
}

/// The action behind a navigation of the welcome page, or `None` for an
/// ordinary navigation (the page itself loading).
pub fn welcome_action(url: &str) -> Option<WelcomeAction> {
    let rest = url.split_once("://").map(|(_, r)| r).unwrap_or(url);
    let path_and_query = &rest[rest.find('/')?..];
    let (path, query) = path_and_query
        .split_once('?')
        .unwrap_or((path_and_query, ""));
    let action = path.strip_prefix(WELCOME_PATH)?;
    let key = || {
        query
            .split('&')
            .find_map(|pair| pair.strip_prefix("key="))
            .and_then(percent_decode)
            .filter(|k| !k.is_empty())
    };
    match action {
        "ready" => Some(WelcomeAction::Ready),
        "add-project" => Some(WelcomeAction::AddProject),
        "start" => key().map(WelcomeAction::Start),
        "open" => key().map(WelcomeAction::Open),
        _ => None,
    }
}

/// Whether the welcome webview may load `url`: only the app's own page
/// (`tauri://localhost/welcome.html` on macOS, `http://tauri.localhost/…`
/// elsewhere). Anything else the page tried to open is refused.
pub fn welcome_page_allowed(url: &str) -> bool {
    let Some((scheme, rest)) = url.split_once("://") else {
        return false;
    };
    let (authority, path) = rest.split_at(rest.find('/').unwrap_or(rest.len()));
    let path = path.split(['?', '#']).next().unwrap_or("");
    let app_origin = matches!(
        (scheme, authority),
        ("tauri", "localhost") | ("http" | "https", "tauri.localhost")
    );
    app_origin && path == format!("/{WELCOME_PAGE}")
}

/// One project of the welcome list.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WelcomeRow {
    pub key: String,
    pub name: String,
    pub health: RailHealth,
    /// The row's one-line status (the tray's status line).
    pub detail: String,
    /// The dashboard can be opened: the page shows "Open".
    pub openable: bool,
    /// A stopped local project: the page shows "Start".
    pub startable: bool,
}

/// The whole page, as pushed to it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WelcomeView {
    /// `mac`, `linux` or `windows`: where the page says the icon is, and
    /// whether it shows the notch hint.
    pub platform: &'static str,
    pub projects: Vec<WelcomeRow>,
}

fn platform_name(os: Os) -> &'static str {
    match os {
        Os::Mac => "mac",
        Os::Linux => "linux",
        Os::Windows => "windows",
    }
}

/// The welcome list from the project tabs (tab order, each project once).
pub fn welcome_view(rail: &RailView, os: Os) -> WelcomeView {
    let mut projects: Vec<WelcomeRow> = Vec::new();
    for r in rail.rows() {
        if projects.iter().any(|p| p.key == r.key) {
            continue;
        }
        projects.push(WelcomeRow {
            key: r.key.clone(),
            name: r.name.clone(),
            health: r.health,
            detail: r.detail.clone(),
            openable: r.openable,
            startable: !r.openable
                && r.health == RailHealth::Stopped
                && !is_remote_key(&r.key)
                && os != Os::Windows,
        });
    }
    WelcomeView {
        platform: platform_name(os),
        projects,
    }
}

/// Whether `action` may run against `view` (what the page was last sent):
/// Start only for a project listed as startable, Open only for one listed
/// as openable. Anything else the page asks for is ignored.
pub fn allowed(action: &WelcomeAction, view: &WelcomeView) -> bool {
    let row = |key: &str| view.projects.iter().find(|p| p.key == key);
    match action {
        WelcomeAction::Ready | WelcomeAction::AddProject => true,
        WelcomeAction::Start(key) => row(key).is_some_and(|r| r.startable),
        WelcomeAction::Open(key) => row(key).is_some_and(|r| r.openable),
    }
}

/// The script that hands the list to the page.
pub fn push_script(view: &WelcomeView) -> String {
    let json = serde_json::to_string(view)
        .unwrap_or_else(|_| "{\"platform\":\"mac\",\"projects\":[]}".into())
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    format!("window.__itervoxWelcome&&window.__itervoxWelcome({json});")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rail::{RailRow, RailSection};

    fn row(key: &str, health: RailHealth, openable: bool) -> RailRow {
        RailRow {
            key: key.into(),
            name: format!("name of {key}"),
            health,
            running: 0,
            needs_you: 0,
            pinned: false,
            shortcut: None,
            openable,
            detail: "detail".into(),
            label: key.into(),
            group: None,
        }
    }

    fn rail(rows: Vec<RailRow>) -> RailView {
        RailView {
            collapsed: false,
            sections: vec![RailSection {
                id: "local".into(),
                title: "Local".into(),
                rows,
            }],
            machine: None,
        }
    }

    #[test]
    fn first_launch_opens_the_welcome_window() {
        assert_eq!(startup_window(0, 0), Some(StartupWindow::Welcome));
    }

    #[test]
    fn a_saved_project_or_a_remote_keeps_startup_as_before() {
        assert_eq!(startup_window(1, 0), None);
        assert_eq!(startup_window(3, 0), None);
        assert_eq!(startup_window(0, 1), None);
        assert_eq!(startup_window(2, 2), None);
    }

    #[test]
    fn reopen_with_a_visible_window_does_nothing() {
        let r = rail(vec![row("/a/WORKFLOW.md", RailHealth::Running, true)]);
        assert_eq!(
            reopen_plan(true, Some("dashboard-0"), &r),
            ReopenPlan::Nothing
        );
        assert_eq!(
            reopen_plan(true, None, &RailView::default()),
            ReopenPlan::Nothing
        );
    }

    #[test]
    fn reopen_brings_back_a_minimized_window_first() {
        let r = rail(vec![row("/a/WORKFLOW.md", RailHealth::Running, true)]);
        assert_eq!(
            reopen_plan(false, Some("dashboard-3"), &r),
            ReopenPlan::Focus("dashboard-3".into())
        );
    }

    #[test]
    fn reopen_opens_the_first_project_with_a_dashboard() {
        let r = rail(vec![
            row("/stopped/WORKFLOW.md", RailHealth::Stopped, false),
            row("/b/WORKFLOW.md", RailHealth::Running, true),
            row("/c/WORKFLOW.md", RailHealth::Running, true),
        ]);
        assert_eq!(
            reopen_plan(false, None, &r),
            ReopenPlan::OpenDashboard("/b/WORKFLOW.md".into())
        );
    }

    #[test]
    fn reopen_without_a_dashboard_to_show_opens_the_welcome_window() {
        assert_eq!(
            reopen_plan(false, None, &RailView::default()),
            ReopenPlan::Welcome
        );
        let stopped = rail(vec![row("/a/WORKFLOW.md", RailHealth::Stopped, false)]);
        assert_eq!(reopen_plan(false, None, &stopped), ReopenPlan::Welcome);
    }

    #[test]
    fn page_actions_are_parsed_from_navigations() {
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/ready"),
            Some(WelcomeAction::Ready)
        );
        assert_eq!(
            welcome_action("http://tauri.localhost/__itervox/welcome/add-project"),
            Some(WelcomeAction::AddProject)
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/start?key=%2Fa%2FWORKFLOW.md"),
            Some(WelcomeAction::Start("/a/WORKFLOW.md".into()))
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/open?key=%2Fa%2FWORKFLOW.md"),
            Some(WelcomeAction::Open("/a/WORKFLOW.md".into()))
        );
        assert_eq!(welcome_action("tauri://localhost/welcome.html"), None);
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/start"),
            None
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/start?key="),
            None
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/open?key=%ZZ"),
            None
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/welcome/remove?key=x"),
            None
        );
        assert_eq!(
            welcome_action("tauri://localhost/__itervox/rail/add-project"),
            None
        );
    }

    #[test]
    fn only_the_app_page_loads_in_the_window() {
        assert!(welcome_page_allowed("tauri://localhost/welcome.html"));
        assert!(welcome_page_allowed("http://tauri.localhost/welcome.html"));
        assert!(welcome_page_allowed(
            "https://tauri.localhost/welcome.html?x#y"
        ));
        assert!(!welcome_page_allowed("tauri://localhost/tabs.html"));
        assert!(!welcome_page_allowed("http://127.0.0.1:8090/welcome.html"));
        assert!(!welcome_page_allowed("https://example.com/welcome.html"));
        assert!(!welcome_page_allowed("welcome.html"));
    }

    #[test]
    fn the_list_offers_start_for_stopped_local_projects_and_open_for_running_ones() {
        let remote = crate::remote::token_key("conn", "/srv/app/WORKFLOW.md");
        let mut pinned = row("/b/WORKFLOW.md", RailHealth::Running, true);
        pinned.pinned = true;
        let r = RailView {
            collapsed: false,
            sections: vec![
                RailSection {
                    id: "pinned".into(),
                    title: "Pinned".into(),
                    rows: vec![pinned],
                },
                RailSection {
                    id: "local".into(),
                    title: "Local".into(),
                    rows: vec![
                        row("/a/WORKFLOW.md", RailHealth::Stopped, false),
                        row("/b/WORKFLOW.md", RailHealth::Running, true),
                        row("/c/WORKFLOW.md", RailHealth::Pending, false),
                        row(&remote, RailHealth::Stopped, false),
                    ],
                },
            ],
            machine: None,
        };
        let v = welcome_view(&r, Os::Mac);
        assert_eq!(v.platform, "mac");
        let keys: Vec<&str> = v.projects.iter().map(|p| p.key.as_str()).collect();
        assert_eq!(
            keys,
            vec![
                "/b/WORKFLOW.md",
                "/a/WORKFLOW.md",
                "/c/WORKFLOW.md",
                remote.as_str()
            ],
            "tab order, each project once"
        );
        let flags: Vec<(bool, bool)> = v
            .projects
            .iter()
            .map(|p| (p.openable, p.startable))
            .collect();
        assert_eq!(
            flags,
            vec![(true, false), (false, true), (false, false), (false, false)]
        );
        // Windows has no local daemon: nothing to start there.
        let w = welcome_view(&r, Os::Windows);
        assert_eq!(w.platform, "windows");
        assert!(w.projects.iter().all(|p| !p.startable));
    }

    #[test]
    fn actions_run_only_for_projects_the_list_offers_them_for() {
        let v = welcome_view(
            &rail(vec![
                row("/a/WORKFLOW.md", RailHealth::Stopped, false),
                row("/b/WORKFLOW.md", RailHealth::Running, true),
            ]),
            Os::Linux,
        );
        assert!(allowed(&WelcomeAction::Ready, &v));
        assert!(allowed(&WelcomeAction::AddProject, &v));
        assert!(allowed(&WelcomeAction::Start("/a/WORKFLOW.md".into()), &v));
        assert!(allowed(&WelcomeAction::Open("/b/WORKFLOW.md".into()), &v));
        assert!(!allowed(&WelcomeAction::Start("/b/WORKFLOW.md".into()), &v));
        assert!(!allowed(&WelcomeAction::Open("/a/WORKFLOW.md".into()), &v));
        assert!(!allowed(
            &WelcomeAction::Start("/etc/other/WORKFLOW.md".into()),
            &v
        ));
    }

    #[test]
    fn the_push_script_carries_no_token_and_escapes_line_separators() {
        let mut r = row("/a/WORKFLOW.md", RailHealth::Running, true);
        r.name = "a\u{2028}b".into();
        let js = push_script(&welcome_view(&rail(vec![r]), Os::Mac));
        assert!(js.starts_with("window.__itervoxWelcome&&window.__itervoxWelcome({"));
        assert!(js.contains("a\\u2028b"));
        assert!(!js.contains('\u{2028}'));
        assert!(!js.to_lowercase().contains("token"));
    }

    /// Drift guard: the page navigates to the paths the window intercepts,
    /// defines the function the push script calls, and says where the icon
    /// is and that the notch can hide it.
    #[test]
    fn page_uses_the_intercepted_paths() {
        let html =
            std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/../dist/welcome.html"))
                .expect("desktop/dist/welcome.html");
        assert!(html.contains(&format!("var BASE = '{WELCOME_PATH}';")));
        for verb in ["'ready'", "'add-project'", "'open'", "'start'"] {
            assert!(html.contains(verb), "{verb}");
        }
        assert!(html.contains("window.__itervoxWelcome = render;"));
        assert!(html.contains("menu bar"));
        assert!(html.contains("notch"));
        assert!(html.contains("Add Project…"));
        assert!(!html.contains("fetch("), "the page has no network access");
    }
}
