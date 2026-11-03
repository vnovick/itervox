//! #229/#232 — the hub thread: Tauri wiring over `hub_notify`. While a
//! local hub serves, the app holds ONE events stream to it: it shows the
//! native notifications the hub routes to the desktop channel, drops the
//! alerts it would raise from polling the local daemons, and the refresh
//! reads the hub's members through the hub (`hub_source`) instead of
//! polling each one. Member-set changes on the stream wake the refresh.
//! When the hub goes away the app falls back to per-daemon polling and
//! alerts until a hub serves again.

use std::sync::atomic::AtomicBool;
use std::sync::Arc;
use std::time::Duration;

use tauri::AppHandle;
use tauri_plugin_notification::NotificationExt;

pub use crate::hub_notify::HubNotify;
use crate::hub_notify::{
    hub_admin_token_command, hub_token_command, parse_created_token, read_hub_lock, run_loop,
    HubEvent, LoopDeps, HUB_ADMIN_TOKEN_KEY, HUB_TOKEN_KEY,
};
use crate::registry::registry_dir;
use crate::supervisor::wait_output;

/// The project key hub notification targets are registered under.
pub const HUB_TARGET: &str = "hub:";

/// Registers a notification target (project key, URL) and returns its id.
pub type RegisterTarget = Arc<dyn Fn(&str, &str) -> i32 + Send + Sync>;

/// Gets a hub token from the keychain entry `key`, creating one with the
/// bundled CLI (`command`) on first use.
fn stored_or_created(
    tokens: &dyn crate::token::TokenStore,
    sidecar: &Result<std::path::PathBuf, String>,
    key: &str,
    command: fn(&std::path::Path) -> std::process::Command,
) -> Option<String> {
    if let Ok(Some(t)) = tokens.get(key) {
        return Some(t);
    }
    let sidecar = sidecar.as_ref().ok()?;
    let child = command(sidecar).spawn().ok()?;
    let out = wait_output(child, Duration::from_secs(10)).ok()?;
    let token = out
        .status
        .success()
        .then(|| parse_created_token(&out.stdout))
        .flatten()?;
    let _ = tokens.set(key, &token);
    Some(token)
}

/// The app's admin hub token for the hub windows (#232), created on first
/// use. A token the hub no longer accepts (revoked, or the hub's store was
/// reset) is replaced once.
pub fn admin_token(
    tokens: &dyn crate::token::TokenStore,
    sidecar: &Result<std::path::PathBuf, String>,
    origin: &str,
) -> Option<String> {
    let t = stored_or_created(
        tokens,
        sidecar,
        HUB_ADMIN_TOKEN_KEY,
        hub_admin_token_command,
    )?;
    if crate::hub_source::token_accepted(origin, &t) == Some(false) {
        let _ = tokens.delete(HUB_ADMIN_TOKEN_KEY);
        return stored_or_created(
            tokens,
            sidecar,
            HUB_ADMIN_TOKEN_KEY,
            hub_admin_token_command,
        );
    }
    Some(t)
}

/// Runs the hub stream loop on its own thread for the app's life. `wake`
/// asks the refresh loop for a refresh now.
pub fn spawn(
    app: &AppHandle,
    hub: Arc<HubNotify>,
    tokens: Arc<dyn crate::token::TokenStore>,
    sidecar: Result<std::path::PathBuf, String>,
    register: RegisterTarget,
    wake: Arc<dyn Fn() + Send + Sync>,
) -> std::io::Result<()> {
    let app = app.clone();
    std::thread::Builder::new()
        .name("itervox-hub".into())
        .spawn(move || {
            let stop = AtomicBool::new(false);
            let lock = || registry_dir().and_then(|d| read_hub_lock(&d));
            let token =
                || stored_or_created(tokens.as_ref(), &sidecar, HUB_TOKEN_KEY, hub_token_command);
            let forget = || {
                let _ = tokens.delete(HUB_TOKEN_KEY);
            };
            let changed_wake = Arc::clone(&wake);
            let changed = move |_: bool| changed_wake();
            let deps = LoopDeps {
                lock: &lock,
                token: &token,
                forget_token: &forget,
                sleep: &std::thread::sleep,
                changed: &changed,
            };
            run_loop(&hub, &deps, &stop, &mut |ev| match ev {
                HubEvent::Notification(n) => {
                    let id = register(HUB_TARGET, &n.url);
                    let _ = app
                        .notification()
                        .builder()
                        .id(id)
                        .title(&n.title)
                        .body(&n.body)
                        .show();
                }
                // The member set or a member's summary changed: show it.
                HubEvent::Projects(_) => wake(),
                // The feed alone changes no row of the menu.
                HubEvent::Feed(_) => {}
            });
        })?;
    Ok(())
}
