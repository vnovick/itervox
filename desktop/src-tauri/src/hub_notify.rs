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
//! routing. #232 makes the same stream the app's one connection to the hub
//! ([`run_loop`], [`HubEvent`]): its `projects` frames wake the refresh,
//! which reads the members through the hub (`hub_source`).

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
    crate::platform::process_alive(pid)
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

/// One frame of the hub's events stream the app acts on (#232). The same
/// connection carries the member count, the merged feed's head and the
/// desktop notifications, so the app holds ONE stream for every member.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum HubEvent {
    /// `event: projects`: the member set or a member's summary changed;
    /// `count` members now.
    Projects(usize),
    /// `event: feed`: the merged feed's head moved.
    Feed(i64),
    /// `event: notification` routed to the desktop channel.
    Notification(NativeNote),
}

#[derive(Deserialize)]
struct CountFrame {
    count: usize,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct FeedFrame {
    head_seq: i64,
}

/// Reads frames from `reader` and hands every hub event to `on`, moving
/// `since` past each notification. Returns when the stream ends or `stop`
/// is set.
pub fn read_events(
    reader: impl Read,
    origin: &str,
    since: &mut Option<i64>,
    stop: &AtomicBool,
    on: &mut dyn FnMut(HubEvent),
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
        match frame.event.as_str() {
            "projects" => {
                if let Ok(f) = serde_json::from_str::<CountFrame>(&frame.data) {
                    on(HubEvent::Projects(f.count));
                }
            }
            "feed" => {
                if let Ok(f) = serde_json::from_str::<FeedFrame>(&frame.data) {
                    on(HubEvent::Feed(f.head_seq));
                }
            }
            "notification" => {
                let Ok(n) = serde_json::from_str::<HubNotification>(&frame.data) else {
                    continue;
                };
                if since.is_some_and(|s| n.id <= s) {
                    continue; // already shown (a replay after a reconnect)
                }
                *since = Some(n.id);
                if let Some(note) = native_note(origin, &n) {
                    on(HubEvent::Notification(note));
                }
            }
            _ => {}
        }
    }
}

/// Reads frames from `reader` and shows every desktop notification, moving
/// `since` past each (the #229 view of [`read_events`]).
pub fn read_notifications(
    reader: impl Read,
    origin: &str,
    since: &mut Option<i64>,
    stop: &AtomicBool,
    show: &mut dyn FnMut(NativeNote),
) -> StreamEnd {
    read_events(reader, origin, since, stop, &mut |ev| {
        if let HubEvent::Notification(n) = ev {
            show(n);
        }
    })
}

/// Opens the hub's events stream once with `token` and hands every event
/// to `on` until it ends (see [`read_events`]).
pub fn stream_events(
    origin: &str,
    token: &str,
    since: &mut Option<i64>,
    stop: &AtomicBool,
    on: &mut dyn FnMut(HubEvent),
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
    read_events(resp.into_body().into_reader(), origin, since, stop, on)
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
    stream_events(origin, token, since, stop, &mut |ev| {
        if let HubEvent::Notification(n) = ev {
            show(n);
        }
    })
}

/// The app's connection to the local hub, shared by the stream thread, the
/// refresh loop and the tray.
#[derive(Default)]
pub struct HubNotify {
    /// True while the events stream is connected (its first frame arrived):
    /// local hub members are then read through the hub, and their
    /// per-daemon alerts left to it.
    pub active: AtomicBool,
    /// The hub's origin and the app's viewer token of the live (or last)
    /// stream, for reading `/hub/api/v1/projects` and opening links.
    pub session: std::sync::Mutex<Option<(String, String)>>,
}

impl HubNotify {
    /// The origin and token while the stream is connected.
    pub fn live_session(&self) -> Option<(String, String)> {
        if !self.active.load(Ordering::Relaxed) {
            return None;
        }
        self.session
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone()
    }
}

/// What the stream loop needs from the app; tests inject fakes.
pub struct LoopDeps<'a> {
    /// The running hub (`hub.lock`), if any.
    pub lock: &'a dyn Fn() -> Option<HubLock>,
    /// The app's hub viewer token, created on first use.
    pub token: &'a dyn Fn() -> Option<String>,
    /// Forgets a token the hub refused.
    pub forget_token: &'a dyn Fn(),
    /// Waits (the app sleeps; tests return at once).
    pub sleep: &'a dyn Fn(Duration),
    /// The stream went up or down: the refresh loop switches between the
    /// hub and per-daemon polling at once.
    pub changed: &'a dyn Fn(bool),
}

/// Longest wait between reconnects to a hub that keeps refusing.
const MAX_BACKOFF: Duration = Duration::from_secs(30);

/// Holds ONE events stream to whichever hub serves, for the app's life (or
/// until `stop`): reconnects with a backoff, follows a new hub after a
/// takeover (resuming its notifications from the start of its ring, since
/// its ids begin again), replaces a refused token, and marks `hub` active
/// only while frames arrive.
pub fn run_loop(
    hub: &HubNotify,
    deps: &LoopDeps<'_>,
    stop: &AtomicBool,
    on: &mut dyn FnMut(HubEvent),
) {
    let mut since = None;
    let mut backoff = Duration::from_secs(2);
    let mut refused = 0u32;
    let mut last: Option<(i32, String)> = None;
    let set_active = |up: bool| {
        if hub.active.swap(up, Ordering::Relaxed) != up {
            (deps.changed)(up);
        }
    };
    while !stop.load(Ordering::Relaxed) {
        let lock = (deps.lock)();
        let token = lock.as_ref().and_then(|_| (deps.token)());
        let (Some(lock), Some(token)) = (lock, token) else {
            set_active(false);
            (deps.sleep)(Duration::from_secs(10));
            continue;
        };
        let origin = lock.origin();
        let ident = (lock.pid, origin.clone());
        if last.as_ref().is_some_and(|l| *l != ident) {
            // A new hub took over: its notification ids start again.
            since = since.map(|_| 0);
            backoff = Duration::from_secs(2);
        }
        last = Some(ident);
        *hub.session
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner) =
            Some((origin.clone(), token.clone()));
        let mut connected = false;
        let end = stream_events(&origin, &token, &mut since, stop, &mut |ev| {
            connected = true;
            set_active(true);
            on(ev);
        });
        set_active(false);
        match end {
            StreamEnd::Stopped => return,
            StreamEnd::Unauthorized => {
                // Revoked or rotated: drop it and create a new one, but not
                // in a loop when every new one is refused.
                (deps.forget_token)();
                since = None;
                refused += 1;
                if refused >= 3 {
                    refused = 0;
                    (deps.sleep)(Duration::from_secs(600));
                }
            }
            StreamEnd::Disconnected(_) => {
                if connected {
                    backoff = Duration::from_secs(2);
                }
                refused = 0;
            }
        }
        if stop.load(Ordering::Relaxed) {
            return;
        }
        (deps.sleep)(backoff);
        backoff = (backoff * 2).min(MAX_BACKOFF);
    }
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
    token_create_command(sidecar, HUB_TOKEN_NAME, "viewer")
}

/// The keychain entry of the app's admin hub token (#232): the token the
/// hub page windows (dashboard, Phones, Notifications) open with. The hub's
/// pairing and notification settings are admin routes; the long-lived
/// events stream keeps the viewer token.
pub const HUB_ADMIN_TOKEN_KEY: &str = "itervox-hub-admin";

/// The name of the admin hub token the app creates for its hub windows.
pub const HUB_ADMIN_TOKEN_NAME: &str = "itervox-desktop-admin";

/// `itervox token create --hub --name itervox-desktop-admin --scope admin`,
/// run the first time the user opens a hub page from the tray.
pub fn hub_admin_token_command(sidecar: &Path) -> std::process::Command {
    token_create_command(sidecar, HUB_ADMIN_TOKEN_NAME, "admin")
}

fn token_create_command(sidecar: &Path, name: &str, scope: &str) -> std::process::Command {
    let mut c = std::process::Command::new(sidecar);
    c.args(["token", "create", "--hub", "--name", name, "--scope", scope])
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

    /// A fake hub serving `body` on every events connection; returns its
    /// origin and the request lines it received.
    fn counting_hub(body: String) -> (String, std::sync::Arc<std::sync::Mutex<Vec<String>>>) {
        let ln = TcpListener::bind("127.0.0.1:0").unwrap();
        let origin = format!("http://{}", ln.local_addr().unwrap());
        let seen = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let seen2 = seen.clone();
        std::thread::spawn(move || {
            for s in ln.incoming() {
                let Ok(mut s) = s else { continue };
                let mut req = Vec::new();
                let mut byte = [0u8; 1];
                while !req.ends_with(b"\r\n\r\n") {
                    if s.read(&mut byte).unwrap_or(0) == 0 {
                        break;
                    }
                    req.push(byte[0]);
                }
                let text = String::from_utf8_lossy(&req).into_owned();
                seen2
                    .lock()
                    .unwrap()
                    .push(text.lines().next().unwrap_or("").to_string());
                let _ = write!(
                    s,
                    "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n{body}"
                );
            }
        });
        (origin, seen)
    }

    fn lock_for(origin: &str, pid: i32) -> HubLock {
        let (host, port) = origin
            .trim_start_matches("http://")
            .rsplit_once(':')
            .unwrap();
        HubLock {
            pid,
            url: format!("{origin}/hub/"),
            host: host.into(),
            port: port.parse().unwrap(),
            version: "v0.2.9".into(),
        }
    }

    /// #232 — the app holds ONE events stream for the whole hub, however
    /// many members it has: the member count, the merged feed's head and
    /// the desktop notifications of all three projects arrive on the same
    /// connection; the stream marks the hub active while frames arrive (the
    /// refresh then reads the members through the hub) and inactive when it
    /// ends (per-daemon polling until a hub serves again).
    #[test]
    fn one_sse_connection_for_all_members() {
        let note = |id: i64, project: &str| {
            format!(
                "event: notification\nid: {id}\ndata: {{\"id\":{id},\"channel\":\"desktop\",\"kind\":\"input_required\",\"project\":\"{project}\",\"identifier\":\"ENG-{id}\",\"title\":\"ENG-{id} needs input\",\"url\":\"/hub/p/{project}/?issue=ENG-{id}\"}}\n\n"
            )
        };
        let body = [
            "event: projects\ndata: {\"count\":3}\n\n".to_string(),
            "event: feed\ndata: {\"headSeq\":7}\n\n".to_string(),
            note(1, "alpha"),
            note(2, "beta"),
            note(3, "gamma"),
            "event: feed\ndata: {\"headSeq\":9}\n\n".to_string(),
        ]
        .concat();
        let (origin, seen) = counting_hub(body);
        let hub = HubNotify::default();
        let stop = AtomicBool::new(false);
        let changes = std::sync::Mutex::new(Vec::new());
        let token = ["hub", "viewer", "fixture"].join("-");
        let me = std::process::id() as i32;
        // A loop that reconnected would ask again: allow one reconnect (so
        // a second connection shows in `requests`), then stop.
        let lock_calls = std::sync::atomic::AtomicUsize::new(0);
        let deps = LoopDeps {
            lock: &|| {
                if lock_calls.fetch_add(1, Ordering::SeqCst) < 2 {
                    Some(lock_for(&origin, me))
                } else {
                    stop.store(true, Ordering::Relaxed);
                    None
                }
            },
            token: &|| Some(token.clone()),
            forget_token: &|| panic!("the token was accepted"),
            sleep: &|_| {},
            changed: &|up| changes.lock().unwrap().push(up),
        };
        let mut events = Vec::new();
        let mut active_during = Vec::new();
        run_loop(&hub, &deps, &stop, &mut |ev| {
            active_during.push(hub.live_session().is_some());
            if ev == HubEvent::Feed(9) {
                stop.store(true, Ordering::Relaxed);
            }
            events.push(ev);
        });
        let requests = seen.lock().unwrap().clone();
        assert_eq!(
            requests,
            ["GET /hub/api/v1/events HTTP/1.1"],
            "one events connection carries every member"
        );
        let projects: Vec<String> = events
            .iter()
            .filter_map(|e| match e {
                HubEvent::Notification(n) => Some(n.project.clone()),
                _ => None,
            })
            .collect();
        assert_eq!(projects, ["alpha", "beta", "gamma"]);
        assert_eq!(events[0], HubEvent::Projects(3));
        assert_eq!(events[1], HubEvent::Feed(7));
        assert!(
            active_during.iter().all(|a| *a),
            "active while frames arrive"
        );
        assert_eq!(*changes.lock().unwrap(), [true, false]);
        assert!(!hub.active.load(Ordering::Relaxed));
        assert_eq!(hub.live_session(), None, "no live session once it ends");
    }

    /// After a takeover (another pid holds hub.lock) the loop reconnects to
    /// the new hub and reads its notifications from the start of its ring:
    /// the new hub's ids begin again.
    #[test]
    fn follows_a_new_hub_after_takeover() {
        let body = "event: notification\nid: 5\ndata: {\"id\":5,\"channel\":\"desktop\",\"kind\":\"test\",\"title\":\"t\"}\n\n".to_string();
        let (origin, seen) = counting_hub(body);
        let hub = HubNotify::default();
        let stop = AtomicBool::new(false);
        let calls = std::sync::atomic::AtomicI32::new(0);
        let me = std::process::id() as i32;
        let deps = LoopDeps {
            // The first hub, then a new one (another pid) after it stops.
            lock: &|| {
                let n = calls.fetch_add(1, Ordering::SeqCst);
                Some(lock_for(&origin, if n == 0 { me } else { me + 1 }))
            },
            token: &|| Some("t".into()),
            forget_token: &|| {},
            sleep: &|_| {},
            changed: &|_| {},
        };
        let mut shown = 0;
        run_loop(&hub, &deps, &stop, &mut |_| {
            shown += 1;
            if shown == 2 {
                stop.store(true, Ordering::Relaxed);
            }
        });
        let requests = seen.lock().unwrap().clone();
        assert_eq!(
            requests,
            [
                "GET /hub/api/v1/events HTTP/1.1",
                "GET /hub/api/v1/events?notify_since=0 HTTP/1.1"
            ]
        );
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
