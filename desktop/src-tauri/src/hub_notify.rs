//! Native notifications from the itervox hub (#229).
//!
//! When a hub runs on this machine (#228: it publishes its address in
//! `~/.itervox/daemons/hub.lock`), the app holds ONE connection to the
//! hub's `GET /hub/api/v1/events` stream instead of raising alerts from
//! each daemon's polled state. The hub routes: every `event: notification`
//! frame it sends is one its notification settings
//! (`~/.itervox/hub/notifications.json`) send to the desktop channel —
//! per project, kind and snooze, with low-priority kinds already batched
//! into digests. The app only shows what arrives; it re-implements no
//! routing. The full "the app uses the hub" (projects, tray, fallback) is
//! #232; this module is the notification path and the hub detection it
//! needs.

use std::io::{BufRead, BufReader, Read};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use serde::Deserialize;

/// The hub lock inside the daemon registry directory (internal/hub
/// `LockFileName`).
pub const HUB_LOCK_FILE: &str = "hub.lock";

/// The keychain entry that holds the app's hub token (a viewer token of
/// the hub's own store, `itervox token create --hub`).
pub const HUB_TOKEN_KEY: &str = "itervox-hub";

/// The channel whose notifications the app shows (internal/hub
/// `ChannelDesktop`).
const DESKTOP_CHANNEL: &str = "desktop";

/// Longest SSE line the app accepts; a longer one ends the stream.
const MAX_LINE_BYTES: usize = 256 * 1024;

/// What `hub.lock` says while a hub holds it (internal/hub `LockInfo`).
#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct HubLock {
    pub pid: i32,
    pub url: String,
    pub host: String,
    pub port: u16,
    #[serde(default)]
    pub version: String,
}

impl HubLock {
    /// The hub's origin, `http://host:port`.
    pub fn origin(&self) -> String {
        if self.host.contains(':') {
            format!("http://[{}]:{}", self.host, self.port)
        } else {
            format!("http://{}:{}", self.host, self.port)
        }
    }
}

fn pid_alive(pid: i32) -> bool {
    if pid <= 0 {
        return false;
    }
    // SAFETY: kill with signal 0 only checks that the process exists.
    let rc = unsafe { libc::kill(pid, 0) };
    rc == 0 || std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

/// The running hub, from `hub.lock` in `registry_dir`: `None` when there is
/// no lock, it is empty (no hub serves) or its process is gone.
pub fn read_hub_lock(registry_dir: &Path) -> Option<HubLock> {
    let data = std::fs::read_to_string(registry_dir.join(HUB_LOCK_FILE)).ok()?;
    let lock: HubLock = serde_json::from_str(data.trim()).ok()?;
    (lock.port != 0 && pid_alive(lock.pid)).then_some(lock)
}

/// One notification frame (internal/hub `Notification`).
#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct HubNotification {
    pub id: i64,
    pub channel: String,
    pub kind: String,
    #[serde(default)]
    pub urgent: bool,
    #[serde(default)]
    pub project: String,
    #[serde(default)]
    pub identifier: String,
    pub title: String,
    #[serde(default)]
    pub body: String,
    /// Hub-relative dashboard link (`/hub/p/{project}/?issue=ID`).
    #[serde(default)]
    pub url: String,
}

/// A native notification to show.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NativeNote {
    /// The hub's stream id of the notification.
    pub id: i64,
    pub title: String,
    pub body: String,
    /// The project name (the `/hub/p/{project}` segment); empty for a
    /// digest across projects or a test.
    pub project: String,
    /// The absolute dashboard URL on the hub.
    pub url: String,
}

/// Turns a hub notification into a native one. Only desktop-channel
/// notifications are shown; a link that is not a hub path opens the hub.
pub fn native_note(origin: &str, n: &HubNotification) -> Option<NativeNote> {
    if n.channel != DESKTOP_CHANNEL || n.title.trim().is_empty() {
        return None;
    }
    let path = if n.url.starts_with("/hub/") && !n.url.contains("//") {
        n.url.as_str()
    } else {
        "/hub/"
    };
    Some(NativeNote {
        id: n.id,
        title: n.title.clone(),
        body: n.body.clone(),
        project: n.project.clone(),
        url: format!("{}{path}", origin.trim_end_matches('/')),
    })
}

/// One SSE frame.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SseFrame {
    pub event: String,
    pub id: String,
    pub data: String,
}

/// A line-at-a-time SSE parser (the subset the hub sends: `event`, `id`,
/// `data`, comments).
#[derive(Debug, Default)]
pub struct SseParser {
    cur: SseFrame,
}

impl SseParser {
    /// Feeds one line (without its line ending); a blank line ends a frame.
    pub fn push_line(&mut self, line: &str) -> Option<SseFrame> {
        if line.is_empty() {
            let frame = std::mem::take(&mut self.cur);
            return (!frame.data.is_empty() || !frame.event.is_empty()).then_some(frame);
        }
        if line.starts_with(':') {
            return None;
        }
        let (field, value) = line.split_once(':').unwrap_or((line, ""));
        let value = value.strip_prefix(' ').unwrap_or(value);
        match field {
            "event" => self.cur.event = value.to_string(),
            "id" => self.cur.id = value.to_string(),
            "data" => {
                if !self.cur.data.is_empty() {
                    self.cur.data.push('\n');
                }
                self.cur.data.push_str(value);
            }
            _ => {}
        }
        None
    }
}

/// Why a stream ended.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StreamEnd {
    /// The stop flag was set.
    Stopped,
    /// The hub refused the token (401): a new one is needed.
    Unauthorized,
    /// The hub closed the stream or could not be reached: retry later.
    Disconnected(String),
}

/// The events URL, resuming after `since` when the app has seen a
/// notification already (nothing old is replayed on a first connect).
pub fn events_url(origin: &str, since: Option<i64>) -> String {
    let base = format!("{}/hub/api/v1/events", origin.trim_end_matches('/'));
    match since {
        Some(id) => format!("{base}?notify_since={id}"),
        None => base,
    }
}

fn stream_agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        // Long-lived stream: no global or body timeout (the hub sends a
        // keep-alive comment every 25 s and closes the stream when it stops).
        .timeout_connect(Some(Duration::from_secs(5)))
        .timeout_recv_response(Some(Duration::from_secs(10)))
        .http_status_as_error(false)
        .proxy(None)
        .build()
        .into()
}

/// Reads frames from `reader` and shows every desktop notification, moving
/// `since` past each. Returns when the stream ends or `stop` is set.
pub fn read_notifications(
    reader: impl Read,
    origin: &str,
    since: &mut Option<i64>,
    stop: &AtomicBool,
    show: &mut dyn FnMut(NativeNote),
) -> StreamEnd {
    let mut lines = BufReader::new(reader);
    let mut parser = SseParser::default();
    let mut buf = Vec::new();
    loop {
        if stop.load(Ordering::Relaxed) {
            return StreamEnd::Stopped;
        }
        buf.clear();
        match (&mut lines)
            .take(MAX_LINE_BYTES as u64)
            .read_until(b'\n', &mut buf)
        {
            Ok(0) => return StreamEnd::Disconnected("the hub closed the stream".into()),
            Ok(_) if !buf.ends_with(b"\n") && buf.len() >= MAX_LINE_BYTES => {
                return StreamEnd::Disconnected("an event line is too long".into())
            }
            Ok(_) => {}
            Err(e) => return StreamEnd::Disconnected(e.to_string()),
        }
        let line = String::from_utf8_lossy(&buf);
        let line = line.trim_end_matches(['\n', '\r']);
        let Some(frame) = parser.push_line(line) else {
            continue;
        };
        if frame.event != "notification" {
            continue;
        }
        let Ok(n) = serde_json::from_str::<HubNotification>(&frame.data) else {
            continue;
        };
        if since.is_some_and(|s| n.id <= s) {
            continue; // already shown (a replay after a reconnect)
        }
        *since = Some(n.id);
        if let Some(note) = native_note(origin, &n) {
            show(note);
        }
    }
}

/// Opens the hub's events stream once with `token` and shows its desktop
/// notifications until it ends (see [`read_notifications`]).
pub fn stream_notifications(
    origin: &str,
    token: &str,
    since: &mut Option<i64>,
    stop: &AtomicBool,
    show: &mut dyn FnMut(NativeNote),
) -> StreamEnd {
    let resp = stream_agent()
        .get(&events_url(origin, *since))
        .header("Authorization", &format!("Bearer {token}"))
        .header("Accept", "text/event-stream")
        .call();
    let resp = match resp {
        Ok(r) => r,
        Err(e) => return StreamEnd::Disconnected(e.to_string()),
    };
    match resp.status().as_u16() {
        200 => {}
        401 => return StreamEnd::Unauthorized,
        other => return StreamEnd::Disconnected(format!("status {other}")),
    }
    read_notifications(resp.into_body().into_reader(), origin, since, stop, show)
}

/// Drops the per-daemon alerts the hub covers while its stream is up: those
/// of local projects that are hub members. Alerts of `--no-hub` daemons,
/// older daemons and remote daemons are kept.
pub fn leave_to_hub(
    alerts: &mut Vec<crate::events::Alert>,
    records: &[crate::status::DaemonRecord],
) {
    let members: std::collections::HashSet<&str> = records
        .iter()
        .filter(|r| r.hub_member)
        .map(|r| r.workflow.as_str())
        .collect();
    alerts.retain(|a| !members.contains(a.project.as_str()));
}

/// The name of the hub token the app creates for itself.
pub const HUB_TOKEN_NAME: &str = "itervox-desktop";

/// `itervox token create --hub --name itervox-desktop --scope viewer`: a
/// hub viewer token for the events stream (stdout is the secret alone).
pub fn hub_token_command(sidecar: &Path) -> std::process::Command {
    let mut c = std::process::Command::new(sidecar);
    c.args([
        "token",
        "create",
        "--hub",
        "--name",
        HUB_TOKEN_NAME,
        "--scope",
        "viewer",
    ])
    .stdin(std::process::Stdio::null())
    .stdout(std::process::Stdio::piped())
    .stderr(std::process::Stdio::piped());
    c
}

/// The secret printed by [`hub_token_command`]: its one non-empty stdout
/// line.
pub fn parse_created_token(stdout: &[u8]) -> Option<String> {
    let text = String::from_utf8_lossy(stdout);
    let mut lines = text.lines().map(str::trim).filter(|l| !l.is_empty());
    let token = lines.next()?;
    (lines.next().is_none() && !token.contains(char::is_whitespace)).then(|| token.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::net::TcpListener;
    use std::sync::mpsc;

    /// A fake hub: answers one GET with `body` as an SSE stream and reports
    /// the request it received.
    fn fake_hub(status: &'static str, body: String) -> (String, mpsc::Receiver<String>) {
        let ln = TcpListener::bind("127.0.0.1:0").unwrap();
        let origin = format!("http://{}", ln.local_addr().unwrap());
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let (mut s, _) = ln.accept().unwrap();
            let mut req = Vec::new();
            let mut byte = [0u8; 1];
            while !req.ends_with(b"\r\n\r\n") {
                if s.read(&mut byte).unwrap() == 0 {
                    break;
                }
                req.push(byte[0]);
            }
            tx.send(String::from_utf8_lossy(&req).into_owned()).unwrap();
            let _ = write!(
                s,
                "HTTP/1.1 {status}\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n{body}"
            );
        });
        (origin, rx)
    }

    /// #229 — the hub's merged feed drives native notifications: the app
    /// opens one events stream with its hub token, shows exactly the
    /// desktop notifications the hub routed (an urgent one, a digest),
    /// opens each at its hub dashboard link, ignores the feed/projects
    /// frames and anything not for the desktop channel, and resumes after
    /// the last one it showed.
    #[test]
    fn hub_feed_drives_native_notifications() {
        let body = [
            "event: projects\ndata: {\"count\":2}\n\n",
            "event: feed\ndata: {\"headSeq\":7}\n\n",
            ": ping\n\n",
            "event: notification\nid: 4\ndata: {\"id\":4,\"channel\":\"desktop\",\"kind\":\"guardrail_approval\",\"urgent\":true,\"project\":\"alpha\",\"identifier\":\"ENG-2\",\"title\":\"ENG-2 needs approval\",\"body\":\"alpha\",\"url\":\"/hub/p/alpha/?issue=ENG-2\",\"seq\":7,\"ts\":\"2026-10-22T09:00:00Z\"}\n\n",
            "event: notification\nid: 5\ndata: {\"id\":5,\"channel\":\"slack\",\"kind\":\"run_failed\",\"title\":\"not for the app\",\"ts\":\"2026-10-22T09:00:00Z\"}\n\n",
            "event: notification\nid: 6\ndata: {\"id\":6,\"channel\":\"desktop\",\"kind\":\"digest\",\"title\":\"3 updates\",\"body\":\"across 2 projects\",\"url\":\"/hub/\",\"items\":[{\"title\":\"ENG-1 run failed\"}],\"ts\":\"2026-10-22T09:15:00Z\"}\n\n",
            "event: notification\nid: 4\ndata: {\"id\":4,\"channel\":\"desktop\",\"kind\":\"input_required\",\"title\":\"a replay\",\"ts\":\"2026-10-22T09:00:00Z\"}\n\n",
        ]
        .concat();
        let (origin, req) = fake_hub("200 OK", body);
        let mut since = Some(3);
        let mut shown = Vec::new();
        let end = stream_notifications(
            &origin,
            "hub-viewer-token",
            &mut since,
            &AtomicBool::new(false),
            &mut |n| shown.push(n),
        );
        let request = req.recv().unwrap();
        assert!(
            request.starts_with("GET /hub/api/v1/events?notify_since=3 HTTP/1.1"),
            "{request}"
        );
        assert!(
            request.contains("Authorization: Bearer hub-viewer-token")
                || request.contains("authorization: Bearer hub-viewer-token"),
            "{request}"
        );
        assert!(matches!(end, StreamEnd::Disconnected(_)));
        assert_eq!(
            shown,
            vec![
                NativeNote {
                    id: 4,
                    title: "ENG-2 needs approval".into(),
                    body: "alpha".into(),
                    project: "alpha".into(),
                    url: format!("{origin}/hub/p/alpha/?issue=ENG-2"),
                },
                NativeNote {
                    id: 6,
                    title: "3 updates".into(),
                    body: "across 2 projects".into(),
                    project: String::new(),
                    url: format!("{origin}/hub/"),
                },
            ]
        );
        assert_eq!(
            since,
            Some(6),
            "the next connect resumes after the last one"
        );

        // A refused token is reported, so the app gets a new one.
        let (origin, _req) = fake_hub("401 Unauthorized", String::new());
        let mut since = None;
        let end = stream_notifications(
            &origin,
            "revoked",
            &mut since,
            &AtomicBool::new(false),
            &mut |_| panic!("nothing to show"),
        );
        assert_eq!(end, StreamEnd::Unauthorized);
    }

    #[test]
    fn hub_members_alerts_are_left_to_the_hub() {
        use crate::events::{Alert, AlertKind};
        use crate::status::DaemonRecord;
        let alert = |project: &str| Alert {
            kind: AlertKind::InputRequired,
            project: project.into(),
            project_name: project.into(),
            identifier: "ENG-1".into(),
            detail: String::new(),
            url: String::new(),
        };
        let records = [
            DaemonRecord {
                workflow: "/a/WORKFLOW.md".into(),
                hub_member: true,
                ..Default::default()
            },
            DaemonRecord {
                workflow: "/b/WORKFLOW.md".into(),
                hub_member: false,
                ..Default::default()
            },
        ];
        let mut alerts = vec![
            alert("/a/WORKFLOW.md"),
            alert("/b/WORKFLOW.md"),
            alert("remote:vm/c"),
        ];
        leave_to_hub(&mut alerts, &records);
        let left: Vec<_> = alerts.iter().map(|a| a.project.as_str()).collect();
        assert_eq!(left, ["/b/WORKFLOW.md", "remote:vm/c"]);
    }

    #[test]
    fn hub_lock_is_read_only_while_its_process_lives() {
        let dir = tempfile::tempdir().unwrap();
        assert_eq!(read_hub_lock(dir.path()), None);
        std::fs::write(dir.path().join(HUB_LOCK_FILE), "").unwrap();
        assert_eq!(
            read_hub_lock(dir.path()),
            None,
            "an empty lock: no hub serves"
        );
        let me = std::process::id() as i32;
        std::fs::write(
            dir.path().join(HUB_LOCK_FILE),
            format!("{{\"schema_version\":1,\"pid\":{me},\"url\":\"http://127.0.0.1:8089/hub/\",\"host\":\"127.0.0.1\",\"port\":8089,\"version\":\"v0.2.9\",\"workflow\":\"/w/WORKFLOW.md\",\"started_at\":\"\"}}"),
        )
        .unwrap();
        let lock = read_hub_lock(dir.path()).expect("a live hub");
        assert_eq!(lock.origin(), "http://127.0.0.1:8089");
        std::fs::write(
            dir.path().join(HUB_LOCK_FILE),
            "{\"pid\":2147483646,\"url\":\"\",\"host\":\"127.0.0.1\",\"port\":8089}",
        )
        .unwrap();
        assert_eq!(read_hub_lock(dir.path()), None, "the hub process is gone");
    }

    #[test]
    fn sse_parser_and_links() {
        let mut p = SseParser::default();
        assert_eq!(p.push_line("event: notification"), None);
        assert_eq!(p.push_line("id: 9"), None);
        assert_eq!(p.push_line("data: {\"a\":"), None);
        assert_eq!(p.push_line("data: 1}"), None);
        let f = p.push_line("").unwrap();
        assert_eq!(
            (f.event.as_str(), f.id.as_str(), f.data.as_str()),
            ("notification", "9", "{\"a\":\n1}")
        );
        assert_eq!(p.push_line(": ping"), None);
        assert_eq!(p.push_line(""), None);
        let n = HubNotification {
            id: 1,
            channel: "desktop".into(),
            kind: "test".into(),
            urgent: false,
            project: String::new(),
            identifier: String::new(),
            title: "t".into(),
            body: String::new(),
            url: "https://elsewhere.example/".into(),
        };
        assert_eq!(
            native_note("http://127.0.0.1:8089/", &n).unwrap().url,
            "http://127.0.0.1:8089/hub/"
        );
        assert_eq!(
            events_url("http://h:1", None),
            "http://h:1/hub/api/v1/events"
        );
        assert_eq!(
            parse_created_token(b"secret-123\n").as_deref(),
            Some("secret-123")
        );
        assert_eq!(parse_created_token(b""), None);
        assert_eq!(parse_created_token(b"a\nb\n"), None);
        let c = hub_token_command(Path::new("/bin/itervox"));
        let args: Vec<_> = c
            .get_args()
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        assert_eq!(
            args,
            [
                "token",
                "create",
                "--hub",
                "--name",
                "itervox-desktop",
                "--scope",
                "viewer"
            ]
        );
    }
}
