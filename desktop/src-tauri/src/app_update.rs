//! Tauri glue for automatic updates and the command-line tool (#130), and
//! Start at Login (#260). The decisions live in `update.rs`, `cli_tool.rs`
//! and `autostart.rs`; this file runs the check thread, calls
//! `tauri-plugin-updater` (macOS) or reads the latest release (Linux,
//! Windows), and shows the results.

use std::path::Path;
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Instant;

use tauri::plugin::TauriPlugin;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_dialog::{DialogExt, MessageDialogKind};
use tauri_plugin_updater::UpdaterExt;

use crate::autostart;
use crate::cli_tool::{self, CliPlan};
use crate::menu::MenuAction;
use crate::platform::Os;
use crate::update::{self, Manifest, Mode, Offer, Schedule, UpdateAction, UpdateState};

/// Called whenever the menu should be rebuilt.
pub type OnChange = Arc<dyn Fn() + Send + Sync>;

pub struct Updates {
    mode: Mode,
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
    let mode = update::mode_for(Os::current());
    let state = update::initial_state_for(mode, pubkey(app).as_deref());
    let enabled = !matches!(state, UpdateState::Disabled(_));
    let (tx, rx) = mpsc::channel();
    let updates = Arc::new(Updates {
        mode,
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
    if updates.mode == Mode::Notify {
        let result = latest_release().map(|version| {
            let current = app.package_info().version.to_string();
            update::is_update(&current, &version).then_some(Offer { version, current })
        });
        lock(&updates.state).finish_check(result);
        on_change();
        return;
    }
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

/// Linux and Windows: the latest release's version, from the GitHub API.
#[cfg(not(target_os = "macos"))]
fn latest_release() -> Result<String, String> {
    let body = ureq::get(update::LATEST_RELEASE_API)
        .header("User-Agent", "itervox-desktop")
        .header("Accept", "application/vnd.github+json")
        .call()
        .map_err(|e| e.to_string())?
        .body_mut()
        .with_config()
        .limit(1 << 20)
        .read_to_string()
        .map_err(|e| e.to_string())?;
    update::parse_latest_release(&body)
}

/// The Mac self-updates through the plugin; this path is never taken there.
#[cfg(target_os = "macos")]
fn latest_release() -> Result<String, String> {
    Err("not used on macOS".into())
}

/// Linux and Windows: the release page of the offered update, if any.
pub fn release_page(app: &AppHandle) -> Option<String> {
    let u = updates(app)?;
    let state = lock(&u.state);
    state.offer().map(|o| update::release_page(&o.version))
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
    let page = release_page(app);
    let mut items: Vec<_> = updates(app)
        .map(|u| lock(&u.state).menu_items_for(u.mode))
        .unwrap_or_default()
        .into_iter()
        .map(|(label, enabled, action)| {
            let action = action.and_then(|a| match a {
                UpdateAction::Check => Some(MenuAction::CheckForUpdates),
                UpdateAction::Install => Some(MenuAction::InstallUpdate),
                UpdateAction::OpenRelease => page.clone().map(MenuAction::OpenExternal),
            });
            (label, enabled, action)
        })
        .collect();
    if let Some(sidecar) = sidecar {
        if let Some(status) = cli_status(app, sidecar) {
            let (label, enabled) = cli_tool::menu_item_for(Os::current(), &status);
            items.push((label, enabled, enabled.then_some(MenuAction::InstallCli)));
        }
    }
    if let Some(enabled) = autostart_enabled(app) {
        items.push((
            autostart::menu_label(enabled).to_string(),
            true,
            Some(MenuAction::ToggleAutostart),
        ));
    }
    items
}

/// `$APPDIR`: set while the app runs from an AppImage (Linux).
fn appdir() -> Option<std::path::PathBuf> {
    std::env::var_os("APPDIR").map(Into::into)
}

fn cli_plan(app: &AppHandle, sidecar: &Path) -> Option<CliPlan> {
    let home = app.path().home_dir().ok();
    cli_tool::plan(Os::current(), home.as_deref(), sidecar, appdir().as_deref())
}

/// The command-line tool's state on this platform.
fn cli_status(app: &AppHandle, sidecar: &Path) -> Option<cli_tool::CliStatus> {
    match cli_plan(app, sidecar)? {
        CliPlan::Link { dirs, target } => Some(cli_tool::status(&dirs, &target)),
        CliPlan::UserPath { dir } => Some(cli_tool::windows_status(
            &user_path::read().unwrap_or_default(),
            &dir,
        )),
    }
}

/// Linux AppImage: a newer AppImage refreshes the copy an installed
/// command-line tool links to, so `itervox` follows the app. Runs once at
/// start, off the main thread; nothing happens unless the tool is
/// installed.
pub fn refresh_cli_copy(app: &AppHandle, sidecar: Option<&Path>) {
    let Some(sidecar) = sidecar else { return };
    if !cli_tool::in_appimage(sidecar, appdir().as_deref()) {
        return;
    }
    let Some(CliPlan::Link { dirs, target }) = cli_plan(app, sidecar) else {
        return;
    };
    if !matches!(
        cli_tool::status(&dirs, &target),
        cli_tool::CliStatus::Installed(_)
    ) {
        return;
    }
    let sidecar = sidecar.to_path_buf();
    let _ = std::thread::Builder::new()
        .name("itervox-cli-copy".into())
        .spawn(move || {
            if let Err(e) = cli_tool::refresh_copy(&sidecar, &target) {
                eprintln!(
                    "itervox desktop: could not refresh {}: {e}",
                    target.display()
                );
            }
        });
}

/// The Windows user PATH (`HKCU\Environment\Path`).
#[cfg(windows)]
mod user_path {
    use winreg::enums::{RegType, HKEY_CURRENT_USER, KEY_READ, KEY_WRITE};
    use winreg::{RegKey, RegValue};

    fn env_key(write: bool) -> std::io::Result<RegKey> {
        let flags = if write {
            KEY_READ | KEY_WRITE
        } else {
            KEY_READ
        };
        RegKey::predef(HKEY_CURRENT_USER).open_subkey_with_flags("Environment", flags)
    }

    pub fn read() -> Option<String> {
        env_key(false).ok()?.get_value::<String, _>("Path").ok()
    }

    /// Writes the user PATH as `REG_EXPAND_SZ` (so `%USERPROFILE%`-style
    /// entries keep working) and tells running programs the environment
    /// changed, so new terminals see it.
    pub fn write(value: &str) -> Result<(), String> {
        let key = env_key(true).map_err(|e| format!("HKCU\\Environment: {e}"))?;
        let mut bytes: Vec<u8> = value
            .encode_utf16()
            .chain(std::iter::once(0))
            .flat_map(u16::to_le_bytes)
            .collect();
        bytes.shrink_to_fit();
        key.set_raw_value(
            "Path",
            &RegValue {
                bytes: bytes.into(),
                vtype: RegType::REG_EXPAND_SZ,
            },
        )
        .map_err(|e| format!("HKCU\\Environment\\Path: {e}"))?;
        broadcast();
        Ok(())
    }

    fn broadcast() {
        use windows_sys::Win32::UI::WindowsAndMessaging::{
            SendMessageTimeoutW, HWND_BROADCAST, SMTO_ABORTIFHUNG, WM_SETTINGCHANGE,
        };
        let env: Vec<u16> = "Environment".encode_utf16().chain(Some(0)).collect();
        let mut result = 0usize;
        // SAFETY: a broadcast with a NUL-terminated string that outlives
        // the call and a local for the result.
        unsafe {
            SendMessageTimeoutW(
                HWND_BROADCAST,
                WM_SETTINGCHANGE,
                0,
                env.as_ptr() as isize,
                SMTO_ABORTIFHUNG,
                5000,
                &mut result,
            );
        }
    }
}

/// Elsewhere there is no user PATH to change.
#[cfg(not(windows))]
mod user_path {
    pub fn read() -> Option<String> {
        None
    }

    pub fn write(_value: &str) -> Result<(), String> {
        Err("the user PATH is changed on Windows only".into())
    }
}

/// Install (or Repair) Command-Line Tool…; on Windows, add it to PATH.
pub fn install_cli(app: &AppHandle, sidecar: Option<&Path>) {
    let (msg, kind) = match install_cli_result(app, sidecar) {
        Ok(msg) => (msg, MessageDialogKind::Info),
        Err(e) => (e, MessageDialogKind::Error),
    };
    app.dialog()
        .message(msg)
        .title("Itervox")
        .kind(kind)
        .show(|_| {});
}

fn install_cli_result(app: &AppHandle, sidecar: Option<&Path>) -> Result<String, String> {
    let sidecar = sidecar.ok_or_else(|| {
        if Os::current() == Os::Windows {
            crate::platform::NO_LOCAL_DAEMON_MESSAGE.to_string()
        } else {
            "the bundled itervox daemon was not found".to_string()
        }
    })?;
    let path_env = std::env::var("PATH").unwrap_or_default();
    match cli_plan(app, sidecar).ok_or("no home directory to install into")? {
        CliPlan::Link { dirs, target } => {
            let link = if target == sidecar {
                cli_tool::install(&dirs, sidecar)?
            } else {
                cli_tool::install_with_copy(&dirs, sidecar, &target)?
            };
            Ok(cli_tool::installed_message(
                Os::current(),
                &link,
                &target,
                &path_env,
            ))
        }
        CliPlan::UserPath { dir } => {
            let current = user_path::read().unwrap_or_default();
            if let Some(new) = cli_tool::windows_path_with(&current, &dir) {
                user_path::write(&new)?;
            }
            Ok(format!(
                "Added {} to your PATH. Open a new terminal to run `itervox`.",
                dir.display()
            ))
        }
    }
}

/// Start at Login: whether it is on, or `None` where the app offers no item
/// (macOS, or no home directory).
pub fn autostart_enabled(app: &AppHandle) -> Option<bool> {
    match autostart_plan(app)? {
        autostart::Plan::XdgEntry { path, .. } => Some(autostart::xdg_enabled(&path)),
        autostart::Plan::RunKey { .. } => run_key_enabled(app),
        autostart::Plan::Unsupported(_) => None,
    }
}

fn autostart_plan(app: &AppHandle) -> Option<autostart::Plan> {
    let home = app.path().home_dir().ok();
    let xdg = std::env::var_os("XDG_CONFIG_HOME").map(std::path::PathBuf::from);
    let config = autostart::config_home(xdg.as_deref(), home.as_deref());
    let exe = std::env::current_exe().ok()?;
    let appimage = std::env::var_os("APPIMAGE").map(std::path::PathBuf::from);
    Some(autostart::plan(
        Os::current(),
        config.as_deref(),
        &exe,
        appimage.as_deref(),
    ))
}

/// Turns Start at Login over; an error shows in a dialog.
pub fn toggle_autostart(app: &AppHandle) {
    let res = match autostart_plan(app) {
        Some(autostart::Plan::XdgEntry { path, contents }) => {
            autostart::xdg_set(&path, &contents, !autostart::xdg_enabled(&path))
        }
        Some(autostart::Plan::RunKey { .. }) => toggle_run_key(app),
        _ => Ok(()),
    };
    if let Err(e) = res {
        app.dialog()
            .message(format!("Could not change Start at Login: {e}"))
            .title("Itervox")
            .kind(MessageDialogKind::Error)
            .show(|_| {});
    }
}

/// The Run key through tauri-plugin-autostart (Windows).
#[cfg(windows)]
fn run_key_enabled(app: &AppHandle) -> Option<bool> {
    use tauri_plugin_autostart::ManagerExt;
    app.autolaunch().is_enabled().ok()
}

#[cfg(windows)]
fn toggle_run_key(app: &AppHandle) -> Result<(), String> {
    use tauri_plugin_autostart::ManagerExt;
    let al = app.autolaunch();
    let on = al.is_enabled().map_err(|e| e.to_string())?;
    if on { al.disable() } else { al.enable() }.map_err(|e| e.to_string())
}

#[cfg(not(windows))]
fn run_key_enabled(_app: &AppHandle) -> Option<bool> {
    None
}

#[cfg(not(windows))]
fn toggle_run_key(_app: &AppHandle) -> Result<(), String> {
    Ok(())
}
