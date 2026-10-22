//! The slice of `GET /api/v1/state` the menu bar needs (docs/api.md "State").

use serde::Deserialize;

/// An issue waiting for a human answer (`inputRequired[]`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InputRequired {
    pub identifier: String,
    /// `input_required` (waiting) or `pending_input_resume` (answered,
    /// resuming); only the former needs attention.
    pub state: String,
    pub queued_at: String,
    pub context: String,
}

/// A recorded operator-relevant failure (`recentFailures[]`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Failure {
    pub kind: String,
    pub identifier: String,
    pub message: String,
    pub occurred_at: String,
}

/// What the app keeps from one state snapshot.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StateSummary {
    pub project_name: String,
    pub running: usize,
    pub retrying: usize,
    pub paused: usize,
    pub input_required: Vec<InputRequired>,
    pub failures: Vec<Failure>,
    /// All dispatch is paused (#213: `dispatchPaused` is not null): running
    /// agents continue, nothing new starts.
    pub dispatch_paused: bool,
}

/// `inputRequired[].state` that needs a human.
pub const INPUT_STATE_WAITING: &str = "input_required";

/// `recentFailures[].kind` values that mean an agent run failed.
pub const RUN_FAILURE_KINDS: &[&str] = &["worker_failed", "worker_stalled"];

impl StateSummary {
    /// Issues currently waiting for input.
    pub fn waiting_for_input(&self) -> impl Iterator<Item = &InputRequired> {
        self.input_required
            .iter()
            .filter(|i| i.state == INPUT_STATE_WAITING)
    }

    /// Failures of agent runs on an issue.
    pub fn run_failures(&self) -> impl Iterator<Item = &Failure> {
        self.failures
            .iter()
            .filter(|f| !f.identifier.is_empty() && RUN_FAILURE_KINDS.contains(&f.kind.as_str()))
    }
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RawCounts {
    running: usize,
    retrying: usize,
    paused: usize,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RawInput {
    identifier: String,
    state: String,
    queued_at: String,
    context: String,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RawFailure {
    kind: String,
    identifier: String,
    message: String,
    occurred_at: String,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RawState {
    project_name: String,
    counts: Option<RawCounts>,
    running: Option<Vec<serde_json::Value>>,
    input_required: Option<Vec<RawInput>>,
    recent_failures: Option<Vec<RawFailure>>,
    /// #213: null (or absent, on older daemons) unless paused.
    dispatch_paused: Option<serde_json::Value>,
}

/// Parses a `/api/v1/state` body. Missing fields are empty: an older or
/// newer daemon still yields counts.
pub fn parse_state(body: &[u8]) -> Result<StateSummary, String> {
    let raw: RawState = serde_json::from_slice(body).map_err(|e| e.to_string())?;
    let counts = raw.counts.unwrap_or_default();
    let running = if counts.running > 0 {
        counts.running
    } else {
        raw.running.map(|r| r.len()).unwrap_or(0)
    };
    Ok(StateSummary {
        dispatch_paused: raw.dispatch_paused.is_some_and(|v| v.is_object()),
        project_name: raw.project_name,
        running,
        retrying: counts.retrying,
        paused: counts.paused,
        input_required: raw
            .input_required
            .unwrap_or_default()
            .into_iter()
            .filter(|i| !i.identifier.is_empty())
            .map(|i| InputRequired {
                identifier: i.identifier,
                state: i.state,
                queued_at: i.queued_at,
                context: i.context,
            })
            .collect(),
        failures: raw
            .recent_failures
            .unwrap_or_default()
            .into_iter()
            .map(|f| Failure {
                kind: f.kind,
                identifier: f.identifier,
                message: f.message,
                occurred_at: f.occurred_at,
            })
            .collect(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_counts_input_and_failures() {
        let body = br#"{
          "generatedAt": "2026-10-14T10:00:00Z",
          "projectName": "web-app",
          "counts": {"running": 2, "retrying": 1, "paused": 0},
          "running": [{}, {}],
          "inputRequired": [
            {"identifier": "ENG-1", "sessionId": "s", "state": "input_required", "context": "Which DB?", "queuedAt": "2026-10-14T09:59:00Z"},
            {"identifier": "ENG-2", "sessionId": "t", "state": "pending_input_resume", "context": "", "queuedAt": "2026-10-14T09:58:00Z"}
          ],
          "recentFailures": [
            {"kind": "worker_failed", "identifier": "ENG-3", "message": "exit 1", "occurredAt": "2026-10-14T09:57:00Z", "recordedAt": "x", "count": 1},
            {"kind": "tracker_poll", "message": "timeout", "occurredAt": "2026-10-14T09:56:00Z", "recordedAt": "x", "count": 3}
          ]
        }"#;
        let s = parse_state(body).unwrap();
        assert_eq!(s.project_name, "web-app");
        assert_eq!((s.running, s.retrying, s.paused), (2, 1, 0));
        let waiting: Vec<_> = s
            .waiting_for_input()
            .map(|i| i.identifier.as_str())
            .collect();
        assert_eq!(waiting, vec!["ENG-1"]);
        let failed: Vec<_> = s.run_failures().map(|f| f.identifier.as_str()).collect();
        assert_eq!(failed, vec!["ENG-3"]);
    }

    #[test]
    fn reads_the_dispatch_pause() {
        let paused =
            parse_state(br#"{"dispatchPaused": {"reason": "deploy", "pausedAt": "t"}}"#).unwrap();
        assert!(paused.dispatch_paused);
        let running = parse_state(br#"{"dispatchPaused": null}"#).unwrap();
        assert!(!running.dispatch_paused);
        assert!(
            !parse_state(b"{}").unwrap().dispatch_paused,
            "an older daemon is not paused"
        );
    }

    #[test]
    fn tolerates_a_minimal_snapshot() {
        let s = parse_state(br#"{"running": [{}, {}, {}]}"#).unwrap();
        assert_eq!(s.running, 3);
        assert!(s.input_required.is_empty());
        assert!(parse_state(b"not json").is_err());
    }
}
