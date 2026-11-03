//! The tray menu as plain data, so its contents are testable without a
//! window system. `app.rs` turns a [`MenuSpec`] into native menu items.

use std::collections::HashMap;

use crate::events::{Alert, AlertKind};
use crate::model::ProjectView;

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
    RefreshNow,
    CheckForUpdates,
    InstallUpdate,
    InstallCli,
    Quit,
}

/// One entry of the menu.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum MenuEntry {
    Item {
        id: String,
        label: String,
        enabled: bool,
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
        }
    }

    fn label(&mut self, label: impl Into<String>) -> MenuEntry {
        self.next += 1;
        MenuEntry::Item {
            id: format!("itervox-{}", self.next),
            label: label.into(),
            enabled: false,
        }
    }
}

/// Builds the menu for the current projects and recent alerts.
pub fn build_menu<'a>(
    views: &[ProjectView],
    recent: impl Iterator<Item = &'a Alert>,
    status_error: Option<&str>,
) -> MenuSpec {
    let mut b = Builder {
        next: 0,
        actions: HashMap::new(),
    };
    let mut entries = Vec::new();

    let running = views.iter().filter(|v| v.running()).count();
    let waiting: usize = views.iter().map(ProjectView::needs_input).sum();
    entries.push(b.label(match views.len() {
        0 => "No projects".to_string(),
        n => format!(
            "{n} project{} · {running} running",
            if n == 1 { "" } else { "s" }
        ),
    }));
    if let Some(e) = status_error {
        entries.push(b.label(format!("⚠ {e}")));
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
    entries.push(b.item("Add Project…", true, MenuAction::AddProject));
    entries.push(b.item("Refresh Now", true, MenuAction::RefreshNow));
    entries.push(MenuEntry::Separator);
    entries.push(b.item(
        "Quit Itervox (daemons keep running)",
        true,
        MenuAction::Quit,
    ));

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
        section.push(MenuEntry::Item { id, label, enabled });
    }
    spec.entries.splice(at..at, section);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::{InputRequired, StateSummary};
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
                    } if il == label => Some((id.as_str(), *enabled)),
                    _ => None,
                })
            }
            _ => None,
        })
    }

    #[test]
    fn start_stop_restart_enablement_follows_the_daemon() {
        let spec = build_menu(&[view("/up", true), view("/down", false)], [].iter(), None);
        assert!(!find(&spec, "Start", "up").unwrap().1);
        assert!(find(&spec, "Stop", "up").unwrap().1);
        assert!(find(&spec, "Restart", "up").unwrap().1);
        assert!(find(&spec, "Open Dashboard", "up").unwrap().1);
        assert!(find(&spec, "Start", "down").unwrap().1);
        assert!(!find(&spec, "Stop", "down").unwrap().1);
        let (id, _) = find(&spec, "Stop", "up").unwrap();
        assert_eq!(spec.actions[id], MenuAction::Stop("/up".into()));
    }

    #[test]
    fn a_pending_operation_disables_the_controls() {
        let mut v = view("/up", true);
        v.pending = Some(PendingOp::Stopping);
        let spec = build_menu(&[v], [].iter(), None);
        for l in ["Start", "Stop", "Restart", "Open Dashboard"] {
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
        let spec = build_menu(&[v], alerts.iter(), None);
        assert_eq!(spec.title, "1");
        let open = spec
            .actions
            .values()
            .find(|a| matches!(a, MenuAction::OpenUrl { .. }))
            .unwrap();
        assert_eq!(
            open,
            &MenuAction::OpenUrl {
                project: "/up".into(),
                url: "http://127.0.0.1:8090/?issue=ENG-1".into()
            }
        );
    }

    #[test]
    fn empty_menu_still_offers_add_and_quit() {
        let spec = build_menu(&[], [].iter(), Some("itervox status failed"));
        let acts: Vec<_> = spec.actions.values().cloned().collect();
        assert!(acts.contains(&MenuAction::AddProject));
        assert!(acts.contains(&MenuAction::Quit));
        assert_eq!(spec.title, "");
        assert!(
            matches!(&spec.entries[1], MenuEntry::Item { label, .. } if label.contains("status failed"))
        );
    }

    #[test]
    fn app_section_sits_above_quit() {
        let mut spec = build_menu(&[view("/up", true)], [].iter(), None);
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
                "Quit Itervox (daemons keep running)"
            ]
        );
        let install = spec.entries.iter().find_map(|e| match e {
            MenuEntry::Item { id, label, enabled } if label.starts_with("Update available") => {
                Some((id.clone(), *enabled))
            }
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
