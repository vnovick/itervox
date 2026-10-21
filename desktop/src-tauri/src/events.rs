//! Notification-event detection: which input-required runs and failed runs
//! are new since the previous poll of a daemon.

use std::collections::{HashMap, HashSet, VecDeque};

use crate::daemon_state::StateSummary;

/// What an alert is about.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AlertKind {
    InputRequired,
    RunFailed,
}

/// One notification to raise.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Alert {
    pub kind: AlertKind,
    /// The project key (absolute WORKFLOW.md path).
    pub project: String,
    pub project_name: String,
    pub identifier: String,
    /// Short text for the notification body (the agent's question or the
    /// classified failure message), already truncated.
    pub detail: String,
    /// The dashboard deep link that opens the issue.
    pub url: String,
}

impl Alert {
    pub fn title(&self) -> String {
        match self.kind {
            AlertKind::InputRequired => format!("{} needs input", self.identifier),
            AlertKind::RunFailed => format!("{} run failed", self.identifier),
        }
    }

    pub fn body(&self) -> String {
        if self.detail.is_empty() {
            self.project_name.clone()
        } else {
            format!("{} — {}", self.project_name, self.detail)
        }
    }
}

/// Longest notification body detail, in characters.
const MAX_DETAIL_CHARS: usize = 140;

/// The marker agents print to ask for input (internal/agent/events.go,
/// `InputRequiredSentinel`); it is noise in a notification.
const INPUT_SENTINEL: &str = "<!-- itervox:needs-input -->";

fn truncate(s: &str) -> String {
    let one_line = s
        .replace(INPUT_SENTINEL, " ")
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    if one_line.chars().count() <= MAX_DETAIL_CHARS {
        one_line
    } else {
        let mut out: String = one_line.chars().take(MAX_DETAIL_CHARS - 1).collect();
        out.push('…');
        out
    }
}

/// Percent-encodes a query value (RFC 3986 unreserved characters pass).
fn encode_query_value(v: &str) -> String {
    let mut out = String::with_capacity(v.len());
    for b in v.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// The dashboard deep link for an issue: the dashboard opens the issue
/// detail for `?issue=<identifier>` (web/src/hooks/useUrlState.ts,
/// `useIssueUrlSync`).
pub fn issue_deep_link(base_url: &str, identifier: &str) -> String {
    let base = if base_url.ends_with('/') {
        base_url.to_string()
    } else {
        format!("{base_url}/")
    };
    format!("{base}?issue={}", encode_query_value(identifier))
}

/// Seconds since the Unix epoch of an RFC 3339 timestamp
/// (`2026-10-14T08:31:40Z`, `…40.123456789Z`, `…40+02:00`); `None` when it
/// does not parse.
pub fn rfc3339_unix(ts: &str) -> Option<i64> {
    let b = ts.trim().as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't' | b' ') {
        return None;
    }
    let num = |r: std::ops::Range<usize>| -> Option<i64> {
        std::str::from_utf8(b.get(r)?).ok()?.parse().ok()
    };
    let (y, mo, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    let (h, mi, sec) = (num(11..13)?, num(14..16)?, num(17..19)?);
    if !(1..=12).contains(&mo) || !(1..=31).contains(&d) || h > 23 || mi > 59 || sec > 60 {
        return None;
    }
    let mut i = 19;
    if b.get(i) == Some(&b'.') {
        i += 1;
        while b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
    }
    let offset = match b.get(i)? {
        b'Z' | b'z' if i + 1 == b.len() => 0,
        sign @ (b'+' | b'-') if i + 6 == b.len() && b[i + 3] == b':' => {
            let o = num(i + 1..i + 3)? * 3600 + num(i + 4..i + 6)? * 60;
            if *sign == b'+' {
                o
            } else {
                -o
            }
        }
        _ => return None,
    };
    // Days from civil (Howard Hinnant).
    let y = if mo <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (mo + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    Some(days * 86_400 + h * 3600 + mi * 60 + sec - offset)
}

/// How many notified keys are remembered per project.
const MAX_NOTIFIED: usize = 512;

#[derive(Default)]
struct Notified {
    keys: HashSet<String>,
    order: VecDeque<String>,
}

impl Notified {
    /// Records `key`; false when it was already there.
    fn insert(&mut self, key: String) -> bool {
        if self.keys.contains(&key) {
            return false;
        }
        self.keys.insert(key.clone());
        self.order.push_back(key);
        while self.order.len() > MAX_NOTIFIED {
            if let Some(old) = self.order.pop_front() {
                self.keys.remove(&old);
            }
        }
        true
    }
}

/// Decides which input requests and run failures to notify about. An entry
/// is notified once, and only when it happened after the app started
/// (`since`): opening the app does not replay old questions and failures,
/// and a daemon restart that restores its pending questions does not repeat
/// them. An entry is judged by its own timestamp, not by when the app first
/// managed to read the daemon, so a question asked in the seconds before the
/// first successful read of a just-started daemon still notifies.
pub struct EventDetector {
    since: i64,
    notified: HashMap<String, Notified>,
}

impl EventDetector {
    /// `since`: Unix seconds; entries older than this never notify.
    pub fn new(since: i64) -> Self {
        Self {
            since,
            notified: HashMap::new(),
        }
    }

    fn fresh(&self, ts: &str) -> bool {
        rfc3339_unix(ts).is_some_and(|t| t >= self.since)
    }

    /// Records a poll of `project` (reachable at `base_url`) and returns the
    /// alerts not raised before.
    pub fn observe(
        &mut self,
        project: &str,
        project_name: &str,
        base_url: &str,
        state: &StateSummary,
    ) -> Vec<Alert> {
        let mut inputs: Vec<_> = state
            .waiting_for_input()
            .filter(|i| self.fresh(&i.queued_at))
            .collect();
        inputs.sort_by(|a, b| a.identifier.cmp(&b.identifier));
        // A coalesced repeat moves occurredAt, so it counts as a new failure.
        let mut failures: Vec<_> = state
            .run_failures()
            .filter(|f| self.fresh(&f.occurred_at))
            .collect();
        failures.sort_by(|a, b| a.identifier.cmp(&b.identifier));

        let seen = self.notified.entry(project.to_string()).or_default();
        let mut alerts = Vec::new();
        for i in inputs {
            if seen.insert(format!("input\u{0}{}\u{0}{}", i.identifier, i.queued_at)) {
                alerts.push(Alert {
                    kind: AlertKind::InputRequired,
                    project: project.to_string(),
                    project_name: project_name.to_string(),
                    identifier: i.identifier.clone(),
                    detail: truncate(&i.context),
                    url: issue_deep_link(base_url, &i.identifier),
                });
            }
        }
        for f in failures {
            if seen.insert(format!(
                "fail\u{0}{}\u{0}{}\u{0}{}",
                f.kind, f.identifier, f.occurred_at
            )) {
                alerts.push(Alert {
                    kind: AlertKind::RunFailed,
                    project: project.to_string(),
                    project_name: project_name.to_string(),
                    identifier: f.identifier.clone(),
                    detail: truncate(&f.message),
                    url: issue_deep_link(base_url, &f.identifier),
                });
            }
        }
        alerts
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::{Failure, InputRequired};

    fn input(id: &str, at: &str) -> InputRequired {
        InputRequired {
            identifier: id.into(),
            state: "input_required".into(),
            queued_at: at.into(),
            context: "Which   database\nshould I use?\n<!-- itervox:needs-input -->".into(),
        }
    }

    fn failure(id: &str, at: &str) -> Failure {
        Failure {
            kind: "worker_failed".into(),
            identifier: id.into(),
            message: "exit status 1".into(),
            occurred_at: at.into(),
        }
    }

    const P: &str = "/p/WORKFLOW.md";
    const URL: &str = "http://127.0.0.1:8090/";
    /// The app started at 2026-10-14T10:00:00Z.
    const SINCE: &str = "2026-10-14T10:00:00Z";
    const BEFORE: &str = "2026-10-14T09:59:59Z";
    const AFTER: &str = "2026-10-14T10:00:01Z";
    const LATER: &str = "2026-10-14T10:05:00.25Z";

    fn detector() -> EventDetector {
        EventDetector::new(rfc3339_unix(SINCE).unwrap())
    }

    #[test]
    fn entries_from_before_the_app_started_do_not_alert() {
        let mut d = detector();
        let s = StateSummary {
            input_required: vec![input("ENG-1", BEFORE)],
            failures: vec![failure("ENG-2", BEFORE)],
            ..Default::default()
        };
        assert!(d.observe(P, "proj", URL, &s).is_empty());
        assert!(d.observe(P, "proj", URL, &s).is_empty());
    }

    #[test]
    fn new_input_required_raises_an_alert_with_the_issue_link() {
        let mut d = detector();
        assert!(d
            .observe(P, "proj", URL, &StateSummary::default())
            .is_empty());
        let s = StateSummary {
            input_required: vec![input("ENG-7", AFTER)],
            ..Default::default()
        };
        let alerts = d.observe(P, "proj", URL, &s);
        assert_eq!(alerts.len(), 1);
        let a = &alerts[0];
        assert_eq!(a.kind, AlertKind::InputRequired);
        assert_eq!(a.identifier, "ENG-7");
        assert_eq!(a.url, "http://127.0.0.1:8090/?issue=ENG-7");
        assert_eq!(a.title(), "ENG-7 needs input");
        assert_eq!(a.body(), "proj — Which database should I use?");
        // Same entry next poll: no repeat.
        assert!(d.observe(P, "proj", URL, &s).is_empty());
    }

    #[test]
    fn a_question_asked_before_the_first_read_still_alerts() {
        // The daemon was unreadable (starting, token required) when the
        // question was asked; the first successful read reports it.
        let mut d = detector();
        let s = StateSummary {
            input_required: vec![input("ENG-3", AFTER)],
            ..Default::default()
        };
        assert_eq!(d.observe(P, "proj", URL, &s).len(), 1);
    }

    #[test]
    fn answered_input_is_not_an_alert() {
        let mut d = detector();
        let mut i = input("ENG-7", AFTER);
        i.state = "pending_input_resume".into();
        let s = StateSummary {
            input_required: vec![i],
            ..Default::default()
        };
        assert!(d.observe(P, "proj", URL, &s).is_empty());
    }

    #[test]
    fn new_run_failure_raises_an_alert_and_a_repeat_counts_again() {
        let mut d = detector();
        let s1 = StateSummary {
            failures: vec![failure("ENG-3", AFTER)],
            ..Default::default()
        };
        let a = d.observe(P, "proj", URL, &s1);
        assert_eq!(a.len(), 1);
        assert_eq!(a[0].kind, AlertKind::RunFailed);
        assert_eq!(a[0].title(), "ENG-3 run failed");
        assert_eq!(a[0].url, "http://127.0.0.1:8090/?issue=ENG-3");
        let s2 = StateSummary {
            failures: vec![failure("ENG-3", LATER)],
            ..Default::default()
        };
        assert_eq!(d.observe(P, "proj", URL, &s2).len(), 1);
        assert!(d.observe(P, "proj", URL, &s2).is_empty());
        // Tracker failures are not run failures.
        let s3 = StateSummary {
            failures: vec![Failure {
                kind: "tracker_poll".into(),
                identifier: String::new(),
                message: "x".into(),
                occurred_at: LATER.into(),
            }],
            ..Default::default()
        };
        assert!(d.observe(P, "proj", URL, &s3).is_empty());
    }

    #[test]
    fn a_restarted_daemon_does_not_repeat_restored_questions() {
        let mut d = detector();
        let s = StateSummary {
            input_required: vec![input("ENG-1", AFTER)],
            ..Default::default()
        };
        assert_eq!(d.observe(P, "proj", URL, &s).len(), 1);
        // Same entry after a restart (new port): still not repeated.
        assert!(d
            .observe(P, "proj", "http://127.0.0.1:9999/", &s)
            .is_empty());
    }

    #[test]
    fn rfc3339_parses_utc_fractions_and_offsets() {
        assert_eq!(rfc3339_unix("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(rfc3339_unix("2026-10-14T10:00:00Z"), Some(1_791_972_000));
        assert_eq!(
            rfc3339_unix("2026-10-14T10:00:00.999999999Z"),
            Some(1_791_972_000)
        );
        assert_eq!(
            rfc3339_unix("2026-10-14T12:00:00+02:00"),
            Some(1_791_972_000)
        );
        assert_eq!(
            rfc3339_unix("2026-10-14T05:30:00-04:30"),
            Some(1_791_972_000)
        );
        assert_eq!(rfc3339_unix("2024-02-29T00:00:00Z"), Some(1_709_164_800));
        for bad in [
            "",
            "yesterday",
            "2026-13-01T00:00:00Z",
            "2026-10-14T10:00:00",
            "2026-10-14T10:00:00Zjunk",
        ] {
            assert_eq!(rfc3339_unix(bad), None, "{bad}");
        }
    }

    #[test]
    fn unparseable_timestamps_never_alert() {
        let mut d = detector();
        let s = StateSummary {
            input_required: vec![input("ENG-1", "")],
            ..Default::default()
        };
        assert!(d.observe(P, "proj", URL, &s).is_empty());
    }

    #[test]
    fn notified_keys_are_bounded() {
        let mut n = Notified::default();
        assert!(n.insert("first".into()));
        assert!(!n.insert("first".into()));
        for i in 0..MAX_NOTIFIED {
            n.insert(format!("k{i}"));
        }
        assert_eq!(n.order.len(), MAX_NOTIFIED);
        assert!(n.insert("first".into()), "evicted, so it counts as new");
    }

    #[test]
    fn deep_link_encodes_the_identifier() {
        assert_eq!(
            issue_deep_link("http://127.0.0.1:8090", "owner/repo#12"),
            "http://127.0.0.1:8090/?issue=owner%2Frepo%2312"
        );
    }

    /// Drift guard: the sentinel stripped from bodies is the daemon's.
    #[test]
    fn input_sentinel_matches_the_daemon() {
        let src = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../internal/agent/events.go"
        ))
        .expect("internal/agent/events.go");
        assert!(src.contains(&format!(
            "const InputRequiredSentinel = \"{INPUT_SENTINEL}\""
        )));
    }

    #[test]
    fn long_detail_is_truncated() {
        let long = "x".repeat(500);
        let t = truncate(&long);
        assert_eq!(t.chars().count(), MAX_DETAIL_CHARS);
        assert!(t.ends_with('…'));
    }
}
