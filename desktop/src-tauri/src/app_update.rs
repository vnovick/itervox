//! Tauri glue for automatic updates and the command-line tool (#130). The
//! decisions live in `update.rs` and `cli_tool.rs`; this file runs the check
//! thread, calls `tauri-plugin-updater`, and shows the results.

use std::path::Path;
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Instant;

use tauri::plugin::TauriPlugin;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_dialog::{DialogExt, MessageDialogKind};
use tauri_plugin_updater::UpdaterExt;

use crate::cli_tool;
use crate::menu::MenuAction;
use crate::update::{self, Manifest, Offer, Schedule, UpdateAction, UpdateState};

/// Called whenever the menu should be rebuilt.
pub type OnChange = Arc<dyn Fn() + Send + Sync>;

pub struct Updates {
    state: Mutex<UpdateState>,
    pending: Mutex<Option<tauri_plugin_updater::Update>>,
    wake: Mutex<Option<Sender<()>>>,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// The updater plugin, comparing versions with `update::is_update`.
pub fn plugin<R: Runtime>() -> TauriPlugin<R, tauri_plugin_updater::Config> {
    tauri_plugin_updater::Builder::new()
        .default_version_comparator(|current, release| {
            update::is_update(&current.to_string(), &release.version.to_string())
        })
        .build()
}

fn pubkey(app: &AppHandle) -> Option<String> {
    app.config()
        .plugins
        .0
        .get("updater")?
        .get("pubkey")?
        .as_str()
        .map(str::to_owned)
}

/// Starts the check thread: shortly after launch, then every
/// `update::CHECK_INTERVAL`, and whenever the user chooses Check.
pub fn setup(app: &AppHandle, on_change: OnChange) -> std::io::Result<()> {
    let state = update::initial_state(pubkey(app).as_deref());
    let enabled = !matches!(state, UpdateState::Disabled(_));
    let (tx, rx) = mpsc::channel();
    let updates = Arc::new(Updates {
        state: Mutex::new(state),
        pending: Mutex::default(),
        wake: Mutex::new(enabled.then_some(tx)),
    });
    app.manage(Arc::clone(&updates));
    if !enabled {
        return Ok(());
    }
    let app = app.clone();
    std::thread::Builder::new()
        .name("itervox-update".into())
        .spawn(move || {
            let mut schedule = Schedule::new(
                Instant::now(),
                update::FIRST_CHECK_DELAY,
                update::CHECK_INTERVAL,
            );
            loop {
                match rx.recv_timeout(schedule.wait(Instant::now())) {
                    Ok(()) | Err(RecvTimeoutError::Timeout) => {}
                    Err(RecvTimeoutError::Disconnected) => return,
                }
                schedule.checked(Instant::now());
                check(&app, &updates, &on_change);
            }
        })?;
    Ok(())
}

fn check(app: &AppHandle, updates: &Updates, on_change: &OnChange) {
    if !lock(&updates.state).begin_check() {
        return;
    }
    on_change();
    let result = tauri::async_runtime::block_on(async { app.updater()?.check().await });
    let result = match result {
        Ok(Some(u)) => match Manifest::parse(&u.raw_json.to_string()) {
            Ok(_) => {
                let offer = Offer {
                    version: u.version.clone(),
                    current: u.current_version.clone(),
                };
                *lock(&updates.pending) = Some(u);
                Ok(Some(offer))
            }
            Err(e) => Err(e),
        },
        Ok(None) => {
            *lock(&updates.pending) = None;
            Ok(None)
        }
        Err(e) => Err(e.to_string()),
    };
    lock(&updates.state).finish_check(result);
    on_change();
}

fn updates(app: &AppHandle) -> Option<Arc<Updates>> {
    app.try_state::<Arc<Updates>>().map(|s| Arc::clone(&s))
}

/// Check for Updates…: wakes the check thread.
pub fn check_now(app: &AppHandle) {
    if let Some(u) = updates(app) {
        if let Some(tx) = lock(&u.wake).as_ref() {
            let _ = tx.send(());
        }
    }
}

/// Install and Restart: downloads, verifies and installs the update, then
/// relaunches. Daemons the app started are detached and keep running.
pub fn install(app: &AppHandle, on_change: OnChange) {
    let Some(updates) = updates(app) else { return };
    if lock(&updates.state).begin_install().is_none() {
        return;
    }
    on_change();
    let pending = lock(&updates.pending).clone();
    let app = app.clone();
    let _ = std::thread::Builder::new()
        .name("itervox-install".into())
        .spawn(move || {
            let res = match pending {
                Some(u) => tauri::async_runtime::block_on(u.download_and_install(|_, _| {}, || {}))
                    .map_err(|e| e.to_string()),
                None => Err("the update is no longer available; check again".into()),
            };
            match res {
                Ok(()) => app.restart(),
                Err(e) => {
                    lock(&updates.state).install_failed(&e);
                    on_change();
                }
            }
        });
}

/// The menu items of the app section: updates, then the command-line tool.
pub fn menu_items(
    app: &AppHandle,
    sidecar: Option<&Path>,
) -> Vec<(String, bool, Option<MenuAction>)> {
    let mut items: Vec<_> = updates(app)
        .map(|u| lock(&u.state).menu_items())
        .unwrap_or_default()
        .into_iter()
        .map(|(label, enabled, action)| {
            let action = action.map(|a| match a {
                UpdateAction::Check => MenuAction::CheckForUpdates,
                UpdateAction::Install => MenuAction::InstallUpdate,
            });
            (label, enabled, action)
        })
        .collect();
    if let Some(sidecar) = sidecar {
        let dirs = cli_tool::link_dirs(app.path().home_dir().ok().as_deref());
        let (label, enabled) = cli_tool::menu_item(&cli_tool::status(&dirs, sidecar));
        items.push((label, enabled, enabled.then_some(MenuAction::InstallCli)));
    }
    items
}

/// Install (or Repair) Command-Line Tool….
pub fn install_cli(app: &AppHandle, sidecar: Option<&Path>) {
    let home = app.path().home_dir().ok();
    let dirs = cli_tool::link_dirs(home.as_deref());
    let (msg, kind) = match sidecar.ok_or("the bundled itervox daemon was not found".to_string()) {
        Err(e) => (e, MessageDialogKind::Error),
        Ok(sidecar) => match cli_tool::install(&dirs, sidecar) {
            Err(e) => (e, MessageDialogKind::Error),
            Ok(link) => {
                let dir = link.parent().unwrap_or(Path::new("/"));
                let mut msg = format!(
                    "Installed {} → {}. It updates together with the app.",
                    link.display(),
                    sidecar.display()
                );
                if !cli_tool::on_path(dir, &std::env::var("PATH").unwrap_or_default())
                    && dir != Path::new("/usr/local/bin")
                {
                    msg.push_str(&format!(
                        " If a new terminal does not find `itervox`, add {} to your PATH.",
                        dir.display()
                    ));
                }
                (msg, MessageDialogKind::Info)
            }
        },
    };
    app.dialog()
        .message(msg)
        .title("Itervox")
        .kind(kind)
        .show(|_| {});
}
