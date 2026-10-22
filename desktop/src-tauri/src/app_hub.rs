//! #229 — the hub notification thread: Tauri wiring over `hub_notify`.
//! While a local hub serves, the app shows the native notifications the
//! hub routes to the desktop channel over ONE events stream, and drops the
//! alerts it would raise from polling the local daemons (the hub covers
//! them, with the operator's per-project settings). When the hub goes away
//! the app falls back to its own per-daemon alerts.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use tauri::AppHandle;
use tauri_plugin_notification::NotificationExt;

use crate::hub_notify::{
    hub_token_command, parse_created_token, read_hub_lock, stream_notifications, StreamEnd,
    HUB_TOKEN_KEY,
};
use crate::registry::registry_dir;
use crate::supervisor::wait_output;

/// The project key hub notification targets are registered under.
pub const HUB_TARGET: &str = "hub:";

/// Registers a notification target (project key, URL) and returns its id.
pub type RegisterTarget = Arc<dyn Fn(&str, &str) -> i32 + Send + Sync>;

/// Shared hub notification state.
#[derive(Default)]
pub struct HubNotify {
    /// True while the events stream is connected: per-daemon alerts of
    /// local projects are then left to the hub.
    pub active: AtomicBool,
    /// The hub's origin and token of the live stream (for opening links).
    pub session: std::sync::Mutex<Option<(String, String)>>,
}

/// Gets the app's hub token from the keychain, creating one with the
/// bundled CLI on first use.
fn hub_token(
    tokens: &dyn crate::token::TokenStore,
    sidecar: &Result<std::path::PathBuf, String>,
) -> Option<String> {
    if let Ok(Some(t)) = tokens.get(HUB_TOKEN_KEY) {
        return Some(t);
    }
    let sidecar = sidecar.as_ref().ok()?;
    let child = hub_token_command(sidecar).spawn().ok()?;
    let out = wait_output(child, Duration::from_secs(10)).ok()?;
    let token = out
        .status
        .success()
        .then(|| parse_created_token(&out.stdout))
        .flatten()?;
    let _ = tokens.set(HUB_TOKEN_KEY, &token);
    Some(token)
}

/// Runs the hub notification loop on its own thread for the app's life.
pub fn spawn(
    app: &AppHandle,
    hub: Arc<HubNotify>,
    tokens: Arc<dyn crate::token::TokenStore>,
    sidecar: Result<std::path::PathBuf, String>,
    register: RegisterTarget,
) -> std::io::Result<()> {
    let app = app.clone();
    std::thread::Builder::new()
        .name("itervox-hub-notify".into())
        .spawn(move || {
            let stop = AtomicBool::new(false);
            let mut since = None;
            let mut backoff = Duration::from_secs(2);
            let mut refused = 0u32;
            loop {
                let lock = registry_dir().and_then(|d| read_hub_lock(&d));
                let token = lock
                    .as_ref()
                    .and_then(|_| hub_token(tokens.as_ref(), &sidecar));
                let (Some(lock), Some(token)) = (lock, token) else {
                    hub.active.store(false, Ordering::Relaxed);
                    std::thread::sleep(Duration::from_secs(10));
                    continue;
                };
                let origin = lock.origin();
                *hub.session.lock().unwrap_or_else(|e| e.into_inner()) =
                    Some((origin.clone(), token.clone()));
                hub.active.store(true, Ordering::Relaxed);
                let end = stream_notifications(&origin, &token, &mut since, &stop, &mut |n| {
                    backoff = Duration::from_secs(2);
                    let id = register(HUB_TARGET, &n.url);
                    let _ = app
                        .notification()
                        .builder()
                        .id(id)
                        .title(&n.title)
                        .body(&n.body)
                        .show();
                });
                hub.active.store(false, Ordering::Relaxed);
                match end {
                    StreamEnd::Stopped => return,
                    StreamEnd::Unauthorized => {
                        // Revoked or rotated: drop it and create a new one,
                        // but not in a loop when every new one is refused.
                        let _ = tokens.delete(HUB_TOKEN_KEY);
                        since = None;
                        refused += 1;
                        if refused >= 3 {
                            refused = 0;
                            std::thread::sleep(Duration::from_secs(600));
                        }
                    }
                    StreamEnd::Disconnected(_) => refused = 0,
                }
                std::thread::sleep(backoff);
                backoff = (backoff * 2).min(Duration::from_secs(60));
            }
        })?;
    Ok(())
}
