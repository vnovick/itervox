//! Tauri glue: the tray icon and menu, the refresh thread, start/stop
//! threads, notifications and dashboard windows. The decisions live in the
//! plain modules; this file only wires them to the window system.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant};

use tauri::menu::{IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{TrayIcon, TrayIconBuilder};
use tauri::{AppHandle, Manager, RunEvent, Runtime, WebviewUrl, WebviewWindowBuilder, Wry};
use tauri_plugin_dialog::{DialogExt, MessageDialogKind};
use tauri_plugin_notification::NotificationExt;

use crate::app_remote::RemoteShared;
use crate::app_update;
use crate::client;
use crate::dashboard::{origin_of, token_init_script, window_label};
use crate::menu::{append_app_section, build_menu, MenuAction, MenuEntry, MenuSpec};
use crate::model::{token_for, Core, NotificationTargets, PendingOp, REFRESH_INTERVAL};
use crate::projects::{project_key, validate_workflow_file, SavedProjects, PROJECTS_FILE};
use crate::remote::is_remote_key;
use crate::sessions::{page_action, push_script, read_all, session_link, PageAction};
use crate::supervisor::{self, last_line, output_log_name, record_for};
use crate::token::{generate_token, platform_store, TokenStore};

const TRAY_ID: &str = "itervox-tray";
/// The Sessions window (#178).
const SESSIONS_LABEL: &str = "sessions";

/// State shared by the tray, the refresh thread and the action threads.
struct Shared {
    core: Mutex<Core>,
    tokens: Box<dyn TokenStore>,
    sidecar: Result<PathBuf, String>,
    projects_file: PathBuf,
    output_dir: PathBuf,
    menu_actions: Mutex<HashMap<String, MenuAction>>,
    targets: Mutex<NotificationTargets>,
    /// Per project: the label of its dashboard window and the token and
    /// origin that window was created with.
    windows: Mutex<HashMap<String, WindowEntry>>,
    window_generation: std::sync::atomic::AtomicU32,
    status_error: Mutex<Option<String>>,
    wake: Mutex<Sender<()>>,
    /// Remote connections and their tunnels (#156).
    remote: Arc<RemoteShared>,
}

/// A dashboard window: label, token, origin.
type WindowEntry = (String, Option<String>, Option<String>);

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// ITERVOX_DESKTOP_DEBUG=1 prints every refresh to stderr (run the app
/// binary from a terminal: Itervox.app/Contents/MacOS/itervox-desktop).
fn debug_enabled() -> bool {
    std::env::var_os("ITERVOX_DESKTOP_DEBUG").is_some_and(|v| v == "1")
}

fn read_token_file(path: &str) -> Option<String> {
    std::fs::read_to_string(path).ok()
}

/// Entry point used by main.rs.
pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(app_update::plugin())
        .setup(|app| {
            #[cfg(target_os = "macos")]
            app.set_activation_policy(tauri::ActivationPolicy::Accessory);
            setup(app.handle())?;
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("itervox desktop: could not build the app");
    app.run(|app, event| match event {
        // Closing the last dashboard window must not quit a menu-bar app;
        // only the Quit item (an explicit exit code) does.
        RunEvent::ExitRequested { api, code, .. } if code.is_none() => api.prevent_exit(),
        RunEvent::WindowEvent {
            label,
            event: tauri::WindowEvent::Destroyed,
            ..
        } => {
            if let Some(shared) = app.try_state::<Arc<Shared>>() {
                lock(&shared.windows).retain(|_, (l, _, _)| *l != label);
            }
            #[cfg(target_os = "macos")]
            if app.webview_windows().is_empty() {
                let _ = app.set_activation_policy(tauri::ActivationPolicy::Accessory);
            }
        }
        _ => {}
    });
}

fn setup(app: &AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let config_dir = app.path().app_config_dir()?;
    let log_dir = app.path().app_log_dir()?;
    let projects_file = config_dir.join(PROJECTS_FILE);
    let saved = match SavedProjects::load(&projects_file) {
        Ok(s) => s,
        Err(e) => {
            // Keep the unreadable file for the user instead of overwriting
            // it with the next change to the list.
            let backup = projects_file.with_extension("json.unreadable");
            let _ = std::fs::rename(&projects_file, &backup);
            error_dialog(
                app,
                &format!(
                    "The saved project list could not be read ({e}). It was moved to {}.",
                    backup.display()
                ),
            );
            SavedProjects::default()
        }
    };
    let exe = std::env::current_exe().ok();
    let sidecar = supervisor::resolve_sidecar(&supervisor::sidecar_candidates(
        exe.as_deref(),
        std::env::var_os(supervisor::DAEMON_OVERRIDE_ENV),
    ));
    let (wake_tx, wake_rx) = mpsc::channel();
    let since = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    let (remote, tunnel_rx, remote_err) = RemoteShared::load(&config_dir, since);
    if let Some(e) = remote_err {
        error_dialog(app, &e);
    }
    let shared = Arc::new(Shared {
        core: Mutex::new(Core::with_since(saved, since)),
        tokens: platform_store(),
        sidecar,
        projects_file,
        output_dir: log_dir.join("daemons"),
        menu_actions: Mutex::default(),
        targets: Mutex::default(),
        windows: Mutex::default(),
        window_generation: std::sync::atomic::AtomicU32::new(0),
        status_error: Mutex::new(None),
        wake: Mutex::new(wake_tx),
        remote,
    });
    app.manage(Arc::clone(&shared));
    // A tunnel that comes up or goes down is shown, and read, at once.
    let tunnel_shared = Arc::clone(&shared);
    shared
        .remote
        .spawn_tunnel_thread(log_dir.join("tunnels"), tunnel_rx, move || {
            wake(&tunnel_shared)
        })?;
    let (change_app, change_shared) = (app.clone(), Arc::clone(&shared));
    app_update::setup(
        app,
        Arc::new(move || update_menu(&change_app, &change_shared)),
    )?;

    let icon = tauri::image::Image::from_bytes(include_bytes!("../icons/tray.png"))?;
    let tray_shared = Arc::clone(&shared);
    TrayIconBuilder::with_id(TRAY_ID)
        .icon(icon)
        .icon_as_template(true)
        .tooltip("Itervox")
        .show_menu_on_left_click(true)
        .menu(&Menu::new(app)?)
        .on_menu_event(move |app, event| {
            let action = lock(&tray_shared.menu_actions)
                .get(event.id().as_ref())
                .cloned();
            if let Some(action) = action {
                handle_action(app, &tray_shared, action);
            }
        })
        .build(app)?;
    update_menu(app, &shared);

    let notify_shared = Arc::clone(&shared);
    let notify_app = app.clone();
    app.notification().on_action(move |performed| {
        let Some(n) = performed.notification() else {
            return;
        };
        let target = lock(&notify_shared.targets).get(n.id()).cloned();
        if let Some((project, url)) = target {
            open_dashboard(&notify_app, &notify_shared, &project, Some(url));
        }
    })?;

    let poll_app = app.clone();
    std::thread::Builder::new()
        .name("itervox-refresh".into())
        .spawn(move || refresh_loop(&poll_app, &shared, &wake_rx))?;
    Ok(())
}

fn refresh_loop(app: &AppHandle, shared: &Arc<Shared>, wake: &Receiver<()>) {
    loop {
        refresh(app, shared);
        match wake.recv_timeout(REFRESH_INTERVAL) {
            Ok(()) | Err(RecvTimeoutError::Timeout) => {
                // Coalesce a burst of wake-ups into one refresh.
                while wake.try_recv().is_ok() {}
            }
            Err(RecvTimeoutError::Disconnected) => return,
        }
    }
}

fn wake(shared: &Shared) {
    let _ = lock(&shared.wake).send(());
}

fn list_daemons(shared: &Shared) -> Result<Vec<crate::status::DaemonRecord>, String> {
    match &shared.sidecar {
        Ok(p) => supervisor::list_daemons(p),
        Err(e) => Err(e.clone()),
    }
}

fn refresh(app: &AppHandle, shared: &Arc<Shared>) {
    let records = list_daemons(shared);
    let jobs = {
        let core = lock(&shared.core);
        core.fetch_jobs(
            records.as_deref().unwrap_or(&[]),
            shared.tokens.as_ref(),
            &read_token_file,
        )
    };
    // Read every daemon in parallel; each read is bounded by its timeout.
    let states: HashMap<_, _> = std::thread::scope(|s| {
        let handles: Vec<_> = jobs
            .iter()
            .map(|j| {
                s.spawn(move || {
                    (
                        j.key.clone(),
                        client::fetch_state(&j.url, j.token.as_deref()),
                    )
                })
            })
            .collect();
        handles.into_iter().filter_map(|h| h.join().ok()).collect()
    });
    let mut outcome = lock(&shared.core).apply(records, states, Instant::now());
    let (remote_alerts, remote_changed) = shared.remote.refresh();
    outcome.alerts.extend(remote_alerts);
    let status_changed = {
        let mut cur = lock(&shared.status_error);
        let changed = *cur != outcome.status_error;
        cur.clone_from(&outcome.status_error);
        changed
    };
    if debug_enabled() {
        let core = lock(&shared.core);
        for v in core.views() {
            eprintln!(
                "itervox desktop: {} [{}] {}",
                v.name,
                v.key,
                v.status_line()
            );
        }
        if let Some(e) = &outcome.status_error {
            eprintln!("itervox desktop: status error: {e}");
        }
        for a in &outcome.alerts {
            eprintln!("itervox desktop: alert {} -> {}", a.title(), a.url);
        }
    }
    if !outcome.diff.is_empty() || !outcome.alerts.is_empty() || status_changed || remote_changed {
        update_menu(app, shared);
    }
    push_sessions(app, shared);
    for alert in &outcome.alerts {
        let id = lock(&shared.targets).register(&alert.project, &alert.url);
        let _ = app
            .notification()
            .builder()
            .id(id)
            .title(alert.title())
            .body(alert.body())
            .show();
    }
}

/// Rebuilds the tray menu from the model, on the main thread.
fn update_menu(app: &AppHandle, shared: &Arc<Shared>) {
    let mut spec = {
        let core = lock(&shared.core);
        let status_error = lock(&shared.status_error).clone();
        let remotes = shared.remote.views();
        build_menu(
            core.views(),
            &remotes,
            core.recent_alerts(),
            status_error.as_deref(),
        )
    };
    append_app_section(
        &mut spec,
        app_update::menu_items(app, shared.sidecar.as_deref().ok()),
    );
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let _ = app.run_on_main_thread(move || {
        if let Err(e) = install_menu(&app2, &shared2, spec) {
            eprintln!("itervox desktop: could not update the menu: {e}");
        }
    });
}

fn install_menu(app: &AppHandle, shared: &Shared, spec: MenuSpec) -> tauri::Result<()> {
    let menu = Menu::new(app)?;
    for e in &spec.entries {
        append_entry(app, &menu, e)?;
    }
    if let Some(tray) = app.tray_by_id(TRAY_ID) {
        set_tray(&tray, menu, &spec)?;
    }
    *lock(&shared.menu_actions) = spec.actions;
    Ok(())
}

fn set_tray(tray: &TrayIcon<Wry>, menu: Menu<Wry>, spec: &MenuSpec) -> tauri::Result<()> {
    tray.set_menu(Some(menu))?;
    tray.set_tooltip(Some(&spec.tooltip))?;
    #[cfg(target_os = "macos")]
    tray.set_title(if spec.title.is_empty() {
        None
    } else {
        Some(spec.title.as_str())
    })?;
    Ok(())
}

trait Appendable<R: Runtime> {
    fn add(&self, item: &dyn IsMenuItem<R>) -> tauri::Result<()>;
}
impl<R: Runtime> Appendable<R> for Menu<R> {
    fn add(&self, item: &dyn IsMenuItem<R>) -> tauri::Result<()> {
        self.append(item)
    }
}
impl<R: Runtime> Appendable<R> for Submenu<R> {
    fn add(&self, item: &dyn IsMenuItem<R>) -> tauri::Result<()> {
        self.append(item)
    }
}

fn append_entry<R: Runtime>(
    app: &AppHandle<R>,
    parent: &dyn Appendable<R>,
    entry: &MenuEntry,
) -> tauri::Result<()> {
    match entry {
        MenuEntry::Separator => parent.add(&PredefinedMenuItem::separator(app)?),
        MenuEntry::Item { id, label, enabled } => parent.add(&MenuItem::with_id(
            app,
            id.as_str(),
            label,
            *enabled,
            None::<&str>,
        )?),
        MenuEntry::Submenu { label, items } => {
            let sub = Submenu::new(app, label, true)?;
            for i in items {
                append_entry(app, &sub, i)?;
            }
            parent.add(&sub)
        }
    }
}

fn handle_action(app: &AppHandle, shared: &Arc<Shared>, action: MenuAction) {
    match action {
        MenuAction::OpenDashboard(key) => open_dashboard(app, shared, &key, None),
        MenuAction::OpenUrl { project, url } => open_dashboard(app, shared, &project, Some(url)),
        MenuAction::Start(key) => spawn_op(app, shared, key, PendingOp::Starting),
        MenuAction::Stop(key) => spawn_op(app, shared, key, PendingOp::Stopping),
        MenuAction::Restart(key) => spawn_op(app, shared, key, PendingOp::Restarting),
        MenuAction::Remove(key) => {
            let res = {
                let mut core = lock(&shared.core);
                core.saved.remove(&key);
                let r = core.saved.save(&shared.projects_file);
                // A stopped project's token is of no further use.
                if !core.view(&key).is_some_and(|v| v.running()) {
                    let _ = shared.tokens.delete(&key);
                }
                core.rebuild();
                r
            };
            if let Err(e) = res {
                error_dialog(app, &format!("Could not save the project list: {e}"));
            }
            update_menu(app, shared);
        }
        MenuAction::RevealLog(key) => {
            let log = shared.output_dir.join(output_log_name(&key));
            if !log.exists() {
                error_dialog(
                    app,
                    "This app has not started that daemon yet, so there is no output log. The daemon's own logs are under ~/.itervox/logs.",
                );
                return;
            }
            open_path(&log);
        }
        MenuAction::AddProject => add_project(app, shared),
        MenuAction::AddRemote => remote_op(app, shared, |r| r.add_flow()),
        MenuAction::RemoteToken(key) => {
            remote_op(app, shared, move |r| r.token_flow(&key).map(|()| None))
        }
        MenuAction::Rediscover(id) => {
            remote_op(app, shared, move |r| r.rediscover_flow(&id).map(|()| None))
        }
        MenuAction::Reconnect(id) => shared.remote.reconnect(&id),
        MenuAction::RemoveRemote(id) => {
            if let Err(e) = shared.remote.remove(&id) {
                error_dialog(app, &format!("Could not save the remote list: {e}"));
            }
            wake(shared);
        }
        MenuAction::OpenSessions => open_sessions(app, shared),
        MenuAction::RefreshNow => wake(shared),
        MenuAction::CheckForUpdates => app_update::check_now(app),
        MenuAction::InstallUpdate => {
            let (app2, shared2) = (app.clone(), Arc::clone(shared));
            app_update::install(app, Arc::new(move || update_menu(&app2, &shared2)));
        }
        MenuAction::InstallCli => {
            app_update::install_cli(app, shared.sidecar.as_deref().ok());
            update_menu(app, shared);
        }
        MenuAction::Quit => {
            // Tunnels belong to the app (unlike the daemons): stop them.
            shared.remote.shutdown();
            app.exit(0)
        }
    }
}

/// Runs a remote flow (prompts, ssh/gcloud) off the main thread; an error
/// shows in a dialog, a message in a notification.
fn remote_op(
    app: &AppHandle,
    shared: &Arc<Shared>,
    op: impl FnOnce(&RemoteShared) -> Result<Option<String>, String> + Send + 'static,
) {
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let _ = std::thread::Builder::new()
        .name("itervox-remote".into())
        .spawn(move || {
            match op(&shared2.remote) {
                Ok(Some(msg)) => {
                    let _ = app2
                        .notification()
                        .builder()
                        .title("Itervox")
                        .body(msg)
                        .show();
                }
                Ok(None) => {}
                Err(e) => error_dialog(&app2, &e),
            }
            update_menu(&app2, &shared2);
            wake(&shared2);
        });
}

fn open_path(path: &Path) {
    #[cfg(target_os = "macos")]
    let opener = "/usr/bin/open";
    #[cfg(not(target_os = "macos"))]
    let opener = "xdg-open";
    let _ = std::process::Command::new(opener).arg(path).spawn();
}

fn error_dialog(app: &AppHandle, msg: &str) {
    app.dialog()
        .message(msg)
        .title("Itervox")
        .kind(MessageDialogKind::Error)
        .show(|_| {});
}

fn add_project(app: &AppHandle, shared: &Arc<Shared>) {
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    app.dialog()
        .file()
        .set_title("Choose a project's WORKFLOW.md")
        .add_filter("WORKFLOW.md", &["md"])
        .pick_file(move |picked| {
            let Some(path) = picked.and_then(|p| p.into_path().ok()) else {
                return;
            };
            if let Err(e) = validate_workflow_file(&path) {
                error_dialog(&app2, &e);
                return;
            }
            let key = project_key(&path, Path::new("/"));
            let res = {
                let mut core = lock(&shared2.core);
                core.saved.add(&key);
                let r = core.saved.save(&shared2.projects_file);
                core.rebuild();
                r
            };
            if let Err(e) = res {
                error_dialog(&app2, &format!("Could not save the project list: {e}"));
            }
            update_menu(&app2, &shared2);
            wake(&shared2);
        });
}

/// Runs a start/stop/restart on its own thread; the menu shows it as
/// pending at once and the refresh loop settles it.
fn spawn_op(app: &AppHandle, shared: &Arc<Shared>, key: String, op: PendingOp) {
    let saved = {
        let mut core = lock(&shared.core);
        // A project the user starts or stops from the app stays listed
        // while stopped, so it can be started again.
        let added = core.saved.add(&key);
        core.begin(&key, op, Instant::now());
        if added {
            core.saved.save(&shared.projects_file)
        } else {
            Ok(())
        }
    };
    if let Err(e) = saved {
        error_dialog(app, &format!("Could not save the project list: {e}"));
    }
    update_menu(app, shared);
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let _ = std::thread::Builder::new()
        .name("itervox-op".into())
        .spawn(move || {
            let res = match op {
                PendingOp::Starting => start_daemon(&app2, &shared2, &key, op),
                PendingOp::Stopping => stop_daemon(&shared2, &key),
                PendingOp::Restarting => stop_daemon(&shared2, &key)
                    .and_then(|()| start_daemon(&app2, &shared2, &key, op)),
            };
            if let Err(e) = res {
                lock(&shared2.core).fail(&key, e);
                update_menu(&app2, &shared2);
            }
            wake(&shared2);
        });
}

fn start_daemon(
    app: &AppHandle,
    shared: &Arc<Shared>,
    key: &str,
    op: PendingOp,
) -> Result<(), String> {
    let sidecar = shared.sidecar.clone()?;
    let workflow = Path::new(key);
    validate_workflow_file(workflow)?;
    if record_for(&list_daemons(shared)?, key).is_some() {
        return Err("already running".into());
    }
    let token = generate_token()?;
    shared.tokens.set(key, &token)?;
    let log = shared.output_dir.join(output_log_name(key));
    let spec = supervisor::start_spec(&sidecar, workflow, &token, &log)?;
    let mut child = spec
        .command()
        .and_then(|mut c| c.spawn())
        .map_err(|e| format!("could not start {}: {e}", sidecar.display()))?;
    // Reap the daemon if it exits while the app runs. An exit before the
    // registry listed it is a failed start: show the last output line.
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let key2 = key.to_string();
    let _ = std::thread::Builder::new()
        .name("itervox-reap".into())
        .spawn(move || {
            let Ok(status) = child.wait() else { return };
            let still_pending = lock(&shared2.core)
                .view(&key2)
                .is_some_and(|v| v.pending == Some(op) && !v.running());
            if still_pending {
                let out = std::fs::read_to_string(&log).unwrap_or_default();
                let line = last_line(&out);
                let msg = if line.is_empty() {
                    format!("exited ({status})")
                } else {
                    format!("exited: {line}")
                };
                lock(&shared2.core).fail(&key2, msg);
                update_menu(&app2, &shared2);
            }
            wake(&shared2);
        });
    Ok(())
}

fn stop_daemon(shared: &Shared, key: &str) -> Result<(), String> {
    let records = list_daemons(shared)?;
    let rec = record_for(&records, key).ok_or("not running")?;
    let pid = rec.pid;
    supervisor::send_sigterm(pid)?;
    if !supervisor::wait_for_exit(
        || supervisor::process_alive(pid),
        supervisor::STOP_TIMEOUT,
        Duration::from_millis(200),
    ) {
        return Err("still draining agent turns — choose Stop again to stop at once".into());
    }
    Ok(())
}

/// Opens (or focuses and navigates) the dashboard window of a project.
fn open_dashboard(app: &AppHandle, shared: &Arc<Shared>, key: &str, url: Option<String>) {
    if is_remote_key(key) {
        // Through the daemon's tunnel, with the same token handoff.
        match shared.remote.dashboard(key, url.as_deref()) {
            Ok((t, title)) => {
                if let Ok(parsed) = tauri::Url::parse(&t.url) {
                    let origin = Some(t.origin);
                    open_window(app, shared, key, parsed, origin, t.token, title);
                }
            }
            Err(e) => error_dialog(app, &e),
        }
        return;
    }
    let rec = lock(&shared.core)
        .last_records()
        .iter()
        .find(|r| r.workflow == key)
        .cloned();
    let Some(rec) = rec else {
        error_dialog(app, "That project's daemon is not running.");
        return;
    };
    let base = rec.base_url();
    let target = url.unwrap_or_else(|| base.clone());
    let Ok(parsed) = tauri::Url::parse(&target) else {
        return;
    };
    let token = token_for(&rec, shared.tokens.as_ref(), &read_token_file).flatten();
    let title = lock(&shared.core)
        .view(key)
        .map(|v| format!("{} — Itervox", v.name))
        .unwrap_or_else(|| "Itervox".into());
    open_window(app, shared, key, parsed, origin_of(&base), token, title);
}

/// Shows a dashboard window for `key` at `url`, handing it `token` on
/// `origin` through the initialization script.
fn open_window(
    app: &AppHandle,
    shared: &Arc<Shared>,
    key: &str,
    parsed: tauri::Url,
    origin: Option<String>,
    token: Option<String>,
    title: String,
) {
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let key = key.to_string();
    let _ = app.run_on_main_thread(move || {
        let mut windows = lock(&shared2.windows);
        if let Some((label, held, held_origin)) = windows.get(&key) {
            if let Some(w) = app2.get_webview_window(label) {
                if *held == token && *held_origin == origin {
                    let _ = w.navigate(parsed);
                    let _ = w.show();
                    let _ = w.set_focus();
                    return;
                }
                // The daemon restarted with a new token, or its tunnel
                // moved to another port: the window's initialization script
                // is stale, so replace the window.
                let _ = w.destroy();
            }
        }
        let generation = shared2
            .window_generation
            .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let label = format!("{}-{generation}", window_label(&key));
        #[cfg(target_os = "macos")]
        let _ = app2.set_activation_policy(tauri::ActivationPolicy::Regular);
        let mut b = WebviewWindowBuilder::new(&app2, &label, WebviewUrl::External(parsed))
            .title(&title)
            .inner_size(1280.0, 860.0)
            .min_inner_size(720.0, 480.0);
        if let (Some(t), Some(origin)) = (&token, origin.as_deref()) {
            b = b.initialization_script(token_init_script(origin, t));
        }
        match b.build() {
            Ok(w) => {
                windows.insert(key, (label, token, origin));
                let _ = w.set_focus();
            }
            Err(e) => eprintln!("itervox desktop: could not open the dashboard: {e}"),
        }
    });
}

/// Reads every daemon's sessions and hands them to the Sessions window,
/// while it is open (#178). Runs on the refresh thread.
fn push_sessions(app: &AppHandle, shared: &Arc<Shared>) {
    let Some(window) = app.get_webview_window(SESSIONS_LABEL) else {
        return;
    };
    let mut jobs = {
        let core = lock(&shared.core);
        core.session_jobs(
            core.last_records(),
            shared.tokens.as_ref(),
            &read_token_file,
        )
    };
    jobs.extend(shared.remote.session_jobs());
    let view = read_all(&jobs, client::fetch_sessions);
    let _ = window.eval(push_script(&view));
}

/// Shows the Sessions window, creating it on first use. Its page asks for
/// data and opens sessions by navigating to app-internal paths, which the
/// navigation handler intercepts (sessions.rs `page_action`).
fn open_sessions(app: &AppHandle, shared: &Arc<Shared>) {
    let app2 = app.clone();
    let shared2 = Arc::clone(shared);
    let _ = app.run_on_main_thread(move || {
        if let Some(w) = app2.get_webview_window(SESSIONS_LABEL) {
            let _ = w.show();
            let _ = w.set_focus();
            wake(&shared2);
            return;
        }
        #[cfg(target_os = "macos")]
        let _ = app2.set_activation_policy(tauri::ActivationPolicy::Regular);
        let nav_app = app2.clone();
        let nav_shared = Arc::clone(&shared2);
        let built = WebviewWindowBuilder::new(
            &app2,
            SESSIONS_LABEL,
            WebviewUrl::App("sessions.html".into()),
        )
        .title("Sessions — Itervox")
        .inner_size(560.0, 680.0)
        .min_inner_size(360.0, 320.0)
        .on_navigation(move |url| match page_action(url.as_str()) {
            Some(action) => {
                handle_page_action(&nav_app, &nav_shared, action);
                false
            }
            None => true,
        })
        .build();
        match built {
            Ok(w) => {
                let _ = w.set_focus();
                wake(&shared2);
            }
            Err(e) => eprintln!("itervox desktop: could not open the Sessions window: {e}"),
        }
    });
}

fn handle_page_action(app: &AppHandle, shared: &Arc<Shared>, action: PageAction) {
    match action {
        PageAction::Ready => wake(shared),
        PageAction::Open { daemon, session } => {
            if is_remote_key(&daemon) {
                // Rebased onto the daemon's tunnel by RemoteShared::dashboard.
                let link = session_link("http://127.0.0.1/", &session);
                open_dashboard(app, shared, &daemon, Some(link));
                return;
            }
            let base = lock(&shared.core)
                .last_records()
                .iter()
                .find(|r| r.workflow == daemon)
                .map(|r| r.base_url());
            match base {
                Some(base) => {
                    open_dashboard(app, shared, &daemon, Some(session_link(&base, &session)))
                }
                None => error_dialog(app, "That project's daemon is not running."),
            }
        }
    }
}
