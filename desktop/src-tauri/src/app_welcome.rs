//! Tauri glue of the welcome window ([`crate::welcome`]): opened on a first
//! launch and when the app is opened again with no project dashboard to
//! show, so the app is never only an icon the notch may hide.

use std::sync::Arc;

use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};

use super::{dash, lock, Shared};
use crate::menu::MenuAction;
use crate::platform::Os;
use crate::welcome::{
    allowed, push_script, reopen_plan, welcome_action, welcome_page_allowed, welcome_view,
    ReopenPlan, WelcomeAction, WelcomeView, WELCOME_LABEL, WELCOME_PAGE,
};

/// The list as the last refresh left it.
fn current_view(shared: &Shared) -> WelcomeView {
    welcome_view(&dash::current_rail(shared), Os::current())
}

/// Shows the welcome window, creating it on first use.
pub(super) fn open(app: &AppHandle, shared: &Arc<Shared>) {
    let (app2, shared2) = (app.clone(), Arc::clone(shared));
    let _ = app.run_on_main_thread(move || {
        if let Some(w) = app2.get_webview_window(WELCOME_LABEL) {
            let _ = w.unminimize();
            let _ = w.show();
            let _ = w.set_focus();
            return;
        }
        #[cfg(target_os = "macos")]
        let _ = app2.set_activation_policy(tauri::ActivationPolicy::Regular);
        let (nav_app, nav_shared) = (app2.clone(), Arc::clone(&shared2));
        let built =
            WebviewWindowBuilder::new(&app2, WELCOME_LABEL, WebviewUrl::App(WELCOME_PAGE.into()))
                .title("Welcome to Itervox")
                .inner_size(560.0, 640.0)
                .min_inner_size(420.0, 420.0)
                .on_navigation(move |url| match welcome_action(url.as_str()) {
                    Some(action) => {
                        let (app, shared) = (nav_app.clone(), Arc::clone(&nav_shared));
                        // Off the navigation callback: opening a dashboard builds
                        // windows on the main thread.
                        let _ = std::thread::Builder::new()
                            .name("itervox-welcome".into())
                            .spawn(move || handle(&app, &shared, action));
                        false
                    }
                    None => welcome_page_allowed(url.as_str()),
                })
                .build();
        match built {
            Ok(w) => {
                // A new page asks for the list with its `ready` navigation.
                *lock(&shared2.welcome_last) = None;
                let _ = w.set_focus();
            }
            Err(e) => eprintln!("itervox desktop: could not open the welcome window: {e}"),
        }
    });
}

/// Pushes the list to the welcome window while it is open, when it changed
/// since the last push (or always, with `force`). Called after each
/// refresh and after the project list changes.
pub(super) fn push(app: &AppHandle, shared: &Shared, force: bool) {
    let Some(w) = app.get_webview_window(WELCOME_LABEL) else {
        return;
    };
    let view = current_view(shared);
    {
        let mut last = lock(&shared.welcome_last);
        if !force && last.as_ref() == Some(&view) {
            return;
        }
        *last = Some(view.clone());
    }
    let _ = w.eval(push_script(&view));
}

/// A request of the welcome page (off the main thread). Start and Open run
/// only for a project the page was last offered them for.
fn handle(app: &AppHandle, shared: &Arc<Shared>, action: WelcomeAction) {
    let permitted = match &action {
        WelcomeAction::Ready | WelcomeAction::AddProject => true,
        _ => lock(&shared.welcome_last)
            .as_ref()
            .is_some_and(|v| allowed(&action, v)),
    };
    if !permitted {
        return;
    }
    match action {
        WelcomeAction::Ready => push(app, shared, true),
        WelcomeAction::AddProject => super::add_project(app, shared),
        WelcomeAction::Start(key) => super::handle_action(app, shared, MenuAction::Start(key)),
        WelcomeAction::Open(key) => {
            dash::open(app, shared, &key, None, dash::Mode::Reuse);
            // Queued after the dashboard's own main-thread work: once the
            // project has its window, the welcome window has done its job.
            let (app2, shared2) = (app.clone(), Arc::clone(shared));
            let _ = app.run_on_main_thread(move || {
                if shared2.dash.shows(&key) {
                    if let Some(w) = app2.get_webview_window(WELCOME_LABEL) {
                        let _ = w.close();
                    }
                }
            });
        }
    }
}

/// The app was opened again while it runs (macOS `RunEvent::Reopen`:
/// Finder, the Dock, Spotlight): bring a window back, else open the first
/// project's dashboard, else the welcome window — never nothing.
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub(super) fn reopen(app: &AppHandle, shared: &Arc<Shared>, has_visible_windows: bool) {
    let existing = {
        let focused = dash::last_focused(shared);
        focused.or_else(|| {
            let mut labels: Vec<String> = app.windows().keys().cloned().collect();
            labels.sort();
            labels.into_iter().next()
        })
    };
    match reopen_plan(
        has_visible_windows,
        existing.as_deref(),
        &dash::current_rail(shared),
    ) {
        ReopenPlan::Nothing => {}
        ReopenPlan::Focus(label) => {
            if let Some(w) = app.get_window(&label) {
                let _ = w.unminimize();
                let _ = w.show();
                let _ = w.set_focus();
            }
        }
        ReopenPlan::OpenDashboard(key) => dash::open(app, shared, &key, None, dash::Mode::Reuse),
        ReopenPlan::Welcome => open(app, shared),
    }
}
