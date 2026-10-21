//! Reading a daemon's `/api/v1/state` and `/api/v1/sessions` over loopback
//! (or a tunnel's local end).

use std::time::Duration;

use crate::daemon_state::{parse_state, StateSummary};
use crate::sessions::{parse_sessions, Session};

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

fn agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(REQUEST_TIMEOUT))
        .http_status_as_error(false)
        // Loopback only: never route through an HTTP(S)_PROXY from the
        // environment.
        .proxy(None)
        .build()
        .into()
}

/// GET `{base_url}api/v1/state`, with `Authorization: Bearer` when a token
/// is given.
pub fn fetch_state(base_url: &str, token: Option<&str>) -> Result<StateSummary, FetchError> {
    let body = get(base_url, "api/v1/state", token)?;
    parse_state(&body).map_err(FetchError::Parse)
}

/// GET `{base_url}api/v1/sessions` (#178): the daemon's agent sessions.
pub fn fetch_sessions(base_url: &str, token: Option<&str>) -> Result<Vec<Session>, FetchError> {
    let body = get(base_url, "api/v1/sessions", token)?;
    parse_sessions(&body).map_err(FetchError::Parse)
}

/// A bounded loopback GET of `path` under `base_url`.
fn get(base_url: &str, path: &str, token: Option<&str>) -> Result<Vec<u8>, FetchError> {
    let url = format!("{}{path}", ensure_slash(base_url));
    let mut req = agent().get(&url);
    if let Some(t) = token {
        req = req.header("Authorization", &format!("Bearer {t}"));
    }
    let mut resp = req
        .call()
        .map_err(|e| FetchError::Transport(e.to_string()))?;
    match resp.status().as_u16() {
        200 => {}
        401 => return Err(FetchError::Unauthorized),
        other => return Err(FetchError::Status(other)),
    }
    resp.body_mut()
        .with_config()
        .limit(32 << 20)
        .read_to_vec()
        .map_err(|e| FetchError::Transport(e.to_string()))
}

fn ensure_slash(u: &str) -> String {
    if u.ends_with('/') {
        u.to_string()
    } else {
        format!("{u}/")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;

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
}
