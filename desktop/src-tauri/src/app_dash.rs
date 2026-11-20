//! Tauri glue of the dashboard windows and their project tabs (#244).
//!
//! A dashboard window holds two webviews, one above the other: the tab
//! strip across the top (the app's own `tabs.html`, see [`crate::rail`])
//! and, under it at the full width, the dashboard of the project it shows
//! (served by that project's daemon, with the token handed over by the
//! initialization script of [`crate::dashboard`]). Neither webview has any
//! IPC permission. Switching projects navigates the dashboard webview in
//! place when the origin and token stay the same, and otherwise replaces
//! only that webview — the window and its tabs stay. A tab's menu and the
//! overflow list are native popup menus, whose items come back through the
//! app's menu events ([`crate::rail::tab_menu_command`]).

use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

use tauri::webview::WebviewBuilder;
use tauri::{
    AppHandle, LogicalPosition, LogicalSize, Manager, Rect, WebviewUrl, Window, WindowEvent,
};

use super::{error_dialog, lock, read_token_file, Shared};
use crate::dashboard::{browser_login_url, origin_of};
use crate::model::token_for;
use crate::rail::{
    build_rail, menu_changed, menu_projects, move_before, nudge, overflow_menu, page_plan,
    page_url, push_script, rail_action, rail_page_allowed, switch_plan, tab_menu, window_layout,
    Bounds, DashTarget, MenuProject, PagePlan, RailAction, RailCommand, RailView, SwitchPlan,
    TabMenuEntry, TABS_PAGE,
};
use crate::remote::is_remote_key;

/// One dashboard window: the project it shows, its dashboard webview, and
/// the origin and token that webview's initialization script carries.
struct DashWin {
    project: String,
    view: String,
    origin: Option<String>,
    token: Option<String>,
}

/// The dashboard windows, by window label.
#[derive(Default)]
pub(super) struct Dash {
    windows: Mutex<HashMap<String, DashWin>>,
    /// The dashboard window focused last: where the shortcuts act.
    focused: Mutex<Option<String>>,
    generation: AtomicU32,
    /// The tabs last pushed to every window, to skip unchanged pushes.
    last_push: Mutex<Option<RailView>>,
    /// The tab menu or overflow list shown last, kept alive until the next
    /// one: on Linux a popup returns at once, and its items must outlive
    /// the call to be chosen.
    popup: Mutex<Option<tauri::menu::Menu<tauri::Wry>>>,
    /// The projects the Projects menu was last built with, to rebuild it
    /// only when its labels change.
    last_menu: Mutex<Option<Vec<MenuProject>>>,
}

impl Dash {
    /// A window closed.
    pub(super) fn forget(&self, label: &str) {
        lock(&self.windows).remove(label);
        let mut f = lock(&self.focused);
        if f.as_deref() == Some(label) {
            *f = None;
        }
    }

    /// Whether a dashboard window shows `key`.
    pub(super) fn shows(&self, key: &str) -> bool {
        lock(&self.windows).values().any(|w| w.project == key)
    }

    /// A window gained focus.
    pub(super) fn note_focus(&self, label: &str) {
        if lock(&self.windows).contains_key(label) {
            *lock(&self.focused) = Some(label.to_string());
        }
    }
}

/// Where a dashboard opens.
pub(super) enum Mode {
    /// The window already showing the project, else a new window (tray,
    /// notifications, the Sessions window).
    Reuse,
    /// A new window (a tab's "Open in New Window", ⌘-click or middle-click).
    NewWindow,
    /// In place, in this window (a tab click or shortcut).
    In(String),
}

fn tabs_label(window: &str) -> String {
    format!("{window}-tabs")
}

/// The project's dashboard: URL (or `url`, a deep link), origin, token and
/// window title. Reads only what the last refresh holds.
pub(super) fn target(
    shared: &Shared,
    key: &str,
    url: Option<String>,
) -> Result<(DashTarget, String), String> {
    if is_remote_key(key) {
        let (t, title) = shared.remote.dashboard(key, url.as_deref())?;
        return Ok((
            DashTarget {
                url: t.url,
                origin: Some(t.origin),
                token: t.token,
            },
            title,
        ));
    }
    let core = lock(&shared.core);
    let rec = core
        .last_records()
        .iter()
        .find(|r| r.workflow == key)
        .cloned()
        .ok_or("That project's daemon is not running.")?;
    let base = rec.base_url();
    let token = token_for(&rec, shared.tokens.as_ref(), &read_token_file).flatten();
    let title = core
        .view(key)
        .map(|v| format!("{} — Itervox", v.name))
        .unwrap_or_else(|| "Itervox".into());
    Ok((
        DashTarget {
            url: url.unwrap_or_else(|| base.clone()),
            origin: origin_of(&base),
            token,
        },
        title,
    ))
}

/// The project's dashboard as "Open in Browser" opens it: Open Dashboard's
/// URL and token ([`target`]: the local daemon's address and the token the
/// app hands its window; a remote daemon's tunnel and token, never through
/// a host's hub), as one `?token=` address built like `itervox open`'s.
fn browser_url(shared: &Shared, key: &str) -> Result<String, String> {
    let (url, token) = if is_remote_key(key) {
        let t = shared.remote.browser_dashboard(key)?;
        (t.url, t.token)
    } else {
        let (t, _) = target(shared, key, None)?;
        (t.url, t.token)
    };
    browser_login_url(&url, token.as_deref())
        .ok_or_else(|| "That project's dashboard has no web address to open.".to_string())
}

/// Menu: Open in Browser. Opens `key`'s dashboard in the default browser,
/// logged in. Errors are shown without the address: it holds the token.
pub(super) fn open_in_browser(app: &AppHandle, shared: &Shared, key: &str) {
    if let Err(e) = browser_url(shared, key).and_then(|u| crate::platform::open_web_url(&u)) {
        error_dialog(app, &e);
    }
}

/// Opens `key`'s dashboard (at `url`, else its home) per `mode`.
pub(super) fn open(
    app: &AppHandle,
    shared: &Arc<Shared>,
    key: &str,
    url: Option<String>,
    mode: Mode,
) {
    let (t, title) = match target(shared, key, url) {
        Ok(x) => x,
        Err(e) => {
            error_dialog(app, &e);
            return;
        }
    };
    let Ok(parsed) = tauri::Url::parse(&t.url) else {
        return;
    };
    let (app2, shared2, key) = (app.clone(), Arc::clone(shared), key.to_string());
    let _ = app.run_on_main_thread(move || {
        let existing = {
            let windows = lock(&shared2.dash.windows);
            match mode {
                Mode::Reuse => windows
                    .iter()
                    .find(|(_, w)| w.project == key)
                    .map(|(l, _)| l.clone()),
                Mode::In(l) => windows.contains_key(&l).then_some(l),
                Mode::NewWindow => None,
            }
        };
        if let Some(window) = existing.and_then(|l| app2.get_window(&l)) {
            switch(&app2, &shared2, &window, &key, parsed, &t, &title);
            let _ = window.show();
            let _ = window.set_focus();
            return;
        }
        if let Err(e) = create(&app2, &shared2, &key, parsed, &t, &title) {
            eprintln!("itervox desktop: could not open the dashboard: {e}");
        }
    });
}

fn dash_webview(
    label: &str,
    url: tauri::Url,
    init_script: Option<String>,
) -> WebviewBuilder<tauri::Wry> {
    let b = WebviewBuilder::new(label, WebviewUrl::External(url));
    match init_script {
        Some(s) => b.initialization_script(s),
        None => b,
    }
}

/// Shows `key` in `window` (main thread).
fn switch(
    app: &AppHandle,
    shared: &Arc<Shared>,
    window: &Window,
    key: &str,
    parsed: tauri::Url,
    t: &DashTarget,
    title: &str,
) {
    let label = window.label().to_string();
    let held = lock(&shared.dash.windows)
        .get(&label)
        .map(|w| (w.view.clone(), w.origin.clone(), w.token.clone()));
    let Some((old_view, origin, token)) = held else {
        return;
    };
    let current = app.get_webview(&old_view);
    let plan = match (&current, switch_plan(Some((&origin, &token)), t)) {
        // The dashboard webview is gone: build a new one.
        (None, SwitchPlan::Navigate { url }) => SwitchPlan::Replace {
            url,
            init_script: crate::rail::init_script_for(t),
        },
        (_, plan) => plan,
    };
    let view = match plan {
        SwitchPlan::Navigate { .. } => {
            if let Some(v) = &current {
                let _ = v.navigate(parsed);
            }
            old_view
        }
        SwitchPlan::Replace { init_script, .. } => {
            let n = shared.dash.generation.fetch_add(1, Ordering::Relaxed);
            let view = format!("{label}-view-{n}");
            let (_, d) = bounds(window);
            match window.add_child(
                dash_webview(&view, parsed, init_script),
                LogicalPosition::new(d.x, d.y),
                LogicalSize::new(d.width, d.height),
            ) {
                Ok(v) => {
                    if let Some(old) = current {
                        let _ = old.close();
                    }
                    let _ = v.set_focus();
                }
                Err(e) => {
                    eprintln!("itervox desktop: could not switch the dashboard: {e}");
                    return;
                }
            }
            view
        }
    };
    if let Some(w) = lock(&shared.dash.windows).get_mut(&label) {
        w.project = key.to_string();
        w.view = view;
        w.origin.clone_from(&t.origin);
        w.token.clone_from(&t.token);
    }
    let _ = window.set_title(title);
    push_to(app, shared, &label);
}

/// The tab strip's and the dashboard's bounds for the window's current
/// content size (under the native title bar).
fn bounds(window: &Window) -> (Bounds, Bounds) {
    let scale = window.scale_factor().unwrap_or(1.0);
    let size = window
        .inner_size()
        .map(|s| s.to_logical::<f64>(scale))
        .unwrap_or(LogicalSize::new(1280.0, 860.0));
    window_layout(size.width, size.height)
}

fn rect(b: Bounds) -> Rect {
    Rect {
        position: LogicalPosition::new(b.x, b.y).into(),
        size: LogicalSize::new(b.width, b.height).into(),
    }
}

/// Lays out a window's tab strip and dashboard (main thread).
fn layout(app: &AppHandle, shared: &Shared, label: &str) {
    let Some(window) = app.get_window(label) else {
        return;
    };
    let view = lock(&shared.dash.windows)
        .get(label)
        .map(|w| w.view.clone());
    let (strip, dash) = bounds(&window);
    if let Some(t) = app.get_webview(&tabs_label(label)) {
        let _ = t.set_bounds(rect(strip));
    }
    if let Some(v) = view.and_then(|v| app.get_webview(&v)) {
        let _ = v.set_bounds(rect(dash));
    }
}

/// A new dashboard window with its tab strip (main thread).
fn create(
    app: &AppHandle,
    shared: &Arc<Shared>,
    key: &str,
    parsed: tauri::Url,
    t: &DashTarget,
    title: &str,
) -> tauri::Result<()> {
    let n = shared.dash.generation.fetch_add(1, Ordering::Relaxed);
    let label = format!("dashboard-{n}");
    #[cfg(target_os = "macos")]
    let _ = app.set_activation_policy(tauri::ActivationPolicy::Regular);
    // An opaque window: the tab strip draws its own solid theme colours
    // (no system material behind it, so nothing shows through).
    let builder = tauri::window::WindowBuilder::new(app, &label)
        .title(title)
        .inner_size(1280.0, 860.0)
        .min_inner_size(720.0, 480.0);
    // Tauri's default macOS style (TitleBarStyle::Visible) turns on
    // NSFullSizeContentView, so the content view starts at the window's top
    // edge, under the title bar: the tab strip laid out at y = 0 was hidden
    // behind it and the dashboard (at y = 40) covered what was left.
    // Transparent keeps the native title bar but no full-size content view,
    // so the content area, inner_size and window_layout all start below it.
    #[cfg(target_os = "macos")]
    let builder = builder.title_bar_style(tauri::TitleBarStyle::Transparent);
    // Linux and Windows have no app menu bar: the window carries the
    // Projects menu, with Ctrl accelerators (#260).
    #[cfg(not(target_os = "macos"))]
    let builder = builder.menu(window_menu(app, &current_rail(shared))?);
    let window = builder.build()?;
    let (strip, d) = bounds(&window);

    let (nav_app, nav_shared, nav_label) = (app.clone(), Arc::clone(shared), label.clone());
    let tabs = WebviewBuilder::new(tabs_label(&label), WebviewUrl::App(TABS_PAGE.into()))
        .on_navigation(move |url| match rail_action(url.as_str()) {
            Some(action) => {
                let (app, shared, label) =
                    (nav_app.clone(), Arc::clone(&nav_shared), nav_label.clone());
                // Off the navigation callback: switching builds webviews.
                let _ = std::thread::Builder::new()
                    .name("itervox-tabs".into())
                    .spawn(move || handle_action(&app, &shared, &label, action));
                false
            }
            None => rail_page_allowed(url.as_str()),
        });
    window.add_child(
        tabs,
        LogicalPosition::new(strip.x, strip.y),
        LogicalSize::new(strip.width, strip.height),
    )?;

    let view = format!("{label}-view-{n}");
    let dash = window.add_child(
        dash_webview(&view, parsed, crate::rail::init_script_for(t)),
        LogicalPosition::new(d.x, d.y),
        LogicalSize::new(d.width, d.height),
    )?;
    lock(&shared.dash.windows).insert(
        label.clone(),
        DashWin {
            project: key.to_string(),
            view,
            origin: t.origin.clone(),
            token: t.token.clone(),
        },
    );
    let (ev_app, ev_shared, ev_label) = (app.clone(), Arc::clone(shared), label.clone());
    window.on_window_event(move |e| {
        if matches!(
            e,
            WindowEvent::Resized(_) | WindowEvent::ScaleFactorChanged { .. }
        ) {
            layout(&ev_app, &ev_shared, &ev_label);
        }
    });
    *lock(&shared.dash.focused) = Some(label);
    let _ = dash.set_focus();
    Ok(())
}

/// The dashboard window focused last, while it is open.
pub(super) fn last_focused(shared: &Shared) -> Option<String> {
    let windows = lock(&shared.dash.windows);
    lock(&shared.dash.focused)
        .clone()
        .filter(|l| windows.contains_key(l))
}

/// The tabs as the last refresh left them.
pub(super) fn current_rail(shared: &Shared) -> RailView {
    let remotes = shared.remote.views();
    let machine = shared.machine.panel();
    let core = lock(&shared.core);
    let mut view = build_rail(core.views(), &remotes, &core.saved.rail);
    view.machine = machine;
    view
}

fn eval_rail(app: &AppHandle, label: &str, view: &RailView, active: &str) {
    if let Some(t) = app.get_webview(&tabs_label(label)) {
        let _ = t.eval(push_script(view, Some(active)));
    }
}

/// Pushes the tabs to every dashboard window when they changed since the
/// last push (or always, with `force`). Called after each refresh: the
/// tabs show what the refresh read and read nothing themselves.
pub(super) fn push_all(app: &AppHandle, shared: &Shared, force: bool) {
    let view = current_rail(shared);
    refresh_menus(app, shared, &view);
    {
        let mut last = lock(&shared.dash.last_push);
        if !force && last.as_ref() == Some(&view) {
            return;
        }
        *last = Some(view.clone());
    }
    let targets: Vec<(String, String)> = lock(&shared.dash.windows)
        .iter()
        .map(|(l, w)| (l.clone(), w.project.clone()))
        .collect();
    for (label, project) in targets {
        eval_rail(app, &label, &view, &project);
    }
}

/// Pushes the tabs to one window (its page loaded, or it switched).
fn push_to(app: &AppHandle, shared: &Shared, label: &str) {
    let project = lock(&shared.dash.windows)
        .get(label)
        .map(|w| w.project.clone());
    if let Some(project) = project {
        eval_rail(app, label, &current_rail(shared), &project);
    }
}

/// Saves a tab arrangement change with the project list and shows it
/// everywhere.
fn save_prefs(
    app: &AppHandle,
    shared: &Shared,
    change: impl FnOnce(&RailView, &mut crate::projects::RailPrefs) -> bool,
) {
    let rail = current_rail(shared);
    let res = {
        let mut core = lock(&shared.core);
        if !change(&rail, &mut core.saved.rail) {
            return;
        }
        core.saved.save(&shared.projects_file)
    };
    if let Err(e) = res {
        error_dialog(app, &format!("Could not save the project list: {e}"));
    }
    push_all(app, shared, true);
}

/// Compact tabs (initials) on or off, in every window. The strip keeps its
/// height, so nothing is laid out again.
fn set_collapsed(app: &AppHandle, shared: &Shared, value: bool) {
    save_prefs(app, shared, |_, p| {
        let changed = p.collapsed != value;
        p.collapsed = value;
        changed
    });
}

/// Shows `entries` as a native popup menu at (`x`, `y`) of window `label`'s
/// tab strip (which starts at the window's content origin).
fn popup(
    app: &AppHandle,
    shared: &Arc<Shared>,
    label: &str,
    x: i32,
    y: i32,
    entries: Vec<TabMenuEntry>,
) {
    if entries.is_empty() {
        return;
    }
    let (app2, shared2, label) = (app.clone(), Arc::clone(shared), label.to_string());
    let _ = app.run_on_main_thread(move || {
        let Some(window) = app2.get_window(&label) else {
            return;
        };
        let menu = match popup_menu(&app2, &entries) {
            Ok(m) => m,
            Err(e) => {
                eprintln!("itervox desktop: could not build the tab menu: {e}");
                return;
            }
        };
        *lock(&shared2.dash.popup) = Some(menu.clone());
        let at = LogicalPosition::new(f64::from(x), f64::from(y));
        if let Err(e) = window.popup_menu_at(&menu, at) {
            eprintln!("itervox desktop: could not show the tab menu: {e}");
        }
    });
}

fn popup_menu(
    app: &AppHandle,
    entries: &[TabMenuEntry],
) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    use tauri::menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem};
    let menu = Menu::new(app)?;
    for e in entries {
        match e {
            TabMenuEntry::Item {
                id,
                label,
                enabled,
                checked: None,
            } => menu.append(&MenuItem::with_id(app, id, label, *enabled, None::<&str>)?)?,
            TabMenuEntry::Item {
                id,
                label,
                enabled,
                checked: Some(c),
            } => menu.append(&CheckMenuItem::with_id(
                app,
                id,
                label,
                *enabled,
                *c,
                None::<&str>,
            )?)?,
            TabMenuEntry::Header(title) => {
                menu.append(&MenuItem::new(app, title, false, None::<&str>)?)?
            }
            TabMenuEntry::Separator => menu.append(&PredefinedMenuItem::separator(app)?)?,
        }
    }
    Ok(menu)
}

/// A tab strip's request (its page's navigation, or an item of a tab menu
/// or the overflow list) from window `label` (off the main thread).
pub(super) fn handle_action(
    app: &AppHandle,
    shared: &Arc<Shared>,
    label: &str,
    action: RailAction,
) {
    match action {
        RailAction::Ready => push_to(app, shared, label),
        RailAction::Switch(key) => open(app, shared, &key, None, Mode::In(label.to_string())),
        RailAction::OpenInNewWindow(key) => open(app, shared, &key, None, Mode::NewWindow),
        RailAction::Pin(key) => save_prefs(app, shared, |_, p| p.set_pinned(&key, true)),
        RailAction::Unpin(key) => save_prefs(app, shared, |_, p| p.set_pinned(&key, false)),
        RailAction::Move { key, before } => save_prefs(app, shared, |r, p| {
            move_before(r, p, &key, before.as_deref())
        }),
        RailAction::Nudge { key, delta } => {
            save_prefs(app, shared, |r, p| nudge(r, p, &key, delta))
        }
        RailAction::Collapse(v) => set_collapsed(app, shared, v),
        RailAction::AddProject => super::add_project(app, shared),
        RailAction::Menu { key, x, y } => {
            let entries = tab_menu(&current_rail(shared), label, &key);
            popup(app, shared, label, x, y, entries);
        }
        RailAction::Overflow { x, y } => {
            let project = lock(&shared.dash.windows)
                .get(label)
                .map(|w| w.project.clone());
            let entries = overflow_menu(&current_rail(shared), label, project.as_deref());
            popup(app, shared, label, x, y, entries);
        }
    }
}

/// An app menu shortcut (⌘1…⌘9, ⌘⇧[ / ⌘⇧], show/hide names), acting on
/// the dashboard window focused last.
pub(super) fn command(app: &AppHandle, shared: &Arc<Shared>, cmd: RailCommand) {
    if cmd == RailCommand::ToggleCollapsed {
        let compact = lock(&shared.core).saved.rail.collapsed;
        set_collapsed(app, shared, !compact);
        return;
    }
    let (label, project) = {
        let windows = lock(&shared.dash.windows);
        let focused = lock(&shared.dash.focused).clone();
        let label = focused
            .filter(|l| windows.contains_key(l))
            .or_else(|| windows.keys().next().cloned());
        let Some(label) = label else { return };
        let project = windows.get(&label).map(|w| w.project.clone());
        (label, project)
    };
    if cmd == RailCommand::NewTask {
        // #251: the focused project's dashboard, with its New task form.
        if let Some(key) = project {
            open_new_task(app, shared, &key, label);
        }
        return;
    }
    if let Some(key) = current_rail(shared).target(project.as_deref(), cmd) {
        open(app, shared, &key, None, Mode::In(label));
    }
}

/// Opens `key`'s New task form in window `label`: its dashboard URL (the
/// remote tunnel's for a remote daemon) with `?newTask=1`, the same deep-link
/// path the tray's "New Task…" item takes.
fn open_new_task(app: &AppHandle, shared: &Arc<Shared>, key: &str, label: String) {
    match target(shared, key, None) {
        Ok((t, _)) => open(
            app,
            shared,
            key,
            Some(crate::menu::new_task_link(&t.url)),
            Mode::In(label),
        ),
        Err(e) => error_dialog(app, &e),
    }
}

/// The tray's "New Task…" (and any in-app page of a project): opens or
/// focuses the window showing `key` and navigates its dashboard to `path`
/// in place through the embedded dashboard's navigate hook, the Go menu's
/// mechanism ([`crate::rail::page_plan`]); without such a window, the
/// dashboard opens at the page.
pub(super) fn open_page(app: &AppHandle, shared: &Arc<Shared>, key: &str, path: &'static str) {
    let t = match target(shared, key, None) {
        Ok((t, _)) => t,
        Err(e) => {
            error_dialog(app, &e);
            return;
        }
    };
    let (app2, shared2, key) = (app.clone(), Arc::clone(shared), key.to_string());
    let _ = app.run_on_main_thread(move || {
        let held = {
            let windows = lock(&shared2.dash.windows);
            windows
                .iter()
                .find(|(_, w)| w.project == key)
                .map(|(l, w)| (l.clone(), w.view.clone(), w.origin.clone(), w.token.clone()))
        };
        let plan = page_plan(
            held.as_ref().map(|(_, _, origin, token)| (origin, token)),
            &t,
            path,
        );
        if let (PagePlan::InPlace { script }, Some((label, view, _, _))) = (&plan, &held) {
            if let (Some(v), Some(w)) = (app2.get_webview(view), app2.get_window(label)) {
                let _ = v.eval(script);
                let _ = w.show();
                let _ = w.set_focus();
                let _ = v.set_focus();
                return;
            }
        }
        let url = match plan {
            PagePlan::Open { url } => url,
            PagePlan::InPlace { .. } => page_url(&t.url, path),
        };
        open(&app2, &shared2, &key, Some(url), Mode::Reuse);
    });
}

/// A Go menu item: shows `path` in the dashboard webview of the window
/// focused last, through the embedded dashboard's navigate hook.
pub(super) fn go(app: &AppHandle, shared: &Shared, path: &str) {
    let target = {
        let windows = lock(&shared.dash.windows);
        let focused = lock(&shared.dash.focused).clone();
        focused
            .filter(|l| windows.contains_key(l))
            .or_else(|| windows.keys().next().cloned())
            .and_then(|l| windows.get(&l).map(|w| (l, w.view.clone())))
    };
    let Some((label, view)) = target else {
        return;
    };
    if let Some(v) = app.get_webview(&view) {
        let _ = v.eval(crate::dashboard::navigate_script(path));
        let _ = v.set_focus();
    }
    if let Some(w) = app.get_window(&label) {
        let _ = w.show();
        let _ = w.set_focus();
    }
}

/// The Go menu: every dashboard page (crate::go_menu).
fn go_submenu(app: &AppHandle) -> tauri::Result<tauri::menu::Submenu<tauri::Wry>> {
    use tauri::menu::{MenuItem, PredefinedMenuItem, Submenu};
    let go = Submenu::new(app, "Go", true)?;
    for (i, (id, label)) in crate::go_menu::go_menu_items().into_iter().enumerate() {
        if crate::go_menu::separator_before(i, &id) {
            go.append(&PredefinedMenuItem::separator(app)?)?;
        }
        go.append(&MenuItem::with_id(app, id, label, true, None::<&str>)?)?;
    }
    Ok(go)
}

/// Rebuilds the Projects menu when the projects it lists changed (names,
/// order, pins, a project added or removed) since it was last built. The
/// rail is pushed after every refresh; the menu is rebuilt only when its
/// labels differ. Menus are built on the main thread.
fn refresh_menus(app: &AppHandle, shared: &Shared, view: &RailView) {
    let next = menu_projects(view);
    {
        let mut last = lock(&shared.dash.last_menu);
        if !menu_changed(last.as_deref(), &next) {
            return;
        }
        *last = Some(next);
    }
    let (app2, view) = (app.clone(), view.clone());
    // Linux and Windows: every dashboard window carries its own menu bar.
    #[cfg(not(target_os = "macos"))]
    let labels: Vec<String> = lock(&shared.dash.windows).keys().cloned().collect();
    let _ = app.run_on_main_thread(move || {
        #[cfg(target_os = "macos")]
        if let Err(e) = set_app_menu(&app2, &view) {
            eprintln!("itervox desktop: could not rebuild the Projects menu: {e}");
        }
        #[cfg(not(target_os = "macos"))]
        for label in labels {
            let Some(window) = app2.get_window(&label) else {
                continue;
            };
            let result = window_menu(&app2, &view).and_then(|m| window.set_menu(m));
            if let Err(e) = result {
                eprintln!("itervox desktop: could not rebuild the Projects menu: {e}");
            }
        }
    });
}

/// The Projects submenu: Next and Previous Project, one item per project
/// of `view` (crate::rail::menu_items), the names toggle and New Task….
/// `accel` maps an accelerator to the platform's; `sep_before_new_task`
/// adds the Mac's separator before New Task….
fn projects_submenu(
    app: &AppHandle,
    view: &RailView,
    accel: impl Fn(&str) -> String,
    sep_before_new_task: bool,
) -> tauri::Result<tauri::menu::Submenu<tauri::Wry>> {
    use tauri::menu::{MenuItem, PredefinedMenuItem, Submenu};
    let projects = Submenu::new(app, "Projects", true)?;
    let items = crate::rail::menu_items(view);
    for (i, (id, label, a)) in items.iter().enumerate() {
        if crate::rail::menu_separator_before(&items, i)
            || (sep_before_new_task && id == crate::rail::NEW_TASK_MENU_ID)
        {
            projects.append(&PredefinedMenuItem::separator(app)?)?;
        }
        let a = accel(a);
        projects.append(&MenuItem::with_id(
            app,
            id.as_str(),
            label.as_str(),
            true,
            Some(a.as_str()),
        )?)?;
    }
    Ok(projects)
}

/// The Projects menu of a dashboard window on Linux and Windows: the rail's
/// shortcuts, each accelerator shown and bound with Ctrl; then the Go menu.
#[cfg(not(target_os = "macos"))]
fn window_menu(app: &AppHandle, view: &RailView) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    use crate::platform::{accelerator, Os};
    use tauri::menu::Menu;
    let menu = Menu::new(app)?;
    let projects = projects_submenu(app, view, |a| accelerator(Os::current(), a), false)?;
    menu.append(&projects)?;
    menu.append(&go_submenu(app)?)?;
    Ok(menu)
}

/// Adds the Projects menu with the rail's shortcuts and the Go menu to the
/// macOS menu bar (shown while a window is open), keeping the default menus
/// (Edit's copy and paste for the dashboard's text fields). Rebuilt by
/// [`refresh_menus`] when the rail's projects change.
#[cfg(target_os = "macos")]
pub(super) fn install_app_menu(app: &AppHandle, shared: &Shared) -> tauri::Result<()> {
    let view = current_rail(shared);
    *lock(&shared.dash.last_menu) = Some(menu_projects(&view));
    set_app_menu(app, &view)
}

/// `app.set_menu` replaces the whole menu bar: the default menus, then
/// Projects and Go.
#[cfg(target_os = "macos")]
fn set_app_menu(app: &AppHandle, view: &RailView) -> tauri::Result<()> {
    use tauri::menu::Menu;
    let menu = Menu::default(app)?;
    let projects = projects_submenu(app, view, |a| a.to_string(), true)?;
    menu.append(&projects)?;
    menu.append(&go_submenu(app)?)?;
    app.set_menu(menu)?;
    Ok(())
}
