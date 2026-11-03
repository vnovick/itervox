//! Reading a daemon's `/api/v1/state` and `/api/v1/sessions` over loopback
//! (or a tunnel's local end).
//!
//! The refresh loop reads each daemon through a [`DaemonClient`] (#212): one
//! keep-alive ureq agent per daemon connection, and a conditional
//! `GET /api/v1/summary` first. While the summary's ETag is unchanged the
//! daemon answers 304 and the client hands back the state it read last,
//! without fetching `/api/v1/state` again.

use std::collections::HashMap;
use std::sync::{Arc, Mutex, OnceLock, PoisonError};
use std::time::Duration;

use crate::daemon_state::{parse_state, StateSummary};
use crate::sessions::{parse_read, SessionsRead};

/// Per-request bound; a hung daemon must not stall the refresh loop.
pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(2);

/// Why the state could not be read.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FetchError {
    /// 401: the token is missing or wrong.
    Unauthorized,
    /// Any other non-200 answer.
    Status(u16),
    /// Connection refused, timeout, …
    Transport(String),
    /// The body was not a state snapshot.
    Parse(String),
}

impl std::fmt::Display for FetchError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            FetchError::Unauthorized => write!(f, "token required"),
            FetchError::Status(c) => write!(f, "HTTP {c}"),
            FetchError::Transport(e) => write!(f, "unreachable: {e}"),
            FetchError::Parse(e) => write!(f, "unreadable state: {e}"),
        }
    }
}

fn new_agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(REQUEST_TIMEOUT))
        .http_status_as_error(false)
        // Loopback only: never route through an HTTP(S)_PROXY from the
        // environment.
        .proxy(None)
        .build()
        .into()
}

/// The agent of the one-off reads (the Sessions window); its connection
/// pool keeps one connection per daemon alive between them.
fn shared_agent() -> &'static ureq::Agent {
    static AGENT: OnceLock<ureq::Agent> = OnceLock::new();
    AGENT.get_or_init(new_agent)
}

/// GET `{base_url}api/v1/state`, with `Authorization: Bearer` when a token
/// is given. Unconditional; the refresh loop uses [`DaemonClient`].
pub fn fetch_state(base_url: &str, token: Option<&str>) -> Result<StateSummary, FetchError> {
    let body = get_body(shared_agent(), base_url, "api/v1/state", token)?;
    parse_state(&body).map_err(FetchError::Parse)
}

/// GET `{base_url}api/v1/sessions` (#178): the daemon's agent sessions.
pub fn fetch_sessions(base_url: &str, token: Option<&str>) -> Result<SessionsRead, FetchError> {
    let body = get_body(shared_agent(), base_url, "api/v1/sessions", token)?;
    parse_read(&body).map_err(FetchError::Parse)
}

/// POST `{base_url}api/v1/dispatch/pause` (or `/resume`) (#213): pause or
/// resume ALL dispatch of one daemon. Running agents continue either way.
pub fn set_dispatch_paused(
    base_url: &str,
    token: Option<&str>,
    paused: bool,
) -> Result<(), FetchError> {
    let path = if paused {
        "api/v1/dispatch/pause"
    } else {
        "api/v1/dispatch/resume"
    };
    let url = format!("{}{path}", ensure_slash(base_url));
    let mut req = shared_agent().post(&url);
    if let Some(t) = token {
        req = req.header("Authorization", &format!("Bearer {t}"));
    }
    let mut resp = req
        .send_empty()
        .map_err(|e| FetchError::Transport(e.to_string()))?;
    let status = resp.status().as_u16();
    let _ = resp.body_mut().with_config().limit(1 << 20).read_to_vec();
    match status {
        200 => Ok(()),
        401 => Err(FetchError::Unauthorized),
        other => Err(FetchError::Status(other)),
    }
}

/// One answer: status, ETag header and body (empty for a 304).
struct Answer {
    status: u16,
    etag: Option<String>,
    body: Vec<u8>,
}

/// A bounded GET of `path` under `base_url`. The body is read to the end,
/// so the connection goes back to the agent's pool.
fn request(
    agent: &ureq::Agent,
    base_url: &str,
    path: &str,
    token: Option<&str>,
    if_none_match: Option<&str>,
) -> Result<Answer, FetchError> {
    let url = format!("{}{path}", ensure_slash(base_url));
    let mut req = agent.get(&url);
    if let Some(t) = token {
        req = req.header("Authorization", &format!("Bearer {t}"));
    }
    if let Some(tag) = if_none_match {
        req = req.header("If-None-Match", tag);
    }
    let mut resp = req
        .call()
        .map_err(|e| FetchError::Transport(e.to_string()))?;
    let status = resp.status().as_u16();
    let etag = resp
        .headers()
        .get("etag")
        .and_then(|v| v.to_str().ok())
        .map(str::to_string);
    let body = resp
        .body_mut()
        .with_config()
        .limit(32 << 20)
        .read_to_vec()
        .map_err(|e| FetchError::Transport(e.to_string()))?;
    Ok(Answer { status, etag, body })
}

/// [`request`] that wants a 200.
fn get_body(
    agent: &ureq::Agent,
    base_url: &str,
    path: &str,
    token: Option<&str>,
) -> Result<Vec<u8>, FetchError> {
    let a = request(agent, base_url, path, token, None)?;
    match a.status {
        200 => Ok(a.body),
        401 => Err(FetchError::Unauthorized),
        other => Err(FetchError::Status(other)),
    }
}

fn ensure_slash(u: &str) -> String {
    if u.ends_with('/') {
        u.to_string()
    } else {
        format!("{u}/")
    }
}

/// One daemon connection: its keep-alive agent and the state read under
/// the last summary ETag.
pub struct DaemonClient {
    agent: ureq::Agent,
    url: String,
    token: Option<String>,
    cache: Mutex<Option<(String, StateSummary)>>,
}

impl DaemonClient {
    pub fn new(url: &str, token: Option<&str>) -> Self {
        DaemonClient {
            agent: new_agent(),
            url: url.to_string(),
            token: token.map(str::to_string),
            cache: Mutex::new(None),
        }
    }

    /// Whether this client talks to `url` with `token`.
    fn serves(&self, url: &str, token: Option<&str>) -> bool {
        self.url == url && self.token.as_deref() == token
    }

    /// The daemon's state: `GET api/v1/summary` with `If-None-Match`, and
    /// `GET api/v1/state` only when the summary changed (200) or the daemon
    /// predates it (404). A 304 returns the state read last.
    ///
    /// The state is cached under the summary's tag although it is read a
    /// moment later; a change in between shows up as a new tag on the next
    /// poll, so the cache is at most one change behind until then.
    pub fn fetch_state(&self) -> Result<StateSummary, FetchError> {
        let cached_tag = lock(&self.cache).as_ref().map(|(t, _)| t.clone());
        let token = self.token.as_deref();
        let summary = request(
            &self.agent,
            &self.url,
            "api/v1/summary",
            token,
            cached_tag.as_deref(),
        )?;
        let tag = match summary.status {
            304 => {
                if let Some((_, st)) = lock(&self.cache).as_ref() {
                    return Ok(st.clone());
                }
                None
            }
            200 => summary.etag,
            404 => None, // a daemon older than the summary endpoint
            401 => return Err(FetchError::Unauthorized),
            other => return Err(FetchError::Status(other)),
        };
        let body = get_body(&self.agent, &self.url, "api/v1/state", token)?;
        let state = parse_state(&body).map_err(FetchError::Parse)?;
        *lock(&self.cache) = tag.map(|t| (t, state.clone()));
        Ok(state)
    }
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// The refresh loop's clients, one per daemon key. A daemon that moved to
/// another URL or token gets a fresh client (and connection); keys no
/// longer listed are dropped.
#[derive(Default)]
pub struct ClientPool {
    clients: Mutex<HashMap<String, Arc<DaemonClient>>>,
}

impl ClientPool {
    /// The client for `key` at `url` with `token`.
    pub fn client(&self, key: &str, url: &str, token: Option<&str>) -> Arc<DaemonClient> {
        let mut clients = lock(&self.clients);
        match clients.get(key) {
            Some(c) if c.serves(url, token) => Arc::clone(c),
            _ => {
                let c = Arc::new(DaemonClient::new(url, token));
                clients.insert(key.to_string(), Arc::clone(&c));
                c
            }
        }
    }

    /// Drops the clients of every key `keep` rejects.
    pub fn retain(&self, keep: impl Fn(&str) -> bool) {
        lock(&self.clients).retain(|k, _| keep(k));
    }

    pub fn len(&self) -> usize {
        lock(&self.clients).len()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

/// Reads every job's daemon in parallel through `pool` (each read bounded
/// by its timeout) and drops the clients of daemons no longer listed.
pub fn fetch_all(
    pool: &ClientPool,
    jobs: &[crate::model::FetchJob],
) -> HashMap<String, Result<StateSummary, FetchError>> {
    pool.retain(|k| jobs.iter().any(|j| j.key == k));
    std::thread::scope(|s| {
        let handles: Vec<_> = jobs
            .iter()
            .map(|j| {
                let c = pool.client(&j.key, &j.url, j.token.as_deref());
                s.spawn(move || (j.key.clone(), c.fetch_state()))
            })
            .collect();
        handles.into_iter().filter_map(|h| h.join().ok()).collect()
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::sync::{Arc, Mutex};

    /// A one-shot HTTP server that answers with `status`/`body` and reports
    /// the Authorization header it saw.
    fn serve_once(status: &'static str, body: &'static str) -> (String, mpsc::Receiver<String>) {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = l.local_addr().unwrap();
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let (mut s, _) = l.accept().unwrap();
            let mut r = BufReader::new(s.try_clone().unwrap());
            let mut auth = String::new();
            let mut first = String::new();
            r.read_line(&mut first).unwrap();
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
            tx.send(format!("{} {}", first.trim(), auth)).unwrap();
            write!(
                s,
                "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
        });
        (format!("http://{addr}/"), rx)
    }

    /// A keep-alive HTTP server for a daemon with a summary endpoint: the
    /// summary's ETag is `tag` (shared, changeable), 304 when the request's
    /// If-None-Match names it; `summary` false answers it 404 like an older
    /// daemon. Returns the base URL, the request log ("GET /path inm") and
    /// the number of TCP connections accepted.
    type Shared<T> = Arc<Mutex<T>>;

    fn daemon(tag: Shared<String>, summary: bool) -> (String, Shared<Vec<String>>, Shared<usize>) {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = l.local_addr().unwrap();
        let log = Arc::new(Mutex::new(Vec::new()));
        let conns = Arc::new(Mutex::new(0));
        let (log2, conns2) = (Arc::clone(&log), Arc::clone(&conns));
        std::thread::spawn(move || {
            for s in l.incoming() {
                let Ok(mut s) = s else { return };
                *conns2.lock().unwrap() += 1;
                let (tag, log) = (Arc::clone(&tag), Arc::clone(&log2));
                std::thread::spawn(move || {
                    let mut r = BufReader::new(s.try_clone().unwrap());
                    loop {
                        let mut first = String::new();
                        if r.read_line(&mut first).unwrap_or(0) == 0 {
                            return;
                        }
                        let mut inm = String::new();
                        loop {
                            let mut line = String::new();
                            r.read_line(&mut line).unwrap();
                            if line == "\r\n" || line.is_empty() {
                                break;
                            }
                            if line.to_ascii_lowercase().starts_with("if-none-match:") {
                                inm = line["if-none-match:".len()..].trim().to_string();
                            }
                        }
                        let path = first.split(' ').nth(1).unwrap_or("").to_string();
                        log.lock()
                            .unwrap()
                            .push(format!("GET {path} {inm}").trim().to_string());
                        let cur = format!("\"{}\"", tag.lock().unwrap());
                        let (status, etag, body) = match path.as_str() {
                            "/api/v1/summary" if !summary => {
                                ("404 Not Found", String::new(), "{}".to_string())
                            }
                            "/api/v1/summary" if inm == cur => {
                                ("304 Not Modified", cur, String::new())
                            }
                            "/api/v1/summary" => ("200 OK", cur, "{\"etag\":\"x\"}".to_string()),
                            _ => {
                                let n: usize = tag.lock().unwrap().len();
                                (
                                    "200 OK",
                                    String::new(),
                                    format!("{{\"counts\":{{\"running\":{n}}}}}"),
                                )
                            }
                        };
                        let etag = if etag.is_empty() {
                            String::new()
                        } else {
                            format!("ETag: {etag}\r\n")
                        };
                        let len = if status.starts_with("304") {
                            String::new()
                        } else {
                            format!("Content-Length: {}\r\n", body.len())
                        };
                        write!(s, "HTTP/1.1 {status}\r\n{etag}{len}\r\n{body}").unwrap();
                    }
                });
            }
        });
        (format!("http://{addr}/"), log, conns)
    }

    #[test]
    fn summary_304_skips_state_fetch() {
        let tag = Arc::new(Mutex::new("a".to_string()));
        let (url, log, conns) = daemon(Arc::clone(&tag), true);
        let c = DaemonClient::new(&url, Some("secret-token-123456"));
        // First poll: summary 200, then the state.
        assert_eq!(c.fetch_state().unwrap().running, 1);
        // Unchanged: 304 and the cached state, no state read.
        assert_eq!(c.fetch_state().unwrap().running, 1);
        assert_eq!(c.fetch_state().unwrap().running, 1);
        // Changed: the state is read again.
        *tag.lock().unwrap() = "bbb".into();
        assert_eq!(c.fetch_state().unwrap().running, 3);
        assert_eq!(
            *log.lock().unwrap(),
            vec![
                "GET /api/v1/summary",
                "GET /api/v1/state",
                "GET /api/v1/summary \"a\"",
                "GET /api/v1/summary \"a\"",
                "GET /api/v1/summary \"a\"",
                "GET /api/v1/state",
            ]
        );
        assert_eq!(
            *conns.lock().unwrap(),
            1,
            "one keep-alive connection for every request"
        );
    }

    #[test]
    fn daemon_without_summary_reads_the_state_every_time() {
        let (url, log, _) = daemon(Arc::new(Mutex::new("a".into())), false);
        let c = DaemonClient::new(&url, None);
        assert_eq!(c.fetch_state().unwrap().running, 1);
        assert_eq!(c.fetch_state().unwrap().running, 1);
        assert_eq!(
            *log.lock().unwrap(),
            vec![
                "GET /api/v1/summary",
                "GET /api/v1/state",
                "GET /api/v1/summary",
                "GET /api/v1/state"
            ]
        );
    }

    #[test]
    fn pool_reuses_a_client_until_its_url_or_token_changes() {
        let pool = ClientPool::default();
        let a = pool.client("/p/WORKFLOW.md", "http://127.0.0.1:1/", Some("t1"));
        let b = pool.client("/p/WORKFLOW.md", "http://127.0.0.1:1/", Some("t1"));
        assert!(Arc::ptr_eq(&a, &b));
        let c = pool.client("/p/WORKFLOW.md", "http://127.0.0.1:1/", Some("t2"));
        assert!(!Arc::ptr_eq(&a, &c), "a new token gets a new client");
        let d = pool.client("/p/WORKFLOW.md", "http://127.0.0.1:2/", Some("t2"));
        assert!(!Arc::ptr_eq(&c, &d), "a new port gets a new client");
        pool.client("/q/WORKFLOW.md", "http://127.0.0.1:3/", None);
        assert_eq!(pool.len(), 2);
        pool.retain(|k| k == "/q/WORKFLOW.md");
        assert_eq!(pool.len(), 1);
    }

    #[test]
    fn sends_the_bearer_token_and_parses_the_state() {
        let (url, seen) = serve_once("200 OK", r#"{"counts":{"running":3}}"#);
        let s = fetch_state(&url, Some("secret-token-123456")).unwrap();
        assert_eq!(s.running, 3);
        assert_eq!(
            seen.recv().unwrap(),
            "GET /api/v1/state HTTP/1.1 Bearer secret-token-123456"
        );
    }

    #[test]
    fn unauthorized_is_reported_as_token_required() {
        let (url, _seen) = serve_once("401 Unauthorized", "{}");
        assert_eq!(fetch_state(&url, None), Err(FetchError::Unauthorized));
        assert_eq!(FetchError::Unauthorized.to_string(), "token required");
    }

    #[test]
    fn other_errors_are_classified() {
        let (url, _seen) = serve_once("500 Internal Server Error", "{}");
        assert_eq!(fetch_state(&url, None), Err(FetchError::Status(500)));
        let (url, _seen) = serve_once("200 OK", "nope");
        assert!(matches!(fetch_state(&url, None), Err(FetchError::Parse(_))));
        // Nothing listens on port 1.
        assert!(matches!(
            fetch_state("http://127.0.0.1:1/", None),
            Err(FetchError::Transport(_))
        ));
    }

    /// #213: the tray's "Pause all agents" / "Resume all agents" POST the
    /// daemon's dispatch endpoints with its token.
    #[test]
    fn pause_and_resume_post_the_dispatch_endpoints() {
        let (url, seen) = serve_once("200 OK", r#"{"dispatchPaused":{"pausedAt":"t"}}"#);
        assert_eq!(
            set_dispatch_paused(&url, Some("secret-token-123456"), true),
            Ok(())
        );
        assert_eq!(
            seen.recv().unwrap(),
            "POST /api/v1/dispatch/pause HTTP/1.1 Bearer secret-token-123456"
        );
        let (url, seen) = serve_once("200 OK", r#"{"dispatchPaused":null}"#);
        assert_eq!(set_dispatch_paused(&url, None, false), Ok(()));
        assert_eq!(
            seen.recv().unwrap(),
            "POST /api/v1/dispatch/resume HTTP/1.1 "
        );
        let (url, _seen) = serve_once("503 Service Unavailable", "{}");
        assert_eq!(
            set_dispatch_paused(&url, None, true),
            Err(FetchError::Status(503))
        );
    }
}
