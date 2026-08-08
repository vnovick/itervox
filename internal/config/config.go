package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/profiles"
	"github.com/vnovick/itervox/internal/workflow"
)

// profiles_Lookup is a thin wrapper that lets parseAgentProfiles call the
// built-in profile registry without colliding with the local profiles map
// variable name elsewhere in this file.
func profiles_Lookup(name string) *profiles.Builtin {
	return profiles.Lookup(name)
}

var envVarRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// LatestWorkflowSchemaVersion is the newest WORKFLOW.md front-matter shape
// accepted for daemon startup.
const LatestWorkflowSchemaVersion = 2

// DefaultDependencyAuditRefreshIntervalMs / TimeoutMs / BatchSize are the
// config-loader defaults for the off-loop dependency-audit refresh (see
// AgentConfig.DependencyAuditRefresh* below). Exported so
// internal/orchestrator can fall back to the same values when clamping a
// non-positive runtime value (Task 6 review Gap F): positiveIntField already
// floors these at load time for anything reachable from WORKFLOW.md, so a
// non-positive value can only originate from a hand-constructed
// config.Config (tests). If it ever reached production unclamped,
// batchSize<=0 would silently disable the refresh and
// context.WithTimeout(ctx, 0<=timeout) would make every batch fetch nothing
// while still arming the throttle. Sharing these constants keeps the
// load-time default and the runtime clamp from drifting apart.
// The interval default is 10 minutes, NOT a poll-interval-scale value, and that
// is deliberate. This throttle is the only thing bounding how often a given
// audit row costs a tracker request, and the dependency audit is one of the
// largest consumers of the tracker's request budget (see issue #42: it was
// ~28% of Linear traffic on a real deployment that sat at zero remaining
// budget for 35 minutes of every hour).
//
// An interval SHORTER than polling.interval_ms never binds — every row is
// eligible on every tick — so the batch cap alone governs, and at
// BatchSize=100 against a 60s poll that is 6000 requests/hour from this path
// alone, roughly 2.4x Linear's documented 2500/hour ceiling. At 10 minutes a
// 59-row audit set costs ~354/hour instead. Raise it if you want fresher
// dependency state and have the budget; do not lower it below
// polling.interval_ms without checking the rate-limit headroom the dashboard
// already reports.
const (
	DefaultDependencyAuditRefreshIntervalMs = 600000
	DefaultDependencyAuditRefreshTimeoutMs  = 30000
	DefaultDependencyAuditRefreshBatchSize  = 100
)

// DefaultDepsAnalyzerTimeoutMs / DefaultDepsAnalyzerChunkSize are exported so
// the analyzer service clamps to the SAME values the loader defaults to. A
// second copy in the service would drift.
const (
	DefaultDepsAnalyzerTimeoutMs = 600000 // 10 min — matches the dashboard's poll deadline
	DefaultDepsAnalyzerChunkSize = 75
)

// DefaultServerPort is the HTTP port bound when `server.port` is absent from
// WORKFLOW.md. A fixed default — not ephemeral — so the dashboard URL survives
// daemon restarts and config reloads; 8090 matches the Vite dev proxy target
// and the scaffolded WORKFLOW.md. An explicit `server.port: 0` still asks the
// OS for a free port, which is the knob for running several daemons at once.
const DefaultServerPort = 8090

// DefaultDependenciesConfidenceThreshold / DefaultDependenciesStalenessHours
// are the config-loader defaults for DependenciesConfig. Exported so callers
// that need to fall back to the same values (e.g. clamping a
// hand-constructed config.Config in tests) do not drift from the load-time
// default.
const (
	DefaultDependenciesConfidenceThreshold = 0.7
	DefaultDependenciesStalenessHours      = 168
)

// DefaultDependenciesAutoAnalyzeMinIntervalMinutes /
// DefaultDependenciesAutoAnalyzeDebounceMinutes are the config-loader
// defaults for auto-analyzer triggering. These values parse via positiveIntField
// so <=0 in YAML becomes the default; unlike escalate_blocked_after_hours,
// there is no meaningful zero here (analyzer must not run every tick).
const (
	DefaultDependenciesAutoAnalyzeMinIntervalMinutes = 60
	DefaultDependenciesAutoAnalyzeDebounceMinutes    = 5
)

// DependenciesOrderingCriticalPath / DependenciesOrderingSimple are the
// accepted values for dependencies.ordering. DefaultDependenciesOrdering is
// the config-loader default (critical-path-aware dispatch ordering).
// DefaultDependenciesEscalateHours is the config-loader default for
// dependencies.escalate_blocked_after_hours: how long an issue may sit
// blocked before automation escalation triggers. An explicit `0` disables
// escalation (a meaningful, deliberately-chosen value) and must NOT be
// treated as "absent" — see the parse site in LoadFromFrontMatter, which
// uses a plain intField + explicit sign check instead of positiveIntField
// specifically to preserve that distinction.
const (
	DependenciesOrderingCriticalPath = "critical_path"
	DependenciesOrderingSimple       = "simple"
	DefaultDependenciesOrdering      = DependenciesOrderingCriticalPath
	DefaultDependenciesEscalateHours = 48
)

// TrackerConfig holds tracker-related configuration.
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	TerminalStates []string
	// WorkingState is the state name to transition an issue to when it is
	// dispatched to an agent (e.g. "In Progress"). Empty string = no transition.
	WorkingState string
	// CompletionState is the state name to transition an issue to when the agent
	// finishes successfully (e.g. "In Review", "Done"). Empty string = no transition.
	// When set, the issue leaves active_states so Itervox stops re-dispatching it.
	CompletionState string
	// BacklogStates are always fetched and shown as the leftmost board column(s).
	// Defaults to ["Backlog"] for linear, [] for github.
	BacklogStates []string
	// FailedState is the state to move issues to when max retries are exhausted.
	// When empty, issues are paused instead of transitioned.
	FailedState string
	// Outbox enables the write-ahead outbox for tracker state transitions and
	// comments: writes are persisted durably and flushed by an independent
	// worker instead of being made synchronously (with inline retries) from
	// the orchestrator's completion/failed-state paths. Default true; set
	// false as a kill switch to restore the old synchronous behavior.
	Outbox bool
}

// PollingConfig holds polling settings.
type PollingConfig struct {
	IntervalMs int
}

// WorkspaceConfig holds workspace settings.
type WorkspaceConfig struct {
	Root string
	// AutoClearWorkspace removes the cloned workspace directory after a task
	// succeeds (reaches the completion state) so disk space is reclaimed
	// automatically. Logs are kept separately and unaffected.
	AutoClearWorkspace bool
	// Worktree enables git worktree mode. When true, Itervox manages
	// per-issue git worktrees inside workspace.root instead of creating
	// one empty directory per issue. Requires the base git clone at
	// workspace.root to already exist. Default: false.
	Worktree bool
	// CloneURL is the git remote URL used to initialise the bare clone when
	// worktree mode is enabled. When empty and worktree is true, the caller
	// must ensure a git repo already exists at Root.
	CloneURL string
	// BaseBranch is the branch worktrees are created from (default: "main").
	BaseBranch string
}

// DefaultReviewerPrompt is used when reviewer_prompt is absent from WORKFLOW.md.
const DefaultReviewerPrompt = `You are an AI code reviewer for issue {{ issue.identifier }}.

Your job:
1. Run: gh pr diff to read the PR changes on branch {{ issue.branch_name }}
2. Review for: correctness, test coverage, edge cases, security issues, code style
3. If you find problems:
   - Fix them directly in the workspace
   - Push the fixes: git add -A && git commit -m "fix: reviewer corrections" && git push
   - Post a comment on the issue summarising what you fixed
   - Move issue {{ issue.identifier }} to state "Rework"
4. If the PR is clean:
   - Post an approval comment: "AI review passed ✓ — no issues found"
   - Move issue {{ issue.identifier }} to state "Merging"

Be concise in your review comments. Focus on real problems, not style nits.`

// AgentProfile holds settings for a named agent profile.
type AgentProfile struct {
	// Command overrides the default agent CLI command (e.g. "claude --model ...").
	Command string
	// Prompt is the legacy inline role description for this sub-agent.
	// Schema 2 rejects this field; it remains for legacy migration and tests.
	Prompt string
	// SoulFile is the configured path to this profile's SOUL.md file, relative
	// to WORKFLOW.md when not absolute.
	SoulFile string
	// InstructionsFile is the configured path to this profile's INSTRUCTIONS.md
	// file, relative to WORKFLOW.md when not absolute.
	InstructionsFile string
	// Soul is the loaded SOUL.md template content for this profile.
	Soul string
	// Instructions is the loaded INSTRUCTIONS.md template content for this profile.
	Instructions string
	// Backend optionally overrides runner selection when it cannot be inferred
	// from the command binary alone (for example, a wrapper script around codex).
	Backend string
	// Enabled controls whether the profile is selectable and dispatchable.
	// Nil means true for backward compatibility with older tests/config literals.
	Enabled *bool
	// AllowedActions grants the profile access to daemon-backed actions such as
	// tracker comments or provide-input. Empty = no extra actions.
	AllowedActions []string
	// CreateIssueState is the tracker state/column used when the create_issue
	// action is allowed for this profile.
	CreateIssueState string
}

// AgentConfig holds agent runner settings.
type AgentConfig struct {
	MaxConcurrentAgents        int
	MaxConcurrentAgentsByState map[string]int
	// PauseDispatchWhenAnyInState lists tracker states (case-insensitive); when
	// ANY tracked issue is in one of these states, no new dispatch may begin
	// regardless of available slots. Use case: pause Todo dispatch while any
	// issue is "In Review" so PRs queue and merge before the next start. Empty
	// disables the guard. Snapshotted into State at tick boundary.
	PauseDispatchWhenAnyInState []string
	// MergeStrategy controls how the daemon-backed `merge_pr` agent action
	// finalizes a pull request: "squash" (default), "rebase", or "merge".
	// Read at request time by the merge_pr handler; misconfigured values
	// fall back to "squash".
	MergeStrategy string
	// MergeBlockLabels lists case-insensitive PR labels that, when present,
	// cause the `merge_pr` action to refuse the merge with reason
	// "blocked_label:<label>". Empty list disables the guard. Default at
	// load time:
	//   ["needs-human", "migration", "auth", "feature-flag", "breaking"]
	MergeBlockLabels []string
	// AllowUncheckedMerge controls the SRV-1 unarmed-gate refusal in the
	// merge_pr agent action. gh's `pr checks --required` exits non-zero with
	// "no required checks reported" on a repo with no branch-protection
	// required checks configured — that is an unarmed gate (spec F3: "a gate
	// that can never fail is not a gate"), so the default (false) refuses the
	// merge with reason "unarmed_gate:...". Set true to proceed anyway,
	// logging a loud warning instead of refusing. Read-only after startup.
	AllowUncheckedMerge bool
	// PreferHighOutdegreeSort, when true, inserts a tiebreaker between
	// priority and createdAt that ranks issues which block more dependent
	// siblings first (P2). Default false to preserve existing behaviour.
	PreferHighOutdegreeSort bool
	// TransportErrorPatterns lists case-insensitive substrings that, when
	// matched against an agent-runner error message, classify the failure
	// as a transient transport error (codex "stream disconnected", network
	// hiccups, etc.) so the orchestrator pauses dispatch with reason
	// "transport_error" instead of marking the issue failed. todolist4 A.4.
	TransportErrorPatterns []string
	// MaxAutomationQueueLength caps durable automation dispatch entries waiting
	// for worker capacity or dependency resolution. Values <= 0 use the default
	// of 100; the queue is never unlimited.
	MaxAutomationQueueLength int
	// MaxRetryBackoffMs caps the exponential back-off between agent retries.
	// The progression is 10 s × 2^(attempt-1): 10 s, 20 s, 40 s, 80 s, 160 s,
	// then capped at MaxRetryBackoffMs for all subsequent attempts.
	// Values <= 0 fall back to the default. Retry count is controlled by
	// MaxRetries.
	// Default: 300 000 ms (5 min).
	MaxRetryBackoffMs int
	MaxTurns          int
	Command           string
	// Backend optionally overrides runner selection for the default agent command
	// when it cannot be inferred from the command string alone.
	Backend string
	// TurnTimeoutMs is the hard wall-clock limit for an entire agent session
	// (all turns combined). When the limit is exceeded the subprocess is killed
	// and the issue is scheduled for retry. Default: 3 600 000 ms (1 hour).
	// Set to 0 to disable.
	TurnTimeoutMs int
	// ReadTimeoutMs is the per-read timeout on the subprocess stdout pipe. If
	// no bytes arrive within this window the read is considered stalled and the
	// subprocess is killed. This catches hangs at the OS/pipe level before the
	// higher-level stall detector fires. Default: 30 000 ms (30 s).
	ReadTimeoutMs int
	// StallTimeoutMs is the orchestrator-level inactivity timeout. The
	// orchestrator checks every tick whether any running worker has produced an
	// SSE event within this window; if not, it cancels the worker context and
	// schedules a retry. Unlike ReadTimeoutMs (pipe-level), this operates on
	// the parsed event stream and can detect semantic stalls (e.g. the agent is
	// looping without making progress). Default: 300 000 ms (5 min).
	// Set to ≤ 0 to disable stall detection entirely.
	StallTimeoutMs int
	// DependencyAuditRefreshIntervalMs is the minimum gap between refreshes of
	// the same dependency-audit row. Startup-only: there is no HTTP setter, so
	// it needs no cfgMu guard (same reasoning as StallTimeoutMs — see
	// CLAUDE.md, section "`cfgMu` guards exactly these fields (and nothing
	// else)").
	DependencyAuditRefreshIntervalMs int
	// DependencyAuditRefreshTimeoutMs bounds one off-loop refresh batch.
	// Startup-only; see DependencyAuditRefreshIntervalMs.
	DependencyAuditRefreshTimeoutMs int
	// DependencyAuditRefreshBatchSize caps how many rows one batch may fetch.
	// Replaces the former dependencyAuditRefreshPerTickBudget constant, whose
	// value of 20 existed only because the fetches blocked the event loop.
	// Startup-only; see DependencyAuditRefreshIntervalMs.
	DependencyAuditRefreshBatchSize int
	// SSHHosts is an optional list of "host" or "host:port" addresses.
	// When set, agent turns are executed on these hosts via SSH in order,
	// falling back to the next host on failure. Empty = run locally.
	SSHHosts []string
	// SSHHostDescriptions maps SSH host address -> optional human-readable label.
	// Keys must match entries in SSHHosts. The dashboard shows these descriptions
	// alongside the host list, and runtime edits persist them back to WORKFLOW.md.
	SSHHostDescriptions map[string]string
	// SSHStrictHostChecking is the default StrictHostKeyChecking mode applied
	// to every SSH-hosted runner command, unless overridden per-host via
	// SSHStrictHostByHost. Defaults to "accept-new" (TOFU — pin on first
	// contact, reject on mismatch). Other valid values: "yes", "no", "ask",
	// "off". Read at startup and applied via agent.SetSSHStrictHostDefault.
	// T-32.
	SSHStrictHostChecking string
	// SSHStrictHostByHost maps SSH host address -> StrictHostKeyChecking mode
	// (overrides SSHStrictHostChecking for the specific host). Useful for
	// per-host hardening (e.g. "yes" on production, "no" on a sandbox VM).
	// Applied via agent.SetSSHStrictHostOverrides. T-32.
	SSHStrictHostByHost map[string]string
	// DispatchStrategy controls how issues are routed to SSH hosts when
	// multiple are configured. Valid values: "round-robin" (default),
	// "least-loaded". Ignored when SSHHosts is empty.
	DispatchStrategy string
	// ReviewerPrompt is the Liquid template used when a reviewer worker is
	// dispatched (e.g. via the AI Review button). Falls back to DefaultReviewerPrompt.
	// Deprecated: prefer ReviewerProfile which uses the profile's own prompt.
	ReviewerPrompt string
	// ReviewerProfile is the name of the agent profile used for code review.
	// When set, the reviewer uses this profile's command, backend, and prompt
	// instead of the legacy ReviewerPrompt field. The reviewer runs as a
	// regular worker in the queue with Kind="reviewer".
	ReviewerProfile string
	// AutoReview, when true, automatically dispatches a reviewer worker
	// using ReviewerProfile after each successful worker completion.
	// Requires ReviewerProfile to be set. Default: false.
	AutoReview bool
	// Profiles is an optional map of named agent profiles. Each profile can
	// override the default agent Command. Profiles can be selected per-issue
	// from the web UI.
	Profiles map[string]AgentProfile
	// DepsAnalyzerProfile is the name of the agent profile used to populate
	// the inferred dependency layer for the Deps tab. When empty, the
	// dashboard's "Analyze dependencies" button is disabled and the Deps tab
	// shows tracker-declared edges only.
	DepsAnalyzerProfile string
	// DepsAnalyzerTimeoutMs bounds one analyzer job end to end, all chunks
	// included. Independent of TurnTimeoutMs, whose 1-hour default is far too
	// long for this job. Startup-only: no HTTP setter, so no cfgMu guard is
	// needed (same precedent as StallTimeoutMs).
	DepsAnalyzerTimeoutMs int
	// DepsAnalyzerChunkSize caps how many issues go into one analyzer turn.
	// Startup-only; see DepsAnalyzerTimeoutMs.
	DepsAnalyzerChunkSize int
	// InlineInput controls whether agent input-required signals are posted as
	// tracker comments (true) or queued in the dashboard UI (false).
	// When true, the issue moves to the completion state with a question comment;
	// the user replies in the tracker and moves the issue back to continue.
	// When false (default), the dashboard shows a reply UI and posts the user's
	// response as a tracker comment before resuming the agent.
	// Default: false.
	InlineInput bool
	// MaxSwitchesPerIssuePerWindow caps how many times a `rate_limited`
	// automation can swap an issue to a different profile / backend within
	// SwitchWindowHours. Default 2. 0 means "unlimited" (not recommended).
	// Gap E.
	MaxSwitchesPerIssuePerWindow int
	// SwitchWindowHours is the rolling window over which
	// MaxSwitchesPerIssuePerWindow is counted. Default 6. Gap E.
	SwitchWindowHours int
	// SwitchRevertHours, when > 0, triggers a periodic check that reverts
	// rate_limited auto-switched profile/backend overrides whose age has
	// exceeded the TTL. 0 (default) keeps the prior behaviour: overrides
	// only clear on the next successful worker exit. Gap §6.2.
	SwitchRevertHours int
	// RateLimitErrorPatterns are case-insensitive substrings the
	// orchestrator's terminal-failure classifier matches against the
	// worker's last-error text to decide if a failure was rate-limit
	// driven (and so should fire `rate_limited` automations rather than
	// just `run_failed`). Empty → uses the built-in default list
	// (rate_limit_exceeded / rate limit / 429 / quota / too many requests).
	// Operators can extend or override the list when a vendor surfaces
	// new throttle wording. Gap §5.1.
	RateLimitErrorPatterns []string
	// MaxRetries is the maximum number of retry attempts before an issue is
	// moved to the failed state. 0 means unlimited retries (legacy behavior).
	// Default: 5.
	MaxRetries int
	// BaseBranch is the remote branch used as the base for git diffs when
	// enriching PR context (e.g. "origin/main", "origin/develop", "origin/master").
	// When empty, Itervox auto-detects via `git symbolic-ref refs/remotes/origin/HEAD`,
	// falling back to "origin/main" if detection fails.
	BaseBranch string
	// AvailableModels maps backend names ("claude", "codex") to model options
	// discovered at init time. The dashboard profile editor uses these for the
	// model dropdown. When empty, the frontend falls back to a built-in default
	// list.
	AvailableModels map[string][]ModelOption
}

// ModelOption represents an available model for a backend. Matches the
// agent.ModelOption type but lives here to avoid import cycles.
type ModelOption struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label" yaml:"label"`
}

// HooksConfig holds lifecycle hook settings.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	// AfterRunRequired makes the after_run hook a per-unit gate (spec F3):
	// when true, a worker whose final after_run hook exits non-zero fails the
	// unit instead of reaching TerminalSucceeded. Default false preserves the
	// historical best-effort behavior (hook failures logged and ignored).
	AfterRunRequired bool
	TimeoutMs        int
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port *int
	Host string
	// AllowUnauthenticatedLAN, when true, lets the daemon start (on ANY
	// bind address, loopback included) without requiring ITERVOX_API_TOKEN.
	// Explicit opt-in for trusted environments. Default false: a random
	// token is always auto-generated when no token is set, regardless of
	// bind address — see #48. Bind address is not a security boundary (a
	// loopback daemon behind a tunnel or reverse proxy is exactly as
	// exposed as a non-loopback one), so this is no longer LAN-specific.
	//
	// YAML key: `server.allow_unauthenticated` (preferred). The legacy key
	// `server.allow_unauthenticated_lan` still parses as a deprecated alias
	// (one-time slog.Warn); the new key wins if both are present.
	AllowUnauthenticatedLAN bool
}

// DependenciesConfig holds settings for the unified dependency graph:
// whether inferred (non-tracker) dependency edges gate automation dispatch,
// and the confidence/staleness thresholds inferred edges are held to.
type DependenciesConfig struct {
	// InferredGating, when true, lets inferred dependency edges (not just
	// tracker-declared blockers) hold automation dispatch until resolved.
	// Default true.
	InferredGating bool
	// ConfidenceThreshold is the minimum confidence score (0.0-1.0) an
	// inferred dependency edge must meet to be treated as gating. Values
	// outside [0,1] fall back to DefaultDependenciesConfidenceThreshold.
	ConfidenceThreshold float64
	// StalenessHours is how long an inferred dependency edge is trusted
	// before it is considered stale and re-evaluated. Non-positive values
	// fall back to DefaultDependenciesStalenessHours.
	StalenessHours int
	// Ordering selects the dispatch ordering strategy: "critical_path"
	// (default) ranks issues by how many dependents they unblock, "simple"
	// uses the legacy priority/createdAt sort. An unrecognized value falls
	// back to DefaultDependenciesOrdering with a slog.Warn. See also the
	// deprecated agent.sort.prefer_high_outdegree alias, handled at the
	// parse site.
	Ordering string
	// EscalateBlockedAfterHours is how long an issue may remain blocked
	// before automation escalation fires. Default 48. An explicit `0` is
	// meaningful — it disables escalation — and is preserved as-is. A
	// negative value falls back to DefaultDependenciesEscalateHours with a
	// slog.Warn.
	EscalateBlockedAfterHours int
	// AutoAnalyze, when true, enables periodic dependency analysis.
	// Default true.
	AutoAnalyze bool
	// AutoAnalyzeMinIntervalMinutes is the minimum gap between consecutive
	// dependency analyses on the same issue. Parsed via positiveIntField;
	// <=0 becomes the default. There is no meaningful zero here (unlike
	// escalate_blocked_after_hours) — analyzer must not run every tick.
	AutoAnalyzeMinIntervalMinutes int
	// AutoAnalyzeDebounceMinutes delays analysis start after a dispatch to
	// avoid analyzing while state is still settling. Parsed via positiveIntField;
	// <=0 becomes the default. No meaningful zero — debounce must delay.
	AutoAnalyzeDebounceMinutes int
}

// Config is the fully-parsed, defaulted, and resolved Itervox configuration.
type Config struct {
	// SchemaVersion is the WORKFLOW.md schema version declared by
	// itervox_schema_version. Missing versions parse as 0 and are rejected
	// by ValidateDispatch before daemon startup.
	SchemaVersion int
	// WorkflowPath is the path passed to Load, used for actionable validation
	// and migration errors.
	WorkflowPath   string
	Tracker        TrackerConfig
	Polling        PollingConfig
	Workspace      WorkspaceConfig
	Agent          AgentConfig
	Hooks          HooksConfig
	Server         ServerConfig
	Dependencies   DependenciesConfig
	Automations    []AutomationConfig
	PromptTemplate string
}

// Load reads a WORKFLOW.md file, parses front matter, applies defaults, and resolves env vars.
// It does not validate required fields. Call ValidateDispatch before starting the agent
// dispatch loop (i.e. before calling Orchestrator.Run). Utility callers that only need
// cfg.Workspace.Root, cfg.Tracker.Kind, or similar non-critical fields may omit ValidateDispatch.
func Load(path string) (*Config, error) {
	wf, err := workflow.Load(path)
	if err != nil {
		return nil, err
	}
	// Reject removed fields loudly so operators see a clear migration error
	// instead of silently-changed behavior. (`agent_mode` + `enable_agent_teams`
	// were removed in favor of always-on profile prompt + subagent roster
	// injection — see CHANGELOG.)
	if intField(wf.Config, "itervox_schema_version", 0) == LatestWorkflowSchemaVersion {
		agent := nestedMap(wf.Config, "agent")
		if _, has := agent["agent_mode"]; has {
			return nil, fmt.Errorf("config: agent.agent_mode has been removed; delete this field from WORKFLOW.md (see CHANGELOG)")
		}
		if _, has := agent["enable_agent_teams"]; has {
			return nil, fmt.Errorf("config: agent.enable_agent_teams has been removed; delete this field from WORKFLOW.md (see CHANGELOG)")
		}
	}
	cfg, err := fromWorkflow(wf, path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// fromWorkflow builds a Config from a parsed Workflow, applying all defaults.
func fromWorkflow(wf *workflow.Workflow, workflowPath string) (*Config, error) {
	raw := wf.Config

	cfg := &Config{
		SchemaVersion:  intField(raw, "itervox_schema_version", 0),
		WorkflowPath:   workflowPath,
		PromptTemplate: wf.PromptTemplate,
	}
	if cfg.SchemaVersion > LatestWorkflowSchemaVersion {
		return nil, fmt.Errorf("unsupported itervox_schema_version %d: latest supported version is %d", cfg.SchemaVersion, LatestWorkflowSchemaVersion)
	}

	// Tracker
	tracker := nestedMap(raw, "tracker")
	cfg.Tracker.Kind = strField(tracker, "kind", "")
	// Apply the Linear default endpoint only when tracker.kind is "linear" so
	// that GitHub users who omit endpoint: get an empty string, triggering the
	// GitHub client's own default (https://api.github.com).
	defaultEndpoint := ""
	if cfg.Tracker.Kind == "linear" {
		defaultEndpoint = "https://api.linear.app/graphql"
	}
	cfg.Tracker.Endpoint = strField(tracker, "endpoint", defaultEndpoint)
	cfg.Tracker.APIKey = resolveSecret(strField(tracker, "api_key", ""))
	cfg.Tracker.ProjectSlug = strField(tracker, "project_slug", "")
	cfg.Tracker.ActiveStates = strSliceField(tracker, "active_states", []string{"Todo", "In Progress"})
	cfg.Tracker.TerminalStates = strSliceField(tracker, "terminal_states", []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"})
	cfg.Tracker.WorkingState = strField(tracker, "working_state", "In Progress")
	cfg.Tracker.CompletionState = strField(tracker, "completion_state", "")
	defaultBacklog := []string{}
	if cfg.Tracker.Kind == "linear" {
		defaultBacklog = []string{"Backlog"}
	}
	cfg.Tracker.BacklogStates = strSliceField(tracker, "backlog_states", defaultBacklog)
	cfg.Tracker.FailedState = strField(tracker, "failed_state", "")
	cfg.Tracker.Outbox = boolField(tracker, "outbox", true)

	// Polling
	polling := nestedMap(raw, "polling")
	cfg.Polling.IntervalMs = intField(polling, "interval_ms", 30000)

	// Workspace
	ws := nestedMap(raw, "workspace")
	defaultWSRoot := defaultWorkspaceRoot()
	cfg.Workspace.Root = resolvePathValue(strField(ws, "root", ""), defaultWSRoot)
	cfg.Workspace.AutoClearWorkspace = boolField(ws, "auto_clear", false)
	cfg.Workspace.Worktree = boolField(ws, "worktree", false)
	cfg.Workspace.CloneURL = strField(ws, "clone_url", "")
	cfg.Workspace.BaseBranch = strField(ws, "base_branch", "main")

	// Agent
	agent := nestedMap(raw, "agent")
	cfg.Agent.MaxConcurrentAgents = positiveIntField(agent, "max_concurrent_agents", 10)
	cfg.Agent.MaxAutomationQueueLength = positiveIntField(agent, "max_automation_queue_length", 100)
	cfg.Agent.MaxRetryBackoffMs = positiveIntField(agent, "max_retry_backoff_ms", 300000)
	cfg.Agent.MaxTurns = positiveIntField(agent, "max_turns", 20)
	cfg.Agent.Command = strField(agent, "command", "claude")
	cfg.Agent.Backend = strField(agent, "backend", "")
	cfg.Agent.TurnTimeoutMs = intField(agent, "turn_timeout_ms", 3600000)
	cfg.Agent.ReadTimeoutMs = positiveIntField(agent, "read_timeout_ms", 30000)
	cfg.Agent.StallTimeoutMs = intField(agent, "stall_timeout_ms", 300000)
	cfg.Agent.DependencyAuditRefreshIntervalMs = positiveIntField(agent, "dependency_audit_refresh_interval_ms", DefaultDependencyAuditRefreshIntervalMs)
	cfg.Agent.DependencyAuditRefreshTimeoutMs = positiveIntField(agent, "dependency_audit_refresh_timeout_ms", DefaultDependencyAuditRefreshTimeoutMs)
	cfg.Agent.DependencyAuditRefreshBatchSize = positiveIntField(agent, "dependency_audit_refresh_batch_size", DefaultDependencyAuditRefreshBatchSize)
	cfg.Agent.MaxConcurrentAgentsByState = normalizeStateLimits(mapField(agent, "max_concurrent_agents_by_state"))
	cfg.Agent.PauseDispatchWhenAnyInState = strSliceField(agent, "pause_dispatch_when_any_in_state", nil)
	cfg.Agent.MergeStrategy = strField(agent, "merge_strategy", "squash")
	cfg.Agent.MergeBlockLabels = strSliceField(agent, "merge_block_labels", []string{"needs-human", "migration", "auth", "feature-flag", "breaking"})
	cfg.Agent.AllowUncheckedMerge = boolField(agent, "allow_unchecked_merge", false)
	cfg.Agent.TransportErrorPatterns = strSliceField(agent, "transport_error_patterns", []string{"stream disconnected", "connection reset", "i/o timeout"})
	sortMap := mapField(agent, "sort")
	cfg.Agent.PreferHighOutdegreeSort = boolField(sortMap, "prefer_high_outdegree", false)
	cfg.Agent.SSHHosts = strSliceField(agent, "ssh_hosts", nil)
	cfg.Agent.SSHHostDescriptions = stringMapField(agent, "ssh_host_descriptions", nil)
	// T-32: optional StrictHostKeyChecking config. Default "accept-new" (TOFU)
	// is enforced at the agent package level even if the field is omitted from
	// WORKFLOW.md, so this just lets users override the default.
	cfg.Agent.SSHStrictHostChecking = strField(agent, "ssh_strict_host_checking", "")
	cfg.Agent.SSHStrictHostByHost = stringMapField(agent, "ssh_strict_host_by_host", nil)
	cfg.Agent.DispatchStrategy = strField(agent, "dispatch_strategy", "round-robin")
	cfg.Agent.ReviewerPrompt = strField(agent, "reviewer_prompt", DefaultReviewerPrompt)
	cfg.Agent.ReviewerProfile = strField(agent, "reviewer_profile", "")
	cfg.Agent.DepsAnalyzerProfile = strField(agent, "deps_analyzer_profile", "")
	cfg.Agent.DepsAnalyzerTimeoutMs = positiveIntField(agent, "deps_analyzer_timeout_ms", DefaultDepsAnalyzerTimeoutMs)
	cfg.Agent.DepsAnalyzerChunkSize = positiveIntField(agent, "deps_analyzer_chunk_size", DefaultDepsAnalyzerChunkSize)
	cfg.Agent.AutoReview = boolField(agent, "auto_review", false)
	cfg.Agent.InlineInput = boolField(agent, "inline_input", false)
	cfg.Agent.MaxRetries = intField(agent, "max_retries", 5)
	// Gap E — per-issue switch cap defaults: 2 switches per 6h window.
	cfg.Agent.MaxSwitchesPerIssuePerWindow = intField(agent, "max_switches_per_issue_per_window", 2)
	// Gap §6.2 — operator-configurable TTL for auto-switched overrides.
	cfg.Agent.SwitchRevertHours = intField(agent, "switch_revert_hours", 0)
	// Gap §5.1 — operator-configurable rate-limit error-message patterns.
	cfg.Agent.RateLimitErrorPatterns = strSliceField(agent, "rate_limit_error_patterns", nil)
	cfg.Agent.SwitchWindowHours = intField(agent, "switch_window_hours", 6)
	cfg.Agent.BaseBranch = strField(agent, "base_branch", "")
	profiles, err := parseAgentProfiles(mapField(agent, "profiles"), cfg.SchemaVersion, workflowPath)
	if err != nil {
		return nil, err
	}
	cfg.Agent.Profiles = profiles
	cfg.Agent.AvailableModels = parseAvailableModels(mapField(agent, "available_models"))

	// Hooks
	hooks := nestedMap(raw, "hooks")
	cfg.Hooks.AfterCreate = strField(hooks, "after_create", "")
	cfg.Hooks.BeforeRun = strField(hooks, "before_run", "")
	cfg.Hooks.AfterRun = strField(hooks, "after_run", "")
	cfg.Hooks.BeforeRemove = strField(hooks, "before_remove", "")
	cfg.Hooks.AfterRunRequired = boolField(hooks, "after_run_required", false)
	hooksTimeout := intField(hooks, "timeout_ms", 0)
	if hooksTimeout <= 0 {
		hooksTimeout = 60000
	}
	cfg.Hooks.TimeoutMs = hooksTimeout

	// Server
	srv := nestedMap(raw, "server")
	cfg.Server.Host = strField(srv, "host", "127.0.0.1")
	if p, ok := srv["port"]; ok {
		if pInt, ok := toInt(p); ok && pInt >= 0 {
			cfg.Server.Port = &pInt
		}
	}
	// Absent (or unparseable) port → fixed default. Port stays a *int only so
	// an explicit `0` (OS picks a free port, for multi-daemon setups) remains
	// distinguishable in the YAML; after load it is always non-nil.
	if cfg.Server.Port == nil {
		defaultPort := DefaultServerPort
		cfg.Server.Port = &defaultPort
	}
	// Alias: server.allow_unauthenticated_lan is deprecated in favor of
	// server.allow_unauthenticated (#48 — the flag is no longer LAN-scoped,
	// it opts out of auth entirely regardless of bind address). The new key
	// wins if both are present in the same WORKFLOW.md.
	_, newAllowUnauthKeySet := srv["allow_unauthenticated"]
	_, oldAllowUnauthKeySet := srv["allow_unauthenticated_lan"]
	switch {
	case newAllowUnauthKeySet:
		cfg.Server.AllowUnauthenticatedLAN = boolField(srv, "allow_unauthenticated", false)
		if oldAllowUnauthKeySet {
			slog.Warn("config: both server.allow_unauthenticated and deprecated server.allow_unauthenticated_lan are set; using server.allow_unauthenticated")
		}
	case oldAllowUnauthKeySet:
		slog.Warn("config: server.allow_unauthenticated_lan is deprecated, use server.allow_unauthenticated instead")
		cfg.Server.AllowUnauthenticatedLAN = boolField(srv, "allow_unauthenticated_lan", false)
	default:
		cfg.Server.AllowUnauthenticatedLAN = false
	}

	// Dependencies
	deps := nestedMap(raw, "dependencies")
	cfg.Dependencies.InferredGating = boolField(deps, "inferred_gating", true)
	confidenceThreshold := floatField(deps, "confidence_threshold", DefaultDependenciesConfidenceThreshold)
	if confidenceThreshold < 0 || confidenceThreshold > 1 {
		slog.Warn("config: dependencies.confidence_threshold out of range [0,1], using default",
			"value", confidenceThreshold, "default", DefaultDependenciesConfidenceThreshold)
		confidenceThreshold = DefaultDependenciesConfidenceThreshold
	}
	cfg.Dependencies.ConfidenceThreshold = confidenceThreshold
	cfg.Dependencies.StalenessHours = positiveIntField(deps, "staleness_hours", DefaultDependenciesStalenessHours)

	_, orderingExplicit := deps["ordering"]
	cfg.Dependencies.Ordering = strField(deps, "ordering", DefaultDependenciesOrdering)
	if cfg.Dependencies.Ordering != DependenciesOrderingCriticalPath && cfg.Dependencies.Ordering != DependenciesOrderingSimple {
		slog.Warn("config: dependencies.ordering unrecognized, using default",
			"value", cfg.Dependencies.Ordering, "default", DefaultDependenciesOrdering)
		cfg.Dependencies.Ordering = DefaultDependenciesOrdering
	}

	// escalate_blocked_after_hours: absent -> default (48); explicit 0 is a
	// meaningful "disabled" value and must be preserved; negative -> default
	// with a warning. Deliberately NOT positiveIntField, which would treat
	// an explicit 0 the same as absent and destroy the "disabled" signal —
	// the exact footgun documented for timeout fields in CLAUDE.md.
	if _, ok := deps["escalate_blocked_after_hours"]; ok {
		escalateHours := intField(deps, "escalate_blocked_after_hours", DefaultDependenciesEscalateHours)
		if escalateHours < 0 {
			slog.Warn("config: dependencies.escalate_blocked_after_hours negative, using default",
				"value", escalateHours, "default", DefaultDependenciesEscalateHours)
			escalateHours = DefaultDependenciesEscalateHours
		}
		cfg.Dependencies.EscalateBlockedAfterHours = escalateHours
	} else {
		cfg.Dependencies.EscalateBlockedAfterHours = DefaultDependenciesEscalateHours
	}

	// auto_analyze: enable/disable periodic dependency analysis. Default true.
	cfg.Dependencies.AutoAnalyze = boolField(deps, "auto_analyze", true)
	// auto_analyze_min_interval_minutes and auto_analyze_debounce_minutes:
	// both parsed via positiveIntField (<=0 -> default). No meaningful zero
	// here — analyzer must not run every tick, and debounce must delay.
	cfg.Dependencies.AutoAnalyzeMinIntervalMinutes = positiveIntField(deps, "auto_analyze_min_interval_minutes", DefaultDependenciesAutoAnalyzeMinIntervalMinutes)
	cfg.Dependencies.AutoAnalyzeDebounceMinutes = positiveIntField(deps, "auto_analyze_debounce_minutes", DefaultDependenciesAutoAnalyzeDebounceMinutes)

	// Alias: agent.sort.prefer_high_outdegree is deprecated in favor of
	// dependencies.ordering. If set, log a one-time deprecation warning. If
	// dependencies.ordering was absent from the YAML, the alias leaves the
	// default critical_path in place (net effect: same behavior, better
	// tiebreaking). If dependencies.ordering was explicitly present
	// (including explicit "simple"), it wins outright.
	if cfg.Agent.PreferHighOutdegreeSort {
		slog.Warn("config: agent.sort.prefer_high_outdegree is deprecated, use dependencies.ordering: critical_path instead")
		if !orderingExplicit {
			cfg.Dependencies.Ordering = DependenciesOrderingCriticalPath
		}
	}

	cfg.Automations = parseAutomations(raw["automations"])
	if len(cfg.Automations) == 0 {
		legacy := legacySchedulesToAutomations(parseSchedules(raw["schedules"]))
		if len(legacy) > 0 {
			slog.Warn("config: schedules: is deprecated; migrate to automations:", "count", len(legacy))
		}
		cfg.Automations = legacy
	}

	return cfg, nil
}

// resolveSecret resolves $VAR_NAME references for secret fields.
// Returns the resolved value, or empty string if unresolvable.
func resolveSecret(value string) string {
	if m := envVarRe.FindStringSubmatch(value); m != nil {
		return os.Getenv(m[1])
	}
	return value
}

// resolvePathValue resolves $VAR and ~ for path fields.
func resolvePathValue(value, defaultVal string) string {
	if value == "" {
		return defaultVal
	}
	// $VAR resolution
	if m := envVarRe.FindStringSubmatch(value); m != nil {
		resolved := os.Getenv(m[1])
		if resolved == "" {
			return defaultVal
		}
		return expandTilde(resolved)
	}
	expanded := expandTilde(value)
	if expanded == "" {
		return defaultVal
	}
	return expanded
}

// defaultWorkspaceRoot returns ~/.itervox/workspaces, falling back to
// os.TempDir()/itervox_workspaces if the home directory cannot be determined.
func defaultWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".itervox", "workspaces")
	}
	return filepath.Join(os.TempDir(), "itervox_workspaces")
}

func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("config: cannot expand ~, using path as-is", "path", path, "error", err)
			return path // return unexpanded path rather than silently returning ""
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// normalizeStateLimits lowercases state keys and drops invalid (non-positive) entries.
func normalizeStateLimits(raw map[string]any) map[string]int {
	result := make(map[string]int)
	for k, v := range raw {
		normalized := strings.ToLower(k)
		if normalized == "" {
			continue
		}
		if n, ok := toInt(v); ok && n > 0 {
			result[normalized] = n
		}
	}
	return result
}

// parseAgentProfiles parses the agent.profiles map from YAML into a
// map[string]AgentProfile. Unknown or invalid legacy entries are silently skipped.
func parseAgentProfiles(raw map[string]any, schemaVersion int, workflowPath string) (map[string]AgentProfile, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	profiles := make(map[string]AgentProfile, len(raw))
	for name, v := range raw {
		m := nestedMap(map[string]any{name: v}, name)
		if schemaVersion == LatestWorkflowSchemaVersion {
			if _, hasPrompt := m["prompt"]; hasPrompt {
				return nil, fmt.Errorf("%s", LegacyInlineProfilePromptMessage(workflowPath))
			}
			soulFile := strField(m, "soul_file", "")
			instructionsFile := strField(m, "instructions_file", "")
			builtin := profiles_Lookup(name)
			var soul, instructions string
			if soulFile == "" && instructionsFile == "" && builtin != nil {
				soul = builtin.Soul
				instructions = builtin.Instructions
				soulFile = builtin.SoulFilePath
				instructionsFile = builtin.InstructionsPath
			} else {
				if soulFile == "" || instructionsFile == "" {
					return nil, fmt.Errorf("config: agent.profiles.%s requires soul_file and instructions_file in schema %d", name, LatestWorkflowSchemaVersion)
				}
				readSoul, err := readProfileTemplateFile(workflowPath, soulFile)
				if err != nil {
					if builtin != nil && os.IsNotExist(errors.Unwrap(err)) {
						soul = builtin.Soul
					} else {
						return nil, fmt.Errorf("config: agent.profiles.%s.soul_file: %w", name, err)
					}
				} else {
					soul = readSoul
				}
				readInstructions, err := readProfileTemplateFile(workflowPath, instructionsFile)
				if err != nil {
					if builtin != nil && os.IsNotExist(errors.Unwrap(err)) {
						instructions = builtin.Instructions
					} else {
						return nil, fmt.Errorf("config: agent.profiles.%s.instructions_file: %w", name, err)
					}
				} else {
					instructions = readInstructions
				}
			}
			cmd := strField(m, "command", "")
			if cmd == "" && builtin != nil {
				cmd = builtin.DefaultCommand
			}
			if cmd == "" {
				return nil, fmt.Errorf("config: agent.profiles.%s.command is required in schema %d", name, LatestWorkflowSchemaVersion)
			}
			backend := strField(m, "backend", "")
			if backend == "" && builtin != nil {
				backend = builtin.DefaultBackend
			}
			allowed := NormalizeAllowedActions(strSliceField(m, "allowed_actions", nil))
			if len(allowed) == 0 && builtin != nil {
				allowed = NormalizeAllowedActions(builtin.DefaultActions)
			}
			profiles[name] = AgentProfile{
				Command:          cmd,
				SoulFile:         soulFile,
				InstructionsFile: instructionsFile,
				Soul:             soul,
				Instructions:     instructions,
				Backend:          backend,
				Enabled:          boolPtr(boolField(m, "enabled", true)),
				AllowedActions:   allowed,
				CreateIssueState: strField(m, "create_issue_state", ""),
			}
			continue
		}
		cmd := strField(m, "command", "")
		if cmd == "" {
			continue
		}
		profiles[name] = AgentProfile{
			Command:          cmd,
			Prompt:           strField(m, "prompt", ""),
			Backend:          strField(m, "backend", ""),
			Enabled:          boolPtr(boolField(m, "enabled", true)),
			AllowedActions:   NormalizeAllowedActions(strSliceField(m, "allowed_actions", nil)),
			CreateIssueState: strField(m, "create_issue_state", ""),
		}
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	return profiles, nil
}

func readProfileTemplateFile(workflowPath, configuredPath string) (string, error) {
	resolved := resolveWorkflowRelativeFile(workflowPath, configuredPath)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", resolved, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func resolveWorkflowRelativeFile(workflowPath, configuredPath string) string {
	if filepath.IsAbs(configuredPath) {
		return filepath.Clean(configuredPath)
	}
	base := "."
	if workflowPath != "" {
		base = filepath.Dir(workflowPath)
	}
	return filepath.Clean(filepath.Join(base, configuredPath))
}

func boolPtr(v bool) *bool {
	return &v
}

func ProfileEnabled(profile AgentProfile) bool {
	return profile.Enabled == nil || *profile.Enabled
}

// parseAvailableModels parses the agent.available_models YAML field.
// Expected format:
//
//	available_models:
//	  claude:
//	    - { id: "claude-sonnet-4-6", label: "Sonnet 4.6" }
//	  codex:
//	    - { id: "gpt-5.2-codex", label: "GPT-5.2 Codex" }
func parseAvailableModels(raw map[string]any) map[string][]ModelOption {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string][]ModelOption, len(raw))
	for backend, v := range raw {
		items, ok := v.([]any)
		if !ok {
			continue
		}
		models := make([]ModelOption, 0, len(items))
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			label, _ := m["label"].(string)
			if id == "" {
				continue
			}
			if label == "" {
				label = id
			}
			models = append(models, ModelOption{ID: id, Label: label})
		}
		if len(models) > 0 {
			result[backend] = models
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// --- helpers ---

func nestedMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	v, ok := m[key]
	if !ok {
		return map[string]any{}
	}
	switch cast := v.(type) {
	case map[string]any:
		return cast
	case map[any]any:
		out := make(map[string]any, len(cast))
		for k, val := range cast {
			out[fmt.Sprintf("%v", k)] = val
		}
		return out
	}
	return map[string]any{}
}

func mapField(m map[string]any, key string) map[string]any {
	return nestedMap(m, key)
}

func stringMapField(m map[string]any, key string, defaultVal map[string]string) map[string]string {
	if m == nil {
		return defaultVal
	}
	raw := mapField(m, key)
	if len(raw) == 0 {
		return defaultVal
	}
	out := make(map[string]string, len(raw))
	for mapKey, value := range raw {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			out[mapKey] = typed
		default:
			out[mapKey] = fmt.Sprintf("%v", typed)
		}
	}
	return out
}

func strField(m map[string]any, key, defaultVal string) string {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return defaultVal
	}
	return s
}

func intField(m map[string]any, key string, defaultVal int) int {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	n, ok := toInt(v)
	if !ok {
		return defaultVal
	}
	return n
}

func floatField(m map[string]any, key string, defaultVal float64) float64 {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	f, ok := toFloat(v)
	if !ok {
		return defaultVal
	}
	return f
}

func boolField(m map[string]any, key string, defaultVal bool) bool {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	b, ok := v.(bool)
	if !ok {
		return defaultVal
	}
	return b
}

func positiveIntField(m map[string]any, key string, defaultVal int) int {
	n := intField(m, key, 0)
	if n <= 0 {
		return defaultVal
	}
	return n
}

func strSliceField(m map[string]any, key string, defaultVal []string) []string {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	raw, ok := v.([]any)
	if !ok {
		return defaultVal
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// toFloat coerces a YAML-decoded numeric value to float64. YAML numbers
// arrive as int or float64 in a map[string]any, depending on whether the
// literal contained a decimal point — yaml.v3 never produces a float32, so
// there is no float32 case here (#50: it was dead defensive code, removed as
// pure churn).
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
