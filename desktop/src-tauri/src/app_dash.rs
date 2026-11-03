//! Tauri glue of the dashboard windows and their project rail (#244).
//!
//! A dashboard window holds two webviews side by side: the rail (the app's
//! own `rail.html`, see [`crate::rail`]) and the dashboard of the project
//! it shows (served by that project's daemon, with the token handed over by
//! the initialization script of [`crate::dashboard`]). Neither webview has
//! any IPC permission. Switching projects navigates the dashboard webview
//! in place when the origin and token stay the same, and otherwise replaces
//! only that webview — the window and its rail stay.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

use tauri::webview::WebviewBuilder;
use tauri::{
    AppHandle, LogicalPosition, LogicalSize, Manager, Rect, WebviewUrl, Window, WindowEvent,
};

use super::{error_dialog, lock, read_token_file, Shared};
use crate::dashboard::origin_of;
use crate::model::token_for;
use crate::rail::{
    build_rail, move_before, nudge, push_script, rail_action, rail_page_allowed, rail_width,
    switch_plan, DashTarget, RailAction, RailCommand, RailView, SwitchPlan, RAIL_PAGE,
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
    /// The rail last pushed to every window, to skip unchanged pushes.
    last_push: Mutex<Option<RailView>>,
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
    /// A new window (the rail's "Open in New Window").
    NewWindow,
    /// In place, in this window (a rail click or shortcut).
    In(String),
}

fn rail_label(window: &str) -> String {
    format!("{window}-rail")
}

/// The project's dashboard: URL (or `url`, a deep link), origin, token and
/// window title. Reads only what the last refresh holds.
fn target(shared: &Shared, key: &str, url: Option<String>) -> Result<(DashTarget, String), String> {
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
            let (x, w, h) = bounds(window, collapsed(shared));
            match window.add_child(
                dash_webview(&view, parsed, init_script),
                LogicalPosition::new(x, 0.0),
                LogicalSize::new(w, h),
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

fn collapsed(shared: &Shared) -> bool {
    lock(&shared.core).saved.rail.collapsed
}

/// The rail's width and the dashboard's x, width and height, in logical
/// pixels, for the window's current size.
fn bounds(window: &Window, collapsed: bool) -> (f64, f64, f64) {
    let scale = window.scale_factor().unwrap_or(1.0);
    let size = window
        .inner_size()
        .map(|s| s.to_logical::<f64>(scale))
        .unwrap_or(LogicalSize::new(1280.0, 860.0));
    let rail = rail_width(collapsed).min(size.width);
    (rail, (size.width - rail).max(0.0), size.height)
}

fn rect(x: f64, w: f64, h: f64) -> Rect {
    Rect {
        position: LogicalPosition::new(x, 0.0).into(),
        size: LogicalSize::new(w, h).into(),
    }
}

/// Lays out a window's rail and dashboard (main thread).
fn layout(app: &AppHandle, shared: &Shared, label: &str) {
    let Some(window) = app.get_window(label) else {
        return;
    };
    let view = lock(&shared.dash.windows)
        .get(label)
        .map(|w| w.view.clone());
    let (x, w, h) = bounds(&window, collapsed(shared));
    if let Some(r) = app.get_webview(&rail_label(label)) {
        let _ = r.set_bounds(rect(0.0, x, h));
    }
    if let Some(v) = view.and_then(|v| app.get_webview(&v)) {
        let _ = v.set_bounds(rect(x, w, h));
    }
}

/// A new dashboard window with its rail (main thread).
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
    let window = tauri::window::WindowBuilder::new(app, &label)
        .title(title)
        .inner_size(1280.0, 860.0)
        .min_inner_size(720.0, 480.0)
        .build()?;
    let (x, w, h) = bounds(&window, collapsed(shared));

    let (nav_app, nav_shared, nav_label) = (app.clone(), Arc::clone(shared), label.clone());
    let rail = WebviewBuilder::new(rail_label(&label), WebviewUrl::App(RAIL_PAGE.into()))
        .on_navigation(move |url| match rail_action(url.as_str()) {
            Some(action) => {
                let (app, shared, label) =
                    (nav_app.clone(), Arc::clone(&nav_shared), nav_label.clone());
                // Off the navigation callback: switching builds webviews.
                let _ = std::thread::Builder::new()
                    .name("itervox-rail".into())
                    .spawn(move || handle_action(&app, &shared, &label, action));
                false
            }
            None => rail_page_allowed(url.as_str()),
        });
    window.add_child(rail, LogicalPosition::new(0.0, 0.0), LogicalSize::new(x, h))?;

    let view = format!("{label}-view-{n}");
    let dash = window.add_child(
        dash_webview(&view, parsed, crate::rail::init_script_for(t)),
        LogicalPosition::new(x, 0.0),
        LogicalSize::new(w, h),
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

/// The rail as the last refresh left it.
fn current_rail(shared: &Shared) -> RailView {
    let remotes = shared.remote.views();
    let core = lock(&shared.core);
    build_rail(core.views(), &remotes, &core.saved.rail)
}

fn eval_rail(app: &AppHandle, label: &str, view: &RailView, active: &str) {
    if let Some(r) = app.get_webview(&rail_label(label)) {
        let _ = r.eval(push_script(view, Some(active)));
    }
}

/// Pushes the rail to every dashboard window when it changed since the
/// last push (or always, with `force`). Called after each refresh: the
/// rail shows what the refresh read and reads nothing itself.
pub(super) fn push_all(app: &AppHandle, shared: &Shared, force: bool) {
    let view = current_rail(shared);
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

/// Pushes the rail to one window (its page loaded, or it switched).
fn push_to(app: &AppHandle, shared: &Shared, label: &str) {
    let project = lock(&shared.dash.windows)
        .get(label)
        .map(|w| w.project.clone());
    if let Some(project) = project {
        eval_rail(app, label, &current_rail(shared), &project);
    }
}

/// Saves a rail change with the project list and shows it everywhere.
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

fn set_collapsed(app: &AppHandle, shared: &Arc<Shared>, value: bool) {
    save_prefs(app, shared, |_, p| {
        let changed = p.collapsed != value;
        p.collapsed = value;
        changed
    });
    let labels: Vec<String> = lock(&shared.dash.windows).keys().cloned().collect();
    let (app2, shared2) = (app.clone(), Arc::clone(shared));
    let _ = app.run_on_main_thread(move || {
        for l in labels {
            layout(&app2, &shared2, &l);
        }
    });
}

/// A rail page's request from window `label` (off the main thread).
fn handle_action(app: &AppHandle, shared: &Arc<Shared>, label: &str, action: RailAction) {
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
    }
}

/// An app menu shortcut (⌘1…⌘9, ⌘⇧[ / ⌘⇧], show/hide names), acting on
/// the dashboard window focused last.
pub(super) fn command(app: &AppHandle, shared: &Arc<Shared>, cmd: RailCommand) {
    if cmd == RailCommand::ToggleCollapsed {
        set_collapsed(app, shared, !collapsed(shared));
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
    if let Some(key) = current_rail(shared).target(project.as_deref(), cmd) {
        open(app, shared, &key, None, Mode::In(label));
    }
}

/// Adds the Projects menu with the rail's shortcuts to the macOS menu bar
/// (shown while a window is open), keeping the default menus (Edit's copy
/// and paste for the dashboard's text fields).
#[cfg(target_os = "macos")]
pub(super) fn install_app_menu(app: &AppHandle) -> tauri::Result<()> {
    use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu};
    let menu = Menu::default(app)?;
    let projects = Submenu::new(app, "Projects", true)?;
    for (i, (id, label, accel)) in crate::rail::menu_items().into_iter().enumerate() {
        // Separators after Previous and after Project 9.
        if i == 2 || i == 2 + crate::rail::MAX_SHORTCUTS {
            projects.append(&PredefinedMenuItem::separator(app)?)?;
        }
        projects.append(&MenuItem::with_id(app, id, label, true, Some(accel))?)?;
    }
    menu.append(&projects)?;
    app.set_menu(menu)?;
    Ok(())
}
