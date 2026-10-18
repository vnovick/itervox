//! Parsing `itervox status --all --json` (the #119 supervisor contract,
//! docs/api.md "Supervisor contract").

use serde::{Deserialize, Serialize};

/// The `schema_version` this app understands. Fields may be added within a
/// version; a rename, removal or change of meaning bumps it.
pub const SUPPORTED_SCHEMA_VERSION: u32 = 1;

/// One element of `itervox status --json`. Every field defaults, so a record
/// from a newer daemon that adds fields still parses.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct DaemonRecord {
    pub schema_version: u32,
    pub pid: i32,
    pub workflow: String,
    pub project_dir: String,
    pub url: String,
    pub host: String,
    pub port: u16,
    pub version: String,
    pub commit: String,
    pub started_at: String,
    pub token_required: bool,
    pub token_source: String,
    pub token_file: String,
    pub logs_dir: String,
    pub heartbeat: String,
    pub executable: String,
    pub path_source: String,
    pub source: String,
}

/// Token sources (`token_source`).
pub const TOKEN_SOURCE_SUPERVISOR: &str = "supervisor";
pub const TOKEN_SOURCE_GENERATED: &str = "generated";

/// `source` of a daemon found by process scan rather than the registry.
pub const SOURCE_PROCESS_SCAN: &str = "process_scan";

/// Why `status --json` output could not be used.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StatusError {
    /// Not a JSON array of objects.
    Malformed(String),
    /// A record carries a schema version this app does not know.
    UnsupportedSchema(u32),
}

impl std::fmt::Display for StatusError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            StatusError::Malformed(e) => write!(f, "unreadable `itervox status --json` output: {e}"),
            StatusError::UnsupportedSchema(v) => write!(
                f,
                "`itervox status --json` reports schema_version {v}; this app understands {SUPPORTED_SCHEMA_VERSION} — update the app"
            ),
        }
    }
}

/// Parses the stdout of `itervox status --all --json`. Records without a pid
/// or workflow are dropped (they cannot be acted on); a record with an
/// unknown schema version fails the whole parse, because its fields may
/// mean something else.
pub fn parse_status(stdout: &[u8]) -> Result<Vec<DaemonRecord>, StatusError> {
    let records: Vec<DaemonRecord> =
        serde_json::from_slice(stdout).map_err(|e| StatusError::Malformed(e.to_string()))?;
    if let Some(bad) = records
        .iter()
        .find(|r| r.schema_version != SUPPORTED_SCHEMA_VERSION)
    {
        return Err(StatusError::UnsupportedSchema(bad.schema_version));
    }
    Ok(records
        .into_iter()
        .filter(|r| r.pid > 0 && !r.workflow.is_empty())
        .collect())
}

impl DaemonRecord {
    /// The base URL the app talks to and opens, always ending in `/`. A
    /// daemon bound to every interface (`0.0.0.0`, `::`) is reached over
    /// loopback: the webview refuses the unspecified address.
    pub fn base_url(&self) -> String {
        let mut url = self.url.trim().to_string();
        if url.is_empty() && self.port > 0 {
            let host = if self.host.is_empty() {
                "127.0.0.1"
            } else {
                &self.host
            };
            url = if host.contains(':') {
                format!("http://[{host}]:{}/", self.port)
            } else {
                format!("http://{host}:{}/", self.port)
            };
        }
        if url.is_empty() {
            return url;
        }
        url = url
            .replacen("://0.0.0.0:", "://127.0.0.1:", 1)
            .replacen("://[::]:", "://[::1]:", 1);
        if !url.ends_with('/') {
            url.push('/');
        }
        url
    }

    /// Whether the app may read this daemon's API without a token.
    pub fn open_api(&self) -> bool {
        !self.token_required
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const SAMPLE: &str = r#"[
      {
        "schema_version": 1, "pid": 4242,
        "workflow": "/Users/me/proj/WORKFLOW.md", "project_dir": "/Users/me/proj",
        "url": "http://127.0.0.1:8090/", "host": "127.0.0.1", "port": 8090,
        "version": "0.2.5", "commit": "abc", "started_at": "2026-10-14T10:00:00Z",
        "token_required": true, "token_source": "supervisor", "token_file": "",
        "logs_dir": "/Users/me/.itervox/logs/proj", "heartbeat": "/Users/me/proj/.itervox/HEARTBEAT.md",
        "executable": "/Applications/Itervox.app/Contents/MacOS/itervox", "path_source": "login_shell",
        "source": "registry", "some_future_field": {"x": 1}
      },
      {
        "schema_version": 1, "pid": 99, "workflow": "/b/WORKFLOW.md", "project_dir": "/b",
        "url": "", "host": "", "port": 0, "version": "", "commit": "", "started_at": "",
        "token_required": true, "token_source": "unknown", "token_file": "", "logs_dir": "",
        "heartbeat": "/b/.itervox/HEARTBEAT.md", "executable": "", "path_source": "", "source": "process_scan"
      }
    ]"#;

    #[test]
    fn parses_every_record_including_unknown_fields() {
        let recs = parse_status(SAMPLE.as_bytes()).unwrap();
        assert_eq!(recs.len(), 2);
        assert_eq!(recs[0].pid, 4242);
        assert_eq!(recs[0].workflow, "/Users/me/proj/WORKFLOW.md");
        assert_eq!(recs[0].token_source, TOKEN_SOURCE_SUPERVISOR);
        assert_eq!(recs[0].path_source, "login_shell");
        assert_eq!(recs[1].source, "process_scan");
    }

    #[test]
    fn empty_array_is_no_daemons() {
        assert_eq!(parse_status(b"[]\n").unwrap(), vec![]);
    }

    #[test]
    fn rejects_non_array_and_unknown_schema() {
        assert!(matches!(
            parse_status(b"null"),
            Err(StatusError::Malformed(_))
        ));
        assert!(matches!(
            parse_status(b"usage: itervox"),
            Err(StatusError::Malformed(_))
        ));
        let v2 = r#"[{"schema_version":2,"pid":1,"workflow":"/a/WORKFLOW.md"}]"#;
        assert_eq!(
            parse_status(v2.as_bytes()),
            Err(StatusError::UnsupportedSchema(2))
        );
    }

    #[test]
    fn drops_records_that_cannot_be_acted_on() {
        let raw = r#"[{"schema_version":1,"pid":0,"workflow":"/a/WORKFLOW.md"},
                      {"schema_version":1,"pid":5,"workflow":""}]"#;
        assert!(parse_status(raw.as_bytes()).unwrap().is_empty());
    }

    #[test]
    fn base_url_normalises_wildcard_binds_and_trailing_slash() {
        let mut r = DaemonRecord {
            url: "http://0.0.0.0:8090/".into(),
            ..Default::default()
        };
        assert_eq!(r.base_url(), "http://127.0.0.1:8090/");
        r.url = "http://[::]:9000".into();
        assert_eq!(r.base_url(), "http://[::1]:9000/");
        r.url = String::new();
        r.host = "127.0.0.1".into();
        r.port = 8091;
        assert_eq!(r.base_url(), "http://127.0.0.1:8091/");
        r.port = 0;
        assert_eq!(r.base_url(), "");
    }

    #[test]
    fn open_api_only_without_a_required_token() {
        let mut r = DaemonRecord {
            token_required: true,
            token_source: "env".into(),
            ..Default::default()
        };
        assert!(!r.open_api());
        r.token_required = false;
        assert!(r.open_api());
    }
}
