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

/// The initialization script for a dashboard window. It does nothing on any
/// other origin (a link the user follows out of the dashboard).
pub fn token_init_script(origin: &str, token: &str) -> String {
    let origin_js = serde_json::to_string(origin).unwrap_or_else(|_| "\"\"".into());
    let token_js = serde_json::to_string(token).unwrap_or_else(|_| "\"\"".into());
    let skey = serde_json::to_string(SESSION_TOKEN_KEY).unwrap_or_default();
    let pkey = serde_json::to_string(PERSISTENT_TOKEN_KEY).unwrap_or_default();
    format!(
        "(function(){{try{{if(window.location.origin!=={origin_js})return;\
window.localStorage.removeItem({pkey});\
window.sessionStorage.setItem({skey},{token_js});}}catch(e){{}}}})();"
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
