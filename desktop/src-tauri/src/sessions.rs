//! The Sessions window (#178): the agent sessions of every daemon — local
//! and remote through its tunnel — in one list, grouped by daemon, sessions
//! waiting for input first.
//!
//! The window is an app page (`dist/sessions.html`). The refresh loop reads
//! each daemon's `GET /api/v1/sessions` with the token it already holds for
//! `/api/v1/state`, builds a [`SessionsView`] here and hands it to the page
//! as JSON ([`push_script`]). Choosing a session navigates the page to an
//! app-internal path ([`OPEN_PATH`]) that the window's navigation handler
//! intercepts ([`page_action`]) and turns into that daemon's dashboard at
//! `/sessions?id=…` ([`session_link`]), opened with the usual token handoff.
//! The dashboard cannot be framed (it sends `frame-ancestors 'none'`), so a
//! session opens in its daemon's dashboard window rather than inside this
//! one.
//!
//! Nothing here writes to an agent: replies and messages go through the
//! dashboard, which talks only to sessions its daemon started.
//!
//! Sessions started by hand on a daemon's host (#178 step 3) are listed
//! too, after the daemon's own, marked "started outside itervox": they are
//! read-only, shown as running (with why) or exited.

use serde::{Deserialize, Serialize};

use crate::client::FetchError;

/// `status` of a session waiting for a human answer.
pub const STATUS_INPUT_REQUIRED: &str = "input_required";
/// `owner` of a session started by hand, outside itervox.
pub const OWNER_EXTERNAL: &str = "external";

/// One agent session as the window shows it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Session {
    /// The CLI's own session ID; empty until the CLI reported one.
    pub id: String,
    pub run_id: String,
    /// What the dashboard's `/sessions?id=` takes: `id`, else `run_id`.
    pub key: String,
    /// `itervox` for a daemon's own runs, `external` for sessions started
    /// by hand (read-only).
    pub owner: String,
    /// The issue; empty for a session started by hand.
    pub identifier: String,
    /// How the window names the session: the issue, else the CLI's title,
    /// else its directory's name, else the start of its id.
    pub label: String,
    /// The kind of run (e.g. `reviewer`); empty for an ordinary run.
    pub kind: String,
    /// The directory of a session started by hand.
    pub cwd: String,
    /// Why a session started by hand counts as still running.
    pub in_use_reason: String,
    /// Runs in a tmux pane (a hand-started session matched to one, or a
    /// session itervox started in tmux).
    pub tmux: bool,
    /// The pane, "session:window.pane"; empty when unknown.
    pub tmux_label: String,
    /// The dashboard can show its screen and type into it.
    pub controllable: bool,
    pub title: String,
    pub profile: String,
    pub backend: String,
    pub worker_host: String,
    /// `running`, `input_required` or `pending_input_resume`.
    pub status: String,
    pub input_context: String,
    pub started_at: String,
    pub queued_at: String,
}

impl Session {
    pub fn waiting(&self) -> bool {
        self.status == STATUS_INPUT_REQUIRED
    }

    /// Started by hand, outside itervox: read-only.
    pub fn external(&self) -> bool {
        self.owner == OWNER_EXTERNAL
    }
}

/// The window's name for a session (see [`Session::label`]).
fn label_of(identifier: &str, title: &str, cwd: &str, id: &str) -> String {
    if !identifier.is_empty() {
        return identifier.to_string();
    }
    if !title.is_empty() {
        return title.to_string();
    }
    if let Some(dir) = cwd.split('/').rfind(|p| !p.is_empty()) {
        return dir.to_string();
    }
    id.chars().take(8).collect()
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RawSession {
    id: String,
    run_id: String,
    owner: String,
    identifier: String,
    title: String,
    profile: String,
    backend: String,
    worker_host: String,
    status: String,
    input_context: String,
    started_at: String,
    queued_at: String,
    kind: String,
    cwd: String,
    in_use_reason: String,
    tmux_pane: Option<RawPane>,
    controllable: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawPane {
    label: String,
}

/// The daemon's tmux status (`GET /api/v1/sessions` top-level `tmux`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct TmuxInfo {
    pub available: bool,
    pub install_hint: String,
}

/// One daemon's `GET /api/v1/sessions`: its sessions and, from a daemon
/// with tmux control, its tmux status.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SessionsRead {
    pub sessions: Vec<Session>,
    pub tmux: Option<TmuxInfo>,
}

/// The warning a group shows while its daemon has no tmux.
pub fn tmux_warning(t: &TmuxInfo) -> String {
    format!(
        "tmux not found — install it to control sessions from here: {}",
        t.install_hint
    )
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct RawSessions {
    sessions: Option<Vec<RawSession>>,
    tmux: Option<TmuxInfo>,
}

/// Parses a `/api/v1/sessions` body into the sessions (see
/// [`parse_sessions`]) and the tmux status.
pub fn parse_read(body: &[u8]) -> Result<SessionsRead, String> {
    let raw: RawSessions = serde_json::from_slice(body).map_err(|e| e.to_string())?;
    Ok(SessionsRead {
        tmux: raw.tmux.clone(),
        sessions: sessions_of(raw),
    })
}

/// Parses a `/api/v1/sessions` body; rows without a key are skipped, and so
/// are rows with no issue unless they are sessions started by hand or have
/// a directory instead.
#[cfg(test)]
pub fn parse_sessions(body: &[u8]) -> Result<Vec<Session>, String> {
    let raw: RawSessions = serde_json::from_slice(body).map_err(|e| e.to_string())?;
    Ok(sessions_of(raw))
}

fn sessions_of(raw: RawSessions) -> Vec<Session> {
    raw.sessions
        .unwrap_or_default()
        .into_iter()
        .filter_map(|r| {
            let key = if r.id.is_empty() {
                r.run_id.clone()
            } else {
                r.id.clone()
            };
            let named = !r.identifier.is_empty() || r.owner == OWNER_EXTERNAL || !r.cwd.is_empty();
            let tmux = r.tmux_pane.is_some() || r.kind == "tmux";
            (!key.is_empty() && named).then_some(Session {
                label: label_of(&r.identifier, &r.title, &r.cwd, &key),
                id: r.id,
                run_id: r.run_id,
                key,
                owner: r.owner,
                identifier: r.identifier,
                kind: r.kind,
                cwd: r.cwd,
                in_use_reason: r.in_use_reason,
                tmux,
                tmux_label: r.tmux_pane.map(|p| p.label).unwrap_or_default(),
                controllable: r.controllable,
                title: r.title,
                profile: r.profile,
                backend: r.backend,
                worker_host: r.worker_host,
                status: r.status,
                input_context: r.input_context,
                started_at: r.started_at,
                queued_at: r.queued_at,
            })
        })
        .collect()
}

/// One daemon's read for this refresh.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DaemonSessions {
    /// The daemon's key: a workflow path, or a remote key.
    pub key: String,
    pub name: String,
    /// The remote connection's name; `None` for a local daemon.
    pub remote: Option<String>,
    pub result: Result<Vec<Session>, String>,
    /// The daemon's tmux status; `None` from a daemon without tmux control.
    pub tmux: Option<TmuxInfo>,
}

/// One daemon's group in the window.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionGroup {
    pub daemon: String,
    pub name: String,
    /// "local", or "remote · <connection>".
    pub location: String,
    pub waiting: usize,
    /// Sessions started by hand on the daemon's host.
    pub external: usize,
    pub error: Option<String>,
    /// Set while the daemon reports tmux missing (see [`tmux_warning`]).
    pub tmux_warning: Option<String>,
    /// The daemon can start a session in tmux ("New session…").
    pub new_session: bool,
    pub sessions: Vec<Session>,
}

/// Everything the window shows.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionsView {
    pub waiting: usize,
    pub total: usize,
    /// Sessions started by hand, across daemons (included in `total`).
    pub external: usize,
    pub groups: Vec<SessionGroup>,
}

/// Groups the reads by daemon. Within a group, sessions waiting for input
/// come first and sessions started by hand last (each daemon already lists
/// them so; this keeps it true for any daemon). Groups with a waiting
/// session come first, then local daemons, then by name.
pub fn aggregate(daemons: Vec<DaemonSessions>) -> SessionsView {
    let mut groups: Vec<SessionGroup> = daemons
        .into_iter()
        .map(|d| {
            let (mut sessions, error) = match d.result {
                Ok(s) => (s, None),
                Err(e) => (Vec::new(), Some(e)),
            };
            sessions.sort_by_key(|s| (!s.waiting(), s.external()));
            SessionGroup {
                waiting: sessions.iter().filter(|s| s.waiting()).count(),
                external: sessions.iter().filter(|s| s.external()).count(),
                location: match &d.remote {
                    Some(c) => format!("remote · {c}"),
                    None => "local".into(),
                },
                tmux_warning: d.tmux.as_ref().filter(|t| !t.available).map(tmux_warning),
                new_session: d.tmux.as_ref().is_some_and(|t| t.available),
                daemon: d.key,
                name: d.name,
                error,
                sessions,
            }
        })
        .collect();
    groups.sort_by(|a, b| {
        (a.waiting == 0, a.location != "local", &a.name, &a.daemon).cmp(&(
            b.waiting == 0,
            b.location != "local",
            &b.name,
            &b.daemon,
        ))
    });
    SessionsView {
        waiting: groups.iter().map(|g| g.waiting).sum(),
        total: groups.iter().map(|g| g.sessions.len()).sum(),
        external: groups.iter().map(|g| g.external).sum(),
        groups,
    }
}

/// One read of the Sessions window: a daemon's API (local, or a tunnel's
/// local end), its token and how the window names it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SessionsJob {
    pub key: String,
    pub url: String,
    pub token: Option<String>,
    pub name: String,
    /// The remote connection's name; `None` for a local daemon.
    pub remote: Option<String>,
}

/// Reads every daemon in parallel with `fetch` (each read is bounded by the
/// client's timeout) and aggregates the answers.
pub fn read_all<F>(jobs: &[SessionsJob], fetch: F) -> SessionsView
where
    F: Fn(&str, Option<&str>) -> Result<SessionsRead, FetchError> + Sync,
{
    let fetch = &fetch;
    let reads: Vec<DaemonSessions> = std::thread::scope(|s| {
        let handles: Vec<_> = jobs
            .iter()
            .map(|j| {
                s.spawn(move || {
                    let read = fetch(&j.url, j.token.as_deref()).map_err(|e| match e {
                        // An older daemon has no sessions endpoint.
                        FetchError::Status(404) => {
                            "this daemon has no Sessions API (update it)".to_string()
                        }
                        e => e.to_string(),
                    });
                    let (result, tmux) = match read {
                        Ok(r) => (Ok(r.sessions), r.tmux),
                        Err(e) => (Err(e), None),
                    };
                    DaemonSessions {
                        key: j.key.clone(),
                        name: j.name.clone(),
                        remote: j.remote.clone(),
                        result,
                        tmux,
                    }
                })
            })
            .collect();
        handles.into_iter().filter_map(|h| h.join().ok()).collect()
    });
    aggregate(reads)
}

/// The script that hands `view` to the page (`window.__itervoxSessions`).
/// JSON is valid JavaScript; U+2028/U+2029 are escaped for older engines.
pub fn push_script(view: &SessionsView) -> String {
    let json = serde_json::to_string(view)
        .unwrap_or_else(|_| "{\"waiting\":0,\"total\":0,\"external\":0,\"groups\":[]}".into())
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    format!("window.__itervoxSessions&&window.__itervoxSessions({json});")
}

/// The app-internal path the page navigates to when a session is chosen.
pub const OPEN_PATH: &str = "/__itervox/open-session";
/// The path the page navigates to once loaded, asking for data.
pub const READY_PATH: &str = "/__itervox/ready";
/// The path the page navigates to for "New session…" (#178 tmux).
pub const NEW_SESSION_PATH: &str = "/__itervox/new-session";

/// What a navigation of the Sessions page asks for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PageAction {
    /// The page loaded: push the current view.
    Ready,
    /// Open `session` (its key) in the dashboard of `daemon`.
    Open { daemon: String, session: String },
    /// Open the dashboard of `daemon` at the New session form.
    NewSession { daemon: String },
}

/// The action behind a navigation of the Sessions page, or `None` for an
/// ordinary navigation (the page itself loading).
pub fn page_action(url: &str) -> Option<PageAction> {
    let rest = url.split_once("://").map(|(_, r)| r).unwrap_or(url);
    let path_and_query = &rest[rest.find('/')?..];
    let (path, query) = path_and_query
        .split_once('?')
        .unwrap_or((path_and_query, ""));
    match path {
        READY_PATH => Some(PageAction::Ready),
        NEW_SESSION_PATH => query
            .split('&')
            .find_map(|pair| pair.strip_prefix("daemon="))
            .and_then(percent_decode)
            .filter(|d| !d.is_empty())
            .map(|daemon| PageAction::NewSession { daemon }),
        OPEN_PATH => {
            let mut daemon = None;
            let mut session = None;
            for pair in query.split('&') {
                let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
                match k {
                    "daemon" => daemon = percent_decode(v),
                    "id" => session = percent_decode(v),
                    _ => {}
                }
            }
            match (daemon, session) {
                (Some(d), Some(s)) if !d.is_empty() && !s.is_empty() => Some(PageAction::Open {
                    daemon: d,
                    session: s,
                }),
                _ => None,
            }
        }
        _ => None,
    }
}

/// The dashboard route of one session: `{base}sessions?id=<key>`.
pub fn session_link(base_url: &str, key: &str) -> String {
    let mut b = base_url.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}sessions?id={}", percent_encode(key))
}

/// The dashboard route of the New session form: `{base}sessions?new=1`.
pub fn new_session_link(base_url: &str) -> String {
    let mut b = base_url.to_string();
    if !b.ends_with('/') {
        b.push('/');
    }
    format!("{b}sessions?new=1")
}

/// Percent-encodes everything but RFC 3986 unreserved characters.
pub fn percent_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// Decodes `%XX` and `+`; `None` for a malformed escape or invalid UTF-8.
pub fn percent_decode(s: &str) -> Option<String> {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'%' => {
                let hex = s.get(i + 1..i + 3)?;
                out.push(u8::from_str_radix(hex, 16).ok()?);
                i += 3;
            }
            b'+' => {
                out.push(b' ');
                i += 1;
            }
            b => {
                out.push(b);
                i += 1;
            }
        }
    }
    String::from_utf8(out).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn session(identifier: &str, status: &str) -> Session {
        Session {
            id: format!("sess-{identifier}"),
            run_id: String::new(),
            key: format!("sess-{identifier}"),
            owner: "itervox".into(),
            identifier: identifier.into(),
            label: identifier.into(),
            kind: String::new(),
            cwd: String::new(),
            in_use_reason: String::new(),
            tmux: false,
            tmux_label: String::new(),
            controllable: false,
            title: String::new(),
            profile: String::new(),
            backend: "claude".into(),
            worker_host: String::new(),
            status: status.into(),
            input_context: String::new(),
            started_at: String::new(),
            queued_at: String::new(),
        }
    }

    #[test]
    fn parses_the_sessions_endpoint() {
        let body = br#"{"generatedAt":"2026-10-15T09:00:00Z","sessions":[
          {"id":"sess-1","owner":"itervox","identifier":"ENG-1","status":"input_required","inputContext":"Which DB?","backend":"claude","queuedAt":"2026-10-15T08:59:00Z"},
          {"id":"","runId":"run-2","owner":"itervox","identifier":"ENG-2","status":"running","workerHost":"vm-1","startedAt":"2026-10-15T08:00:00Z","messages":[{"id":"m","text":"t","status":"queued","queuedAt":"x"}]},
          {"id":"","owner":"itervox","identifier":"ENG-3","status":"running"}
        ]}"#;
        let s = parse_sessions(body).unwrap();
        assert_eq!(s.len(), 2, "a row with no key is skipped");
        assert_eq!(s[0].key, "sess-1");
        assert!(s[0].waiting());
        assert_eq!(s[0].input_context, "Which DB?");
        assert_eq!(
            s[1].key, "run-2",
            "the run id addresses a session until the CLI reports one"
        );
        assert_eq!(s[1].worker_host, "vm-1");
        assert!(parse_sessions(b"{}").unwrap().is_empty());
        assert!(parse_sessions(b"nope").is_err());
    }

    #[test]
    fn groups_by_daemon_with_waiting_sessions_first() {
        let view = aggregate(vec![
            DaemonSessions {
                tmux: None,
                key: "/a/WORKFLOW.md".into(),
                name: "alpha".into(),
                remote: None,
                result: Ok(vec![session("A-1", "running")]),
            },
            DaemonSessions {
                tmux: None,
                key: "remote:vm:/srv/WORKFLOW.md".into(),
                name: "server".into(),
                remote: Some("vm".into()),
                result: Ok(vec![
                    session("R-1", "running"),
                    session("R-2", "input_required"),
                ]),
            },
            DaemonSessions {
                tmux: None,
                key: "/b/WORKFLOW.md".into(),
                name: "beta".into(),
                remote: None,
                result: Err("token required".into()),
            },
        ]);
        assert_eq!((view.waiting, view.total), (1, 3));
        let names: Vec<_> = view.groups.iter().map(|g| g.name.as_str()).collect();
        assert_eq!(names, vec!["server", "alpha", "beta"]);
        let remote = &view.groups[0];
        assert_eq!(remote.location, "remote · vm");
        assert_eq!(remote.waiting, 1);
        let order: Vec<_> = remote
            .sessions
            .iter()
            .map(|s| s.identifier.as_str())
            .collect();
        assert_eq!(order, vec!["R-2", "R-1"]);
        assert_eq!(view.groups[2].error.as_deref(), Some("token required"));
    }

    #[test]
    fn push_script_hands_json_to_the_page() {
        let mut s = session("ENG-1", "running");
        s.title = "a\u{2028}b</script>".into();
        let view = aggregate(vec![DaemonSessions {
            tmux: None,
            key: "k".into(),
            name: "n".into(),
            remote: None,
            result: Ok(vec![s]),
        }]);
        let js = push_script(&view);
        assert!(js.starts_with("window.__itervoxSessions&&window.__itervoxSessions({"));
        assert!(js.contains(r#""identifier":"ENG-1""#));
        assert!(js.contains("a\\u2028b"));
        assert!(!js.contains('\u{2028}'));
    }

    #[test]
    fn page_actions_are_parsed_from_navigations() {
        assert_eq!(
            page_action(
                "tauri://localhost/__itervox/open-session?daemon=%2Fa%2FWORKFLOW.md&id=sess-1"
            ),
            Some(PageAction::Open {
                daemon: "/a/WORKFLOW.md".into(),
                session: "sess-1".into()
            })
        );
        assert_eq!(
            page_action("http://tauri.localhost/__itervox/ready"),
            Some(PageAction::Ready)
        );
        assert_eq!(page_action("tauri://localhost/sessions.html"), None);
        assert_eq!(
            page_action("tauri://localhost/__itervox/open-session?daemon=x"),
            None
        );
        assert_eq!(
            page_action("tauri://localhost/__itervox/open-session?daemon=x&id=%ZZ"),
            None
        );
    }

    #[test]
    fn session_links_open_the_dashboard_route() {
        assert_eq!(
            session_link("http://127.0.0.1:8090/", "sess-1"),
            "http://127.0.0.1:8090/sessions?id=sess-1"
        );
        assert_eq!(
            session_link("http://127.0.0.1:8090", "a b/c"),
            "http://127.0.0.1:8090/sessions?id=a%20b%2Fc"
        );
        assert_eq!(
            percent_decode(&percent_encode("x/y z?é")).unwrap(),
            "x/y z?é"
        );
    }

    /// A one-shot HTTP server answering `GET /api/v1/sessions` with `body`;
    /// reports the request line and Authorization header it saw.
    fn serve_sessions(body: &'static str) -> (String, std::sync::mpsc::Receiver<String>) {
        use std::io::{BufRead, BufReader, Write};
        let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = l.local_addr().unwrap();
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let (mut s, _) = l.accept().unwrap();
            let mut r = BufReader::new(s.try_clone().unwrap());
            let mut first = String::new();
            r.read_line(&mut first).unwrap();
            let mut auth = String::new();
            loop {
                let mut line = String::new();
                r.read_line(&mut line).unwrap();
                if line == "\r\n" || line.is_empty() {
                    break;
                }
                if line.to_ascii_lowercase().starts_with("authorization:") {
                    auth = line["authorization:".len()..].trim().to_string();
                }
            }
            tx.send(format!("{} {auth}", first.trim())).unwrap();
            write!(
                s,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
        });
        (format!("http://{addr}/"), rx)
    }

    /// #178 acceptance: the Sessions window lists the running itervox
    /// sessions of every daemon — a local one and a remote one read through
    /// its tunnel's local end, each with its own token — with the session
    /// waiting for input badged (status) and sorted first.
    #[test]
    fn sessions_window_lists_sessions_from_local_and_remote_daemons() {
        let (local, local_seen) = serve_sessions(
            r#"{"generatedAt":"x","sessions":[{"id":"sess-l","owner":"itervox","identifier":"LOC-1","status":"running","backend":"claude"}]}"#,
        );
        let (remote, remote_seen) = serve_sessions(
            r#"{"generatedAt":"x","sessions":[{"id":"sess-w","owner":"itervox","identifier":"REM-2","status":"input_required","inputContext":"Which DB?"},{"id":"sess-r","owner":"itervox","identifier":"REM-1","status":"running","workerHost":"gpu-1"}]}"#,
        );
        let jobs = vec![
            SessionsJob {
                key: "/work/app/WORKFLOW.md".into(),
                url: local,
                token: Some("local-token".into()),
                name: "app".into(),
                remote: None,
            },
            SessionsJob {
                key: "remote:prod:/srv/app/WORKFLOW.md".into(),
                url: remote,
                token: Some("remote-token".into()),
                name: "server".into(),
                remote: Some("PROD".into()),
            },
        ];
        let view = read_all(&jobs, crate::client::fetch_sessions);
        assert_eq!(
            local_seen.recv().unwrap(),
            "GET /api/v1/sessions HTTP/1.1 Bearer local-token"
        );
        assert_eq!(
            remote_seen.recv().unwrap(),
            "GET /api/v1/sessions HTTP/1.1 Bearer remote-token"
        );
        assert_eq!((view.total, view.waiting), (3, 1));
        let g: Vec<_> = view
            .groups
            .iter()
            .map(|g| (g.name.as_str(), g.location.as_str()))
            .collect();
        assert_eq!(g, vec![("server", "remote · PROD"), ("app", "local")]);
        let first = &view.groups[0].sessions[0];
        assert_eq!(first.identifier, "REM-2");
        assert!(first.waiting());
        assert_eq!(view.groups[1].sessions[0].key, "sess-l");
    }

    #[test]
    fn an_unreadable_daemon_shows_its_error() {
        let jobs = vec![SessionsJob {
            key: "k".into(),
            url: "http://127.0.0.1:1/".into(),
            token: None,
            name: "down".into(),
            remote: None,
        }];
        let view = read_all(&jobs, |_, _| Err(FetchError::Status(404)));
        assert!(view.groups[0]
            .error
            .as_deref()
            .unwrap()
            .contains("no Sessions API"));
        let view = read_all(&jobs, |_, _| Err(FetchError::Unauthorized));
        assert_eq!(view.groups[0].error.as_deref(), Some("token required"));
    }

    /// #178 step 3: sessions started by hand on a daemon's host are listed
    /// read-only after the daemon's own, named by title or directory, with
    /// why one still counts as running.
    #[test]
    fn sessions_window_lists_hand_started_sessions_after_own() {
        let body = br#"{"generatedAt":"x","sessions":[
          {"id":"hand-1","owner":"external","identifier":"","title":"Add health check","status":"exited","cwd":"/work/app","backend":"claude"},
          {"id":"hand-2","owner":"external","identifier":"","status":"running","cwd":"/work/app/svc","inUseReason":"codex process 42 has the transcript open"},
          {"id":"sess-w","owner":"itervox","identifier":"ENG-2","status":"input_required"},
          {"id":"","owner":"external","identifier":"","status":"exited","cwd":"/x"}
        ]}"#;
        let parsed = parse_sessions(body).unwrap();
        assert_eq!(parsed.len(), 3, "a row with no key is still skipped");
        let view = aggregate(vec![DaemonSessions {
            tmux: None,
            key: "/work/app/WORKFLOW.md".into(),
            name: "app".into(),
            remote: None,
            result: Ok(parsed),
        }]);
        assert_eq!((view.total, view.waiting, view.external), (3, 1, 2));
        let g = &view.groups[0];
        assert_eq!(g.external, 2);
        let order: Vec<_> = g
            .sessions
            .iter()
            .map(|s| (s.label.as_str(), s.external()))
            .collect();
        assert_eq!(
            order,
            vec![("ENG-2", false), ("Add health check", true), ("svc", true)]
        );
        assert_eq!(
            g.sessions[2].in_use_reason,
            "codex process 42 has the transcript open"
        );
        let js = push_script(&view);
        assert!(js.contains(r#""external":2"#));
        assert!(js.contains(r#""inUseReason":"codex process 42 has the transcript open""#));
    }

    /// Drift guard: the route and query parameter the links use are the
    /// ones the dashboard serves.
    #[test]
    fn session_route_matches_the_dashboard() {
        let app = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/App.tsx"
        ))
        .expect("web/src/App.tsx");
        assert!(app.contains(r#"path="/sessions""#));
        let page = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../web/src/pages/Sessions/index.tsx"
        ))
        .expect("web/src/pages/Sessions/index.tsx");
        assert!(page.contains("params.get('id')"));
        assert!(
            page.contains("params.get('new') === '1'"),
            "the dashboard opens the New session form on ?new=1"
        );
    }

    /// #178 tmux: a session in a tmux pane carries the window's "tmux" badge
    /// (and whether the dashboard can control it), a daemon without tmux
    /// shows the install warning instead of "New session…", and a daemon
    /// with tmux offers "New session…", which opens /sessions?new=1.
    #[test]
    fn sessions_window_shows_tmux_badge() {
        let with_tmux = br#"{"generatedAt":"x","tmux":{"available":true,"version":"tmux 3.4","installHint":"brew install tmux","control":true},"sessions":[
          {"id":"hand-1","owner":"external","identifier":"","status":"running","cwd":"/work/app","tmuxPane":{"id":"%3","label":"work:0.0"},"controllable":true},
          {"id":"itervox-abc123","owner":"itervox","kind":"tmux","identifier":"","status":"running","cwd":"/work/app","tmuxPane":null,"controllable":true},
          {"id":"hand-2","owner":"external","identifier":"","status":"exited","cwd":"/work/app","tmuxPane":null,"controllable":false}
        ]}"#;
        let read = parse_read(with_tmux).unwrap();
        assert_eq!(read.tmux.as_ref().map(|t| t.available), Some(true));
        let s = &read.sessions;
        assert!(s[0].tmux && s[0].controllable);
        assert_eq!(s[0].tmux_label, "work:0.0");
        assert!(s[1].tmux, "a session itervox started in tmux is badged too");
        assert!(!s[2].tmux && !s[2].controllable);

        let without = br#"{"generatedAt":"x","tmux":{"available":false,"installHint":"brew install tmux","control":false},"sessions":[]}"#;
        let missing = parse_read(without).unwrap();
        let old = parse_read(br#"{"generatedAt":"x","sessions":[]}"#).unwrap();
        assert_eq!(old.tmux, None, "a daemon without tmux control");
        let daemon = |key: &str, read: SessionsRead| DaemonSessions {
            key: key.into(),
            name: key.into(),
            remote: None,
            result: Ok(read.sessions),
            tmux: read.tmux,
        };
        let view = aggregate(vec![
            daemon("a", read),
            daemon("b", missing),
            daemon("c", old),
        ]);
        let by = |k: &str| view.groups.iter().find(|g| g.daemon == k).unwrap();
        assert!(by("a").new_session && by("a").tmux_warning.is_none());
        assert!(!by("b").new_session);
        assert_eq!(
            by("b").tmux_warning.as_deref(),
            Some("tmux not found — install it to control sessions from here: brew install tmux")
        );
        assert!(!by("c").new_session && by("c").tmux_warning.is_none());
        let js = push_script(&view);
        assert!(js.contains(r#""tmux":true"#));
        assert!(js.contains(r#""tmuxLabel":"work:0.0""#));
        assert!(js.contains(r#""controllable":true"#));
        assert!(js.contains(r#""tmuxWarning":"tmux not found"#));
        assert!(js.contains(r#""newSession":true"#));

        let html = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../dist/sessions.html"
        ))
        .expect("desktop/dist/sessions.html");
        assert!(html.contains("el('span', s.controllable ? 'tmux controllable' : 'tmux', 'tmux')"));
        assert!(html.contains("el('p', 'tmux-warning', g.tmuxWarning)"));
        assert!(html.contains(NEW_SESSION_PATH));
        assert!(html.contains("'New session…'"));

        assert_eq!(
            page_action(
                "tauri://localhost/__itervox/new-session?daemon=%2Fwork%2Fapp%2FWORKFLOW.md"
            ),
            Some(PageAction::NewSession {
                daemon: "/work/app/WORKFLOW.md".into()
            })
        );
        assert_eq!(
            page_action("tauri://localhost/__itervox/new-session?daemon="),
            None
        );
        assert_eq!(
            new_session_link("http://127.0.0.1:8090"),
            "http://127.0.0.1:8090/sessions?new=1"
        );
    }

    /// Drift guard: the page navigates to the paths the window intercepts
    /// and defines the function the push script calls.
    #[test]
    fn page_uses_the_intercepted_paths() {
        let html = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../dist/sessions.html"
        ))
        .expect("desktop/dist/sessions.html");
        assert!(html.contains(OPEN_PATH));
        assert!(html.contains(READY_PATH));
        assert!(html.contains("window.__itervoxSessions ="));
        assert!(
            html.contains("'origin', 'started outside itervox'"),
            "hand-started sessions are labelled in the list"
        );
        assert!(html.contains("s.label"));
    }
}
