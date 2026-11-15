//! Opening a project's dashboard in a webview window without putting the
//! token in a URL.
//!
//! The dashboard reads its bearer token from `sessionStorage`
//! (`itervox.apiToken`, web/src/auth/tokenStore.ts) when it loads; its
//! `?token=` capture (AuthGate) exists for browser links. The app instead
//! hands the token to the window through an initialization script that runs
//! before the dashboard's own scripts, only on the daemon's origin, and
//! writes it to `sessionStorage`. The URL the window loads therefore never
//! carries the token, so it does not end up in the webview history, the
//! daemon's request log, or a copied link.

/// sessionStorage key the dashboard reads first-party tokens from.
pub const SESSION_TOKEN_KEY: &str = "itervox.apiToken";
/// localStorage key of a "Remember" token; cleared so a stale remembered
/// token cannot shadow the supervisor token.
pub const PERSISTENT_TOKEN_KEY: &str = "itervox.apiToken.persistent";

/// The origin (`scheme://host:port`) of a base URL.
pub fn origin_of(base_url: &str) -> Option<String> {
    let (scheme, rest) = base_url.split_once("://")?;
    let authority = rest.split('/').next().unwrap_or("");
    if authority.is_empty() || !(scheme == "http" || scheme == "https") {
        return None;
    }
    Some(format!("{scheme}://{authority}"))
}

/// The global the dashboard reads, once at boot, to know it is embedded in
/// the app's window (web/src/hooks/useEmbeddedShell.ts): it then drops its
/// own sidebar (the window has the rail and the Go menu), lands on the issue
/// view, and exposes `window.__itervoxNavigate` for the Go menu.
pub const SHELL_GLOBAL: &str = "__ITERVOX_SHELL__";
/// [`SHELL_GLOBAL`]'s value in the app's dashboard windows.
pub const SHELL_DESKTOP: &str = "desktop";

fn js(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| "\"\"".into())
}

/// The statements that hand `token` to the dashboard.
fn token_statements(token: &str) -> String {
    format!(
        "window.localStorage.removeItem({pkey});\
window.sessionStorage.setItem({skey},{token_js});",
        pkey = js(PERSISTENT_TOKEN_KEY),
        skey = js(SESSION_TOKEN_KEY),
        token_js = js(token),
    )
}

/// `body`, run only on `origin` (never on a page the user follows a link
/// out to), and never throwing into the page.
fn origin_guarded(origin: &str, body: &str) -> String {
    let origin_js = js(origin);
    format!("(function(){{try{{if(window.location.origin!=={origin_js})return;{body}}}catch(e){{}}}})();")
}

/// The initialization script that only hands over a token (the hub
/// window, which is not a project's dashboard). It does nothing on any
/// other origin (a link the user follows out of the page).
pub fn token_init_script(origin: &str, token: &str) -> String {
    origin_guarded(origin, &token_statements(token))
}

/// The initialization script of a project's dashboard webview in an app
/// window: marks the page as embedded ([`SHELL_GLOBAL`]) and hands over the
/// token, if the daemon has one. Origin-guarded like [`token_init_script`].
pub fn dashboard_init_script(origin: &str, token: Option<&str>) -> String {
    let shell = format!("window.{SHELL_GLOBAL}={};", js(SHELL_DESKTOP));
    let token = token.map(token_statements).unwrap_or_default();
    origin_guarded(origin, &format!("{shell}{token}"))
}

/// The script the Go menu evaluates in a dashboard webview: an in-app
/// navigation through the hook the embedded dashboard exposes. A page
/// without the hook (another site, an older daemon) is left alone.
pub fn navigate_script(path: &str) -> String {
    let p = js(path);
    format!("window.__itervoxNavigate&&window.__itervoxNavigate({p});")
}

/// The script the tray's "New Task…" evaluates in a dashboard webview that
/// already shows the project: the in-app navigation of [`navigate_script`]
/// when the page has the hook and accepts `path`; otherwise (an older
/// daemon, a page the user followed a link out to) it loads `url`, the
/// same page by its full address.
pub fn navigate_or_load_script(path: &str, url: &str) -> String {
    let (p, u) = (js(path), js(url));
    format!(
        "(function(){{try{{if(window.__itervoxNavigate&&window.__itervoxNavigate({p}))return;}}catch(e){{}}window.location.assign({u});}})();"
    )
}

/// The window label for a project: stable, and only `[a-zA-Z0-9-]` as
/// Tauri requires.
pub fn window_label(project: &str) -> String {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in project.bytes() {
        h ^= u64::from(b);
        h = h.wrapping_mul(0x100000001b3);
    }
    format!("dashboard-{h:016x}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn origin_strips_path_and_query() {
        assert_eq!(
            origin_of("http://127.0.0.1:8090/?issue=ENG-1").as_deref(),
            Some("http://127.0.0.1:8090")
        );
        assert_eq!(
            origin_of("http://[::1]:9000/").as_deref(),
            Some("http://[::1]:9000")
        );
        assert_eq!(origin_of("file:///x"), None);
        assert_eq!(origin_of("garbage"), None);
    }

    #[test]
    fn init_script_is_origin_guarded_and_escapes_the_token() {
        let s = token_init_script("http://127.0.0.1:8090", "ab\"c</script>");
        assert!(s.contains(r#"window.location.origin!=="http://127.0.0.1:8090""#));
        assert!(s.contains(r#"sessionStorage.setItem("itervox.apiToken","ab\"c</script>")"#));
        assert!(s.contains(r#"localStorage.removeItem("itervox.apiToken.persistent")"#));
    }

    #[test]
    fn dashboard_script_marks_the_page_embedded_and_hands_over_the_token() {
        let s = dashboard_init_script("http://127.0.0.1:8090", Some("tok"));
        let guard = r#"if(window.location.origin!=="http://127.0.0.1:8090")return;"#;
        let flag = r#"window.__ITERVOX_SHELL__="desktop";"#;
        // The flag is set only on the daemon's origin, before the token.
        let (g, f) = (s.find(guard).unwrap(), s.find(flag).unwrap());
        assert!(g < f, "{s}");
        assert!(s.contains(r#"sessionStorage.setItem("itervox.apiToken","tok")"#));
        // An open API: the flag, no token statements.
        let open = dashboard_init_script("http://127.0.0.1:8090", None);
        assert!(open.contains(guard) && open.contains(flag), "{open}");
        assert!(!open.contains("sessionStorage"), "{open}");
        // The hub window is not a project dashboard: no flag.
        assert!(!token_init_script("http://127.0.0.1:8090", "tok").contains(SHELL_GLOBAL));
    }

    #[test]
    fn navigate_script_quotes_the_path_and_needs_the_hook() {
        assert_eq!(
            navigate_script("/focus"),
            r#"window.__itervoxNavigate&&window.__itervoxNavigate("/focus");"#
        );
        let s = navigate_script("/x\");alert(1)//");
        assert!(s.contains(r#"("/x\");alert(1)//")"#), "{s}");
    }

    #[test]
    fn navigate_or_load_prefers_the_hook_and_falls_back_to_the_url() {
        let s = navigate_or_load_script("/?newTask=1", "http://127.0.0.1:8090/?newTask=1");
        assert_eq!(
            s,
            r#"(function(){try{if(window.__itervoxNavigate&&window.__itervoxNavigate("/?newTask=1"))return;}catch(e){}window.location.assign("http://127.0.0.1:8090/?newTask=1");})();"#
        );
        // Both values are JSON-quoted, never spliced raw.
        let s = navigate_or_load_script("/\");x(", "http://h/\");y(");
        assert!(s.contains(r#"("/\");x(")"#), "{s}");
        assert!(s.contains(r#"assign("http://h/\");y(")"#), "{s}");
    }

    /// Drift guard: the global, its value and the navigate hook are the
    /// ones the dashboard reads.
    #[test]
    fn shell_flag_and_hook_match_the_dashboard() {
        let src = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/hooks/useEmbeddedShell.ts"
        ))
        .expect("web/src/hooks/useEmbeddedShell.ts");
        assert!(src.contains(&format!("export const DESKTOP_SHELL = '{SHELL_DESKTOP}';")));
        assert!(src.contains(&format!("win.{SHELL_GLOBAL} === DESKTOP_SHELL")));
        assert!(src.contains("window.__itervoxNavigate = hook;"));
    }

    #[test]
    fn window_labels_are_stable_and_valid() {
        let a = window_label("/a/WORKFLOW.md");
        assert_eq!(a, window_label("/a/WORKFLOW.md"));
        assert_ne!(a, window_label("/b/WORKFLOW.md"));
        assert!(a.chars().all(|c| c.is_ascii_alphanumeric() || c == '-'));
    }

    /// Drift guard: the keys must match what the dashboard reads.
    #[test]
    fn storage_keys_match_the_dashboard_token_store() {
        let src = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/auth/tokenStore.ts"
        ))
        .expect("web/src/auth/tokenStore.ts");
        assert!(
            src.contains(&format!("const SESSION_KEY = '{SESSION_TOKEN_KEY}';")),
            "SESSION_KEY changed in tokenStore.ts"
        );
        assert!(
            src.contains(&format!("const LOCAL_KEY = '{PERSISTENT_TOKEN_KEY}';")),
            "LOCAL_KEY changed in tokenStore.ts"
        );
    }

    /// Drift guard: the deep link the notifications open is the one the
    /// dashboard understands.
    #[test]
    fn issue_query_param_matches_the_dashboard_router() {
        let src = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/hooks/useUrlState.ts"
        ))
        .expect("web/src/hooks/useUrlState.ts");
        assert!(src.contains("new URLSearchParams(location.search).get('issue')"));
    }
}
