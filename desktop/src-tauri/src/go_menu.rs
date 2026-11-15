//! The Go menu of the dashboard windows: every page of the dashboard, opened
//! in place in the focused window's dashboard webview. The embedded
//! dashboard (see [`crate::dashboard::SHELL_GLOBAL`]) shows its own sidebar
//! too; this menu reaches the same pages from the keyboard and the menu
//! bar. Plain data, so the ids, labels and paths are testable without a
//! window system; `app_dash.rs` builds the native menu and evaluates
//! [`crate::dashboard::navigate_script`] for the chosen path.

/// Prefix of the Go menu's item ids.
pub const GO_MENU_ID_PREFIX: &str = "itervox-go-";

/// The Go menu: (id suffix, label, in-app path). The pages of the embedded
/// dashboard's sidebar (web/src/App.tsx `NAV_ITEMS`, plus Issue View), plus
/// New Task…, which opens the board's New task form (`?newTask=1`, #251).
const ITEMS: &[(&str, &str, &str)] = &[
    ("board", "Board", "/"),
    ("issue-view", "Issue View", "/focus"),
    ("sessions", "Sessions", "/sessions"),
    ("timeline", "Timeline", "/timeline"),
    ("logs", "Logs", "/logs"),
    ("agents", "Agents", "/agents"),
    ("automations", "Automations", "/automations"),
    ("rules", "Rules", "/rules"),
    ("repo", "Repo Analysis", "/repo"),
    ("memory", "Memory", "/memory"),
    ("settings", "Settings", "/settings"),
    ("new-task", "New Task…", crate::menu::NEW_TASK_PATH),
];

/// The id of the Go menu's New Task… item (a separator goes before it).
pub const GO_NEW_TASK_ID: &str = "itervox-go-new-task";

/// The Go menu's items: (id, label).
pub fn go_menu_items() -> Vec<(String, String)> {
    ITEMS
        .iter()
        .map(|(id, label, _)| (format!("{GO_MENU_ID_PREFIX}{id}"), (*label).to_string()))
        .collect()
}

/// The in-app path a Go menu item opens; None for any other menu id.
pub fn go_menu_path(id: &str) -> Option<&'static str> {
    let suffix = id.strip_prefix(GO_MENU_ID_PREFIX)?;
    ITEMS
        .iter()
        .find(|(s, _, _)| *s == suffix)
        .map(|(_, _, path)| *path)
}

/// Separators after Issue View (the two views of the work) and before New
/// Task….
pub fn separator_before(index: usize, id: &str) -> bool {
    index == 2 || id == GO_NEW_TASK_ID
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_item_maps_to_its_path() {
        let items = go_menu_items();
        let labels: Vec<&str> = items.iter().map(|(_, l)| l.as_str()).collect();
        assert_eq!(
            labels,
            [
                "Board",
                "Issue View",
                "Sessions",
                "Timeline",
                "Logs",
                "Agents",
                "Automations",
                "Rules",
                "Repo Analysis",
                "Memory",
                "Settings",
                "New Task…"
            ]
        );
        for (id, _) in &items {
            let path = go_menu_path(id).unwrap_or_else(|| panic!("{id}"));
            assert!(path.starts_with('/') && !path.starts_with("//"), "{path}");
        }
        assert_eq!(go_menu_path("itervox-go-issue-view"), Some("/focus"));
        assert_eq!(go_menu_path(GO_NEW_TASK_ID), Some("/?newTask=1"));
        // Other menus' ids are not Go items.
        assert_eq!(go_menu_path("itervox-rail-new-task"), None);
        assert_eq!(go_menu_path("itervox-go-"), None);
        assert_eq!(go_menu_path("open-dashboard"), None);
        // No id collides with the Projects menu's ids.
        for (id, _, _) in crate::rail::menu_items(&crate::rail::test_rail(&["a", "b"])) {
            assert_eq!(go_menu_path(&id), None, "{id}");
        }
    }

    #[test]
    fn separators_frame_the_views_and_new_task() {
        let items = go_menu_items();
        let at: Vec<usize> = items
            .iter()
            .enumerate()
            .filter(|(i, (id, _))| separator_before(*i, id))
            .map(|(i, _)| i)
            .collect();
        assert_eq!(at, [2, items.len() - 1]);
    }

    /// Drift guard: every Go menu page is a route of the dashboard and an
    /// entry of the embedded dashboard's sidebar, and every sidebar entry
    /// is in the Go menu.
    #[test]
    fn go_menu_matches_the_dashboard_routes_and_sidebar() {
        let read = |rel: &str| {
            std::fs::read_to_string(format!(
                "{}/../../web/src/{rel}",
                env!("CARGO_MANIFEST_DIR")
            ))
            .unwrap_or_else(|e| panic!("{rel}: {e}"))
        };
        let app = read("App.tsx");
        assert!(
            !std::path::Path::new(&format!(
                "{}/../../web/src/layout/embeddedPages.ts",
                env!("CARGO_MANIFEST_DIR")
            ))
            .exists(),
            "the top bar's More menu is gone"
        );
        for (suffix, _, path) in ITEMS {
            if *suffix == "new-task" {
                assert!(read("hooks/useUrlState.ts").contains("newTask"));
                continue;
            }
            match *path {
                "/" => assert!(app.contains("<Route index element={<BoardIndex />} />")),
                "/focus" => assert!(app.contains(r#"path="/focus/:identifier?""#)),
                "/logs" => assert!(app.contains(r#"path="/logs/:identifier?""#)),
                p => assert!(app.contains(&format!(r#"path="{p}""#)), "route {p}"),
            }
            let entry = if *path == "/focus" {
                "{ to: FOCUS_PATH, icon: <FocusIcon />, label: 'Issue view' }".to_string()
            } else {
                format!("{{ to: '{path}', icon:")
            };
            assert!(app.contains(&entry), "the sidebar lacks {path}");
        }
        // Every sidebar entry has a Go menu item.
        for (i, _) in app.match_indices("{ to: '") {
            let rest = &app[i + "{ to: '".len()..];
            let path = &rest[..rest.find('\'').unwrap()];
            assert!(
                ITEMS.iter().any(|(_, _, p)| *p == path),
                "the Go menu lacks the sidebar's {path}"
            );
        }
    }
}
