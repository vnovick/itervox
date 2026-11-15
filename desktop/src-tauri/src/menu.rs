//! The tray menu as plain data, so its contents are testable without a
//! window system. `app.rs` turns a [`MenuSpec`] into native menu items.

use std::collections::HashMap;

use crate::events::{Alert, AlertKind};
use crate::hub_source::{HubPage, HubStatus};
use crate::model::ProjectView;
use crate::remote_model::ConnectionView;

/// What a menu item does.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum MenuAction {
    OpenDashboard(String),
    /// Open the dashboard of `project` at `url` (an issue deep link).
    OpenUrl {
        project: String,
        url: String,
    },
    Start(String),
    Stop(String),
    Restart(String),
    Remove(String),
    RevealLog(String),
    AddProject,
    /// Remote connections (#156). Opening a remote dashboard is
    /// `OpenDashboard` with the remote daemon's key.
    AddRemote,
    /// Read the daemon's token file again, or type the token.
    RemoteToken(String),
    /// Restart the tunnels of a connection now.
    Reconnect(String),
    /// List the host's daemons again.
    Rediscover(String),
    RemoveRemote(String),
    /// The Sessions window: every daemon's agent sessions (#178). The
    /// window has no per-project filter, so a project's "Sessions…" item
    /// opens it as the top-level item does.
    OpenSessions,
    /// A project's "New Task…": its dashboard window, opened or focused,
    /// navigated to [`NEW_TASK_PATH`] in place through the embedded
    /// dashboard's navigate hook (the Go menu's mechanism).
    NewTask(String),
    /// Pause / resume ALL dispatch on every reachable local daemon (#213).
    /// Running agents continue; nothing new starts until resumed.
    PauseAll,
    ResumeAll,
    RefreshNow,
    /// Open a view of the machine's hub (#232): its dashboard, the Phones
    /// view with the pairing QR code, or the notification settings.
    OpenHub(HubPage),
    CheckForUpdates,
    InstallUpdate,
    InstallCli,
    /// Open a web page in the default browser (#260: the release page of an
    /// update on Linux and Windows, the guide to local projects on Windows).
    OpenExternal(String),
    /// Turn Start at Login on or off (#260, Linux and Windows).
    ToggleAutostart,
    Quit,
}

/// One entry of the menu.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum MenuEntry {
    Item {
        id: String,
        label: String,
        enabled: bool,
        /// A platform-neutral accelerator (`CmdOrCtrl+Q`); `app.rs` shows
        /// it as the platform spells it (`platform::accelerator`).
        accelerator: Option<String>,
    },
    Submenu {
        label: String,
        items: Vec<MenuEntry>,
    },
    Separator,
}

/// The menu plus the action behind every item id.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct MenuSpec {
    pub entries: Vec<MenuEntry>,
    pub actions: HashMap<String, MenuAction>,
    /// Text next to the tray icon (macOS): the number of issues waiting for
    /// input, empty when none.
    pub title: String,
    pub tooltip: String,
}

struct Builder {
    next: usize,
    actions: HashMap<String, MenuAction>,
}

impl Builder {
    fn item(&mut self, label: impl Into<String>, enabled: bool, action: MenuAction) -> MenuEntry {
        self.next += 1;
        let id = format!("itervox-{}", self.next);
        self.actions.insert(id.clone(), action);
        MenuEntry::Item {
            id,
            label: label.into(),
            enabled,
            accelerator: None,
        }
    }

    fn label(&mut self, label: impl Into<String>) -> MenuEntry {
        self.next += 1;
        MenuEntry::Item {
            id: format!("itervox-{}", self.next),
            label: label.into(),
            enabled: false,
            accelerator: None,
        }
    }
}

/// The in-app path of the dashboard's New task form (#251), which the
/// tray's "New Task…" opens; the Go menu's New Task… uses the same path.
pub const NEW_TASK_PATH: &str = "/?newTask=1";

/// The Quit item's label: quitting the app leaves the daemons running.
pub const QUIT_LABEL: &str = "Quit Itervox (daemons keep running)";

/// The Quit item's accelerator: ⌘Q on macOS, Ctrl+Q on Linux and Windows.
pub const QUIT_ACCELERATOR: &str = "CmdOrCtrl+Q";

/// The dashboard's Repo Analysis page (#200): `{base}repo`. A remote
/// daemon's link is rebased onto its tunnel when it is opened.
pub fn repo_analysis_link(base_url: &str) -> String {
    page_link(base_url, "repo")
}

/// The dashboard's Memory page (#202): `{base}memory`. Rebased onto a remote
/// daemon's tunnel like the Repo Analysis link.
pub fn memory_link(base_url: &str) -> String {
    page_link(base_url, "memory")
}

/// The dashboard with its New task form open (#251): `{base}?newTask=1`.
/// The dashboard opens the form once and drops the parameter
/// (web/src/hooks/useUrlState.ts, useNewTaskUrlOpen). Rebased onto a remote
/// daemon's tunnel like the other page links.
pub fn new_task_link(base_url: &str) -> String {
    let mut b = base_url.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}?newTask=1")
}

fn page_link(base_url: &str, page: &str) -> String {
    let mut b = base_url.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}{page}")
}

/// Builds the menu for the current projects and recent alerts.
pub fn build_menu<'a>(
    views: &[ProjectView],
    remotes: &[ConnectionView],
    recent: impl Iterator<Item = &'a Alert>,
    status_error: Option<&str>,
) -> MenuSpec {
    let mut b = Builder {
        next: 0,
        actions: HashMap::new(),
    };
    let mut entries = Vec::new();

    let running = views.iter().filter(|v| v.running()).count();
    let waiting: usize = views.iter().map(ProjectView::needs_input).sum::<usize>()
        + remotes
            .iter()
            .flat_map(|c| c.daemons.iter())
            .map(|d| d.needs_input())
            .sum::<usize>();
    let mut header = match views.len() {
        0 => "No projects".to_string(),
        n => format!(
            "{n} project{} · {running} running",
            if n == 1 { "" } else { "s" }
        ),
    };
    // The summary names what waits for a human, across every daemon.
    if waiting > 0 {
        header.push_str(&format!(
            " · {waiting} need{} input",
            if waiting == 1 { "s" } else { "" }
        ));
    }
    entries.push(b.label(header));
    if let Some(e) = status_error {
        entries.push(b.label(format!("⚠ {e}")));
    }
    // System visibility: a project whose workspace disk is low.
    for line in views.iter().filter_map(ProjectView::disk_warning) {
        entries.push(b.label(format!("⚠ {line}")));
    }
    entries.push(MenuEntry::Separator);

    for v in views {
        let busy = v.pending.is_some();
        let mut items = vec![
            b.label(v.status_line()),
            MenuEntry::Separator,
            b.item(
                "Open Dashboard",
                v.running() && !busy,
                MenuAction::OpenDashboard(v.key.clone()),
            ),
            b.item(
                "Repo Analysis",
                v.running() && !busy,
                MenuAction::OpenUrl {
                    project: v.key.clone(),
                    url: repo_analysis_link(
                        &v.record.as_ref().map(|r| r.base_url()).unwrap_or_default(),
                    ),
                },
            ),
            b.item(
                "Memory",
                v.running() && !busy,
                MenuAction::OpenUrl {
                    project: v.key.clone(),
                    url: memory_link(&v.record.as_ref().map(|r| r.base_url()).unwrap_or_default()),
                },
            ),
            b.item(
                "New Task…",
                v.running() && !busy,
                MenuAction::NewTask(v.key.clone()),
            ),
            b.item("Sessions…", true, MenuAction::OpenSessions),
            MenuEntry::Separator,
            b.item(
                "Start",
                !v.running() && !busy,
                MenuAction::Start(v.key.clone()),
            ),
            b.item(
                "Stop",
                v.running() && !busy,
                MenuAction::Stop(v.key.clone()),
            ),
            b.item(
                "Restart",
                v.running() && !busy,
                MenuAction::Restart(v.key.clone()),
            ),
            MenuEntry::Separator,
            b.item(
                "Show Daemon Output",
                true,
                MenuAction::RevealLog(v.key.clone()),
            ),
        ];
        if v.added {
            items.push(b.item("Remove from List", !busy, MenuAction::Remove(v.key.clone())));
        }
        items.push(b.label(v.key.clone()));
        let marker = if v.needs_input() > 0 {
            "● "
        } else if v.running() {
            "▶ "
        } else {
            "○ "
        };
        entries.push(MenuEntry::Submenu {
            label: format!("{marker}{} — {}", v.name, v.status_line()),
            items,
        });
    }

    if !remotes.is_empty() {
        entries.push(MenuEntry::Separator);
        entries.push(b.label("Remotes"));
        for c in remotes {
            entries.push(remote_submenu(&mut b, c));
        }
    }

    let recent: Vec<&Alert> = recent.collect();
    if !recent.is_empty() {
        entries.push(MenuEntry::Separator);
        entries.push(b.label("Recent"));
        for a in recent {
            let label = match a.kind {
                AlertKind::InputRequired => {
                    format!("{} needs input — {}", a.identifier, a.project_name)
                }
                AlertKind::RunFailed => format!("{} failed — {}", a.identifier, a.project_name),
            };
            entries.push(b.item(
                label,
                true,
                MenuAction::OpenUrl {
                    project: a.project.clone(),
                    url: a.url.clone(),
                },
            ));
        }
    }

    entries.push(MenuEntry::Separator);
    entries.push(pause_all_item(&mut b, views));
    entries.push(b.item(
        if waiting > 0 {
            format!("Sessions… ({waiting} waiting)")
        } else {
            "Sessions…".to_string()
        },
        true,
        MenuAction::OpenSessions,
    ));
    entries.push(b.item("Add Project…", true, MenuAction::AddProject));
    entries.push(b.item("Add Remote…", true, MenuAction::AddRemote));
    entries.push(b.item("Refresh Now", true, MenuAction::RefreshNow));
    entries.push(MenuEntry::Separator);
    let mut quit = b.item(QUIT_LABEL, true, MenuAction::Quit);
    if let MenuEntry::Item { accelerator, .. } = &mut quit {
        *accelerator = Some(QUIT_ACCELERATOR.into());
    }
    entries.push(quit);

    MenuSpec {
        entries,
        actions: b.actions,
        title: if waiting > 0 {
            waiting.to_string()
        } else {
            String::new()
        },
        tooltip: if waiting > 0 {
            format!("Itervox — {waiting} waiting for input")
        } else {
            format!("Itervox — {running} running")
        },
    }
}

/// The "Pause all agents" item (#213): it pauses every reachable local
/// daemon, and becomes "Resume all agents" once all of them are paused.
/// Disabled when no daemon can be reached.
fn pause_all_item(b: &mut Builder, views: &[ProjectView]) -> MenuEntry {
    let reachable: Vec<&ProjectView> = views.iter().filter(|v| v.reachable()).collect();
    let all_paused = !reachable.is_empty() && reachable.iter().all(|v| v.dispatch_paused());
    if all_paused {
        b.item("Resume all agents", true, MenuAction::ResumeAll)
    } else {
        b.item(
            "Pause all agents",
            !reachable.is_empty(),
            MenuAction::PauseAll,
        )
    }
}

fn remote_submenu(b: &mut Builder, c: &ConnectionView) -> MenuEntry {
    let mut items = vec![b.label(c.transport.clone())];
    if let Some(e) = &c.error {
        items.push(b.label(format!("⚠ {e}")));
    }
    if let Some(h) = &c.hub {
        items.push(b.label(h.clone()));
    }
    items.push(MenuEntry::Separator);
    for d in &c.daemons {
        let up = d.url().is_some();
        let marker = if d.needs_input() > 0 {
            "● "
        } else if up {
            "▶ "
        } else {
            "○ "
        };
        let token_label = if d.has_token {
            "Update Token…"
        } else {
            "Enter Token…"
        };
        let sub = vec![
            b.label(d.status_line()),
            MenuEntry::Separator,
            b.item(
                "Open Dashboard",
                up,
                MenuAction::OpenDashboard(d.key.clone()),
            ),
            b.item(
                "Repo Analysis",
                up,
                MenuAction::OpenUrl {
                    project: d.key.clone(),
                    url: repo_analysis_link(&d.url().unwrap_or_default()),
                },
            ),
            b.item(
                "Memory",
                up,
                MenuAction::OpenUrl {
                    project: d.key.clone(),
                    url: memory_link(&d.url().unwrap_or_default()),
                },
            ),
            b.item("New Task…", up, MenuAction::NewTask(d.key.clone())),
            b.item("Sessions…", true, MenuAction::OpenSessions),
            b.item(token_label, true, MenuAction::RemoteToken(d.key.clone())),
            b.label(d.workflow.clone()),
        ];
        items.push(MenuEntry::Submenu {
            label: format!("{marker}{} — {}", d.name, d.status_line()),
            items: sub,
        });
    }
    items.push(MenuEntry::Separator);
    items.push(b.item("Reconnect", true, MenuAction::Reconnect(c.id.clone())));
    items.push(b.item(
        "Refresh Daemon List",
        true,
        MenuAction::Rediscover(c.id.clone()),
    ));
    items.push(b.item(
        "Remove Remote",
        true,
        MenuAction::RemoveRemote(c.id.clone()),
    ));
    let marker = if c.daemons.iter().any(|d| d.needs_input() > 0) {
        "● "
    } else if !c.daemons.is_empty() && c.daemons.iter().all(|d| d.url().is_some()) {
        "▶ "
    } else {
        "○ "
    };
    MenuEntry::Submenu {
        label: format!("{marker}{} — {}", c.name, c.status_line()),
        items,
    }
}

/// Inserts the hub section (#232) under the header: the hub's status and
/// member count, and the hub views — its dashboard, "Phone…" (the Phones
/// view with the pairing QR code, #231) and the notification settings
/// (#229) — enabled while the app is connected to the hub. The tooltip
/// names the hub too.
pub fn append_hub_section(spec: &mut MenuSpec, hub: &HubStatus) {
    let at = spec
        .entries
        .iter()
        .position(|e| matches!(e, MenuEntry::Separator))
        .unwrap_or(spec.entries.len());
    let up = hub.serving();
    let item = |n: usize, label: &str, page: HubPage, spec: &mut MenuSpec| {
        let id = format!("itervox-hub-{n}");
        spec.actions.insert(id.clone(), MenuAction::OpenHub(page));
        MenuEntry::Item {
            id,
            label: label.into(),
            enabled: up,
            accelerator: None,
        }
    };
    let section = vec![
        MenuEntry::Separator,
        MenuEntry::Item {
            id: "itervox-hub-0".into(),
            label: hub.line(),
            enabled: false,
            accelerator: None,
        },
        item(1, "Open Hub Dashboard", HubPage::Dashboard, spec),
        item(2, "Phone…", HubPage::Phones, spec),
        item(3, "Notification Settings…", HubPage::Notifications, spec),
    ];
    spec.entries.splice(at..at, section);
    if let HubStatus::Serving { members, .. } = hub {
        spec.tooltip.push_str(&format!(
            " · hub: {members} project{}",
            if *members == 1 { "" } else { "s" }
        ));
    }
}

/// Inserts the app section (updates, the command-line tool) right above
/// the Quit item, which stays last. Items are `(label, enabled, action)`;
/// `None` makes a label.
pub fn append_app_section(spec: &mut MenuSpec, items: Vec<(String, bool, Option<MenuAction>)>) {
    if items.is_empty() {
        return;
    }
    // build_menu ends with Separator, Quit.
    let at = spec
        .entries
        .iter()
        .rposition(|e| matches!(e, MenuEntry::Separator))
        .unwrap_or(spec.entries.len());
    let mut section = vec![MenuEntry::Separator];
    for (n, (label, enabled, action)) in items.into_iter().enumerate() {
        let id = format!("itervox-app-{n}");
        if let Some(a) = action {
            spec.actions.insert(id.clone(), a);
        }
        section.push(MenuEntry::Item {
            id,
            label,
            enabled,
            accelerator: None,
        });
    }
    spec.entries.splice(at..at, section);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::{DiskWarning, InputRequired, StateSummary};
    use crate::model::{Health, PendingOp};
    use crate::status::DaemonRecord;

    fn view(key: &str, running: bool) -> ProjectView {
        ProjectView {
            key: key.into(),
            name: key.trim_start_matches('/').into(),
            record: running.then(|| DaemonRecord {
                pid: 1,
                workflow: key.into(),
                ..Default::default()
            }),
            added: true,
            managed: running,
            health: if running {
                Health::Ok(StateSummary::default())
            } else {
                Health::Stopped
            },
            pending: None,
            error: None,
        }
    }

    fn find<'a>(spec: &'a MenuSpec, label: &str, within: &str) -> Option<(&'a str, bool)> {
        spec.entries.iter().find_map(|e| match e {
            MenuEntry::Submenu { label: l, items } if l.contains(within) => {
                items.iter().find_map(|i| match i {
                    MenuEntry::Item {
                        id,
                        label: il,
                        enabled,
                        ..
                    } if il == label => Some((id.as_str(), *enabled)),
                    _ => None,
                })
            }
            _ => None,
        })
    }

    fn label_of(spec: &MenuSpec, id: &str) -> Option<(String, bool)> {
        spec.entries.iter().find_map(|e| match e {
            MenuEntry::Item {
                id: i,
                label,
                enabled,
                ..
            } if i == id => Some((label.clone(), *enabled)),
            _ => None,
        })
    }

    /// #232 — the tray shows the hub's status and member count under the
    /// header, with "Open Hub Dashboard", "Phone…" (the pairing QR code)
    /// and "Notification Settings…" opening the hub's views while the app
    /// is connected; when no hub serves they are shown disabled.
    #[test]
    fn tray_shows_hub_status() {
        let mut spec = build_menu(&[view("/a", true), view("/b", true)], &[], [].iter(), None);
        append_hub_section(
            &mut spec,
            &HubStatus::Serving {
                members: 2,
                version: "v0.2.9".into(),
            },
        );
        // Header, separator, then the hub section, then the projects.
        assert!(matches!(spec.entries[1], MenuEntry::Separator));
        assert_eq!(
            label_of(&spec, "itervox-hub-0"),
            Some(("Hub · 2 projects".into(), false))
        );
        assert!(matches!(&spec.entries[2], MenuEntry::Item { id, .. } if id == "itervox-hub-0"));
        assert!(matches!(spec.entries[6], MenuEntry::Separator));
        assert!(matches!(&spec.entries[7], MenuEntry::Submenu { .. }));
        for (id, label, page) in [
            ("itervox-hub-1", "Open Hub Dashboard", HubPage::Dashboard),
            ("itervox-hub-2", "Phone…", HubPage::Phones),
            (
                "itervox-hub-3",
                "Notification Settings…",
                HubPage::Notifications,
            ),
        ] {
            assert_eq!(label_of(&spec, id), Some((label.into(), true)));
            assert_eq!(spec.actions[id], MenuAction::OpenHub(page));
        }
        assert_eq!(spec.tooltip, "Itervox — 2 running · hub: 2 projects");

        for (status, line) in [
            (HubStatus::Absent, "Hub: not running — each project polled"),
            (HubStatus::Connecting, "Hub: connecting…"),
            (
                HubStatus::Unreachable("refused".into()),
                "Hub unreachable — polling each project",
            ),
        ] {
            let mut spec = build_menu(&[view("/a", true)], &[], [].iter(), None);
            append_hub_section(&mut spec, &status);
            assert_eq!(label_of(&spec, "itervox-hub-0"), Some((line.into(), false)));
            for id in ["itervox-hub-1", "itervox-hub-2", "itervox-hub-3"] {
                assert!(!label_of(&spec, id).unwrap().1, "{id} disabled: {status:?}");
            }
            assert_eq!(spec.tooltip, "Itervox — 1 running");
        }
        let mut one = build_menu(&[], &[], [].iter(), None);
        append_hub_section(
            &mut one,
            &HubStatus::Serving {
                members: 1,
                version: String::new(),
            },
        );
        assert_eq!(
            label_of(&one, "itervox-hub-0").unwrap().0,
            "Hub · 1 project"
        );
    }

    #[test]
    fn start_stop_restart_enablement_follows_the_daemon() {
        let spec = build_menu(
            &[view("/up", true), view("/down", false)],
            &[],
            [].iter(),
            None,
        );
        assert!(!find(&spec, "Start", "up").unwrap().1);
        assert!(find(&spec, "Stop", "up").unwrap().1);
        assert!(find(&spec, "Restart", "up").unwrap().1);
        assert!(find(&spec, "Open Dashboard", "up").unwrap().1);
        assert!(find(&spec, "Start", "down").unwrap().1);
        assert!(!find(&spec, "Stop", "down").unwrap().1);
        let (id, _) = find(&spec, "Stop", "up").unwrap();
        assert_eq!(spec.actions[id], MenuAction::Stop("/up".into()));
    }

    /// #200: each daemon's submenu opens its dashboard's Repo Analysis
    /// page, the same way a Recent alert opens an issue: OpenUrl with the
    /// project key, through the usual token handoff.
    #[test]
    fn repo_analysis_item_opens_the_repo_route() {
        let mut up = view("/up", true);
        if let Some(r) = up.record.as_mut() {
            r.url = "http://127.0.0.1:8090/".into();
        }
        let spec = build_menu(&[up, view("/down", false)], &[], [].iter(), None);
        let (id, enabled) = find(&spec, "Repo Analysis", "up").unwrap();
        assert!(enabled);
        assert_eq!(
            spec.actions[id],
            MenuAction::OpenUrl {
                project: "/up".into(),
                url: "http://127.0.0.1:8090/repo".into(),
            }
        );
        assert!(!find(&spec, "Repo Analysis", "down").unwrap().1);
        assert_eq!(
            repo_analysis_link("http://127.0.0.1:50001"),
            "http://127.0.0.1:50001/repo"
        );
    }

    /// #202: each daemon's submenu opens its dashboard's Memory page, the
    /// same way as Repo Analysis: OpenUrl with the project key, through the
    /// usual token handoff.
    #[test]
    fn memory_item_opens_the_memory_route() {
        let mut up = view("/up", true);
        if let Some(r) = up.record.as_mut() {
            r.url = "http://127.0.0.1:8090/".into();
        }
        let spec = build_menu(&[up, view("/down", false)], &[], [].iter(), None);
        let (id, enabled) = find(&spec, "Memory", "up").unwrap();
        assert!(enabled);
        assert_eq!(
            spec.actions[id],
            MenuAction::OpenUrl {
                project: "/up".into(),
                url: "http://127.0.0.1:8090/memory".into(),
            }
        );
        assert!(!find(&spec, "Memory", "down").unwrap().1);
        assert_eq!(
            memory_link("http://127.0.0.1:50001"),
            "http://127.0.0.1:50001/memory"
        );
    }

    /// #251: each daemon's submenu has "New Task…". It routes to
    /// `NewTask(key)`, which opens or focuses that project's dashboard window
    /// and navigates it to the New task form in place (`NEW_TASK_PATH`, the
    /// Go menu's path), enabled while the daemon runs.
    #[test]
    fn new_task_menu_opens_form() {
        let mut up = view("/up", true);
        if let Some(r) = up.record.as_mut() {
            r.url = "http://127.0.0.1:8090/".into();
        }
        let spec = build_menu(&[up, view("/down", false)], &[], [].iter(), None);
        let (id, enabled) = find(&spec, "New Task…", "up").unwrap();
        assert!(enabled);
        assert_eq!(spec.actions[id], MenuAction::NewTask("/up".into()));
        assert!(!find(&spec, "New Task…", "down").unwrap().1);
        assert_eq!(NEW_TASK_PATH, "/?newTask=1");
        assert_eq!(
            crate::go_menu::go_menu_path(crate::go_menu::GO_NEW_TASK_ID),
            Some(NEW_TASK_PATH),
            "the tray and the Go menu open the same page"
        );
        assert_eq!(
            new_task_link("http://127.0.0.1:50001"),
            "http://127.0.0.1:50001/?newTask=1"
        );
    }

    /// Each project's submenu, in order: the status line, the pages (with
    /// New Task… and Sessions…), the daemon controls, the log and the key.
    /// Every item id routes to that project's action; Sessions… opens the
    /// Sessions window (it has no per-project filter).
    #[test]
    fn project_submenu_lists_new_task_and_sessions() {
        let spec = build_menu(&[view("/up", true)], &[], [].iter(), None);
        let items = spec
            .entries
            .iter()
            .find_map(|e| match e {
                MenuEntry::Submenu { label, items } if label.contains("up") => Some(items),
                _ => None,
            })
            .unwrap();
        let labels: Vec<&str> = items
            .iter()
            .map(|e| match e {
                MenuEntry::Item { label, .. } => label.as_str(),
                MenuEntry::Separator => "-",
                MenuEntry::Submenu { .. } => "submenu",
            })
            .collect();
        assert_eq!(
            labels[1..],
            [
                "-",
                "Open Dashboard",
                "Repo Analysis",
                "Memory",
                "New Task…",
                "Sessions…",
                "-",
                "Start",
                "Stop",
                "Restart",
                "-",
                "Show Daemon Output",
                "Remove from List",
                "/up"
            ]
        );
        let (new_task, _) = find(&spec, "New Task…", "up").unwrap();
        let (sessions, enabled) = find(&spec, "Sessions…", "up").unwrap();
        assert!(enabled, "the Sessions window lists stopped daemons too");
        assert_ne!(new_task, sessions);
        assert_eq!(spec.actions[new_task], MenuAction::NewTask("/up".into()));
        assert_eq!(spec.actions[sessions], MenuAction::OpenSessions);
        // A stopped project still offers Sessions…, not New Task….
        let down = build_menu(&[view("/down", false)], &[], [].iter(), None);
        assert!(!find(&down, "New Task…", "down").unwrap().1);
        assert!(find(&down, "Sessions…", "down").unwrap().1);
    }

    /// Quit is the last item, keeps its "(daemons keep running)" wording
    /// and is the only item with an accelerator: CmdOrCtrl+Q, shown as ⌘Q
    /// on macOS and Ctrl+Q on Linux and Windows.
    #[test]
    fn quit_has_the_platform_accelerator_and_nothing_else_does() {
        use crate::platform::{accelerator, Os};
        let mut spec = build_menu(&[view("/up", true)], &[], [].iter(), None);
        append_hub_section(
            &mut spec,
            &HubStatus::Serving {
                members: 1,
                version: "0.3.1".into(),
            },
        );
        append_app_section(
            &mut spec,
            vec![("x".into(), true, Some(MenuAction::InstallCli))],
        );
        fn accelerated<'a>(entries: &'a [MenuEntry], out: &mut Vec<(&'a str, &'a str, &'a str)>) {
            for e in entries {
                match e {
                    MenuEntry::Item {
                        id,
                        label,
                        accelerator: Some(a),
                        ..
                    } => out.push((id, label, a)),
                    MenuEntry::Submenu { items, .. } => accelerated(items, out),
                    _ => {}
                }
            }
        }
        let mut found = Vec::new();
        accelerated(&spec.entries, &mut found);
        assert_eq!(found.len(), 1, "{found:?}");
        let (id, label, accel) = found[0];
        assert_eq!(label, QUIT_LABEL);
        assert_eq!(label, "Quit Itervox (daemons keep running)");
        assert_eq!(accel, QUIT_ACCELERATOR);
        assert_eq!(spec.actions[id], MenuAction::Quit);
        assert!(
            matches!(spec.entries.last(), Some(MenuEntry::Item { id: last, .. }) if last == id)
        );
        assert_eq!(accelerator(Os::Mac, accel), "Cmd+Q");
        assert_eq!(accelerator(Os::Linux, accel), "Ctrl+Q");
        assert_eq!(accelerator(Os::Windows, accel), "Ctrl+Q");
    }

    /// Drift guard: the parameter is the one the dashboard reads.
    #[test]
    fn new_task_query_param_matches_the_dashboard() {
        let src = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/hooks/useUrlState.ts"
        ))
        .expect("web/src/hooks/useUrlState.ts");
        assert!(src.contains("new URLSearchParams(location.search).get('newTask')"));
        assert!(new_task_link("http://h:1/").ends_with("?newTask=1"));
    }

    #[test]
    fn a_pending_operation_disables_the_controls() {
        let mut v = view("/up", true);
        v.pending = Some(PendingOp::Stopping);
        let spec = build_menu(&[v], &[], [].iter(), None);
        for l in [
            "Start",
            "Stop",
            "Restart",
            "Open Dashboard",
            "Repo Analysis",
            "Memory",
        ] {
            assert!(!find(&spec, l, "up").unwrap().1, "{l}");
        }
    }

    #[test]
    fn waiting_input_shows_in_title_and_recent_alerts_link_to_the_issue() {
        let mut v = view("/up", true);
        v.health = Health::Ok(StateSummary {
            input_required: vec![InputRequired {
                identifier: "ENG-1".into(),
                state: "input_required".into(),
                queued_at: "t".into(),
                context: String::new(),
            }],
            ..Default::default()
        });
        let alert = Alert {
            kind: AlertKind::InputRequired,
            project: "/up".into(),
            project_name: "up".into(),
            identifier: "ENG-1".into(),
            detail: String::new(),
            url: "http://127.0.0.1:8090/?issue=ENG-1".into(),
        };
        let alerts = [alert];
        let spec = build_menu(&[v], &[], alerts.iter(), None);
        assert_eq!(spec.title, "1");
        let open = spec
            .actions
            .values()
            .find(|a| matches!(a, MenuAction::OpenUrl { url, .. } if url.contains("?issue=")))
            .unwrap();
        assert_eq!(
            open,
            &MenuAction::OpenUrl {
                project: "/up".into(),
                url: "http://127.0.0.1:8090/?issue=ENG-1".into()
            }
        );
    }

    /// #178: the Sessions window is one item away, with the number of
    /// sessions waiting for input across every daemon.
    #[test]
    fn sessions_item_opens_the_window_and_counts_waiting_sessions() {
        let label_of = |spec: &MenuSpec| {
            spec.entries
                .iter()
                .find_map(|e| match e {
                    MenuEntry::Item { id, label, .. }
                        if spec.actions.get(id) == Some(&MenuAction::OpenSessions) =>
                    {
                        Some(label.clone())
                    }
                    _ => None,
                })
                .unwrap()
        };
        assert_eq!(
            label_of(&build_menu(&[], &[], [].iter(), None)),
            "Sessions…"
        );
        let mut v = view("/up", true);
        v.health = Health::Ok(StateSummary {
            input_required: vec![InputRequired {
                identifier: "ENG-1".into(),
                state: "input_required".into(),
                queued_at: "t".into(),
                context: String::new(),
            }],
            ..Default::default()
        });
        assert_eq!(
            label_of(&build_menu(&[v], &[], [].iter(), None)),
            "Sessions… (1 waiting)"
        );
    }

    /// #213: one tray item pauses all agents on every reachable daemon and
    /// turns into "Resume all agents" once they are all paused; it is
    /// disabled while no daemon can be reached.
    #[test]
    fn pause_all_agents_item_follows_the_daemons() {
        let item = |spec: &MenuSpec| {
            spec.entries
                .iter()
                .find_map(|e| match e {
                    MenuEntry::Item {
                        id, label, enabled, ..
                    } if matches!(
                        spec.actions.get(id),
                        Some(MenuAction::PauseAll | MenuAction::ResumeAll)
                    ) =>
                    {
                        Some((label.clone(), *enabled, spec.actions[id].clone()))
                    }
                    _ => None,
                })
                .unwrap()
        };
        let paused = |key: &str, on: bool| {
            let mut v = view(key, true);
            v.health = Health::Ok(StateSummary {
                dispatch_paused: on,
                ..Default::default()
            });
            v
        };
        assert_eq!(
            item(&build_menu(&[view("/down", false)], &[], [].iter(), None)),
            ("Pause all agents".into(), false, MenuAction::PauseAll),
            "nothing to pause"
        );
        assert_eq!(
            item(&build_menu(
                &[paused("/a", false), view("/down", false)],
                &[],
                [].iter(),
                None
            )),
            ("Pause all agents".into(), true, MenuAction::PauseAll)
        );
        assert_eq!(
            item(&build_menu(
                &[paused("/a", true), paused("/b", false)],
                &[],
                [].iter(),
                None
            )),
            ("Pause all agents".into(), true, MenuAction::PauseAll),
            "one daemon still dispatches"
        );
        let all = build_menu(
            &[paused("/a", true), paused("/b", true), view("/down", false)],
            &[],
            [].iter(),
            None,
        );
        assert_eq!(
            item(&all),
            ("Resume all agents".into(), true, MenuAction::ResumeAll)
        );
        assert!(paused("/a", true)
            .status_line()
            .ends_with("dispatch paused"));
    }

    /// The header summarises the projects, the running daemons and, when
    /// any, how many items wait for input across local and remote daemons.
    #[test]
    fn header_summarises_running_and_waiting() {
        let header = |spec: &MenuSpec| match &spec.entries[0] {
            MenuEntry::Item { label, enabled, .. } => {
                assert!(!enabled, "the header is a label");
                label.clone()
            }
            e => panic!("header: {e:?}"),
        };
        let input = |id: &str| InputRequired {
            identifier: id.into(),
            state: "input_required".into(),
            queued_at: "t".into(),
            context: String::new(),
        };
        assert_eq!(
            header(&build_menu(&[], &[], [].iter(), None)),
            "No projects"
        );
        let views = [view("/a", true), view("/b", false)];
        assert_eq!(
            header(&build_menu(&views, &[], [].iter(), None)),
            "2 projects · 1 running"
        );
        let mut one = view("/a", true);
        one.health = Health::Ok(StateSummary {
            input_required: vec![input("ENG-1")],
            ..Default::default()
        });
        assert_eq!(
            header(&build_menu(
                &[one.clone(), view("/b", false)],
                &[],
                [].iter(),
                None
            )),
            "2 projects · 1 running · 1 needs input"
        );
        one.health = Health::Ok(StateSummary {
            input_required: vec![input("ENG-1"), input("ENG-2")],
            ..Default::default()
        });
        assert_eq!(
            header(&build_menu(&[one], &[], [].iter(), None)),
            "1 project · 1 running · 2 need input"
        );
    }

    /// System visibility: a project's submenu label carries what its live
    /// runs hold in memory ("2 agents · 1.2 GB"), and a low workspace disk
    /// adds a warning line under the header (with the pause when critical).
    #[test]
    fn shows_run_memory_and_low_disk() {
        let labels = |spec: &MenuSpec| -> Vec<String> {
            spec.entries
                .iter()
                .filter_map(|e| match e {
                    MenuEntry::Item { label, .. } | MenuEntry::Submenu { label, .. } => {
                        Some(label.clone())
                    }
                    MenuEntry::Separator => None,
                })
                .collect()
        };
        let mut v = view("/a", true);
        v.name = "web-app".into();
        v.health = Health::Ok(StateSummary {
            running: 2,
            runs_rss_bytes: 1_288_490_188,
            disk: Some(DiskWarning {
                free_bytes: 3_435_973_836,
                critical: false,
            }),
            ..Default::default()
        });
        let got = labels(&build_menu(&[v.clone()], &[], [].iter(), None));
        assert_eq!(got[1], "⚠ web-app: low disk — 3.2 GB free");
        assert!(
            got.contains(&"▶ web-app — running · 2 agents · 1.2 GB".to_string()),
            "{got:?}"
        );

        v.health = Health::Ok(StateSummary {
            running: 0,
            runs_rss_bytes: 0,
            disk: Some(DiskWarning {
                free_bytes: 1 << 30,
                critical: true,
            }),
            ..Default::default()
        });
        let got = labels(&build_menu(&[v.clone()], &[], [].iter(), None));
        assert_eq!(
            got[1],
            "⚠ web-app: disk almost full — 1.0 GB free, dispatch paused"
        );
        assert!(got.contains(&"▶ web-app — running · 0 agents".to_string()));

        v.health = Health::Ok(StateSummary::default());
        let got = labels(&build_menu(&[v], &[], [].iter(), None));
        assert!(!got.iter().any(|l| l.contains("disk")), "{got:?}");
    }

    #[test]
    fn empty_menu_still_offers_add_and_quit() {
        let spec = build_menu(&[], &[], [].iter(), Some("itervox status failed"));
        let acts: Vec<_> = spec.actions.values().cloned().collect();
        assert!(acts.contains(&MenuAction::AddProject));
        assert!(acts.contains(&MenuAction::AddRemote));
        assert!(acts.contains(&MenuAction::Quit));
        assert_eq!(spec.title, "");
        assert!(
            matches!(&spec.entries[1], MenuEntry::Item { label, .. } if label.contains("status failed"))
        );
    }

    #[test]
    fn remotes_section_shows_each_connection_with_its_daemons() {
        use crate::remote_model::RemoteDaemonView;
        use crate::tunnel::{TunnelPhase, TunnelStatus};
        let daemon = |key: &str, phase: TunnelPhase| RemoteDaemonView {
            key: key.into(),
            workflow: "/srv/app/WORKFLOW.md".into(),
            name: "app".into(),
            tunnel: Some(TunnelStatus {
                id: key.into(),
                local_port: 50001,
                phase,
                pid: Some(9),
                starts: 1,
                last_error: None,
            }),
            health: Health::Ok(StateSummary::default()),
            has_token: true,
            hub_path: None,
        };
        let conns = vec![
            ConnectionView {
                id: "prod".into(),
                name: "Prod".into(),
                transport: "ssh me@box".into(),
                daemons: vec![daemon("remote/prod//srv/app/WORKFLOW.md", TunnelPhase::Up)],
                error: None,
                hub: None,
            },
            ConnectionView {
                id: "eu".into(),
                name: "EU".into(),
                transport: "IAP vm".into(),
                daemons: vec![daemon(
                    "remote/eu//srv/app/WORKFLOW.md",
                    TunnelPhase::Connecting,
                )],
                error: None,
                hub: None,
            },
        ];
        let spec = build_menu(&[], &conns, [].iter(), None);
        assert!(spec
            .entries
            .iter()
            .any(|e| matches!(e, MenuEntry::Item { label, .. } if label == "Remotes")));
        let labels: Vec<&str> = spec
            .entries
            .iter()
            .filter_map(|e| match e {
                MenuEntry::Submenu { label, .. } => Some(label.as_str()),
                _ => None,
            })
            .collect();
        assert_eq!(
            labels,
            vec![
                "▶ Prod — 1/1 connected",
                "○ EU — connecting… (0/1 connected)"
            ]
        );
        let open: Vec<_> = spec
            .actions
            .values()
            .filter(|a| matches!(a, MenuAction::OpenDashboard(_)))
            .collect();
        assert_eq!(open.len(), 2);
        let repo: Vec<_> = spec
            .actions
            .values()
            .filter_map(|a| match a {
                MenuAction::OpenUrl { project, url } if url.ends_with("/repo") => {
                    Some(project.as_str())
                }
                _ => None,
            })
            .collect();
        assert_eq!(repo.len(), 2, "a Repo Analysis item per remote daemon");
        let memory = spec
            .actions
            .values()
            .filter(|a| matches!(a, MenuAction::OpenUrl { url, .. } if url.ends_with("/memory")))
            .count();
        assert_eq!(memory, 2, "a Memory item per remote daemon");
        assert!(spec
            .actions
            .values()
            .any(|a| *a == MenuAction::Reconnect("eu".into())));
        assert!(spec
            .actions
            .values()
            .any(|a| *a == MenuAction::RemoveRemote("prod".into())));
    }

    #[test]
    fn app_section_sits_above_quit() {
        let mut spec = build_menu(&[view("/up", true)], &[], [].iter(), None);
        append_app_section(
            &mut spec,
            vec![
                (
                    "Update available — Install and Restart".into(),
                    true,
                    Some(MenuAction::InstallUpdate),
                ),
                ("Itervox 0.2.6 (you have 0.2.5)".into(), false, None),
                (
                    "Install Command-Line Tool…".into(),
                    true,
                    Some(MenuAction::InstallCli),
                ),
            ],
        );
        let labels: Vec<_> = spec
            .entries
            .iter()
            .map(|e| match e {
                MenuEntry::Item { label, .. } => label.as_str(),
                MenuEntry::Separator => "-",
                MenuEntry::Submenu { .. } => "submenu",
            })
            .collect();
        let n = labels.len();
        assert_eq!(
            labels[n - 6..],
            [
                "-",
                "Update available — Install and Restart",
                "Itervox 0.2.6 (you have 0.2.5)",
                "Install Command-Line Tool…",
                "-",
                QUIT_LABEL
            ]
        );
        let install = spec.entries.iter().find_map(|e| match e {
            MenuEntry::Item {
                id, label, enabled, ..
            } if label.starts_with("Update available") => Some((id.clone(), *enabled)),
            _ => None,
        });
        let (id, enabled) = install.unwrap();
        assert!(enabled);
        assert_eq!(spec.actions[&id], MenuAction::InstallUpdate);
        // Ids stay unique across the whole menu.
        fn ids<'a>(entries: &'a [MenuEntry], out: &mut Vec<&'a str>) {
            for e in entries {
                match e {
                    MenuEntry::Item { id, .. } => out.push(id),
                    MenuEntry::Submenu { items, .. } => ids(items, out),
                    MenuEntry::Separator => {}
                }
            }
        }
        let mut all = Vec::new();
        ids(&spec.entries, &mut all);
        let unique: std::collections::HashSet<_> = all.iter().collect();
        assert_eq!(unique.len(), all.len());
    }
}
