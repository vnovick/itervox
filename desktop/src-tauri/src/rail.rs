//! The project tabs of the dashboard windows (#244, a strip of tabs across
//! the top of the window since 0.3.3; it began as a rail beside the
//! dashboard, hence the module's name): every project the app knows, one
//! tab each, above the dashboard.
//!
//! The tabs are built from what the refresh already holds — the local
//! projects ([`crate::model::Core`], read from the hub's one projects
//! request when a hub serves, per daemon otherwise) and the remote daemons
//! ([`crate::remote_model::RemoteCore`]) — so they add no reads of their
//! own. They are shown by an app-local page (`desktop/dist/tabs.html`) in
//! its own webview above the dashboard's webview: the page has no IPC and
//! no network access; the app pushes the tabs to it with `eval` and the
//! page asks for actions by navigating to app-internal paths, which the
//! webview's navigation handler intercepts ([`rail_action`]) — the same
//! pattern as the Sessions window (#178). A tab's menu and the overflow
//! list are native popup menus ([`tab_menu`], [`overflow_menu`]): the strip
//! is one row high, so a menu drawn by the page would be cut off. Tokens
//! never reach the tabs page.

use serde::Serialize;

use crate::dashboard::dashboard_init_script;
use crate::model::{Health, PendingOp, ProjectView};
use crate::projects::RailPrefs;
use crate::remote_model::{ConnectionView, RemoteDaemonView};
use crate::sessions::percent_decode;

/// Height of the tab strip, in logical pixels. The strip sits at the top
/// of the window's content area, under the native title bar; compact tabs
/// (initials) keep the same height.
pub const TAB_STRIP_HEIGHT: f64 = 40.0;

/// A webview's place in its window, in logical pixels.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Bounds {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// Where the tab strip and the dashboard go in a window whose content area
/// is `width` × `height`: the strip across the top, the dashboard under it
/// at the full width.
pub fn window_layout(width: f64, height: f64) -> (Bounds, Bounds) {
    let (width, height) = (width.max(0.0), height.max(0.0));
    let strip = TAB_STRIP_HEIGHT.min(height);
    (
        Bounds {
            x: 0.0,
            y: 0.0,
            width,
            height: strip,
        },
        Bounds {
            x: 0.0,
            y: strip,
            width,
            height: height - strip,
        },
    )
}

/// How many rows have a ⌘1…⌘9 shortcut.
pub const MAX_SHORTCUTS: usize = 9;

/// The health dot of a row.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RailHealth {
    Running,
    Stopped,
    Unreachable,
    TokenRequired,
    /// A start, stop or restart the app began is in progress.
    Pending,
}

impl RailHealth {
    fn label(self) -> &'static str {
        match self {
            RailHealth::Running => "running",
            RailHealth::Stopped => "stopped",
            RailHealth::Unreachable => "unreachable",
            RailHealth::TokenRequired => "token required",
            RailHealth::Pending => "changing state",
        }
    }
}

/// Which group a project is listed under.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RailGroup {
    /// A local daemon that is a member of this Mac's hub.
    Hub,
    /// A local project outside the hub (`--no-hub`, older daemons, or
    /// stopped).
    Local,
    /// A remote daemon, under its connection (host).
    Remote { host: String },
}

/// One project of the strip (a tab).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RailRow {
    pub key: String,
    pub name: String,
    pub health: RailHealth,
    /// Running agents.
    pub running: usize,
    /// Questions and guardrail approvals waiting for a human.
    pub needs_you: usize,
    pub pinned: bool,
    /// 1…9: the ⌘ shortcut of the row.
    pub shortcut: Option<usize>,
    /// The dashboard can be opened (the daemon runs, or its tunnel is up).
    pub openable: bool,
    /// The row's one-line status (the tray's status line).
    pub detail: String,
    /// What a screen reader announces for the row.
    pub label: String,
    #[serde(skip)]
    pub group: Option<RailGroup>,
}

/// A titled group of tabs (a thin divider in the strip, a title in the
/// overflow list).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RailSection {
    /// `pinned`, `hub`, `local` or `remote:<host>`.
    pub id: String,
    pub title: String,
    pub rows: Vec<RailRow>,
}

/// The whole strip, as pushed to the page.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RailView {
    pub collapsed: bool,
    pub sections: Vec<RailSection>,
    /// The machine's model servers and GPU (machine_health.rs), shown at the
    /// end of the strip; absent when no project uses a model server. The app
    /// sets it after [`build_rail`].
    #[serde(skip_serializing_if = "Option::is_none")]
    pub machine: Option<crate::machine_health::MachinePanel>,
}

fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

fn row_label(name: &str, health: RailHealth, running: usize, needs_you: usize) -> String {
    let mut parts = vec![name.to_string(), health.label().to_string()];
    if health == RailHealth::Running {
        parts.push(plural(running, "agent running", "agents running"));
    }
    if needs_you > 0 {
        parts.push(format!(
            "{} waiting for you",
            plural(needs_you, "item", "items")
        ));
    }
    parts.join(", ")
}

fn local_row(v: &ProjectView) -> RailRow {
    let health = match (&v.pending, &v.health) {
        (Some(PendingOp::Starting | PendingOp::Stopping | PendingOp::Restarting), _) => {
            RailHealth::Pending
        }
        (None, Health::Stopped) => RailHealth::Stopped,
        (None, Health::Ok(_)) => RailHealth::Running,
        (None, Health::TokenRequired) => RailHealth::TokenRequired,
        (None, Health::Unreachable(_)) => RailHealth::Unreachable,
    };
    let running = match &v.health {
        Health::Ok(s) => s.running,
        _ => 0,
    };
    let needs_you = v.needs_input();
    let group = if v.record.as_ref().is_some_and(|r| r.hub_member) {
        RailGroup::Hub
    } else {
        RailGroup::Local
    };
    RailRow {
        key: v.key.clone(),
        name: v.name.clone(),
        health,
        running,
        needs_you,
        pinned: false,
        shortcut: None,
        openable: v.running(),
        detail: v.status_line(),
        label: row_label(&v.name, health, running, needs_you),
        group: Some(group),
    }
}

fn remote_row(host: &str, d: &RemoteDaemonView) -> RailRow {
    let up = d.tunnel.as_ref().is_some_and(|t| t.is_up());
    let health = match (&d.health, up) {
        (_, false) => RailHealth::Unreachable,
        (Health::Ok(_), true) => RailHealth::Running,
        (Health::TokenRequired, true) => RailHealth::TokenRequired,
        (Health::Unreachable(_) | Health::Stopped, true) => RailHealth::Unreachable,
    };
    let running = match &d.health {
        Health::Ok(s) => s.running,
        _ => 0,
    };
    let needs_you = d.needs_input();
    RailRow {
        key: d.key.clone(),
        name: d.name.clone(),
        health,
        running,
        needs_you,
        pinned: false,
        shortcut: None,
        openable: up,
        detail: d.status_line(),
        label: row_label(&d.name, health, running, needs_you),
        group: Some(RailGroup::Remote {
            host: host.to_string(),
        }),
    }
}

/// Orders `rows` by their place in `order` (listed first, in that order),
/// keeping the default order for the rest.
fn arrange(rows: &mut [RailRow], order: &[String]) {
    let rank = |k: &str| order.iter().position(|o| o == k).unwrap_or(usize::MAX);
    // Stable: unlisted rows keep their default (model) order.
    rows.sort_by_key(|r| rank(&r.key));
}

/// Builds the rail from the refresh's local views and remote connections
/// and the user's arrangement.
pub fn build_rail(
    local: &[ProjectView],
    remotes: &[ConnectionView],
    prefs: &RailPrefs,
) -> RailView {
    let mut all: Vec<RailRow> = local.iter().map(local_row).collect();
    for c in remotes {
        all.extend(c.daemons.iter().map(|d| remote_row(&c.name, d)));
    }
    let mut pinned: Vec<RailRow> = Vec::new();
    let mut hub: Vec<RailRow> = Vec::new();
    let mut localrows: Vec<RailRow> = Vec::new();
    let mut by_host: Vec<(String, Vec<RailRow>)> = Vec::new();
    for mut r in all {
        if prefs.is_pinned(&r.key) {
            r.pinned = true;
            pinned.push(r);
            continue;
        }
        match r.group.clone() {
            Some(RailGroup::Hub) => hub.push(r),
            Some(RailGroup::Remote { host }) => {
                match by_host.iter_mut().find(|(h, _)| *h == host) {
                    Some((_, rows)) => rows.push(r),
                    None => by_host.push((host, vec![r])),
                }
            }
            _ => localrows.push(r),
        }
    }
    arrange(&mut pinned, &prefs.pinned);
    let mut sections = Vec::new();
    let mut push = |id: String, title: String, mut rows: Vec<RailRow>, order: &[String]| {
        if rows.is_empty() {
            return;
        }
        arrange(&mut rows, order);
        sections.push(RailSection { id, title, rows });
    };
    push("pinned".into(), "Pinned".into(), pinned, &prefs.pinned);
    push("hub".into(), "Hub".into(), hub, &prefs.order);
    push("local".into(), "This Mac".into(), localrows, &prefs.order);
    for (host, rows) in by_host {
        push(format!("remote:{host}"), host, rows, &prefs.order);
    }
    let mut n = 0;
    for s in &mut sections {
        for r in &mut s.rows {
            n += 1;
            r.shortcut = (n <= MAX_SHORTCUTS).then_some(n);
        }
    }
    RailView {
        collapsed: prefs.collapsed,
        sections,
        machine: None,
    }
}

impl RailView {
    /// Every row, top to bottom.
    pub fn rows(&self) -> impl Iterator<Item = &RailRow> {
        self.sections.iter().flat_map(|s| s.rows.iter())
    }

    pub fn row(&self, key: &str) -> Option<&RailRow> {
        self.rows().find(|r| r.key == key)
    }

    fn section_of(&self, key: &str) -> Option<&RailSection> {
        self.sections
            .iter()
            .find(|s| s.rows.iter().any(|r| r.key == key))
    }

    /// The project a rail command goes to from the window showing
    /// `current`: ⌘1…⌘9 the nth row, ⌘⇧] / ⌘⇧[ the next or previous
    /// openable row (wrapping). `None` when there is nowhere to go (a row
    /// without a dashboard, or the project already shown).
    pub fn target(&self, current: Option<&str>, cmd: RailCommand) -> Option<String> {
        let rows: Vec<&RailRow> = self.rows().collect();
        match cmd {
            RailCommand::Goto(n) => rows
                .iter()
                .find(|r| r.shortcut == Some(n))
                .filter(|r| r.openable && Some(r.key.as_str()) != current)
                .map(|r| r.key.clone()),
            RailCommand::Next | RailCommand::Previous => {
                let open: Vec<&&RailRow> = rows.iter().filter(|r| r.openable).collect();
                if open.is_empty() {
                    return None;
                }
                let at = current.and_then(|c| open.iter().position(|r| r.key == c));
                let len = open.len();
                let i = match (at, cmd) {
                    (Some(i), RailCommand::Next) => (i + 1) % len,
                    (Some(i), _) => (i + len - 1) % len,
                    (None, RailCommand::Next) => 0,
                    (None, _) => len - 1,
                };
                let key = &open[i].key;
                (Some(key.as_str()) != current).then(|| key.clone())
            }
            RailCommand::ToggleCollapsed | RailCommand::NewTask => None,
        }
    }
}

/// Moves `key` within its section of `rail` (a drag of its tab): before
/// `before` (a tab of the same section; `None` = to the end), and records the new arrangement in
/// `prefs` (the pinned list for pinned rows, the order otherwise). False
/// when the move is not possible or changes nothing.
pub fn move_before(
    rail: &RailView,
    prefs: &mut RailPrefs,
    key: &str,
    before: Option<&str>,
) -> bool {
    let Some(section) = rail.section_of(key) else {
        return false;
    };
    let mut keys: Vec<String> = section.rows.iter().map(|r| r.key.clone()).collect();
    if let Some(b) = before {
        if b == key || !keys.iter().any(|k| k == b) {
            return false;
        }
    }
    let old = keys.clone();
    keys.retain(|k| k != key);
    let at = before
        .and_then(|b| keys.iter().position(|k| k == b))
        .unwrap_or(keys.len());
    keys.insert(at, key.to_string());
    if keys == old {
        return false;
    }
    if section.id == "pinned" {
        // Pinned projects not shown right now keep their place at the end.
        let hidden: Vec<String> = prefs
            .pinned
            .iter()
            .filter(|k| !keys.contains(k))
            .cloned()
            .collect();
        prefs.pinned = keys.into_iter().chain(hidden).collect();
        return true;
    }
    // Every shown unpinned row in its displayed order (with the move),
    // then the arranged projects not shown right now.
    let mut order: Vec<String> = Vec::new();
    for s in rail.sections.iter().filter(|s| s.id != "pinned") {
        if s.id == section.id {
            order.extend(keys.iter().cloned());
        } else {
            order.extend(s.rows.iter().map(|r| r.key.clone()));
        }
    }
    let hidden: Vec<String> = prefs
        .order
        .iter()
        .filter(|k| !order.contains(k) && rail.row(k).is_none())
        .cloned()
        .collect();
    order.extend(hidden);
    prefs.order = order;
    true
}

/// Moves `key` one tab left (`delta` < 0) or right within its section.
pub fn nudge(rail: &RailView, prefs: &mut RailPrefs, key: &str, delta: i32) -> bool {
    let Some(section) = rail.section_of(key) else {
        return false;
    };
    let keys: Vec<&str> = section.rows.iter().map(|r| r.key.as_str()).collect();
    let Some(i) = keys.iter().position(|k| *k == key) else {
        return false;
    };
    if delta < 0 {
        match i.checked_sub(1) {
            Some(j) => move_before(rail, prefs, key, Some(keys[j])),
            None => false,
        }
    } else {
        match i + 1 {
            j if j >= keys.len() => false,
            j if j + 1 >= keys.len() => move_before(rail, prefs, key, None),
            j => move_before(rail, prefs, key, Some(keys[j + 1])),
        }
    }
}

/// A rail command from the app menu's shortcuts.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RailCommand {
    /// ⌘1…⌘9.
    Goto(usize),
    /// ⌘⇧].
    Next,
    /// ⌘⇧[.
    Previous,
    /// Show or hide the project names.
    ToggleCollapsed,
    /// Open the focused project's New task form (#251).
    NewTask,
}

/// Prefix of the app menu's rail item ids.
pub const MENU_ID_PREFIX: &str = "itervox-rail-";

/// The app menu's rail items: (id, label, accelerator).
pub fn menu_items() -> Vec<(String, String, String)> {
    let mut v = vec![
        (
            format!("{MENU_ID_PREFIX}next"),
            "Next Project".to_string(),
            "CmdOrCtrl+Shift+]".to_string(),
        ),
        (
            format!("{MENU_ID_PREFIX}previous"),
            "Previous Project".to_string(),
            "CmdOrCtrl+Shift+[".to_string(),
        ),
    ];
    for n in 1..=MAX_SHORTCUTS {
        v.push((
            format!("{MENU_ID_PREFIX}goto-{n}"),
            format!("Project {n}"),
            format!("CmdOrCtrl+{n}"),
        ));
    }
    v.push((
        format!("{MENU_ID_PREFIX}toggle"),
        "Show or Hide Project Names".to_string(),
        "CmdOrCtrl+Alt+S".to_string(),
    ));
    v.push((
        NEW_TASK_MENU_ID.to_string(),
        "New Task…".to_string(),
        "CmdOrCtrl+Shift+N".to_string(),
    ));
    v
}

/// The Projects menu's "New Task…" item (#251).
pub const NEW_TASK_MENU_ID: &str = "itervox-rail-new-task";

/// The rail command of an app menu item id.
pub fn menu_command(id: &str) -> Option<RailCommand> {
    match id.strip_prefix(MENU_ID_PREFIX)? {
        "next" => Some(RailCommand::Next),
        "previous" => Some(RailCommand::Previous),
        "toggle" => Some(RailCommand::ToggleCollapsed),
        "new-task" => Some(RailCommand::NewTask),
        other => other
            .strip_prefix("goto-")?
            .parse()
            .ok()
            .filter(|n| (1..=MAX_SHORTCUTS).contains(n))
            .map(RailCommand::Goto),
    }
}

/// The script that hands `view` to the tabs page, marking `active` (the
/// project the window shows).
pub fn push_script(view: &RailView, active: Option<&str>) -> String {
    let json = serde_json::to_string(view)
        .unwrap_or_else(|_| "{\"collapsed\":false,\"sections\":[]}".into())
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    let active = serde_json::to_string(&active).unwrap_or_else(|_| "null".into());
    format!("window.__itervoxRail&&window.__itervoxRail({json},{active});")
}

/// The app-internal path prefix the tabs page navigates to.
pub const RAIL_PATH: &str = "/__itervox/rail/";

/// What a navigation of the tabs page (or a tab menu item) asks for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RailAction {
    /// The page loaded: push the tabs.
    Ready,
    /// Show `key` in this window.
    Switch(String),
    /// Open `key` in a new window.
    OpenInNewWindow(String),
    Pin(String),
    Unpin(String),
    /// Drag and drop of a tab: `key` before `before` (`None`: end of its
    /// group).
    Move {
        key: String,
        before: Option<String>,
    },
    /// Keyboard reorder (and the tab menu's Move Left / Move Right): one
    /// tab left (-1) or right (1).
    Nudge {
        key: String,
        delta: i32,
    },
    /// Compact tabs, initials only (true), or tabs with names.
    Collapse(bool),
    /// The strip's "+" button: the tray's "Add Project…".
    AddProject,
    /// A tab's context menu, at (`x`, `y`) in the strip (logical pixels).
    Menu {
        key: String,
        x: i32,
        y: i32,
    },
    /// The "⌄" overflow button: every project, at (`x`, `y`).
    Overflow {
        x: i32,
        y: i32,
    },
}

/// The action behind a navigation of the tabs page, or `None` for an
/// ordinary navigation (the page itself loading).
pub fn rail_action(url: &str) -> Option<RailAction> {
    let rest = url.split_once("://").map(|(_, r)| r).unwrap_or(url);
    let path_and_query = &rest[rest.find('/')?..];
    let (path, query) = path_and_query
        .split_once('?')
        .unwrap_or((path_and_query, ""));
    let action = path.strip_prefix(RAIL_PATH)?;
    let param = |name: &str| {
        query.split('&').find_map(|pair| {
            let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
            (k == name).then(|| percent_decode(v)).flatten()
        })
    };
    let key = || param("key").filter(|k| !k.is_empty());
    // A menu's anchor in the strip; anything but a plausible pixel position
    // is no action.
    let coord = |name: &str| {
        param(name)
            .and_then(|v| v.parse::<i32>().ok())
            .filter(|v| (0..=MAX_COORD).contains(v))
    };
    match action {
        "ready" => Some(RailAction::Ready),
        "add-project" => Some(RailAction::AddProject),
        "switch" => key().map(RailAction::Switch),
        "open-new" => key().map(RailAction::OpenInNewWindow),
        "pin" => key().map(RailAction::Pin),
        "unpin" => key().map(RailAction::Unpin),
        "move" => key().map(|key| RailAction::Move {
            key,
            before: param("before").filter(|b| !b.is_empty()),
        }),
        "nudge" => {
            let delta = match param("delta").as_deref() {
                Some("-1") => -1,
                Some("1") => 1,
                _ => return None,
            };
            key().map(|key| RailAction::Nudge { key, delta })
        }
        "collapse" => match param("value").as_deref() {
            Some("1") => Some(RailAction::Collapse(true)),
            Some("0") => Some(RailAction::Collapse(false)),
            _ => None,
        },
        "menu" => {
            let (x, y) = (coord("x")?, coord("y")?);
            key().map(|key| RailAction::Menu { key, x, y })
        }
        "overflow" => Some(RailAction::Overflow {
            x: coord("x")?,
            y: coord("y")?,
        }),
        _ => None,
    }
}

/// The largest menu anchor coordinate the page may send.
const MAX_COORD: i32 = 100_000;

/// The tabs page's own file in the app's bundled frontend.
pub const TABS_PAGE: &str = "tabs.html";

/// Whether the tab strip's webview may load `url`: only the app's own tabs
/// page (`tauri://localhost/tabs.html` on macOS, `http://tauri.localhost/…`
/// elsewhere). Anything else the page tried to open is refused, so the
/// strip never shows a page from a daemon or the network.
pub fn rail_page_allowed(url: &str) -> bool {
    let Some((scheme, rest)) = url.split_once("://") else {
        return false;
    };
    let (authority, path) = rest.split_at(rest.find('/').unwrap_or(rest.len()));
    let path = path.split(['?', '#']).next().unwrap_or("");
    let app_origin = matches!(
        (scheme, authority),
        ("tauri", "localhost") | ("http" | "https", "tauri.localhost")
    );
    app_origin && path == format!("/{TABS_PAGE}")
}

/// Prefix of the ids of the tab menu's and the overflow list's items:
/// `itervox-tabs|<window>|<verb>|<argument>`. Other menus' ids never start
/// with it (the Projects menu's are `itervox-rail-…`, the Go menu's
/// `itervox-go-…`).
pub const TAB_MENU_PREFIX: &str = "itervox-tabs|";

/// One entry of a native popup menu the tab strip opens.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TabMenuEntry {
    /// A command; `checked` makes it a check item (the overflow list marks
    /// the project the window shows).
    Item {
        id: String,
        label: String,
        enabled: bool,
        checked: Option<bool>,
    },
    /// A disabled title line (a group of the overflow list).
    Header(String),
    Separator,
}

fn menu_id(window: &str, verb: &str, arg: &str) -> String {
    format!("{TAB_MENU_PREFIX}{window}|{verb}|{arg}")
}

fn item(id: String, label: &str, enabled: bool) -> TabMenuEntry {
    TabMenuEntry::Item {
        id,
        label: label.to_string(),
        enabled,
        checked: None,
    }
}

/// The menu of `key`'s tab in window `window` (right-click, the context
/// menu key or Shift+F10): open in a new window, pin or unpin, move left or
/// right within its group, and compact tabs on or off. Empty when the
/// project is not in the strip any more.
pub fn tab_menu(view: &RailView, window: &str, key: &str) -> Vec<TabMenuEntry> {
    let Some(section) = view.section_of(key) else {
        return Vec::new();
    };
    let Some(i) = section.rows.iter().position(|r| r.key == key) else {
        return Vec::new();
    };
    let r = &section.rows[i];
    let (pin_verb, pin_label) = if r.pinned {
        ("unpin", "Unpin Tab")
    } else {
        ("pin", "Pin Tab")
    };
    let (compact, compact_label) = if view.collapsed {
        ("0", "Show Project Names")
    } else {
        ("1", "Hide Project Names")
    };
    vec![
        item(
            menu_id(window, "open-new", key),
            "Open in New Window",
            r.openable,
        ),
        item(menu_id(window, pin_verb, key), pin_label, true),
        TabMenuEntry::Separator,
        item(menu_id(window, "left", key), "Move Left", i > 0),
        item(
            menu_id(window, "right", key),
            "Move Right",
            i + 1 < section.rows.len(),
        ),
        TabMenuEntry::Separator,
        item(menu_id(window, "compact", compact), compact_label, true),
    ]
}

/// A project's line in the overflow list: its name and what waits.
fn overflow_label(r: &RailRow) -> String {
    let mut s = r.name.clone();
    if r.needs_you > 0 {
        s.push_str(&format!(" — {} waiting", r.needs_you));
    }
    if r.running > 0 {
        s.push_str(&format!(" · {} running", r.running));
    }
    if !r.openable {
        s.push_str(&format!(" ({})", r.health.label()));
    }
    s
}

/// The "⌄" list of window `window`, which shows `active`: every project in
/// tab order under its group's title, the shown one checked, the ones
/// without a dashboard disabled. Choosing one shows it in this window.
pub fn overflow_menu(view: &RailView, window: &str, active: Option<&str>) -> Vec<TabMenuEntry> {
    let mut out = Vec::new();
    for (n, s) in view.sections.iter().enumerate() {
        if n > 0 {
            out.push(TabMenuEntry::Separator);
        }
        out.push(TabMenuEntry::Header(s.title.clone()));
        for r in &s.rows {
            out.push(TabMenuEntry::Item {
                id: menu_id(window, "switch", &r.key),
                label: overflow_label(r),
                enabled: r.openable,
                checked: Some(Some(r.key.as_str()) == active),
            });
        }
    }
    out
}

/// The window and action behind a tab menu or overflow list item id;
/// `None` for any other menu's ids.
pub fn tab_menu_command(id: &str) -> Option<(String, RailAction)> {
    let rest = id.strip_prefix(TAB_MENU_PREFIX)?;
    let mut parts = rest.splitn(3, '|');
    let (window, verb, arg) = (parts.next()?, parts.next()?, parts.next()?);
    if window.is_empty() || arg.is_empty() {
        return None;
    }
    let key = arg.to_string();
    let action = match verb {
        "switch" => RailAction::Switch(key),
        "open-new" => RailAction::OpenInNewWindow(key),
        "pin" => RailAction::Pin(key),
        "unpin" => RailAction::Unpin(key),
        "left" => RailAction::Nudge { key, delta: -1 },
        "right" => RailAction::Nudge { key, delta: 1 },
        "compact" => match arg {
            "1" => RailAction::Collapse(true),
            "0" => RailAction::Collapse(false),
            _ => return None,
        },
        _ => return None,
    };
    Some((window.to_string(), action))
}

/// What a dashboard webview needs to show a project: its URL, and the
/// origin and token the token initialization script hands over
/// ([`dashboard_init_script`]).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DashTarget {
    pub url: String,
    pub origin: Option<String>,
    pub token: Option<String>,
}

/// How a window switches its dashboard webview to another project.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SwitchPlan {
    /// Same origin and token: navigate the webview in place; its
    /// initialization script already hands over the right token.
    Navigate { url: String },
    /// Another origin or token: an initialization script is fixed when a
    /// webview is created, so replace the dashboard webview (the window
    /// and its rail stay) with one carrying the new script.
    Replace {
        url: String,
        init_script: Option<String>,
    },
}

/// The switch from a dashboard webview created for `held` (origin, token)
/// to `next`. The token travels only in the initialization script, never
/// in the URL, exactly as when a dashboard window is first opened.
pub fn switch_plan(
    held: Option<(&Option<String>, &Option<String>)>,
    next: &DashTarget,
) -> SwitchPlan {
    match held {
        Some((origin, token)) if *origin == next.origin && *token == next.token => {
            SwitchPlan::Navigate {
                url: next.url.clone(),
            }
        }
        _ => SwitchPlan::Replace {
            url: next.url.clone(),
            init_script: init_script_for(next),
        },
    }
}

/// How the tray opens an in-app page (`path`, e.g. New Task…) of a project.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PagePlan {
    /// A window already shows the project with the same origin and token:
    /// evaluate `script` in its dashboard webview (the Go menu's in-place
    /// navigation, falling back to loading the page) and focus the window.
    InPlace { script: String },
    /// No such window (or its webview was made for another origin or
    /// token): open the project's dashboard at `url`, reusing its window.
    Open { url: String },
}

/// The full address of in-app `path` on the dashboard at `base`:
/// `http://h:1/` + `/?newTask=1` → `http://h:1/?newTask=1`.
pub fn page_url(base: &str, path: &str) -> String {
    format!(
        "{}/{}",
        base.trim_end_matches('/'),
        path.trim_start_matches('/')
    )
}

/// The tray's plan for showing `path` of the project at `next` (its
/// dashboard home), given the window that shows the project, if any:
/// `held` is the (origin, token) its dashboard webview was created with.
pub fn page_plan(
    held: Option<(&Option<String>, &Option<String>)>,
    next: &DashTarget,
    path: &str,
) -> PagePlan {
    let url = page_url(&next.url, path);
    match switch_plan(held, next) {
        SwitchPlan::Navigate { .. } => PagePlan::InPlace {
            script: crate::dashboard::navigate_or_load_script(path, &url),
        },
        _ => PagePlan::Open { url },
    }
}

/// The initialization script of a dashboard webview showing `t`: the
/// embedded flag, and the token when the daemon has one.
pub fn init_script_for(t: &DashTarget) -> Option<String> {
    t.origin
        .as_deref()
        .map(|origin| dashboard_init_script(origin, t.token.as_deref()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::{InputRequired, StateSummary};
    use crate::model::Core;
    use crate::projects::SavedProjects;
    use crate::status::DaemonRecord;
    use crate::tunnel::{TunnelPhase, TunnelStatus};
    use std::collections::HashMap;
    use std::time::Instant;

    fn rec(workflow: &str, pid: i32, hub_member: bool) -> DaemonRecord {
        DaemonRecord {
            schema_version: 1,
            pid,
            workflow: workflow.into(),
            url: format!("http://127.0.0.1:{}/", 8000 + pid),
            token_source: "supervisor".into(),
            token_required: true,
            hub_member,
            ..Default::default()
        }
    }

    fn state(name: &str, running: usize, waiting: usize) -> StateSummary {
        StateSummary {
            project_name: name.into(),
            running,
            input_required: (0..waiting)
                .map(|i| InputRequired {
                    identifier: format!("ENG-{i}"),
                    state: "input_required".into(),
                    queued_at: String::new(),
                    context: String::new(),
                })
                .collect(),
            ..Default::default()
        }
    }

    fn tunnel(id: &str, up: bool) -> TunnelStatus {
        TunnelStatus {
            id: id.into(),
            local_port: 19001,
            phase: if up {
                TunnelPhase::Up
            } else {
                TunnelPhase::Connecting
            },
            pid: None,
            starts: 1,
            last_error: None,
        }
    }

    fn remote(host: &str, daemons: &[(&str, &str, Option<StateSummary>, bool)]) -> ConnectionView {
        ConnectionView {
            id: host.into(),
            name: host.into(),
            transport: "ssh".into(),
            daemons: daemons
                .iter()
                .map(|(key, name, st, up)| RemoteDaemonView {
                    key: (*key).into(),
                    workflow: format!("/srv/{name}/WORKFLOW.md"),
                    name: (*name).into(),
                    tunnel: Some(tunnel(key, *up)),
                    health: match st {
                        Some(s) => Health::Ok(s.clone()),
                        None => Health::Stopped,
                    },
                    has_token: true,
                    hub_path: None,
                })
                .collect(),
            error: None,
            hub: None,
        }
    }

    /// A model with two hub members, one `--no-hub` daemon and one added
    /// project that is stopped.
    fn core_with_projects() -> Core {
        let mut saved = SavedProjects::default();
        saved.add("/p/stopped/WORKFLOW.md");
        let mut core = Core::with_since(saved, 0);
        let mut states = HashMap::new();
        states.insert("/p/alpha/WORKFLOW.md".to_string(), Ok(state("alpha", 2, 1)));
        states.insert("/p/beta/WORKFLOW.md".to_string(), Ok(state("beta", 0, 0)));
        states.insert("/p/solo/WORKFLOW.md".to_string(), Ok(state("solo", 1, 0)));
        core.apply(
            Ok(vec![
                rec("/p/alpha/WORKFLOW.md", 1, true),
                rec("/p/beta/WORKFLOW.md", 2, true),
                rec("/p/solo/WORKFLOW.md", 3, false),
            ]),
            states,
            Instant::now(),
        );
        core
    }

    fn keys(s: &RailSection) -> Vec<&str> {
        s.rows.iter().map(|r| r.key.as_str()).collect()
    }

    #[test]
    fn project_rail_lists_hub_members_non_members_and_remotes() {
        let core = core_with_projects();
        let remotes = vec![
            remote(
                "build-box",
                &[
                    (
                        "remote/build-box/srv/api/WORKFLOW.md",
                        "api",
                        Some(state("api", 3, 2)),
                        true,
                    ),
                    ("remote/build-box/srv/web/WORKFLOW.md", "web", None, false),
                ],
            ),
            remote(
                "gpu-vm",
                &[(
                    "remote/gpu-vm/srv/ml/WORKFLOW.md",
                    "ml",
                    Some(state("ml", 0, 0)),
                    true,
                )],
            ),
        ];
        let rail = build_rail(core.views(), &remotes, &RailPrefs::default());

        let ids: Vec<&str> = rail.sections.iter().map(|s| s.id.as_str()).collect();
        assert_eq!(ids, ["hub", "local", "remote:build-box", "remote:gpu-vm"]);
        let titles: Vec<&str> = rail.sections.iter().map(|s| s.title.as_str()).collect();
        assert_eq!(titles, ["Hub", "This Mac", "build-box", "gpu-vm"]);
        assert_eq!(
            keys(&rail.sections[0]),
            ["/p/alpha/WORKFLOW.md", "/p/beta/WORKFLOW.md"],
            "hub members"
        );
        assert_eq!(
            keys(&rail.sections[1]),
            ["/p/solo/WORKFLOW.md", "/p/stopped/WORKFLOW.md"],
            "daemons outside the hub, and stopped projects"
        );
        assert_eq!(rail.sections[2].rows.len(), 2, "remotes grouped by host");
        assert_eq!(rail.sections[3].rows.len(), 1);

        // Name, health dot, running count, needs-you badge.
        let alpha = rail.row("/p/alpha/WORKFLOW.md").unwrap();
        assert_eq!(
            (
                alpha.name.as_str(),
                alpha.health,
                alpha.running,
                alpha.needs_you
            ),
            ("alpha", RailHealth::Running, 2, 1)
        );
        assert!(alpha.openable);
        assert_eq!(
            alpha.label,
            "alpha, running, 2 agents running, 1 item waiting for you"
        );
        let stopped = rail.row("/p/stopped/WORKFLOW.md").unwrap();
        assert_eq!(stopped.health, RailHealth::Stopped);
        assert!(!stopped.openable, "a stopped daemon has no dashboard");
        let api = rail.row("remote/build-box/srv/api/WORKFLOW.md").unwrap();
        assert_eq!(
            (api.health, api.running, api.needs_you),
            (RailHealth::Running, 3, 2)
        );
        let web = rail.row("remote/build-box/srv/web/WORKFLOW.md").unwrap();
        assert_eq!(web.health, RailHealth::Unreachable, "tunnel not up");
        assert!(!web.openable);

        // ⌘1…⌘9 follow the rows top to bottom.
        let shortcuts: Vec<Option<usize>> = rail.rows().map(|r| r.shortcut).collect();
        assert_eq!(
            shortcuts,
            [
                Some(1),
                Some(2),
                Some(3),
                Some(4),
                Some(5),
                Some(6),
                Some(7)
            ]
        );
        assert_eq!(
            rail.target(None, RailCommand::Goto(3)).as_deref(),
            Some("/p/solo/WORKFLOW.md")
        );
        assert_eq!(rail.target(None, RailCommand::Goto(4)), None, "stopped");
        assert_eq!(
            rail.target(Some("/p/solo/WORKFLOW.md"), RailCommand::Goto(3)),
            None,
            "already shown"
        );
        // ⌘⇧] / ⌘⇧[ step over rows that cannot open, wrapping.
        let next = rail.target(Some("/p/solo/WORKFLOW.md"), RailCommand::Next);
        assert_eq!(
            next.as_deref(),
            Some("remote/build-box/srv/api/WORKFLOW.md")
        );
        let wrap = rail.target(Some("remote/gpu-vm/srv/ml/WORKFLOW.md"), RailCommand::Next);
        assert_eq!(wrap.as_deref(), Some("/p/alpha/WORKFLOW.md"));
        let prev = rail.target(Some("/p/alpha/WORKFLOW.md"), RailCommand::Previous);
        assert_eq!(prev.as_deref(), Some("remote/gpu-vm/srv/ml/WORKFLOW.md"));

        // The app menu's shortcuts map to the commands.
        let items = menu_items();
        let accel = |id: &str| {
            items
                .iter()
                .find(|(i, _, _)| i == id)
                .map(|(_, _, a)| a.clone())
        };
        assert_eq!(accel("itervox-rail-goto-1").as_deref(), Some("CmdOrCtrl+1"));
        assert_eq!(accel("itervox-rail-goto-9").as_deref(), Some("CmdOrCtrl+9"));
        assert_eq!(
            accel("itervox-rail-next").as_deref(),
            Some("CmdOrCtrl+Shift+]")
        );
        assert_eq!(
            accel("itervox-rail-previous").as_deref(),
            Some("CmdOrCtrl+Shift+[")
        );
        for (id, _, _) in &items {
            assert!(menu_command(id).is_some(), "{id}");
        }
        assert_eq!(
            menu_command(NEW_TASK_MENU_ID),
            Some(RailCommand::NewTask),
            "#251: the Projects menu's New Task… item"
        );
        assert_eq!(menu_command("itervox-rail-goto-10"), None);
        assert_eq!(menu_command("open-dashboard"), None);
    }

    #[test]
    fn project_rail_badges_follow_refresh_without_extra_polling() {
        use crate::client::ClientPool;
        use crate::hub_source::tests::FakeServer;
        use crate::hub_source::{read_local, HubReader};
        use crate::model::FetchJob;
        use std::sync::atomic::Ordering;
        use std::sync::{Arc, Mutex};

        // The hub serves both members' summaries; their counts change
        // between the two refreshes.
        let counts = Arc::new(Mutex::new((1usize, 0usize)));
        let c2 = Arc::clone(&counts);
        let hub = FakeServer::start(move |path| {
            assert_eq!(
                path, "/hub/api/v1/projects",
                "the rail reads nothing itself"
            );
            let (running, input) = *c2.lock().unwrap();
            let body = serde_json::json!({
                "hub": {"version": "test", "pid": 1, "url": "", "headSeq": 1},
                "projects": [
                    {"name": "alpha", "workflow": "/p/alpha/WORKFLOW.md", "pid": 1,
                     "health": "ok", "summary": {"projectName": "alpha",
                        "counts": {"running": running, "inputRequired": input}}},
                    {"name": "beta", "workflow": "/p/beta/WORKFLOW.md", "pid": 2,
                     "health": "ok", "summary": {"projectName": "beta", "counts": {}}}
                ]
            });
            (200, body.to_string())
        });
        let records = vec![
            rec("/p/alpha/WORKFLOW.md", 1, true),
            rec("/p/beta/WORKFLOW.md", 2, true),
        ];
        // Unreachable per-daemon URLs: a member poll would show up as an
        // unreachable row instead of the hub's counts.
        let jobs: Vec<FetchJob> = records
            .iter()
            .map(|r| FetchJob {
                key: r.workflow.clone(),
                url: "http://127.0.0.1:9/".into(),
                token: None,
            })
            .collect();
        let (pool, reader) = (ClientPool::default(), HubReader::default());
        let mut core = Core::with_since(SavedProjects::default(), 0);
        let prefs = RailPrefs::default();
        let refresh = |core: &mut Core| {
            let read = read_local(
                &pool,
                &reader,
                Some((hub.origin.as_str(), "viewer")),
                true,
                &records,
                &jobs,
            );
            core.apply(Ok(records.clone()), read.states, Instant::now());
        };

        refresh(&mut core);
        assert_eq!(
            hub.hits.load(Ordering::SeqCst),
            1,
            "one hub read per refresh"
        );
        let rail = build_rail(core.views(), &[], &prefs);
        let alpha = rail.row("/p/alpha/WORKFLOW.md").unwrap();
        assert_eq!(
            (alpha.health, alpha.running, alpha.needs_you),
            (RailHealth::Running, 1, 0)
        );

        // The hub's counts change; the rail shows the old ones however
        // often it is built, and makes no request of its own…
        *counts.lock().unwrap() = (3, 2);
        for _ in 0..5 {
            let again = build_rail(core.views(), &[], &prefs);
            assert_eq!(again.row("/p/alpha/WORKFLOW.md").unwrap().needs_you, 0);
            let _ = push_script(&again, Some("/p/alpha/WORKFLOW.md"));
        }
        assert_eq!(
            hub.hits.load(Ordering::SeqCst),
            1,
            "building the rail polls nothing"
        );

        // …until the next refresh, whose read it shows.
        refresh(&mut core);
        assert_eq!(hub.hits.load(Ordering::SeqCst), 2);
        let rail = build_rail(core.views(), &[], &prefs);
        let alpha = rail.row("/p/alpha/WORKFLOW.md").unwrap();
        assert_eq!((alpha.running, alpha.needs_you), (3, 2));
        assert!(
            alpha.label.ends_with("2 items waiting for you"),
            "{}",
            alpha.label
        );
        let script = push_script(&rail, Some("/p/alpha/WORKFLOW.md"));
        assert!(script.contains("\"needsYou\":2"), "{script}");
        assert!(script.ends_with(",\"/p/alpha/WORKFLOW.md\");"), "{script}");

        // A remote's badge follows the remote refresh the same way.
        let r1 = remote(
            "box",
            &[("remote/box/a", "a", Some(state("a", 0, 0)), true)],
        );
        let r2 = remote(
            "box",
            &[("remote/box/a", "a", Some(state("a", 1, 1)), true)],
        );
        assert_eq!(
            build_rail(&[], &[r1], &prefs)
                .row("remote/box/a")
                .unwrap()
                .needs_you,
            0
        );
        assert_eq!(
            build_rail(&[], &[r2], &prefs)
                .row("remote/box/a")
                .unwrap()
                .needs_you,
            1
        );

        // Drift guard: the rail module reads no daemon itself.
        let src = include_str!("rail.rs");
        let body = &src[..src.find("#[cfg(test)]").unwrap()];
        for forbidden in ["ureq", "client::", "fetch_", "TcpStream", "std::thread"] {
            assert!(!body.contains(forbidden), "rail.rs uses {forbidden}");
        }
    }

    #[test]
    fn project_rail_order_and_pins_persist() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join(crate::projects::PROJECTS_FILE);
        let core = core_with_projects();
        let remotes = vec![remote(
            "box",
            &[
                ("remote/box/a", "a", Some(state("a", 0, 0)), true),
                ("remote/box/b", "b", Some(state("b", 0, 0)), true),
            ],
        )];
        let mut saved = core.saved.clone();
        let rail = build_rail(core.views(), &remotes, &saved.rail);

        // Drag beta above alpha; pin the remote b, then solo; move solo
        // above b in the pinned group; collapse the rail.
        assert!(move_before(
            &rail,
            &mut saved.rail,
            "/p/beta/WORKFLOW.md",
            Some("/p/alpha/WORKFLOW.md")
        ));
        assert!(
            !move_before(
                &rail,
                &mut saved.rail,
                "/p/beta/WORKFLOW.md",
                Some("remote/box/a")
            ),
            "rows move within their group only"
        );
        assert!(saved.rail.set_pinned("remote/box/b", true));
        assert!(saved.rail.set_pinned("/p/solo/WORKFLOW.md", true));
        assert!(!saved.rail.set_pinned("/p/solo/WORKFLOW.md", true));
        let rail = build_rail(core.views(), &remotes, &saved.rail);
        assert_eq!(
            keys(&rail.sections[0]),
            ["remote/box/b", "/p/solo/WORKFLOW.md"]
        );
        assert!(nudge(&rail, &mut saved.rail, "/p/solo/WORKFLOW.md", -1));
        let rail = build_rail(core.views(), &remotes, &saved.rail);
        assert!(
            !nudge(&rail, &mut saved.rail, "/p/solo/WORKFLOW.md", -1),
            "already first"
        );
        saved.rail.collapsed = true;
        saved.save(&path).unwrap();

        // A restart: the file alone gives back the same rail.
        let back = SavedProjects::load(&path).unwrap();
        assert_eq!(back, saved);
        let rail = build_rail(core.views(), &remotes, &back.rail);
        assert!(rail.collapsed);
        let ids: Vec<&str> = rail.sections.iter().map(|s| s.id.as_str()).collect();
        assert_eq!(ids, ["pinned", "hub", "local", "remote:box"]);
        assert_eq!(
            keys(&rail.sections[0]),
            ["/p/solo/WORKFLOW.md", "remote/box/b"],
            "pinned, in the user's order"
        );
        assert!(rail.sections[0].rows.iter().all(|r| r.pinned));
        assert_eq!(
            keys(&rail.sections[1]),
            ["/p/beta/WORKFLOW.md", "/p/alpha/WORKFLOW.md"],
            "the dragged order"
        );
        assert_eq!(keys(&rail.sections[2]), ["/p/stopped/WORKFLOW.md"]);
        assert_eq!(keys(&rail.sections[3]), ["remote/box/a"]);
        assert_eq!(
            rail.rows().next().unwrap().shortcut,
            Some(1),
            "⌘1 is the first pin"
        );

        // Unpinning puts the row back in its group; removing a project
        // forgets its place.
        let mut again = back.clone();
        assert!(again.rail.set_pinned("/p/solo/WORKFLOW.md", false));
        assert!(again.remove("/p/stopped/WORKFLOW.md"));
        let rail = build_rail(core.views(), &remotes, &again.rail);
        assert_eq!(
            keys(&rail.sections[2]),
            ["/p/solo/WORKFLOW.md", "/p/stopped/WORKFLOW.md"]
        );
        assert!(!again
            .rail
            .order
            .iter()
            .any(|k| k == "/p/stopped/WORKFLOW.md"));

        // A file written before #244 (no rail) still loads.
        std::fs::write(
            &path,
            br#"{"version":1,"projects":[{"workflow":"/p/x/WORKFLOW.md"}]}"#,
        )
        .unwrap();
        assert_eq!(
            SavedProjects::load(&path).unwrap().rail,
            RailPrefs::default()
        );
    }

    /// The tray's "New Task…": a window already showing the project with
    /// the same origin and token navigates in place through the embedded
    /// dashboard's hook (falling back to loading the page); no window, or
    /// one whose webview was made for another origin or token (a tunnel
    /// that came back on a new port, a rotated token), opens the
    /// dashboard at the page's full address. The token is never in it.
    #[test]
    fn tray_page_plan_navigates_in_place_or_opens_the_page() {
        let t = DashTarget {
            url: "http://127.0.0.1:8090/".into(),
            origin: Some("http://127.0.0.1:8090".into()),
            token: Some("tok".into()),
        };
        let path = crate::menu::NEW_TASK_PATH;
        assert_eq!(
            page_plan(Some((&t.origin, &t.token)), &t, path),
            PagePlan::InPlace {
                script: crate::dashboard::navigate_or_load_script(
                    "/?newTask=1",
                    "http://127.0.0.1:8090/?newTask=1"
                )
            }
        );
        let open = PagePlan::Open {
            url: "http://127.0.0.1:8090/?newTask=1".into(),
        };
        assert_eq!(page_plan(None, &t, path), open);
        let stale_origin = Some("http://127.0.0.1:9999".to_string());
        assert_eq!(page_plan(Some((&stale_origin, &t.token)), &t, path), open);
        let old_token = Some("old".to_string());
        assert_eq!(page_plan(Some((&t.origin, &old_token)), &t, path), open);
        let PagePlan::Open { url } = page_plan(None, &t, path) else {
            unreachable!()
        };
        assert!(!url.contains("tok"));
        // The page URL is the one the dashboard's New task deep link uses.
        assert_eq!(
            page_url("http://127.0.0.1:50001", path),
            crate::menu::new_task_link("http://127.0.0.1:50001")
        );
    }

    #[test]
    fn rail_switch_keeps_token_injection() {
        let a = DashTarget {
            url: "http://127.0.0.1:8090/".into(),
            origin: Some("http://127.0.0.1:8090".into()),
            token: Some("tok-a".into()),
        };
        // Another route of the same daemon: navigate in place; the
        // webview's script already hands over this token.
        let deep = DashTarget {
            url: "http://127.0.0.1:8090/?issue=ENG-1".into(),
            ..a.clone()
        };
        assert_eq!(
            switch_plan(Some((&a.origin, &a.token)), &deep),
            SwitchPlan::Navigate {
                url: "http://127.0.0.1:8090/?issue=ENG-1".into()
            }
        );
        // Another project: a new dashboard webview whose initialization
        // script is exactly the one a freshly opened window gets.
        let b = DashTarget {
            url: "http://127.0.0.1:8091/".into(),
            origin: Some("http://127.0.0.1:8091".into()),
            token: Some("tok-b".into()),
        };
        let plan = switch_plan(Some((&a.origin, &a.token)), &b);
        assert_eq!(
            plan,
            SwitchPlan::Replace {
                url: "http://127.0.0.1:8091/".into(),
                init_script: Some(dashboard_init_script(
                    "http://127.0.0.1:8091",
                    Some("tok-b")
                )),
            }
        );
        let SwitchPlan::Replace { url, init_script } = &plan else {
            unreachable!()
        };
        assert!(!url.contains("tok-b"), "the token never goes in the URL");
        let script = init_script.as_deref().unwrap();
        assert!(script.contains(r#"window.location.origin!=="http://127.0.0.1:8091""#));
        assert!(
            !script.contains("tok-a"),
            "the old token is not carried over"
        );
        // Another origin with the same token (one stored token reused on
        // two daemons): replaced, since the held script only hands the
        // token to its own origin.
        let same_token_elsewhere = DashTarget {
            token: a.token.clone(),
            ..b.clone()
        };
        assert!(matches!(
            switch_plan(Some((&a.origin, &a.token)), &same_token_elsewhere),
            SwitchPlan::Replace { .. }
        ));
        // Same origin but a new token (the daemon restarted): replaced.
        let restarted = DashTarget {
            token: Some("tok-a2".into()),
            ..a.clone()
        };
        assert!(matches!(
            switch_plan(Some((&a.origin, &a.token)), &restarted),
            SwitchPlan::Replace { .. }
        ));
        // An open API (no token): the embedded flag only.
        let open = DashTarget {
            url: "http://127.0.0.1:8092/".into(),
            origin: Some("http://127.0.0.1:8092".into()),
            token: None,
        };
        assert_eq!(
            switch_plan(Some((&a.origin, &a.token)), &open),
            SwitchPlan::Replace {
                url: "http://127.0.0.1:8092/".into(),
                init_script: Some(dashboard_init_script("http://127.0.0.1:8092", None)),
            }
        );
        // A new window: the same script.
        assert_eq!(
            switch_plan(None, &a),
            SwitchPlan::Replace {
                url: a.url.clone(),
                init_script: Some(dashboard_init_script(
                    "http://127.0.0.1:8090",
                    Some("tok-a")
                )),
            }
        );

        // The tabs page never sees a token.
        let core = core_with_projects();
        let script = push_script(&build_rail(core.views(), &[], &RailPrefs::default()), None);
        assert!(!script.contains("tok-"), "{script}");
        assert!(!script.contains("token\":"), "{script}");

        // The strip's navigations become actions; anything else loads.
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/switch?key=%2Fp%2Fa%2FWORKFLOW.md"),
            Some(RailAction::Switch("/p/a/WORKFLOW.md".into()))
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/move?key=a&before=b"),
            Some(RailAction::Move {
                key: "a".into(),
                before: Some("b".into())
            })
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/move?key=a&before="),
            Some(RailAction::Move {
                key: "a".into(),
                before: None
            })
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/nudge?key=a&delta=-1"),
            Some(RailAction::Nudge {
                key: "a".into(),
                delta: -1
            })
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/open-new?key=a"),
            Some(RailAction::OpenInNewWindow("a".into()))
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/collapse?value=1"),
            Some(RailAction::Collapse(true))
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/ready"),
            Some(RailAction::Ready)
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/add-project"),
            Some(RailAction::AddProject)
        );
        assert_eq!(
            rail_action("tauri://localhost/__itervox/rail/switch?key="),
            None
        );
        assert_eq!(rail_action("tauri://localhost/tabs.html"), None);
        assert_eq!(rail_action("http://127.0.0.1:8090/"), None);
        // The tab strip's webview loads its own page and nothing else.
        assert!(rail_page_allowed("tauri://localhost/tabs.html"));
        assert!(rail_page_allowed("http://tauri.localhost/tabs.html"));
        assert!(rail_page_allowed("https://tauri.localhost/tabs.html?x=1#y"));
        assert!(!rail_page_allowed("http://127.0.0.1:8090/tabs.html"));
        assert!(
            !rail_page_allowed("tauri://localhost/rail.html"),
            "the old page"
        );
        assert!(!rail_page_allowed("tauri://localhost/sessions.html"));
        assert!(!rail_page_allowed("tauri://localhost/tabs.html/../x"));
        assert!(!rail_page_allowed("tauri://evil.test/tabs.html"));
        assert!(!rail_page_allowed("file:///tmp/tabs.html"));
        assert!(!rail_page_allowed("https://example.test/"));
        assert!(!rail_page_allowed("tabs.html"));
    }

    /// The strip's menu anchors: a tab's menu and the "⌄" list ask for a
    /// native popup at a pixel of the strip; the "+" asks for Add Project.
    #[test]
    fn tab_strip_menu_and_add_actions() {
        assert_eq!(
            rail_action(
                "tauri://localhost/__itervox/rail/menu?key=%2Fp%2Fa%2FWORKFLOW.md&x=120&y=34"
            ),
            Some(RailAction::Menu {
                key: "/p/a/WORKFLOW.md".into(),
                x: 120,
                y: 34
            })
        );
        assert_eq!(
            rail_action("http://tauri.localhost/__itervox/rail/overflow?x=1180&y=36"),
            Some(RailAction::Overflow { x: 1180, y: 36 })
        );
        assert_eq!(
            rail_action("http://tauri.localhost/__itervox/rail/add-project"),
            Some(RailAction::AddProject),
            "the + button"
        );
        for bad in [
            "menu?key=a&x=-1&y=3",
            "menu?key=a&x=1",
            "menu?key=a&x=1.5&y=3",
            "menu?key=&x=1&y=3",
            "menu?x=1&y=3",
            "menu?key=a&x=100001&y=3",
            "overflow?x=1",
            "overflow?x=a&y=2",
        ] {
            assert_eq!(
                rail_action(&format!("tauri://localhost/__itervox/rail/{bad}")),
                None,
                "{bad}"
            );
        }
    }

    /// The window: the strip across the top of the content area (under the
    /// native title bar), the dashboard under it at the full width.
    #[test]
    fn window_layout_puts_the_strip_above_a_full_width_dashboard() {
        let (strip, dash) = window_layout(1280.0, 860.0);
        assert_eq!(
            strip,
            Bounds {
                x: 0.0,
                y: 0.0,
                width: 1280.0,
                height: TAB_STRIP_HEIGHT
            }
        );
        assert_eq!(
            dash,
            Bounds {
                x: 0.0,
                y: TAB_STRIP_HEIGHT,
                width: 1280.0,
                height: 860.0 - TAB_STRIP_HEIGHT
            }
        );
        // The minimum window size still leaves the dashboard most of it.
        let (_, dash) = window_layout(720.0, 480.0);
        assert_eq!((dash.width, dash.height), (720.0, 440.0));
        // A window shorter than the strip: no negative sizes.
        let (strip, dash) = window_layout(-5.0, 20.0);
        assert_eq!((strip.width, strip.height), (0.0, 20.0));
        assert_eq!((dash.y, dash.height), (20.0, 0.0));
    }

    fn ids(entries: &[TabMenuEntry]) -> Vec<(String, bool, Option<bool>)> {
        entries
            .iter()
            .filter_map(|e| match e {
                TabMenuEntry::Item {
                    id,
                    enabled,
                    checked,
                    ..
                } => Some((id.clone(), *enabled, *checked)),
                _ => None,
            })
            .collect()
    }

    fn labels(entries: &[TabMenuEntry]) -> Vec<String> {
        entries
            .iter()
            .map(|e| match e {
                TabMenuEntry::Item { label, .. } => label.clone(),
                TabMenuEntry::Header(t) => format!("[{t}]"),
                TabMenuEntry::Separator => "-".into(),
            })
            .collect()
    }

    /// A tab's menu and the overflow list carry the rail's actions: their
    /// items come back as the same [`RailAction`]s the page's navigations
    /// give, addressed to the window that opened them.
    #[test]
    fn tab_menus_carry_the_rail_actions() {
        let core = core_with_projects();
        let remotes = vec![remote(
            "box",
            &[("remote/box/a", "a", Some(state("a", 1, 3)), true)],
        )];
        let mut prefs = RailPrefs::default();
        prefs.set_pinned("/p/solo/WORKFLOW.md", true);
        let view = build_rail(core.views(), &remotes, &prefs);
        let w = "dashboard-3";

        // An unpinned hub tab, first of its group.
        let m = tab_menu(&view, w, "/p/alpha/WORKFLOW.md");
        assert_eq!(
            labels(&m),
            [
                "Open in New Window",
                "Pin Tab",
                "-",
                "Move Left",
                "Move Right",
                "-",
                "Hide Project Names"
            ]
        );
        let got: Vec<(Option<(String, RailAction)>, bool)> = ids(&m)
            .into_iter()
            .map(|(id, enabled, _)| (tab_menu_command(&id), enabled))
            .collect();
        let alpha = || "/p/alpha/WORKFLOW.md".to_string();
        assert_eq!(
            got,
            vec![
                (Some((w.into(), RailAction::OpenInNewWindow(alpha()))), true),
                (Some((w.into(), RailAction::Pin(alpha()))), true),
                (
                    Some((
                        w.into(),
                        RailAction::Nudge {
                            key: alpha(),
                            delta: -1
                        }
                    )),
                    false
                ),
                (
                    Some((
                        w.into(),
                        RailAction::Nudge {
                            key: alpha(),
                            delta: 1
                        }
                    )),
                    true
                ),
                (Some((w.into(), RailAction::Collapse(true))), true),
            ]
        );
        // A pinned tab unpins; a stopped one cannot open a window; with
        // compact tabs the toggle shows the names again.
        let solo = tab_menu(&view, w, "/p/solo/WORKFLOW.md");
        assert_eq!(labels(&solo)[1], "Unpin Tab");
        assert_eq!(
            tab_menu_command(&ids(&solo)[1].0),
            Some((w.into(), RailAction::Unpin("/p/solo/WORKFLOW.md".into())))
        );
        let stopped = tab_menu(&view, w, "/p/stopped/WORKFLOW.md");
        assert!(!ids(&stopped)[0].1, "no dashboard to open");
        assert!(
            !ids(&stopped)[2].1 && !ids(&stopped)[3].1,
            "alone in its group"
        );
        let mut compact = view.clone();
        compact.collapsed = true;
        let c = tab_menu(&compact, w, "/p/alpha/WORKFLOW.md");
        assert_eq!(labels(&c).last().unwrap(), "Show Project Names");
        assert_eq!(
            tab_menu_command(&ids(&c).last().unwrap().0),
            Some((w.into(), RailAction::Collapse(false)))
        );
        assert!(tab_menu(&view, w, "/p/gone/WORKFLOW.md").is_empty());

        // The overflow list: every project in tab order under its group's
        // title, the shown one checked, closed ones disabled.
        let o = overflow_menu(&view, w, Some("/p/beta/WORKFLOW.md"));
        assert_eq!(
            labels(&o),
            [
                "[Pinned]",
                "solo · 1 running",
                "-",
                "[Hub]",
                "alpha — 1 waiting · 2 running",
                "beta",
                "-",
                "[This Mac]",
                "stopped (stopped)",
                "-",
                "[box]",
                "a — 3 waiting · 1 running",
            ]
        );
        let items = ids(&o);
        let order: Vec<String> = items
            .iter()
            .map(|(id, _, _)| match tab_menu_command(id) {
                Some((win, RailAction::Switch(k))) if win == w => k,
                other => panic!("{other:?}"),
            })
            .collect();
        let tab_order: Vec<String> = view.rows().map(|r| r.key.clone()).collect();
        assert_eq!(order, tab_order, "the list follows the tabs");
        let checked: Vec<bool> = items.iter().map(|(_, _, c)| c.unwrap()).collect();
        assert_eq!(checked, [false, false, true, false, false]);
        let enabled: Vec<bool> = items.iter().map(|(_, e, _)| *e).collect();
        assert_eq!(enabled, [true, true, true, false, true]);

        // Keys with the separator still round-trip (the key is last).
        assert_eq!(
            tab_menu_command("itervox-tabs|dashboard-1|switch|/p/a|b/WORKFLOW.md"),
            Some((
                "dashboard-1".into(),
                RailAction::Switch("/p/a|b/WORKFLOW.md".into())
            ))
        );
        // Anything else is no tab command, and the other menus' ids never
        // parse as one (nor a tab item's id as theirs).
        for bad in [
            "itervox-tabs|",
            "itervox-tabs|w|switch|",
            "itervox-tabs||switch|k",
            "itervox-tabs|w|delete|k",
            "itervox-tabs|w|compact|2",
            "itervox-tabs|w|switch",
            "itervox-rail-next",
        ] {
            assert_eq!(tab_menu_command(bad), None, "{bad}");
        }
        for (id, _, _) in menu_items() {
            assert_eq!(tab_menu_command(&id), None, "{id}");
        }
        for (id, _) in crate::go_menu::go_menu_items() {
            assert_eq!(tab_menu_command(&id), None, "{id}");
        }
        for (id, _, _) in items.iter().chain(ids(&m).iter()) {
            assert_eq!(menu_command(id), None, "{id}");
            assert_eq!(crate::go_menu::go_menu_path(id), None, "{id}");
        }
    }

    /// Drift guard: the page navigates to the paths the window intercepts,
    /// reads the fields the push script sends, defines the function it
    /// calls, and labels every badge.
    #[test]
    fn tabs_page_uses_the_intercepted_paths() {
        let html = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../dist/",
            "tabs.html"
        ))
        .expect("desktop/dist/tabs.html");
        assert!(html.contains(&format!("var BASE = '{RAIL_PATH}';")));
        assert!(html.contains("window.__itervoxRail = function (next, current)"));
        for action in ["switch", "open-new", "move", "nudge", "menu", "overflow"] {
            let url = format!(
                "tauri://localhost{RAIL_PATH}{action}?key=k&before=&delta=1&value=1&x=1&y=2"
            );
            assert!(rail_action(&url).is_some(), "{action}");
        }
        for call in [
            "go('ready')",
            "go('switch', { key: r.key })",
            "go('open-new', { key: r.key })",
            "go('move', { key: key, before: before })",
            "go('nudge', { key: r.key, delta: String(delta) })",
            "go('menu', { key: r.key, x:",
            "go('overflow', { x:",
            "go('add-project')",
        ] {
            assert!(html.contains(call), "{call}");
        }
        // ⌘/Ctrl-click and middle-click open a new window.
        assert!(html.contains("if (e.metaKey || e.ctrlKey) openNew(r);"));
        assert!(html.contains("b.addEventListener('auxclick'"));
        assert!(html.contains("if (e.button !== 1) return;"));
        for field in [
            "r.needsYou",
            "r.running",
            "r.health",
            "r.pinned",
            "r.shortcut",
            "r.openable",
            "r.detail",
            "r.label",
            "view.collapsed",
            "view.machine",
            "s.rows",
            "s.title",
        ] {
            assert!(html.contains(field), "{field}");
        }
        // Accessible: a tab list with one selected tab; badges, dots and
        // counts carry labels; tabs announce their shortcut; arrow keys
        // move focus, Alt+arrows reorder, the menu key opens the menu.
        assert!(html.contains(r#"role="tablist" aria-label="Projects""#));
        assert!(html.contains("b.setAttribute('role', 'tab');"));
        assert!(
            html.contains("b.setAttribute('aria-selected', r.key === active ? 'true' : 'false');")
        );
        assert!(html.contains("img('badge', r.needsYou + ' waiting for you"));
        assert!(html.contains("img('dot ' + r.health, healthText(r.health))"));
        assert!(html.contains("'aria-keyshortcuts', 'Meta+' + r.shortcut"));
        assert!(html.contains("e.key === 'ArrowRight'"));
        assert!(html.contains("e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')"));
        assert!(html.contains("e.key === 'ContextMenu'"));
        assert!(
            html.contains(r#"id="add-project""#) && html.contains(r#"aria-label="Add project""#)
        );
        assert!(html.contains(r#"id="overflow""#) && html.contains(r#"aria-haspopup="menu""#));
        // One row, never wrapped: the tabs scroll sideways.
        assert!(html.contains("overflow-x:auto"));
        assert!(!html.contains("flex-wrap"));
        // Opaque: no glass, no material, no blur.
        for forbidden in [
            "glass",
            "data-material",
            "backdrop-filter",
            "__ITERVOX_MATERIAL__",
        ] {
            assert!(!html.contains(forbidden), "tabs.html uses {forbidden}");
        }
        assert!(html.contains("background:var(--strip)"));
        // No network access of its own.
        for forbidden in [
            "fetch(",
            "XMLHttpRequest",
            "EventSource",
            "WebSocket",
            "__TAURI",
        ] {
            assert!(!html.contains(forbidden), "tabs.html uses {forbidden}");
        }
        // The old rail page is gone.
        assert!(
            !std::path::Path::new(concat!(env!("CARGO_MANIFEST_DIR"), "/../dist/rail.html"))
                .exists()
        );
    }

    /// The app's pages share one stylesheet (`dist/theme.css`): every
    /// colour lives there as a custom property, light and dark, so a page
    /// carries no colour literals or inline styles of its own.
    #[test]
    fn pages_share_the_theme_stylesheet() {
        let dist = concat!(env!("CARGO_MANIFEST_DIR"), "/../dist/");
        let theme = std::fs::read_to_string(format!("{dist}theme.css")).expect("dist/theme.css");
        assert!(theme.contains("@media (prefers-color-scheme: dark)"));
        assert!(theme.contains("@media (prefers-reduced-transparency: reduce)"));
        // The tab strip's opaque tokens, light and dark.
        for token in [
            "--strip:",
            "--tab-active:",
            "--tab-active-border:",
            "--shadow-tab:",
        ] {
            assert_eq!(theme.matches(token).count(), 2, "{token}");
        }
        for page in ["tabs.html", "sessions.html", "index.html"] {
            let html = std::fs::read_to_string(format!("{dist}{page}")).expect(page);
            assert!(
                html.contains(r#"<link rel="stylesheet" href="theme.css">"#),
                "{page} links theme.css"
            );
            assert!(
                html.contains(r#"<body class="page""#),
                "{page}: page backdrop"
            );
            for literal in ["rgba(", "rgb(", "style=\"", "#fff", "#000"] {
                assert!(!html.contains(literal), "{page} has {literal}");
            }
            // No hex colours: every `#` in a style block is an id selector.
            if let Some((_, rest)) = html.split_once("<style>") {
                let css = rest.split_once("</style>").map(|(c, _)| c).unwrap_or("");
                for (i, _) in css.match_indices('#') {
                    let next: String = css[i + 1..]
                        .chars()
                        .take_while(|c| c.is_ascii_alphanumeric() || *c == '-' || *c == '_')
                        .collect();
                    assert!(
                        !(matches!(next.len(), 3 | 6 | 8)
                            && next.chars().all(|c| c.is_ascii_hexdigit())),
                        "{page} has the colour #{next}"
                    );
                }
            }
        }
    }

    /// The machine panel rides with the tabs push, and is left out when no
    /// project uses a model server.
    #[test]
    fn push_script_carries_the_machine_panel_only_when_there_is_one() {
        use crate::machine_health::{DaemonModelServer, ListRead, MachineHealth};
        let core = core_with_projects();
        let mut view = build_rail(core.views(), &[], &RailPrefs::default());
        assert!(!push_script(&view, None).contains("\"machine\""));
        let mut m = MachineHealth::default();
        m.apply_lists(vec![ListRead {
            key: "/p/a/WORKFLOW.md".into(),
            name: "a".into(),
            rows: Ok(vec![DaemonModelServer {
                var: "ANTHROPIC_BASE_URL".into(),
                url: "http://127.0.0.1:8000".into(),
                profiles: vec!["coder-local".into()],
                pools: vec!["colibri".into()],
                remote: false,
                unchecked: false,
            }]),
        }]);
        view.machine = m.panel();
        let script = push_script(&view, None);
        assert!(
            script.contains("\"machine\":{\"servers\":[{\"label\":\"colibri 127.0.0.1:8000"),
            "{script}"
        );
        assert!(script.contains("\"gpu\":[\"GPU: checking…\"]"), "{script}");
    }
}
