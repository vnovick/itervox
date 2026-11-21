//! The terminal window (#278 phase 2): one app window per tmux session,
//! showing the daemon's own live terminal (#298) of that session.
//!
//! Reuse, not a copy: the window loads the daemon's dashboard at
//! [`TERMINAL_PAGE_PATH`] (`web/src/pages/TerminalWindow`), a page that
//! renders only the Sessions page's live terminal — the same xterm.js
//! renderer, ticket request (through `authedFetch`) and socket (through
//! `web/src/auth/terminalSocket.ts`). The app bundles no terminal of its
//! own and adds no write path: the daemon decides read-only or read-write
//! from the token's scope and `sessions.terminal_write`, and the page
//! shows the mode the daemon granted.
//!
//! Auth: the page is loaded from the daemon's origin — a local daemon's
//! address, or the local end of a remote daemon's SSH or IAP tunnel — and
//! gets the token the app already holds for that daemon from the
//! origin-guarded initialization script of [`crate::dashboard`], which
//! writes it to the window's `sessionStorage`. The URL never carries it,
//! so it is not in the webview history, the daemon's request log, or
//! anything the window could be restored from; it is gone with the window.
//!
//! Origin: the socket is opened by that page, so its `Origin` is the
//! page's origin and its `Host` the same `127.0.0.1:<port>` (a tunnel
//! forwards the bytes unchanged): the daemon's same-origin check passes,
//! exactly as it does for the app's dashboard windows. A daemon reached
//! through its host's hub has no live terminal here — the hub never
//! proxies the terminal's socket — so such a daemon gets no action.
//!
//! Closing the window closes the socket: the daemon detaches, and tmux
//! keeps the session running.

use crate::dashboard::origin_of;
use crate::sessions::percent_encode;

/// The dashboard route of the terminal window's page (without the leading
/// slash), `web/src/pages/TerminalWindow/terminalWindowPath.ts`
/// `TERMINAL_WINDOW_PATH`.
pub const TERMINAL_PAGE_PATH: &str = "sessions/terminal";

/// Why a daemon reached through its host's hub has no "Live terminal".
pub const HUB_NOTE: &str =
    "The live terminal is not available through the host's hub: open the session's dashboard on the daemon's own tunnel, or use Open in Terminal.";

/// The prefix of a hub-proxied dashboard path (`/hub/p/<project>/`).
const HUB_PATH: &str = "/hub/p/";

/// Whether `url` (a daemon's API base) goes through a host's hub.
pub fn via_hub(url: &str) -> bool {
    url.split_once("://")
        .and_then(|(_, rest)| rest.find('/').map(|i| &rest[i..]))
        .is_some_and(|path| path.starts_with(HUB_PATH))
}

/// The terminal page of session `id` at `base`:
/// `{base}sessions/terminal?id=<id>`. It carries no token.
pub fn page_link(base: &str, id: &str) -> String {
    let mut b = base.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}{TERMINAL_PAGE_PATH}?id={}", percent_encode(id))
}

/// The window key of session `id` of `daemon`: stable, so a second
/// "Live terminal" finds the window already open, and only `[a-z0-9-]`
/// as Tauri requires. The window's label is this key plus a generation
/// (`-<n>`), so a rebuilt window ([`Reopen::Rebuild`]) never waits for
/// the stale one's label to be released.
pub fn window_label(daemon: &str, id: &str) -> String {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in daemon.bytes().chain([0u8]).chain(id.bytes()) {
        h ^= u64::from(b);
        h = h.wrapping_mul(0x100000001b3);
    }
    format!("terminal-{h:016x}")
}

/// The window's title: the session, then the project (and the remote
/// connection, for a remote daemon).
pub fn window_title(session: &str, project: &str, remote: Option<&str>) -> String {
    let project = match remote.filter(|r| !r.is_empty()) {
        Some(r) => format!("{project} ({r})"),
        None => project.to_string(),
    };
    format!("{session} — {project} — Live terminal")
}

/// Whether the terminal window may navigate to `url`: only pages of the
/// daemon's own origin (the page reloading, or a reconnect), and
/// `about:blank`, which some webviews route through the handler and which
/// carries nothing (the token script is origin-guarded, so it never runs
/// there); a link out goes nowhere.
pub fn navigation_allowed(origin: &str, url: &str) -> bool {
    url == "about:blank" || origin_of(url).is_some_and(|o| o == origin)
}

/// What a terminal window was built with: the daemon origin its page and
/// token script are bound to, and a fingerprint of that token. Never the
/// token itself, and never logged or persisted: the fingerprint is a hash
/// keyed per process, only compared in memory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Built {
    origin: String,
    token: Option<u64>,
}

impl Built {
    pub fn new(origin: &str, token: Option<&str>) -> Self {
        Built {
            origin: origin.to_string(),
            token: token.map(token_fingerprint),
        }
    }
}

/// A keyed (random per process) hash of `token`.
fn token_fingerprint(token: &str) -> u64 {
    use std::hash::BuildHasher;
    static KEY: std::sync::OnceLock<std::collections::hash_map::RandomState> =
        std::sync::OnceLock::new();
    KEY.get_or_init(Default::default).hash_one(token)
}

/// What "Live terminal" does with the session's window.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Reopen {
    /// No window yet: build one.
    Create,
    /// The window was built for this origin and token: show and focus it.
    Focus,
    /// The window was built for another origin or token (the daemon
    /// restarted with a new token or port, or its tunnel reconnected on
    /// another local port): its page and token script are dead, so close
    /// it and build a fresh one. A window's initialization script cannot
    /// be changed in place: this is the dashboard windows' `Replace`
    /// (`rail::switch_plan`).
    Rebuild,
}

/// The plan for the session's window: `open` whether one is open, `held`
/// what it was built with (`None` when unknown), `next` the daemon's
/// current target.
pub fn reopen_plan(open: bool, held: Option<&Built>, next: &Built) -> Reopen {
    match (open, held) {
        (false, _) => Reopen::Create,
        (true, Some(h)) if h == next => Reopen::Focus,
        (true, _) => Reopen::Rebuild,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The page link is the daemon's route, and nothing in it is a
    /// credential: the window's URL is safe to keep.
    #[test]
    fn terminal_page_link_carries_no_token() {
        assert_eq!(
            page_link("http://127.0.0.1:8090/", "itervox-abc123"),
            "http://127.0.0.1:8090/sessions/terminal?id=itervox-abc123"
        );
        assert_eq!(
            page_link("http://127.0.0.1:50001", "a b/c"),
            "http://127.0.0.1:50001/sessions/terminal?id=a%20b%2Fc"
        );
        let link = page_link("http://127.0.0.1:8090/", "itervox-abc123");
        assert!(!link.contains("token"));
        // The token reaches the window only through the origin-guarded
        // initialization script.
        let script =
            crate::dashboard::token_init_script(&origin_of(&link).unwrap(), "secret-token-value");
        assert!(script.contains("window.location.origin!==\"http://127.0.0.1:8090\""));
        assert!(script.contains("sessionStorage.setItem"));
        assert!(!link.contains("secret-token-value"));
    }

    /// One window per session: the label is stable for a session and
    /// differs between sessions and between daemons.
    #[test]
    fn terminal_window_label_is_one_per_session() {
        let a = window_label("/w/WORKFLOW.md", "itervox-abc123");
        assert_eq!(a, window_label("/w/WORKFLOW.md", "itervox-abc123"));
        assert!(a.starts_with("terminal-"));
        assert!(a
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-'));
        assert_ne!(a, window_label("/w/WORKFLOW.md", "itervox-def456"));
        assert_ne!(a, window_label("/v/WORKFLOW.md", "itervox-abc123"));
        // The separator keeps (daemon, id) pairs apart.
        assert_ne!(window_label("ab", "c"), window_label("a", "bc"));
        // Never a dashboard window's label.
        assert_ne!(a, crate::dashboard::window_label("/w/WORKFLOW.md"));
    }

    #[test]
    fn terminal_window_title_names_session_and_project() {
        assert_eq!(
            window_title("itervox-abc123", "app", None),
            "itervox-abc123 — app — Live terminal"
        );
        assert_eq!(
            window_title("ENG-1", "server", Some("PROD")),
            "ENG-1 — server (PROD) — Live terminal"
        );
        assert_eq!(
            window_title("ENG-1", "server", Some("")),
            "ENG-1 — server — Live terminal"
        );
    }

    #[test]
    fn terminal_window_detects_hub_routes() {
        assert!(via_hub("http://127.0.0.1:50001/hub/p/app/"));
        assert!(!via_hub("http://127.0.0.1:50001/"));
        assert!(!via_hub("http://127.0.0.1:8090"));
        assert!(!via_hub("http://127.0.0.1:8090/x/hub/p/app/"));
    }

    #[test]
    fn terminal_window_stays_on_the_daemon_origin() {
        let o = "http://127.0.0.1:50001";
        assert!(navigation_allowed(
            o,
            "http://127.0.0.1:50001/sessions/terminal?id=x"
        ));
        assert!(!navigation_allowed(o, "http://127.0.0.1:8090/"));
        assert!(!navigation_allowed(o, "https://example.com/"));
        assert!(!navigation_allowed(o, "file:///etc/passwd"));
    }

    /// `about:blank` carries no token and is allowed; nothing that only
    /// looks like it is.
    #[test]
    fn terminal_window_allows_about_blank() {
        let o = "http://127.0.0.1:50001";
        assert!(navigation_allowed(o, "about:blank"));
        assert!(!navigation_allowed(o, "about:blank#x"));
        assert!(!navigation_allowed(o, "about:srcdoc"));
        assert!(!navigation_allowed(o, "data:text/html,hi"));
        assert!(!navigation_allowed(o, "javascript:alert(1)"));
    }

    /// Same target → focus; a changed origin (new port, tunnel reconnect)
    /// or token (daemon restart), or a token gained or lost → rebuild; no
    /// window → create. The fingerprint never shows the token.
    #[test]
    fn terminal_window_reopen_rebuilds_on_changed_origin_or_token() {
        let o = "http://127.0.0.1:50001";
        let built = Built::new(o, Some("tok-1"));
        assert_eq!(
            reopen_plan(true, Some(&built), &Built::new(o, Some("tok-1"))),
            Reopen::Focus
        );
        assert_eq!(
            reopen_plan(true, Some(&built), &Built::new(o, Some("tok-2"))),
            Reopen::Rebuild
        );
        assert_eq!(
            reopen_plan(
                true,
                Some(&built),
                &Built::new("http://127.0.0.1:50002", Some("tok-1"))
            ),
            Reopen::Rebuild
        );
        assert_eq!(
            reopen_plan(true, Some(&built), &Built::new(o, None)),
            Reopen::Rebuild
        );
        let none = Built::new(o, None);
        assert_eq!(
            reopen_plan(true, Some(&none), &Built::new(o, None)),
            Reopen::Focus
        );
        // A window whose build is unknown is not trusted.
        assert_eq!(reopen_plan(true, None, &built), Reopen::Rebuild);
        assert_eq!(reopen_plan(false, Some(&built), &built), Reopen::Create);
        assert_eq!(reopen_plan(false, None, &built), Reopen::Create);
        assert!(!format!("{built:?}").contains("tok-1"));
    }

    /// Drift guard: the route the app opens is the one the dashboard
    /// renders as the terminal-only page.
    #[test]
    fn terminal_page_path_matches_the_dashboard() {
        let path = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/pages/TerminalWindow/terminalWindowPath.ts"
        ))
        .expect("web/src/pages/TerminalWindow/terminalWindowPath.ts");
        assert!(path.contains(&format!("TERMINAL_WINDOW_PATH = '/{TERMINAL_PAGE_PATH}'")));
        assert!(path.contains("get('id')"));
        let app = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/App.tsx"
        ))
        .expect("web/src/App.tsx");
        assert!(app.contains("if (isTerminalWindowPath())"));
    }
}
