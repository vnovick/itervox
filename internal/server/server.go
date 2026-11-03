package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"

	"github.com/go-chi/chi/v5"
)

// errNotConfigured is returned by no-op callback stubs installed in New()
// for optional Config fields that were left nil by the caller.
var errNotConfigured = errors.New("not configured")

// ErrBusy is the OrchestratorClient-level counterpart of
// orchestrator.ErrBusy: an implementation returns it from CancelIssue,
// ResumeIssue, TerminateIssue, ReanalyzeIssue, ProvideInput or DismissInput
// to report that the underlying event channel was full, not that the issue
// was not found. internal/server cannot import internal/orchestrator
// (package order — server only imports domain and config), so
// cmd/itervox's adapter translates orchestrator.ErrBusy into this sentinel;
// handlers map errors.Is(err, ErrBusy) to 503 with Retry-After, and any
// other non-nil error to 404 (CORE-005).
var ErrBusy = errors.New("server: orchestrator event queue is full")

// ErrDraining is the OrchestratorClient-level counterpart of
// orchestrator.ErrDraining (CORE-057): the daemon is draining for shutdown or
// a WORKFLOW.md reload and admits no new work. Handlers map it to 409
// {"code":"draining"}; the operator retries once the daemon is back.
var ErrDraining = errors.New("server: daemon is draining; not admitting new work")

// ErrBackendLimited is returned by a DepsAnalyzer enqueue while the analyzer
// profile's backend breaker is open (CORE-173 a); the handler answers
// 409 backend_limited.
var ErrBackendLimited = errors.New("server: agent backend is limited")

// RunningRow is a single row in the active sessions table.
type RunningRow struct {
	Identifier    string    `json:"identifier"`
	State         string    `json:"state"`
	TurnCount     int       `json:"turnCount"`
	LastEvent     string    `json:"lastEvent,omitempty"`
	LastEventAt   string    `json:"lastEventAt,omitempty"`
	InputTokens   int       `json:"inputTokens"`
	OutputTokens  int       `json:"outputTokens"`
	Tokens        int       `json:"tokens"`
	ElapsedMs     int64     `json:"elapsedMs"`
	StartedAt     time.Time `json:"startedAt"`
	SessionID     string    `json:"sessionId,omitempty"`
	WorkerHost    string    `json:"workerHost,omitempty"`
	Backend       string    `json:"backend,omitempty"`
	Kind          string    `json:"kind,omitempty"` // "worker" (default) | "reviewer" | "automation"
	SubagentCount int       `json:"subagentCount,omitempty"`
	// AutomationID is set when the run was dispatched by a configured
	// automation rule (cron, input_required, run_failed, …). Empty for
	// manually dispatched runs.
	AutomationID string `json:"automationId,omitempty"`
	// TriggerType identifies how the automation fired ("cron",
	// "input_required", "run_failed", "test"). Empty for manual runs.
	TriggerType string `json:"triggerType,omitempty"`
	// CommentCount counts review/comment actions taken during this run
	// (T-6 surface). Zero for runs that have not commented.
	CommentCount int `json:"commentCount,omitempty"`
	// OutOfScope is set when the issue left the tracker's scope mid-run
	// (#182: reassigned away, moved out of the project): the run finishes,
	// but no new run is dispatched for it.
	OutOfScope bool `json:"outOfScope,omitempty"`
	// RulesHash / RuleIDs name the rule set the run was given (#157).
	RulesHash string   `json:"rulesHash,omitempty"`
	RuleIDs   []string `json:"ruleIds,omitempty"`
}

// HistoryRow is one completed agent session in the run-history list.
type HistoryRow struct {
	Identifier   string    `json:"identifier"`
	Title        string    `json:"title,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	ElapsedMs    int64     `json:"elapsedMs"`
	TurnCount    int       `json:"turnCount"`
	TotalTokens  int       `json:"tokens"`
	InputTokens  int       `json:"inputTokens"`
	OutputTokens int       `json:"outputTokens"`
	Status       string    `json:"status"` // "succeeded" | "failed" | "cancelled" | "stalled" | "input_required"
	WorkerHost   string    `json:"workerHost,omitempty"`
	Backend      string    `json:"backend,omitempty"`
	SessionID    string    `json:"sessionId,omitempty"`
	AppSessionID string    `json:"appSessionId,omitempty"`
	Kind         string    `json:"kind,omitempty"` // "worker" (default) | "reviewer" | "automation"
	// ProfileName is the profile the run used (#186: prompt versions are
	// per profile).
	ProfileName string `json:"profileName,omitempty"`
	// AutomationID / TriggerType propagate the automation context onto
	// completed runs so that the Activity tab and Timeline filter chip can
	// scope history per-automation. Empty for manual runs.
	AutomationID string `json:"automationId,omitempty"`
	TriggerType  string `json:"triggerType,omitempty"`
	// CommentCount: comments posted during this run (T-6 surface).
	CommentCount int `json:"commentCount,omitempty"`
	// RulesHash / RuleIDs name the rule set the run used (#157).
	RulesHash string   `json:"rulesHash,omitempty"`
	RuleIDs   []string `json:"ruleIds,omitempty"`
	// MemoryIDs are the memory entries the run's prompt carried (#202).
	MemoryIDs []string `json:"memoryIds,omitempty"`
	// RunID names the run (#125, #186); empty for runs recorded before it was kept.
	RunID string `json:"runId,omitempty"`
	// PromptDigest identifies the prompt version the run was dispatched with
	// (#186): 64 hex chars of SHA-256 over SOUL.md + INSTRUCTIONS.md + the
	// resolved rules + the prompt template. PromptParts are per-component
	// digests. Hashes only, never prompt text.
	PromptDigest string              `json:"promptDigest,omitempty"`
	PromptParts  *HistoryPromptParts `json:"promptParts,omitempty"`
	// Playbook is the task-type playbook the run was given (#218).
	Playbook string `json:"playbook,omitempty"`
	// Steers are the operator messages delivered to the run (#186), redacted
	// and bounded; SteersDropped counts the older ones not kept.
	Steers        []HistorySteer `json:"steers,omitempty"`
	SteersDropped int            `json:"steersDropped,omitempty"`
}

// HistoryPromptParts are the per-component digests of a run's prompt
// version (#186): truncated SHA-256 hex, "" for a component the run lacked.
type HistoryPromptParts struct {
	Soul         string `json:"soul,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Rules        string `json:"rules,omitempty"`
	// Memory is the digest of the run's memory block (#202).
	Memory string `json:"memory,omitempty"`
	// Playbook is the digest of the run's playbook block (#218).
	Playbook string `json:"playbook,omitempty"`
	Template string `json:"template,omitempty"`
}

// HistorySteer is one operator message delivered to a run (#178, #186).
type HistorySteer struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Delivery is "live" (written into the running turn) or "resume" (the
	// prompt of the next resumed turn).
	Delivery string `json:"delivery"`
}

// RateLimitInfo holds the last observed API rate limit snapshot.
type RateLimitInfo struct {
	RequestsLimit       int        `json:"requestsLimit"`
	RequestsRemaining   int        `json:"requestsRemaining"`
	RequestsReset       *time.Time `json:"requestsReset,omitempty"`
	ComplexityLimit     int        `json:"complexityLimit,omitempty"`
	ComplexityRemaining int        `json:"complexityRemaining,omitempty"`
}

// RetryRow is a single row in the retry queue table.
type RetryRow struct {
	Identifier string    `json:"identifier"`
	Attempt    int       `json:"attempt"`
	DueAt      time.Time `json:"dueAt"`
	Error      string    `json:"error,omitempty"`
}

// AutomationQueueBackpressureRow is the queue-cap snapshot used by dashboard
// alert surfaces. It intentionally omits queued prompt/instruction payloads.
//
// LastRejectedAt is *time.Time so `omitempty` actually omits the field when
// no rejection has been recorded. Go's encoding/json treats time.Time as a
// struct and `omitempty` only omits the zero struct value — it does NOT call
// IsZero(), so a time.Time-typed field with omitempty would still emit
// "0001-01-01T00:00:00Z" on the wire. v0.2.0 audit P1-5.
type AutomationQueueBackpressureRow struct {
	Length             int        `json:"length"`
	MaxLength          int        `json:"maxLength"`
	Saturated          bool       `json:"saturated"`
	PausedProducers    bool       `json:"pausedProducers"`
	RejectedSinceBoot  int        `json:"rejectedSinceBoot"`
	LastRejectedAt     *time.Time `json:"lastRejectedAt,omitempty"`
	LastRejectedReason string     `json:"lastRejectedReason,omitempty"`
}

// DispatchPressureRow reports which resource constrained the agent fleet, so
// the dashboard can answer "would raising max_concurrent_agents help?"
// without the operator having to infer it from an instantaneous gauge.
//
// SlotBoundTicks and DependencyBoundTicks are mutually exclusive per tick and
// need not sum to ObservedTicks — ticks where the fleet had nothing to do are
// charged to neither.
type DispatchPressureRow struct {
	ObservedTicks        int64 `json:"observedTicks"`
	SlotBoundTicks       int64 `json:"slotBoundTicks"`
	DependencyBoundTicks int64 `json:"dependencyBoundTicks"`
	// UtilizationPercent is mean fleet utilization across the session
	// (0-100), capacity-weighted so a mid-session capacity change is
	// accounted for correctly.
	UtilizationPercent int `json:"utilizationPercent"`
	// BlockedByDependency and EligibleWaiting describe the MOST RECENT tick
	// only, unlike the cumulative counters above.
	BlockedByDependency int `json:"blockedByDependency"`
	EligibleWaiting     int `json:"eligibleWaiting"`
}

type BlockerRefRow struct {
	ID         string `json:"id,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	State      string `json:"state,omitempty"`
	URL        string `json:"url,omitempty"`
}

// AutomationQueueRow is the per-entry row exposed by the snapshot.
//
// LastFiredAt and LastAttemptAt are *time.Time so `omitempty` actually
// omits the field on never-fired / never-attempted entries instead of
// emitting "0001-01-01T00:00:00Z" on the wire. v0.2.0 audit P1-5.
type AutomationQueueRow struct {
	ID                string     `json:"id"`
	AutomationID      string     `json:"automationId"`
	TriggerType       string     `json:"triggerType"`
	Identifier        string     `json:"identifier"`
	Title             string     `json:"title,omitempty"`
	IssueState        string     `json:"issueState,omitempty"`
	Profile           string     `json:"profile"`
	Backend           string     `json:"backend,omitempty"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	ReasonDetail      string     `json:"reasonDetail,omitempty"`
	QueuedAt          time.Time  `json:"queuedAt"`
	FiredAt           time.Time  `json:"firedAt"`
	LastFiredAt       *time.Time `json:"lastFiredAt,omitempty"`
	LastAttemptAt     *time.Time `json:"lastAttemptAt,omitempty"`
	AttemptCount      int        `json:"attemptCount"`
	Cron              string     `json:"cron,omitempty"`
	Timezone          string     `json:"timezone,omitempty"`
	PRURL             string     `json:"prUrl,omitempty"`
	InputContext      string     `json:"inputContext,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
	SwitchedToProfile string     `json:"switchedToProfile,omitempty"`
	SwitchedToBackend string     `json:"switchedToBackend,omitempty"`
	MoveToState       string     `json:"moveToState,omitempty"`
}

// DependencyAuditRow is one issue's dependency state as exposed by the
// snapshot.
//
// FirstBlockedAt / UnblockedAt / LastAuditedAt are *time.Time so `omitempty`
// actually omits the field on a never-blocked / never-audited row instead of
// emitting "0001-01-01T00:00:00Z". v0.2.0 audit P1-5.
type DependencyAuditRow struct {
	Identifier            string          `json:"identifier"`
	IssueState            string          `json:"issueState"`
	Status                string          `json:"status"`
	Sources               []string        `json:"sources,omitempty"`
	BlockedBy             []BlockerRefRow `json:"blockedBy,omitempty"`
	UnresolvedBlockers    []BlockerRefRow `json:"unresolvedBlockers,omitempty"`
	ResolvedBlockers      []BlockerRefRow `json:"resolvedBlockers,omitempty"`
	WasBlocked            bool            `json:"wasBlocked"`
	FirstBlockedAt        *time.Time      `json:"firstBlockedAt,omitempty"`
	UnblockedAt           *time.Time      `json:"unblockedAt,omitempty"`
	LastAuditedAt         *time.Time      `json:"lastAuditedAt,omitempty"`
	LastTransitionVersion int64           `json:"lastTransitionVersion,omitempty"`
	LastTransitionReason  string          `json:"lastTransitionReason,omitempty"`
	// Degraded is true when consecutive refresh failures crossed the
	// orchestrator's threshold. Dispatch behaviour is unchanged — the row
	// stays blocked — but the operator needs to know the data is stale.
	Degraded bool `json:"degraded,omitempty"`
}

type DependencyGraphNodeRow struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state,omitempty"`
	Status     string `json:"status,omitempty"`
	Running    bool   `json:"running"`
	Queued     bool   `json:"queued"`
	Terminal   bool   `json:"terminal"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	URL        string `json:"url,omitempty"`
}

type DependencyGraphEdgeRow struct {
	ID               string `json:"id"`
	SourceIdentifier string `json:"sourceIdentifier"`
	TargetIdentifier string `json:"targetIdentifier"`
	SourceState      string `json:"sourceState,omitempty"`
	TargetState      string `json:"targetState,omitempty"`
	Resolved         bool   `json:"resolved"`
	SourceKnown      bool   `json:"sourceKnown"`
	// Origin labels the edge's provenance for the dashboard.
	// "tracker" — declared by Linear/GitHub via BlockedBy.
	// "inferred" — produced by the deps-analyzer agent pass.
	// Empty/absent is treated as "tracker" by the frontend.
	Origin string `json:"origin,omitempty"`
	// Evidence is the short quotation/paraphrase the analyzer attached to the
	// edge. Populated only when Origin == "inferred".
	Evidence string `json:"evidence,omitempty"`
	// Confidence is the analyzer's confidence score for an inferred edge
	// ([0,1]). Zero (and omitted) for tracker edges.
	Confidence float64 `json:"confidence,omitempty"`
	// Stale is true when an inferred edge is older than the configured
	// dependencies staleness window. Always false for tracker edges.
	Stale bool `json:"stale,omitempty"`
	// Overridden is true when an operator has dismissed this inferred edge's
	// target via SetDepsOverride. Always false for tracker edges.
	Overridden bool `json:"overridden,omitempty"`
	// Gating is true when this edge currently blocks dispatch of its target:
	// for tracker edges, the blocker is unresolved; for inferred edges, the
	// entry's InferredDepEntry.Gating (confidence/staleness/override/
	// dependencies.InferredGating all considered).
	Gating bool `json:"gating,omitempty"`
}

// DependencyCycleRow is one strongly-connected-component cycle (or
// self-edge) in the tick graph, mirroring orchestrator.DependencyCycle.
// Members stay blocked — this is a read-only operator alert, not a
// resolution mechanism. critical-path-ordering Task 5.
type DependencyCycleRow struct {
	Members    []string  `json:"members"`
	Kind       string    `json:"kind"` // "tracker" | "inferred" | "mixed"
	DetectedAt time.Time `json:"detectedAt"`
}

// DependencyAttentionRow is one operator-facing dependency alert — either a
// cycle member (Kind "cycle") or an issue blocked longer than the configured
// escalation window (Kind "stale_blocker") — mirroring
// orchestrator.DependencyAttentionEntry. critical-path-ordering Task 5.
type DependencyAttentionRow struct {
	Identifier   string    `json:"identifier"`
	Blockers     []string  `json:"blockers"`
	BlockedSince time.Time `json:"blockedSince"`
	Kind         string    `json:"kind"` // "cycle" | "stale_blocker"
}

// Counts holds summary counts for the state snapshot.
type Counts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
	Paused   int `json:"paused"`
}

// Project is one item in the interactive project picker.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ProjectManager is implemented by tracker adapters that support project
// filtering (currently only the Linear adapter). The server registers project
// endpoints only when a non-nil ProjectManager is provided.
type ProjectManager interface {
	FetchProjects(ctx context.Context) ([]Project, error)
	// SetProjectFilter persists and applies the filter. An error means
	// nothing changed (M4-close D4): a wrapped ErrSettingsReloading is the
	// reload fence (503), anything else a failed persist.
	SetProjectFilter(slugs []string) error
	GetProjectFilter() []string
}

// TrackerScope is the combined tracker scope (#182): issues in any of
// Projects (empty: every project) AND assigned to Assignee ("": anyone;
// "me", a user id or an email) AND carrying any of Labels (empty: no label
// filter).
type TrackerScope struct {
	Projects []string `json:"projects"`
	Assignee string   `json:"assignee"`
	Labels   []string `json:"labels"`
	// Filter is the effective tracker filter (#198) as groups: tracker.filter,
	// or the legacy keys above as one implicit group (empty: every issue).
	Filter issuefilter.Filter `json:"filter"`
	// FilterMode is set when a filter (not the legacy keys) is in effect.
	FilterMode bool `json:"filterMode"`
	// Conditions lists the filter conditions the tracker supports.
	Conditions []string `json:"conditions"`
}

// TrackerTeam is one teams choice of a filter group (#198).
type TrackerTeam struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// FilterPreview is what a proposed filter would read (#198): the number of
// issues in the active states it matches and the first identifiers.
type FilterPreview struct {
	Count       int      `json:"count"`
	Identifiers []string `json:"identifiers"`
}

// ErrFilterInvalid is returned (wrapped) by ScopeManager.SetFilter and
// PreviewFilter for a filter the tracker cannot read (an empty group, an
// unsupported condition, ...): a 422.
var ErrFilterInvalid = errors.New("invalid tracker filter")

// TrackerUser is one assignee choice.
type TrackerUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// ErrScopeAssigneeNotFound is returned (wrapped) by ScopeManager.SetScope
// when the assignee matches no user the tracker key can see: a 422.
var ErrScopeAssigneeNotFound = errors.New("assignee not found")

// ScopeManager is implemented by trackers whose read scope combines
// projects, an assignee and labels (#182, Linear).
type ScopeManager interface {
	GetScope() TrackerScope
	// SetScope resolves the assignee, persists the scope to WORKFLOW.md and
	// applies it. An error means nothing changed: a wrapped
	// ErrSettingsReloading is the reload fence (503), a wrapped
	// ErrScopeAssigneeNotFound an unknown user (422), anything else a
	// failed resolve or persist (500).
	SetScope(ctx context.Context, scope TrackerScope) error
	// SetFilter (#198) validates and resolves the filter, persists it as
	// tracker.filter (commenting the legacy scope keys out) and applies it;
	// an empty filter reads every issue. Errors as SetScope, plus a wrapped
	// ErrFilterInvalid (422).
	SetFilter(ctx context.Context, filter issuefilter.Filter) error
	// PreviewFilter counts what filter would read now, read-only.
	PreviewFilter(ctx context.Context, filter issuefilter.Filter) (FilterPreview, error)
	FetchLabels(ctx context.Context) ([]string, error)
	FetchUsers(ctx context.Context) ([]TrackerUser, error)
	FetchTeams(ctx context.Context) ([]TrackerTeam, error)
}

// OrchestratorClient abstracts the orchestrator and workflow operations called
// by HTTP handlers. A nil value in Config is replaced with noopClient.
// PRMergedEmitter is an optional capability some OrchestratorClient
// implementations expose: the daemon-side merge_pr handler invokes it on a
// successful gh merge so pr_merged automations fire downstream. Discovered
// via type assertion to avoid bloating the main interface.
// IssueBackendPinChecker is optionally implemented by the orchestrator
// client (CORE-056): it reports why a per-issue backend pin would be refused
// by the dispatch resolver (CORE-115) — e.g. codex over a "claude ..."
// command — so POST /api/v1/issues/{identifier}/backend can reject it with
// 409 instead of storing an inert pin.
type IssueBackendPinChecker interface {
	CheckIssueBackendPin(identifier, backend string) error
}

// FailureAcker is optionally implemented by the orchestrator client
// (CORE-175): POST /api/v1/issues/{identifier}/failures/ack records that the
// operator has seen the issue's worker failures up to upTo. It returns
// ErrBusy when the event channel is full and ErrNoWorkerFailure when the
// issue has no recent worker failure.
type FailureAcker interface {
	AckFailures(identifier string, upTo time.Time) error
}

// ErrNoWorkerFailure is FailureAcker's "nothing to acknowledge" (→ 404).
var ErrNoWorkerFailure = errors.New("server: issue has no recent worker failure")

type PRMergedEmitter interface {
	// headRef is the PR's head branch (trigger.pr_branch); prURL and
	// baseRef bind trigger.pr_url and trigger.pr_base_branch (CORE-108).
	EmitPRMerged(ctx context.Context, identifier, prURL string, prNumber int, mergedSHA, baseRef, headRef string) error
}

type OrchestratorClient interface {
	FetchIssues(ctx context.Context) ([]TrackerIssue, error)
	// CancelIssue, ResumeIssue, TerminateIssue and ReanalyzeIssue return nil
	// on success, ErrBusy when the orchestrator's event channel was full
	// (map to 503 with Retry-After, never 404 — CORE-005), or any other
	// non-nil error when a synchronous lookup established the issue is not
	// in the required state (map to 404).
	CancelIssue(identifier string) error
	ResumeIssue(identifier string) error
	TerminateIssue(identifier string) error
	ReanalyzeIssue(identifier string) error
	// FetchLogs returns the retained log lines for identifier. ctx is the
	// request's: an implementation that may wait on disk must stop waiting
	// when it ends (M0-close fix-G).
	FetchLogs(ctx context.Context, identifier string) []string
	// GetSince returns log lines appended for identifier strictly after
	// cursor, the log buffer's current process epoch, the next cursor to
	// resume from, and whether the returned lines are a gap replay of the
	// current window rather than a contiguous continuation. hasCursor=false
	// means "first connect" (epoch/cursor are ignored). Backs
	// handleIssueLogStream's resume-by-sequence SSE contract (CORE-003);
	// see internal/logbuffer.Buffer.GetSince's doc comment for the full
	// sequence/epoch/gap semantics this delegates to. ctx is the request's,
	// as for FetchLogs.
	GetSince(ctx context.Context, identifier string, epoch uint32, cursor int64, hasCursor bool) (lines []string, currentEpoch uint32, next int64, gap bool)
	ClearLogs(identifier string) error
	ClearAllLogs() error
	ClearIssueSubLogs(identifier string) error
	ClearSessionSublog(identifier, sessionID string) error
	FetchSubLogs(ctx context.Context, identifier string) ([]domain.IssueLogEntry, error)
	DispatchReviewer(identifier string) error
	CommentOnIssue(ctx context.Context, identifier, body string) error
	// PostOperatorComment posts a plain (non-managed) comment authored by the
	// dashboard operator. It returns queued=true when the comment was accepted
	// by the write-ahead outbox and will be delivered by the flusher, or
	// queued=false when it was posted to the tracker directly (tracker.outbox:
	// false). The body is NOT marked managed: an operator comment behaves like
	// one typed in the tracker, so it may fire tracker_comment_added
	// automations and, on trackers that do not attribute it to the same author
	// as the agent's question, may count as the answer to an input-required
	// agent — the dashboard's reply box / provide-input is the intended way to
	// answer an agent.
	PostOperatorComment(ctx context.Context, identifier, body string) (queued bool, err error)
	CreateIssue(ctx context.Context, identifier, title, body, stateName string) (*domain.Issue, error)
	UpdateIssueState(ctx context.Context, identifier, stateName string) error
	SetWorkers(n int) error
	BumpWorkers(delta int) (int, error)
	// SetMaxRetries updates the per-issue retry budget. 0 means "unlimited".
	// Negative values are clamped to 0 by the implementation.
	SetMaxRetries(n int) error
	// MaxRetries returns the current retry budget (0 = unlimited).
	MaxRetries() int
	// SetFailedState updates the tracker state issues are moved to when retries
	// exhaust. Empty string means "pause instead of move". The handler is
	// responsible for validating the state name against the known state set.
	SetFailedState(stateName string) error
	// FailedState returns the current failed-state name (empty = pause).
	FailedState() string
	// SetMaxSwitchesPerIssuePerWindow updates the per-issue rate_limited
	// switch cap. 0 = unlimited. Gap E.
	SetMaxSwitchesPerIssuePerWindow(n int) error
	MaxSwitchesPerIssuePerWindow() int
	// SetSwitchWindowHours updates the rolling-window duration over which
	// switches are counted. <= 0 normalises to 6h. Gap E.
	SetSwitchWindowHours(h int) error
	SwitchWindowHours() int
	SetIssueProfile(identifier, profile string)
	SetIssueBackend(identifier, backend string)
	ProfileDefs() map[string]ProfileDef
	// DefaultAgentCommand returns agent.command — the command a profile with
	// an empty command inherits at dispatch. Automation validation needs it
	// to resolve a switch profile's effective command (CORE-010).
	DefaultAgentCommand() string
	AvailableModels() map[string][]ModelOption
	ReviewerConfig() (profile string, autoReview bool)
	SetReviewerConfig(profile string, autoReview bool) error
	UpsertProfile(name string, def ProfileDef, originalName string) error
	DeleteProfile(name string) error
	SetAutomations(automations []AutomationDef) error
	SetAutoClearWorkspace(enabled bool) error
	// SetDepsAnalysisMode updates dependencies.analysis_mode ("auto" |
	// "manual") at runtime, persisting to WORKFLOW.md first.
	SetDepsAnalysisMode(mode string) error
	ClearAllWorkspaces() error
	FetchLogIdentifiers() []string
	UpdateTrackerStates(active, terminal []string, completion string) error
	AddSSHHost(host, description string) error
	RemoveSSHHost(host string) error
	SetDispatchStrategy(strategy string) error
	// ProvideInput and DismissInput perform no lookup of their own (the
	// event loop decides whether the issue is actually waiting for input),
	// so the only non-nil error they can return is ErrBusy — map it to 503,
	// never 404 (CORE-005).
	ProvideInput(identifier, message string) error
	DismissInput(identifier string) error
	SetInlineInput(enabled bool) error
	// BumpCommentCount is invoked after a successful agent-comment action so
	// the snapshot row's CommentCount field can surface review activity on
	// the dashboard (T-6). The implementation must be safe to call from an
	// HTTP handler goroutine.
	BumpCommentCount(identifier string)
	// TestAutomation dispatches a one-off automation worker for the given
	// rule against the given issue (T-10). The resulting run is tagged with
	// TriggerType="test" so timeline / activity surfaces can distinguish it
	// from production fires while keeping it under the same "automation runs
	// only" filter. Errors out when the rule is not found, the referenced
	// profile is missing, or the issue cannot be located.
	TestAutomation(ctx context.Context, automationID, identifier string) error
	// SetDepsOverride enables (true) or clears (false) an operator dismissal
	// of the LLM-inferred dependency gating layer for identifier. Returns
	// false only when the orchestrator's event channel is full (the handler
	// surfaces this as a transient failure, not a 404 — unlike ResumeIssue,
	// there is no "not currently gated" precondition to check).
	// unified-dependency-graph Task 6.
	SetDepsOverride(identifier string, enabled bool) bool
	// RetryOutboxEntry makes a pending write-ahead-outbox entry immediately
	// due (bypassing its backoff). Returns false when no entry with that id
	// exists (handler surfaces 404) — unlike SetDepsOverride, there is no
	// event-loop queue involved: the implementation calls the Outbox handle
	// directly (see cmd/itervox's orchestratorAdapter.RetryOutboxEntry).
	// write-ahead-outbox design, "Surfaces".
	RetryOutboxEntry(id string) bool
	// DropOutboxEntry discards a pending write-ahead-outbox entry (operator
	// action — the remedy for an entry that can never auto-reconcile, e.g.
	// an issue a human moved out of active states while a write was
	// pending). Mirrors outbox.Outbox.Drop's own idempotent-on-unknown-id
	// contract: never errors, so the handler always answers 202.
	// write-ahead-outbox design, "Surfaces".
	DropOutboxEntry(id string)
}

// noopClient implements OrchestratorClient with harmless defaults.
// Boolean methods return false; error methods return errNotConfigured.
type noopClient struct{}

func (noopClient) FetchIssues(context.Context) ([]TrackerIssue, error) { return nil, errNotConfigured }
func (noopClient) CancelIssue(string) error                            { return errNotConfigured }
func (noopClient) ResumeIssue(string) error                            { return errNotConfigured }
func (noopClient) TerminateIssue(string) error                         { return errNotConfigured }
func (noopClient) ReanalyzeIssue(string) error                         { return errNotConfigured }
func (noopClient) FetchLogs(context.Context, string) []string          { return nil }
func (noopClient) GetSince(context.Context, string, uint32, int64, bool) ([]string, uint32, int64, bool) {
	return nil, 0, 0, false
}
func (noopClient) ClearLogs(string) error                  { return errNotConfigured }
func (noopClient) ClearAllLogs() error                     { return errNotConfigured }
func (noopClient) ClearIssueSubLogs(string) error          { return errNotConfigured }
func (noopClient) ClearSessionSublog(string, string) error { return errNotConfigured }
func (noopClient) FetchSubLogs(context.Context, string) ([]domain.IssueLogEntry, error) {
	return nil, nil
}
func (noopClient) DispatchReviewer(string) error                        { return errNotConfigured }
func (noopClient) CommentOnIssue(context.Context, string, string) error { return errNotConfigured }
func (noopClient) PostOperatorComment(context.Context, string, string) (bool, error) {
	return false, errNotConfigured
}
func (noopClient) CreateIssue(context.Context, string, string, string, string) (*domain.Issue, error) {
	return nil, errNotConfigured
}
func (noopClient) UpdateIssueState(context.Context, string, string) error { return errNotConfigured }
func (noopClient) SetWorkers(int) error                                   { return nil }
func (noopClient) BumpWorkers(int) (int, error)                           { return 0, nil }
func (noopClient) SetMaxRetries(int) error                                { return nil }
func (noopClient) MaxRetries() int                                        { return 0 }
func (noopClient) SetFailedState(string) error                            { return nil }
func (noopClient) FailedState() string                                    { return "" }
func (noopClient) SetMaxSwitchesPerIssuePerWindow(int) error              { return nil }
func (noopClient) MaxSwitchesPerIssuePerWindow() int                      { return 0 }
func (noopClient) SetSwitchWindowHours(int) error                         { return nil }
func (noopClient) SwitchWindowHours() int                                 { return 0 }
func (noopClient) SetIssueProfile(string, string)                         {}
func (noopClient) SetIssueBackend(string, string)                         {}
func (noopClient) ProfileDefs() map[string]ProfileDef                     { return nil }
func (noopClient) DefaultAgentCommand() string                            { return "" }
func (noopClient) AvailableModels() map[string][]ModelOption              { return nil }
func (noopClient) ReviewerConfig() (string, bool)                         { return "", false }
func (noopClient) SetReviewerConfig(string, bool) error                   { return nil }
func (noopClient) UpsertProfile(string, ProfileDef, string) error         { return errNotConfigured }
func (noopClient) DeleteProfile(string) error                             { return errNotConfigured }
func (noopClient) SetAutomations([]AutomationDef) error                   { return errNotConfigured }
func (noopClient) SetAutoClearWorkspace(bool) error                       { return errNotConfigured }
func (noopClient) SetDepsAnalysisMode(string) error                       { return errNotConfigured }
func (noopClient) ClearAllWorkspaces() error                              { return errNotConfigured }
func (noopClient) FetchLogIdentifiers() []string                          { return nil }
func (noopClient) UpdateTrackerStates([]string, []string, string) error   { return errNotConfigured }
func (noopClient) AddSSHHost(string, string) error                        { return errNotConfigured }
func (noopClient) RemoveSSHHost(string) error                             { return errNotConfigured }
func (noopClient) SetDispatchStrategy(string) error                       { return errNotConfigured }

// ProvideInput and DismissInput return nil (not errNotConfigured): they
// perform no lookup of their own even in a real implementation, so a noop
// backing has nothing more meaningful to report than "queued" (CORE-005).
func (noopClient) ProvideInput(string, string) error                    { return nil }
func (noopClient) DismissInput(string) error                            { return nil }
func (noopClient) SetInlineInput(bool) error                            { return errNotConfigured }
func (noopClient) BumpCommentCount(string)                              {}
func (noopClient) TestAutomation(context.Context, string, string) error { return errNotConfigured }
func (noopClient) SetDepsOverride(string, bool) bool                    { return false }
func (noopClient) RetryOutboxEntry(string) bool                         { return false }
func (noopClient) DropOutboxEntry(string)                               {}

// StateSnapshot is the payload returned by GET /api/v1/state.
type StateSnapshot struct {
	GeneratedAt         time.Time    `json:"generatedAt"`
	Counts              Counts       `json:"counts"`
	Running             []RunningRow `json:"running"`
	History             []HistoryRow `json:"history,omitempty"`
	Retrying            []RetryRow   `json:"retrying"`
	Paused              []string     `json:"paused"`
	MaxConcurrentAgents int          `json:"maxConcurrentAgents"`
	// MaxRetries is the per-issue retry budget. 0 means "unlimited".
	// Surfaced so the dashboard can show e.g. "↻ retry 2/5" pills.
	MaxRetries int `json:"maxRetries"`
	// FailedState is the tracker state issues are moved to when retries
	// exhaust. Empty string means "pause instead of move" (the issue is
	// added to PausedIdentifiers and persisted to disk).
	FailedState string `json:"failedState,omitempty"`
	// MaxSwitchesPerIssuePerWindow + SwitchWindowHours cap how many times a
	// `rate_limited` automation can switch an issue's profile within the
	// rolling window. Gap E. 0 = unlimited.
	MaxSwitchesPerIssuePerWindow int            `json:"maxSwitchesPerIssuePerWindow"`
	SwitchWindowHours            int            `json:"switchWindowHours"`
	RateLimits                   *RateLimitInfo `json:"rateLimits"`
	// TrackerKind is "linear" or "github" — lets the web UI decide whether to
	// show the project picker.
	TrackerKind string `json:"trackerKind,omitempty"`
	// DemoMode is true while `itervox demo` serves fake issues and a scripted
	// agent (#76); the dashboard shows a "Demo mode" badge.
	DemoMode bool `json:"demoMode,omitempty"`
	// ProjectName is a human-readable label for the project this daemon is
	// serving. Populated from the tracker project slug when available, else
	// the directory basename of the WORKFLOW.md file. Rendered in the web
	// UI header so multi-daemon / multi-repo users can tell which instance
	// they are looking at.
	ProjectName string `json:"projectName,omitempty"`
	// ActiveProjectFilter is the current runtime project filter slugs.
	// nil/absent means "using WORKFLOW.md default"; empty array means "all issues".
	ActiveProjectFilter []string `json:"activeProjectFilter,omitempty"`
	// TrackerScope describes the tracker's read scope for the header
	// (#182), e.g. "ENG · assigned to me · label: agent". Empty for
	// trackers without a combinable scope.
	TrackerScope string `json:"trackerScope,omitempty"`
	// ScopeAssignee is the scope's assignee spec ("me", an id or an email;
	// "" for anyone) and ScopeAssigneeName who it resolved to (#182).
	ScopeAssignee     string `json:"scopeAssignee,omitempty"`
	ScopeAssigneeName string `json:"scopeAssigneeName,omitempty"`
	// ScopeLabels is the scope's label filter (any of them; #182).
	ScopeLabels []string `json:"scopeLabels,omitempty"`
	// FilterConditions lists the tracker filter conditions the tracker
	// supports (#198); absent for trackers without a filter. The dashboard
	// shows the group builder when it is present.
	FilterConditions []string `json:"filterConditions,omitempty"`
	// AvailableProfiles is the list of named agent profile names defined in WORKFLOW.md.
	// Empty/absent means no profiles are configured.
	AvailableProfiles []string `json:"availableProfiles,omitempty"`
	// ProfileDefs is the map of named agent profile definitions from WORKFLOW.md.
	ProfileDefs           map[string]ProfileDef    `json:"profileDefs,omitempty"`
	AvailableModels       map[string][]ModelOption `json:"availableModels,omitempty"`
	SupportedAgentActions []string                 `json:"supportedAgentActions,omitempty"`
	ReviewerProfile       string                   `json:"reviewerProfile,omitempty"`
	AutoReview            bool                     `json:"autoReview,omitempty"`
	// ActiveStates is the list of tracker states the orchestrator will pick up.
	ActiveStates []string `json:"activeStates,omitempty"`
	// TerminalStates is the list of tracker states treated as done/closed.
	TerminalStates []string `json:"terminalStates,omitempty"`
	// CompletionState is the state the agent moves an issue to when it finishes (may be empty).
	CompletionState string `json:"completionState,omitempty"`
	// WorkingState is tracker.working_state: the state an issue is moved to
	// when an agent is dispatched (CORE-070). The dashboard's issue-detail
	// profile lock keys on it; empty/omitted means the web falls back to its
	// "In Progress" default. Read-only after startup.
	WorkingState string `json:"workingState,omitempty"`
	// BacklogStates are always-fetched states shown as the leftmost board column.
	BacklogStates []string `json:"backlogStates,omitempty"`
	// PausedWithPR maps paused issue identifiers to a known open-PR URL.
	// Itervox does NOT auto-pause on an existing open PR — workers continue
	// on the PR branch instead (v0.2.0 audit D2). The daemon currently emits
	// no entries; the field is retained for wire-schema stability (Zod marks
	// it optional) and for snapshot producers that do record PR URLs (e.g.
	// tests driving comment_pr's GitHub-PR routing).
	PausedWithPR map[string]string `json:"pausedWithPR,omitempty"`
	// PauseReasons maps each paused issue to why it is paused (M6-close
	// BH-M6-3): "user_cancelled", "user_dismissed_input",
	// "retries_exhausted" or "transition_failed" (treat unknown values as
	// opaque). An issue paused without a recorded reason (legacy pause file)
	// is absent. Omitted when nothing is listed.
	PauseReasons map[string]string `json:"pauseReasons,omitempty"`
	// PollIntervalMs is the configured tracker poll interval in milliseconds.
	// The TUI uses this to derive a safe background refresh rate.
	PollIntervalMs int `json:"pollIntervalMs,omitempty"`
	// AutoClearWorkspace indicates whether workspace directories are
	// automatically deleted after a task succeeds.
	AutoClearWorkspace bool `json:"autoClearWorkspace,omitempty"`
	// DepsAnalysisMode reports dependencies.analysis_mode ("auto" |
	// "manual") — whether the LLM dependency analyzer is scheduled
	// automatically or only runs on explicit operator triggers.
	DepsAnalysisMode string `json:"depsAnalysisMode,omitempty"`
	// CurrentAppSessionID is the ID of the current daemon invocation.
	// All history rows produced during this run share this ID.
	CurrentAppSessionID string `json:"currentAppSessionId,omitempty"`
	// SSHHosts is the configured SSH worker host pool with optional descriptions.
	// Empty/absent means all work runs locally.
	SSHHosts []SSHHostInfo `json:"sshHosts,omitempty"`
	// DispatchStrategy is the active SSH host dispatch strategy.
	// "round-robin" (default) | "least-loaded"
	DispatchStrategy string `json:"dispatchStrategy,omitempty"`
	// DefaultBackend is the configured default runner backend ("claude" or "codex").
	// Used by the frontend to show the correct badge on non-running issues.
	DefaultBackend string `json:"defaultBackend,omitempty"`
	// InlineInput reports whether the tracker is the only human reply channel
	// for input-required agents (agent.inline_input). The dashboard hides its
	// reply box when true.
	InlineInput bool `json:"inlineInput,omitempty"`
	// Automations is the configured set of lightweight cron or event-driven helper rules.
	Automations []AutomationDef `json:"automations,omitempty"`
	// InputRequired lists issues whose agent is either waiting for human input
	// or has already received a reply that is pending resume.
	InputRequired   []InputRequiredRow   `json:"inputRequired,omitempty"`
	AutomationQueue []AutomationQueueRow `json:"automationQueue,omitempty"`
	// AutomationQueueBackpressure reports queue saturation so the dashboard can
	// warn when automation producers are paused by the bounded durable queue.
	AutomationQueueBackpressure *AutomationQueueBackpressureRow `json:"automationQueueBackpressure,omitempty"`
	// DispatchPressure reports whether the fleet is slot-bound or
	// dependency-bound. Pointer + omitempty so a daemon that has not
	// completed a tick omits the field entirely rather than publishing an
	// all-zero row the dashboard would render as "0% utilized".
	DispatchPressure *DispatchPressureRow `json:"dispatchPressure,omitempty"`
	// AutomationDropsSelfReentryTotal is the monotonic count of input_required
	// automation dispatches suppressed by the self-reentry guard (the previous
	// worker on the issue was itself automation-launched). Surfaced on the
	// dashboard's LiveOpsStrip so operators can distinguish "guarded loop" from
	// "automation never fired". omitempty: absent until the first drop.
	// gaps_11 G-11.
	AutomationDropsSelfReentryTotal uint64               `json:"automationDropsSelfReentryTotal,omitempty"`
	DependencyAudit                 []DependencyAuditRow `json:"dependencyAudit,omitempty"`
	// DepsRefreshingCount is how many dependency-audit rows the off-loop
	// refresher currently holds. omitempty: absent when idle. Named distinctly
	// from orchestrator.State.DepsRefreshInFlight (a bool single-flight latch)
	// — this is a row COUNT, not a latch; the JSON tag is unchanged so the
	// wire contract and web/src/types/schemas.ts stay untouched.
	DepsRefreshingCount int `json:"depsRefreshInFlight,omitempty"`
	// DepsRefreshLastDurationMs is the wall-clock of the last completed
	// refresh batch.
	DepsRefreshLastDurationMs int64 `json:"depsRefreshLastDurationMs,omitempty"`
	// DepsRefreshDegradedCount is how many rows are past the failure threshold.
	DepsRefreshDegradedCount int `json:"depsRefreshDegradedCount,omitempty"`
	// DependencyGraphNodes lists only issues that are an endpoint of at
	// least one DependencyGraphEdges row (#210): an audited issue with no
	// blocker and no dependent is not sent.
	DependencyGraphNodes []DependencyGraphNodeRow `json:"dependencyGraphNodes,omitempty"`
	DependencyGraphEdges []DependencyGraphEdgeRow `json:"dependencyGraphEdges,omitempty"`
	// DependencyCycles surfaces this tick's cycle-detection output (Task 4)
	// so the dashboard and heartbeat can flag issues stuck in a dependency
	// cycle that no amount of waiting will resolve. critical-path-ordering
	// Task 5.
	DependencyCycles []DependencyCycleRow `json:"dependencyCycles,omitempty"`
	// DependencyAttention surfaces this tick's operator-attention entries
	// (cycle members plus blockers past the escalation window). Derived,
	// event-loop-owned state — see orchestrator.DeriveDependencyAttention.
	// critical-path-ordering Task 5.
	DependencyAttention []DependencyAttentionRow `json:"dependencyAttention,omitempty"`
	// DepsAnalyzerProfile is the configured agent.deps_analyzer_profile (Phase
	// 1.1). The dashboard gates the "Analyze dependencies" button on this being
	// non-empty + the named profile existing + enabled.
	DepsAnalyzerProfile string `json:"depsAnalyzerProfile,omitempty"`
	// DepsLastAnalyzedAt is the GeneratedAt timestamp from
	// `.itervox/dependencies.json`. Absent when the sidecar is missing or
	// outdated. Surfaced as a "Last analyzed N ago" label in the Deps toolbar.
	DepsLastAnalyzedAt *time.Time `json:"depsLastAnalyzedAt,omitempty"`
	// DepsAnalyzeJob is the analyzer JobManager's CURRENT job — whatever its
	// status. Running: the dashboard derives the Cancel affordance from this
	// (not mutation-local frontend state, which dies on a page refresh —
	// #46-1). Terminal (succeeded/failed/cancelled): last-run info. Nil only
	// when no analyzer job has ever run this process lifetime. Reusing
	// DepsAnalyzeJobRow (the existing GET /api/v1/deps/analyze/:jobId shape)
	// keeps one wire type for "a job" regardless of how it was reached.
	DepsAnalyzeJob *DepsAnalyzeJobRow `json:"depsAnalyzeJob,omitempty"`
	// ConfigInvalid surfaces an in-flight WORKFLOW.md validation failure to
	// the dashboard / TUI banner. nil/absent means the daemon is reading a
	// valid config; non-nil means the most recent reload tick failed and the
	// daemon is running on the previously-valid config while exponentially
	// backing off retries (T-26).
	ConfigInvalid *ConfigInvalidStatus `json:"configInvalid,omitempty"`
	// LastTrackerError is the most recent tracker failure (CORE-044). Absent
	// when none is recorded: a successful poll clears a poll failure, and a
	// write failure ages out after an hour.
	LastTrackerError *TrackerErrorRow `json:"lastTrackerError,omitempty"`
	// TrackerPoll is the off-loop tracker poll's status (#187): whether a
	// poll is in flight and for how long, whether it is slow, and whether
	// the tracker breaker has it degraded. Absent on daemons predating it.
	TrackerPoll *TrackerPollRow `json:"trackerPoll,omitempty"`
	// SyncStatus is the tracker sync status (#117): last successful pull,
	// lag, pending tracker writes and the last sync error. Absent on daemons
	// predating it.
	SyncStatus *SyncStatusRow `json:"syncStatus,omitempty"`
	// RecentFailures is the bounded ring of operator-relevant failures
	// (CORE-046), oldest recorded first. Deliberately NOT omitempty: always an
	// array (possibly empty) on a current daemon. Messages are redacted.
	RecentFailures []FailureRow `json:"recentFailures"`
	// FailureAcks are the operator's acknowledgements of worker failures
	// (CORE-175): failures of Identifier that occurred at or before UpTo are
	// acknowledged. Omitted when empty.
	FailureAcks []FailureAckRow `json:"failureAcks,omitempty"`
	// Capabilities lists optional daemon features the dashboard may use
	// (CORE-175: "failure_ack"; #213: "dispatch_pause"). Omitted when empty.
	Capabilities []string `json:"capabilities,omitempty"`
	// DispatchPaused is the daemon-wide dispatch pause (#213): null when
	// dispatch runs, else its reason, until and start.
	DispatchPaused *DispatchPauseRow `json:"dispatchPaused"`
	// FeedSeq is the newest event feed seq (#213); GET /api/v1/feed?since=
	// reads what came after a client's cursor.
	FeedSeq int64 `json:"feedSeq"`
	// CallerScope is the requesting token's scope (#214): viewer, operator
	// or admin. Set per caller by GET /api/v1/state and the /events stream,
	// never by the orchestrator's snapshot builder; the dashboard hides the
	// controls the scope cannot use.
	CallerScope string `json:"callerScope,omitempty"`
	// Totals is the daemon-session token and estimated-cost accounting
	// (CORE-091). Omitted only by daemons predating it.
	Totals *TotalsRow `json:"totals,omitempty"`
	// CandidateSeen is this tick's "what tracker polling saw" backlog rows —
	// one per candidate-issue identifier, carrying the tracker's UpdatedAt
	// when known. In-process only: read by cmd/itervox's deps auto-analyze
	// scheduler as its change-signal source — DependencyGraphNodes/
	// DependencyAudit are NOT a substitute because both stay empty until a
	// dependency relation already exists, which is exactly wrong for
	// detecting a fresh backlog with zero relations yet. analyzer-autonomy
	// Task 4 fix round. Not serialized (#210): no API consumer reads it and
	// it was ~19 KB of every dashboard frame.
	CandidateSeen []CandidateSeenRow `json:"-"`
	// OutboxEntries is this tick's write-ahead-outbox contents, in global
	// enqueue order (write-ahead-outbox design, "Surfaces"). Empty/absent
	// when the outbox is empty or the kill switch (tracker.outbox: false)
	// is set — cmd/itervox still constructs the Outbox handle in that case,
	// but nothing is ever enqueued into it.
	OutboxEntries []OutboxEntryRow `json:"outboxEntries,omitempty"`
	// OutboxSyncing is the sorted list of issue identifiers whose tracker
	// state was overlaid this tick by a pending outbox update_state entry —
	// mirrors orchestrator.State.OutboxSyncing's map keys. This is the join
	// key list the web uses to render a "syncing" badge on /api/v1/issues
	// rows: /api/v1/issues builds TrackerIssue rows from a direct
	// client-side tracker fetch, NOT from this snapshot, so there is no
	// Syncing field on TrackerIssue itself — the frontend joins by
	// Identifier against this list instead (see state.go's OutboxSyncing
	// doc comment).
	OutboxSyncing []string `json:"outboxSyncing,omitempty"`
	// BackendHealth is one row per agent backend (and per worker host that
	// has a breaker): the CORE-053 circuit breaker surfaced (CORE-055).
	// Additive and optional: a pre-CORE-055 daemon omits it. This is the
	// AGENT backend health — unrelated to RateLimits (the tracker API
	// budget) and to OutboxEntries[].rateLimitedUntil (tracker writes).
	BackendHealth []BackendHealthRow `json:"backendHealth,omitempty"`
	// AutoSwitches lists the issues whose next dispatch runs on an
	// automatic override (rate_limited automation or backend_fallback),
	// sorted by identifier. Describes the NEXT dispatch; the running
	// session's backend stays on RunningRow.Backend.
	AutoSwitches []AutoSwitchRow `json:"autoSwitches,omitempty"`
	// ProposedLessons is how many proposed lessons wait in the Memory
	// page's review queue (#203): the Memory nav item's count. Omitted when
	// none.
	ProposedLessons int `json:"proposedLessons,omitempty"`
}

// Backend health statuses carried by BackendHealthRow.Status.
const (
	BackendHealthHealthy = "healthy"
	BackendHealthWarning = "warning"
	BackendHealthLimited = "limited"
	BackendHealthProbing = "probing"
)

// BackendHealthRow is one agent-backend circuit breaker (CORE-053/055).
type BackendHealthRow struct {
	Backend string `json:"backend"`
	// Host is the SSH worker host the breaker applies to; "" = local runs.
	Host   string `json:"host,omitempty"`
	Status string `json:"status"`
	// Kind is what opened the breaker: "quota" (a usage limit) or
	// "throttle" (repeated api_retry rate limits).
	Kind      string `json:"kind,omitempty"`
	LimitType string `json:"limitType,omitempty"`
	// LimitedUntil is the vendor-published reset. Deliberately nullable and
	// not omitted: null means "not limited" or "reset unknown" (see
	// RetryAt), never a fabricated time.
	LimitedUntil *time.Time `json:"limitedUntil"`
	// RetryAt is when the breaker half-opens (the reset, or the cooldown
	// end when the reset is unknown). Absent when healthy.
	RetryAt *time.Time `json:"retryAt,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	// ProbeIssue holds the single half-open probe while probing.
	ProbeIssue string `json:"probeIssue,omitempty"`
	// HeldIssues counts issues held with the backend_limited reason on
	// this breaker; ReroutedIssues counts backend_fallback overrides away
	// from it.
	HeldIssues     int `json:"heldIssues"`
	ReroutedIssues int `json:"reroutedIssues"`
}

// AutoSwitchRow is an issue's automatic override and its provenance
// (CORE-055). Source is "automation", "backend_fallback", or "unknown" for
// an override persisted before provenance was recorded.
type AutoSwitchRow struct {
	Identifier  string     `json:"identifier"`
	Source      string     `json:"source"`
	FromBackend string     `json:"fromBackend,omitempty"`
	FromProfile string     `json:"fromProfile,omitempty"`
	ToBackend   string     `json:"toBackend,omitempty"`
	ToProfile   string     `json:"toProfile,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	SwitchedAt  *time.Time `json:"switchedAt,omitempty"`
}

// CandidateSeenRow is the wire shape of orchestrator.CandidateSeenRow.
type CandidateSeenRow struct {
	Identifier string    `json:"identifier"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
}

// OutboxEntryRow is the wire shape of one internal/outbox.Entry, exposed on
// the snapshot for the dashboard's Outbox panel (write-ahead-outbox design,
// "Surfaces"). cmd/itervox builds these from ob.Snapshot() in global enqueue
// order — see snapshot_rows.go's outboxEntryRows.
type OutboxEntryRow struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
	// TargetState is set for "update_state" entries only.
	TargetState string `json:"targetState,omitempty"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"lastError,omitempty"`
	// Degraded mirrors outbox.Entry.Degraded() — true once Attempts crosses
	// the operator-visible-error-badge threshold. Retries continue past this
	// point; there is no terminal give-up.
	Degraded      bool      `json:"degraded,omitempty"`
	EnqueuedAt    time.Time `json:"enqueuedAt"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	// RateLimitedUntil is the tracker-published instant this entry is waiting
	// for, when the last delivery attempt was deferred by a rate limit. A
	// pointer so a never-rate-limited entry omits the field entirely rather
	// than serialising a zero time (same posture as DepsAnalyzeJobRow).
	RateLimitedUntil *time.Time `json:"rateLimitedUntil,omitempty"`
	// LastFailedAt is when the entry's most recent real delivery failure
	// happened (rate-limit deferrals excluded). Nil until the first failure.
	// HEARTBEAT uses it to pick the most recently failing degraded entry
	// (CORE-044).
	LastFailedAt *time.Time `json:"lastFailedAt,omitempty"`
}

// TrackerErrorRow is the wire shape of orchestrator.State.LastTrackerError
// (CORE-044): the most recent tracker failure the event loop observed — a
// failed candidate poll (op "poll") or a failed failed-state move (op
// "update_state"). Kind is "outage" or "rate_limited"; ResetAt is the
// tracker-published reset of a rate limit, when one was published.
// ConsecutiveFailures is the current run of non-rate-limited poll failures.
type TrackerErrorRow struct {
	At                  time.Time  `json:"at"`
	Op                  string     `json:"op"`
	Kind                string     `json:"kind"`
	Message             string     `json:"message"`
	ResetAt             *time.Time `json:"resetAt,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures,omitempty"`
}

// TrackerPollRow is the wire shape of the off-loop tracker poll (#187).
// The poll tick reads the tracker on its own goroutine, so a slow tracker no
// longer freezes the event loop; this row is how an operator sees it.
//
//   - InFlight / StartedAt / RunningForMs: a poll is running, since when,
//     and for how long at the snapshot's generation.
//   - Slow: the running poll (or the last one) took longer than SlowAfterMs
//     (the poll interval plus half an interval, at least 1 s).
//   - Degraded: the tracker breaker opened after
//     ConsecutiveTransportFailures polls in a row hit a timeout, network
//     error or 5xx; tracker reads are skipped until BreakerOpenUntil, then
//     one poll probes the tracker.
type TrackerPollRow struct {
	InFlight                     bool       `json:"inFlight"`
	StartedAt                    *time.Time `json:"startedAt,omitempty"`
	RunningForMs                 int64      `json:"runningForMs,omitempty"`
	LastFinishedAt               *time.Time `json:"lastFinishedAt,omitempty"`
	LastDurationMs               int64      `json:"lastDurationMs"`
	SlowAfterMs                  int64      `json:"slowAfterMs"`
	Slow                         bool       `json:"slow"`
	Degraded                     bool       `json:"degraded"`
	BreakerOpenUntil             *time.Time `json:"breakerOpenUntil,omitempty"`
	ConsecutiveTransportFailures int        `json:"consecutiveTransportFailures,omitempty"`
}

// Sync modes (SyncStatusRow.Mode).
const (
	// SyncModeStore: the local tracker store (#113) pulls in the background
	// and serves every list read.
	SyncModeStore = "store"
	// SyncModeDirect: no store; every poll tick reads the tracker.
	SyncModeDirect = "direct"
	// SyncModeLocalSync: the local issue files (tracker.kind: local) synced
	// both ways with Linear or GitHub (#118, tracker.sync). The pull side
	// is the sync's last good cycle; the pending writes include the sync's
	// queued remote writes.
	SyncModeLocalSync = "local_sync"
)

// SyncStatusRow is the tracker sync status (#117), on the snapshot for the
// dashboard and API clients, and summarised in HEARTBEAT.md for
// `itervox doctor`.
//
// Pull side: LastPullAt is the last successful pull (store mode) or
// candidate poll (direct mode), nil when none has succeeded yet. LagMs is
// now − LastPullAt at snapshot time. Stale is set when LagMs exceeds
// LagWarnAfterMs, or when nothing was ever pulled and the last pull failed.
// LastError is the newest failed pull while the failure run lasts (cleared
// by a good pull), classified (tracker.ClassifyError) and never the
// tracker's raw response.
//
// Push side (the write-ahead outbox, #115): PendingWrites is every queued
// tracker write; PendingUrgent, PendingBatched (waiting for the push
// interval), PendingRetrying (failed at least once), PendingDegraded and
// PendingRateLimited break it down (they overlap). NextPushAt is when the
// next queued write is due; at or before GeneratedAt means "now".
type SyncStatusRow struct {
	Mode                string        `json:"mode"`
	LastPullAt          *time.Time    `json:"lastPullAt,omitempty"`
	LagMs               *int64        `json:"lagMs,omitempty"`
	LagWarnAfterMs      int64         `json:"lagWarnAfterMs"`
	Stale               bool          `json:"stale"`
	SyncIntervalMs      int64         `json:"syncIntervalMs"`
	CursorAt            *time.Time    `json:"cursorAt,omitempty"`
	ConsecutiveFailures int           `json:"consecutiveFailures,omitempty"`
	LastError           *SyncErrorRow `json:"lastError,omitempty"`
	PendingWrites       int           `json:"pendingWrites"`
	PendingUrgent       int           `json:"pendingUrgent"`
	PendingBatched      int           `json:"pendingBatched"`
	PendingRetrying     int           `json:"pendingRetrying"`
	PendingDegraded     int           `json:"pendingDegraded"`
	PendingRateLimited  int           `json:"pendingRateLimited"`
	OldestPendingAt     *time.Time    `json:"oldestPendingAt,omitempty"`
	NextPushAt          *time.Time    `json:"nextPushAt,omitempty"`
	PushIntervalMs      int64         `json:"pushIntervalMs"`
}

// SyncErrorRow is a classified sync failure (#117): Kind is a
// tracker.ErrorClass* value (rate_limited, auth, not_found, api_status,
// graphql, timeout, network, canceled, unknown — clients must tolerate new
// kinds) and Message a fixed-vocabulary summary, redacted.
type SyncErrorRow struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// FailureRow is the wire shape of one orchestrator.FailureRecord (CORE-046).
// Kind is worker_failed | worker_stalled | tracker_poll | tracker_write |
// persist | outbox | panic | client | automation | workspace_fetch (clients
// must tolerate new kinds).
// OccurredAt is when the producer saw the failure (latest repeat when Count >
// 1); RecordedAt is when the event loop recorded it. The ring is ordered by
// RecordedAt; the dashboard sorts by OccurredAt.
// FailureAckRow is one CORE-175 acknowledgement on the wire.
type FailureAckRow struct {
	Identifier string    `json:"identifier"`
	UpTo       time.Time `json:"upTo"`
}

// TotalsRow is the snapshot's `totals` (CORE-091): daemon-session cumulative,
// not persisted. CostUSDEstimated is Claude's client-side total_cost_usd
// estimate summed per session, null until a Claude run reports cost; Codex
// reports none, so CostCoverage.CodexRuns > 0 means the cost covers Claude
// runs only.
type TotalsRow struct {
	InputTokens      int               `json:"inputTokens"`
	OutputTokens     int               `json:"outputTokens"`
	CostUSDEstimated *float64          `json:"costUsdEstimated"`
	CostCoverage     TotalsCoverageRow `json:"costCoverage"`
}

// TotalsCoverageRow counts the runs behind TotalsRow (CORE-091).
type TotalsCoverageRow struct {
	ClaudeRuns int `json:"claudeRuns"`
	CodexRuns  int `json:"codexRuns"`
}

// CapabilityFailureAck is advertised in StateSnapshot.Capabilities when the
// daemon accepts POST /api/v1/issues/{identifier}/failures/ack (CORE-175).
const CapabilityFailureAck = "failure_ack"

type FailureRow struct {
	Kind       string    `json:"kind"`
	Identifier string    `json:"identifier,omitempty"`
	Source     string    `json:"source,omitempty"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
	RecordedAt time.Time `json:"recordedAt"`
	Count      int       `json:"count"`
}

// DepsAnalyzeJobRow is the wire shape returned by the deps-analyze status
// endpoint. Times are omitted when zero (the *time.Time pattern matches the
// existing v0.2.0 audit P1-5 fix elsewhere in this file).
type DepsAnalyzeJobRow struct {
	JobID         string     `json:"jobId"`
	Profile       string     `json:"profile,omitempty"`
	Status        string     `json:"status"`
	QueuedAt      time.Time  `json:"queuedAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	IssuesScanned int        `json:"issuesScanned,omitempty"`
	// IssuesAnalyzed is the count of issues actually sent to the analyzer
	// agent this pass (#52's IssuesScanned honesty fix) — distinct from
	// IssuesScanned, the raw tracker-fetch count, which under incremental
	// mode can be much larger (a revalidation-only run analyzes 0 while
	// scanning the whole active backlog). Additive on the wire; an older
	// daemon or job predating this field simply omits it.
	IssuesAnalyzed int    `json:"issuesAnalyzed,omitempty"`
	EdgesFound     int    `json:"edgesFound,omitempty"`
	Error          string `json:"error,omitempty"`
	// ChunksTotal / ChunksDone make in-flight progress visible on the wire.
	// ChunksTotal is set once chunking completes (before the per-chunk loop
	// starts), not only on terminal success, so a job observed mid-run
	// carries a correct denominator instead of 0.
	ChunksTotal int `json:"chunksTotal,omitempty"`
	ChunksDone  int `json:"chunksDone,omitempty"`
	// LastActivityAt is the analyzer's last progress heartbeat (bumped by
	// MarkProgress on every agent turn event). It is the only liveness
	// signal on a single-chunk run — the common case at the default
	// deps_analyzer_chunk_size (75) — where ChunksTotal/ChunksDone never
	// move past "1 / 1" for the run's entire duration.
	LastActivityAt *time.Time `json:"lastActivityAt,omitempty"`
	// Trigger distinguishes an operator-initiated run ("manual" — dashboard
	// button, direct API call, CLI) from a scheduler-initiated one ("auto" —
	// Task 4). Additive; every job produced by JobManager.Enqueue /
	// EnqueueWithOptions carries a non-empty value ("manual" by default).
	Trigger string `json:"trigger,omitempty"`
}

// DepsAnalyzer is the optional service backing the `/api/v1/deps/analyze`
// endpoints (Phase 2.3 of v0.2.0 todolist6). A nil DepsAnalyzer in Config
// makes both endpoints return 503.
type DepsAnalyzer interface {
	// EnqueueAnalysis kicks off (or returns the in-flight) analyzer job for the
	// given profile. Empty `profile` falls back to the configured
	// agent.deps_analyzer_profile. mode is the requested incremental-pass
	// mode ("auto" | "full" | "incremental"); empty behaves like "auto".
	// Every call through this interface is a manual (operator-initiated)
	// trigger — there is no "auto" trigger variant here because the
	// scheduler (Task 4) calls the concrete depsAnalyzerService directly.
	EnqueueAnalysis(profile, mode string) (jobID string, queuedAt time.Time, err error)
	// Status returns the analyzer job with the given ID, or false when absent.
	Status(jobID string) (DepsAnalyzeJobRow, bool)
	// DefaultProfile returns the configured agent.deps_analyzer_profile, or
	// empty when the analyzer is disabled.
	DefaultProfile() string
	// CancelAnalysis stops the running job with the given ID. Returns false
	// when no such job is running.
	CancelAnalysis(jobID string) bool
}

// ConfigInvalidStatus is the wire shape for a current WORKFLOW.md validation
// failure. The daemon keeps running on the last-valid config; the dashboard
// surfaces this banner so the operator knows their last edit didn't take.
//
// Path/Error are diagnostic and may be empty in older snapshots. RetryAttempt
// is 1-indexed (matches the value the operator sees in slog "retry_attempt"
// field). RetryAt is the absolute time of the next attempt (RFC3339).
type ConfigInvalidStatus struct {
	Path         string `json:"path,omitempty"`
	Error        string `json:"error"`
	RetryAttempt int    `json:"retryAttempt"`
	RetryAt      string `json:"retryAt,omitempty"`
}

// InputRequiredRow is one input-related issue in the snapshot.
type InputRequiredRow struct {
	Identifier string `json:"identifier"`
	SessionID  string `json:"sessionId"`
	State      string `json:"state"` // "input_required" | "pending_input_resume"
	Context    string `json:"context"`
	Backend    string `json:"backend,omitempty"`
	Profile    string `json:"profile,omitempty"`
	QueuedAt   string `json:"queuedAt"`
	// Stale is true when the entry's age exceeds the longest MaxAgeMinutes
	// across all enabled input_required automations (gap A). Surfaced on the
	// dashboard's input-required panel as a badge so an operator sees what
	// has been abandoned. Omitted when false to keep the wire payload tight.
	Stale bool `json:"stale,omitempty"`
	// AgeMinutes is the wall-clock age of the entry in whole minutes — handy
	// for the dashboard tooltip without re-parsing QueuedAt on every render.
	AgeMinutes int `json:"ageMinutes,omitempty"`
}

// SSHHostInfo is one entry in the configured SSH host pool.
type SSHHostInfo struct {
	Host        string `json:"host"`
	Description string `json:"description,omitempty"`
}

// ProfileDef is the JSON representation of one named agent profile.
type ProfileDef struct {
	Command          string   `json:"command"`
	Prompt           string   `json:"prompt,omitempty"`
	Soul             string   `json:"soul,omitempty"`
	Instructions     string   `json:"instructions,omitempty"`
	SoulFile         string   `json:"soulFile,omitempty"`
	InstructionsFile string   `json:"instructionsFile,omitempty"`
	SoulSet          bool     `json:"-"`
	InstructionsSet  bool     `json:"-"`
	Backend          string   `json:"backend,omitempty"`
	Enabled          bool     `json:"enabled"`
	AllowedActions   []string `json:"allowedActions,omitempty"`
	CreateIssueState string   `json:"createIssueState,omitempty"`
}

type AutomationTriggerDef = automationdef.Trigger
type AutomationFilterDef = automationdef.Filter
type AutomationPolicyDef = automationdef.Policy
type AutomationDef = automationdef.Definition

// ModelOption represents an available model for a backend (mirrors config.ModelOption for JSON).
type ModelOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CommentRow is one comment entry in a TrackerIssue response.
type CommentRow struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt,omitempty"` // RFC3339; "" when nil
}

// BlockerDetail is one issue blocking a TrackerIssue.
type BlockerDetail struct {
	Identifier string `json:"identifier"`
	State      string `json:"state,omitempty"`
	URL        string `json:"url,omitempty"`
}

type IssueStatusChangeRow struct {
	FromState    string    `json:"fromState,omitempty"`
	ToState      string    `json:"toState"`
	Source       string    `json:"source"`
	AutomationID string    `json:"automationId,omitempty"`
	TriggerType  string    `json:"triggerType,omitempty"`
	ProfileName  string    `json:"profileName,omitempty"`
	Backend      string    `json:"backend,omitempty"`
	WorkerHost   string    `json:"workerHost,omitempty"`
	At           time.Time `json:"at"`
}

// TrackerIssue is a single issue row returned by /api/v1/issues.
type TrackerIssue struct {
	Identifier        string `json:"identifier"`
	Title             string `json:"title"`
	State             string `json:"state"`
	Description       string `json:"description,omitempty"`
	URL               string `json:"url,omitempty"`
	OrchestratorState string `json:"orchestratorState"` // idle, running, retrying, paused, input_required, pending_input_resume
	TurnCount         int    `json:"turnCount,omitempty"`
	Tokens            int    `json:"tokens,omitempty"`
	ElapsedMs         int64  `json:"elapsedMs,omitempty"`
	LastMessage       string `json:"lastMessage,omitempty"`
	Error             string `json:"error,omitempty"`
	// Enriched fields
	Labels           []string               `json:"labels,omitempty"`
	Priority         *int                   `json:"priority,omitempty"`
	BranchName       *string                `json:"branchName,omitempty"`
	BlockedBy        []string               `json:"blockedBy,omitempty"`
	BlockedByDetails []BlockerDetail        `json:"blockedByDetails,omitempty"`
	Comments         []CommentRow           `json:"comments,omitempty"`
	StatusChanges    []IssueStatusChangeRow `json:"statusChanges,omitempty"`
	// Review is the issue's latest review (#135): each reviewer's verdict
	// and, once every reviewer has run, the quorum result.
	Review           *IssueReviewRow `json:"review,omitempty"`
	IneligibleReason string          `json:"ineligibleReason,omitempty"`
	// AgentProfile is the name of the per-issue agent profile override, if any.
	AgentProfile string `json:"agentProfile,omitempty"`
	// AgentBackend is the per-issue backend override, if any ("claude" or "codex").
	AgentBackend string `json:"agentBackend,omitempty"`
	// AutoSwitch is set when AgentProfile/AgentBackend come from an
	// automatic switch rather than an operator pin (CORE-055).
	AutoSwitch *AutoSwitchRow `json:"autoSwitch,omitempty"`
	// Playbook is the task-type playbook the issue's next run gets (#218),
	// empty for none; PlaybookSource says how it was chosen ("override",
	// "label", "keyword"); PlaybookOverride is the operator's choice (a
	// name or "none"), empty for automatic.
	Playbook         string `json:"playbook,omitempty"`
	PlaybookSource   string `json:"playbookSource,omitempty"`
	PlaybookOverride string `json:"playbookOverride,omitempty"`
}

// IssueLogEntry is one parsed log event for /api/v1/issues/{id}/logs.
type IssueLogEntry struct {
	Level   string `json:"level"`
	Event   string `json:"event"` // "text", "action", "subagent", "info", "warn", "pr", "turn"
	Message string `json:"message"`
	Tool    string `json:"tool,omitempty"`
	Time    string `json:"time,omitempty"` // HH:MM:SS wall-clock time of the event
	// Detail carries backend-specific structured metadata as a JSON string.
	// Populated for Codex shell completions (exit_code, status, output_size).
	Detail    string `json:"detail,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// Shutdown ends every streaming (SSE) handler of this Server — /events,
// /logs, /issues/{id}/log-stream and /issues/{id}/sublog-stream return, which
// ends their responses cleanly — and wakes broadcaster subscribers. Ordinary
// requests are not affected. Safe to call more than once.
//
// http.Server.Shutdown alone does NOT end these handlers: it neither cancels
// in-flight request contexts nor wakes a handler parked on its keepalive
// ticker, so it only waits (for its deadline) while the streams run on. On a
// config reload that left every open dashboard stream pinned to the old
// generation — serving its frozen snapshot plus keepalives, and keeping the
// old http.Server and Orchestrator reachable (CORE-025). cmd/itervox's
// serveOnListener therefore registers this with http.Server.RegisterOnShutdown:
// a reload ends the old generation's streams at once, the client reconnects
// (openAuthedEventStream treats a clean close as retryable, CORE-004), and the
// reconnect lands on the new generation because the old one no longer
// accepts.
func (s *Server) Shutdown() {
	if s == nil {
		return
	}
	s.streamsOnce.Do(func() {
		if s.streamsDone != nil {
			close(s.streamsDone)
		}
	})
	if s.bc != nil {
		s.bc.close()
	}
}

// Config holds all constructor parameters for a Server.
// Required fields: Snapshot, RefreshChan.
// Client provides orchestrator operations; nil → noopClient.
// FetchIssue is an optional fast-path for single-issue detail lookups; nil falls back to Client.FetchIssues.
// ProjectManager is optional: nil means GitHub tracker (no project API).
type Config struct {
	// Required
	Snapshot    func() StateSnapshot
	RefreshChan chan struct{}
	// LogFile is the path to the rotating log file for /api/v1/logs; empty disables it.
	LogFile string

	// Client provides all orchestrator operations. Nil → noopClient (no-ops).
	Client OrchestratorClient
	// FetchIssue is an optional fast-path for single-issue lookups.
	// Nil falls back to Client.FetchIssues scanning all issues.
	FetchIssue func(ctx context.Context, identifier string) (*TrackerIssue, error)
	// ProjectManager supports project filtering (Linear only). Nil = no project API.
	ProjectManager ProjectManager
	// ScopeManager supports the combined tracker scope (#182: projects AND
	// assignee AND labels; Linear only). Nil = no /tracker/* scope API.
	ScopeManager ScopeManager
	// APIToken, when non-empty, enables bearer-token authentication on all
	// /api/ routes except /api/v1/health. Requests must include the header
	// "Authorization: Bearer <token>".
	APIToken string
	// Tokens is the stored-token index (.itervox/tokens.json, #214): named
	// viewer / operator / admin tokens accepted alongside APIToken, which
	// stays admin. Consulted only in token mode (APIToken set); nil means
	// no stored tokens and answers 503 on the token-management routes.
	Tokens *apitokens.Store
	// ActionTokenStore validates short-lived per-run grants for agent action routes.
	ActionTokenStore *agentactions.Store
	// SkillsClient exposes the skills-inventory surface (T-87). Nil → noop.
	SkillsClient SkillsClient
	// Rules backs the Rules editor endpoints (#157). Nil → 503.
	Rules RulesClient
	// Memory backs the Memory page endpoints (#202). Nil → 503.
	Memory MemoryClient
	// Guardrails backs the guardrail endpoints of agent shims and hooks
	// (#204). Nil → 501.
	Guardrails GuardrailsClient
	// SettingsLayers backs the schema-driven Settings page's layer
	// endpoints (#158). Nil → 503 (the schema endpoint always answers).
	SettingsLayers SettingsLayersClient
	// DepsAnalyzer backs the /api/v1/deps/analyze endpoints (Phase 2.3 of
	// v0.2.0 todolist6). Nil makes the endpoints return 503.
	DepsAnalyzer DepsAnalyzer
	// MergeStrategy is the operator-configured default strategy for the
	// merge_pr agent action (agent.merge_strategy). Used when a request omits
	// its own strategy; empty falls back to "squash". Read-only after startup
	// (not in the cfgMu allowlist), so it is passed by value here. gaps_11 G-3.
	MergeStrategy string
	// MergeBlockLabels is the operator-configured PR label block-list for the
	// merge_pr agent action (agent.merge_block_labels). Nil/empty falls back
	// to DefaultMergeBlockLabels(). Read-only after startup. gaps_11 G-3.
	MergeBlockLabels []string
	// AllowUncheckedMerge mirrors config.AgentConfig.AllowUncheckedMerge
	// (agent.allow_unchecked_merge) — SRV-1 unarmed-gate opt-out for the
	// merge_pr agent action. Read-only after startup.
	AllowUncheckedMerge bool
	// BindHost is server.host. In server.allow_unauthenticated mode (no
	// APIToken) it is one of the names the DNS-rebinding Host guard accepts
	// (CORE-162, see hostGuard). IP literals need no listing.
	BindHost string
	// AllowedHosts is server.allowed_hosts: extra Host names (reverse proxy,
	// tunnel, MagicDNS, container service name) the Host guard accepts in
	// server.allow_unauthenticated mode. Ignored in token mode.
	AllowedHosts []string
	// Readiness supplies the /api/v1/ready probe's inputs (CORE-043). It must
	// be cheap and lock-light — atomics published by the event loop plus the
	// tracker rate-limit gate — and must never take cfgMu or build a
	// snapshot. Nil makes /ready answer 503.
	Readiness func() ReadinessSignals
	// Metrics, when non-nil, serves GET /metrics (Prometheus text format,
	// CORE-045) behind the same bearer token as /api. cmd/itervox sets it
	// only when server.metrics.enabled is true; nil answers 404.
	Metrics http.Handler
	// ReportClientError receives one redacted web client error report
	// (POST /api/v1/client-errors, CORE-048) and must not block: cmd/itervox
	// wires it to Orchestrator.RecordFailure. false means the event channel
	// was full (the handler answers 503). Nil: reports are logged and counted
	// only.
	ReportClientError func(ClientErrorReport) bool
	// Webhooks, when non-nil, serves POST /api/v1/webhooks/<tracker> for the
	// tracker it names (#131). The route is outside the bearer-token group
	// and the Host guard: GitHub and Linear cannot send the token, and every
	// delivery must carry the webhook secret's HMAC signature instead.
	Webhooks WebhookHandler
	// PREventWebhooks, when non-nil, serves POST /api/v1/webhooks/pr-events
	// (#173): signed GitHub check_run, status, pull_request and
	// pull_request_review deliveries for the PR-event automation triggers.
	// Same exemptions and signature rule as Webhooks.
	PREventWebhooks WebhookHandler
	// ClientErrorLimiter rate-limits POST /api/v1/client-errors. Nil uses
	// the process-wide default, which survives the Server rebuild every
	// WORKFLOW.md reload performs.
	ClientErrorLimiter *ClientErrorLimiter
	// Sessions backs the /api/v1/sessions endpoints (#178): the list,
	// messages, transcripts, tmux screen and keys, and New session / kill.
	// Nil answers 503.
	Sessions SessionsClient
	// RepoReadiness runs the repository readiness checks for GET
	// /api/v1/repo/readiness (#200): the same check functions as
	// `itervox doctor`, injected by cmd/itervox. Nil answers 503.
	RepoReadiness RepoReadinessFunc
	// RepoEvals reads the persisted eval reports for GET
	// /api/v1/repo/evals (#201). Read-only: no eval run is started from the
	// server. Nil answers 503.
	RepoEvals RepoEvalsFunc
	// RepoHotspots returns the latest failure-hotspots result for GET
	// /api/v1/repo/hotspots (#201), computed by a background job off the
	// event loop. Nil answers 503.
	RepoHotspots RepoHotspotsFunc
	// Version is the daemon's build version, reported by GET
	// /api/v1/summary (#212). Read-only after startup.
	Version string
	// Dispatch backs GET /api/v1/feed and POST /api/v1/dispatch/pause|resume
	// (#213). Nil answers 503.
	Dispatch DispatchClient
	// Changes backs GET /api/v1/issues/{identifier}/context and /diff
	// (#215). Nil answers 503.
	Changes ChangesClient
	// Playbooks backs the playbook, decision-log and verify-skill-issue
	// endpoints (#218). Nil answers 503.
	Playbooks PlaybooksClient
	// SteeringPhrases are the steering chips GET /api/v1/steering/phrases
	// serves (#218, sessions.steering_phrases). Read-only after startup.
	SteeringPhrases []config.SteeringPhrase
}

// Server is an HTTP server exposing orchestrator state.
type Server struct {
	router         *chi.Mux
	snapshot       func() StateSnapshot
	refreshChan    chan struct{}
	logFile        string
	client         OrchestratorClient
	fetchIssue     func(ctx context.Context, identifier string) (*TrackerIssue, error)
	projectManager ProjectManager
	scopeManager   ScopeManager
	bc             *broadcaster
	// frames caches the encoded snapshot frame of the broadcaster's
	// current version for every /events client (#210), one cache per
	// caller scope (#214): the frame carries callerScope, and below admin
	// it is the redacted snapshot.
	frames [len(frameScopes)]sseFrameCache
	// streamsDone is closed by Shutdown; every streaming handler returns
	// when it closes (CORE-025). A Server built without New has a nil
	// channel, which never fires.
	streamsDone  chan struct{}
	streamsOnce  sync.Once
	apiToken     string
	tokens       *apitokens.Store
	actionTokens *agentactions.Store
	skills       SkillsClient
	rules        RulesClient
	memory       MemoryClient
	guardrails   GuardrailsClient
	// settingsLayers mirrors Config.SettingsLayers (#158).
	settingsLayers SettingsLayersClient
	depsAnalyzer   DepsAnalyzer
	// mergeStrategy / mergeBlockLabels mirror Config.MergeStrategy /
	// Config.MergeBlockLabels — startup-fixed merge_pr policy. gaps_11 G-3.
	mergeStrategy    string
	mergeBlockLabels []string
	// allowUncheckedMerge mirrors Config.AllowUncheckedMerge — SRV-1
	// unarmed-gate opt-out, startup-fixed.
	allowUncheckedMerge bool
	// ghRun invokes the gh CLI for PR-surface handlers (merge_pr, comment_pr).
	// Nil falls back to runGH; tests inject a fake.
	ghRun func(ctx context.Context, args ...string) ([]byte, error)
	// readiness mirrors Config.Readiness (CORE-043).
	readiness func() ReadinessSignals
	// metrics mirrors Config.Metrics (CORE-045).
	metrics http.Handler
	// reportClientError mirrors Config.ReportClientError; clientErrors is
	// the route's rate limiter (CORE-048): Config.ClientErrorLimiter or the
	// process-wide DefaultClientErrorLimiter.
	reportClientError func(ClientErrorReport) bool
	webhooks          WebhookHandler
	prEventWebhooks   WebhookHandler
	clientErrors      *ClientErrorLimiter
	sessions          SessionsClient
	// repoReadiness mirrors Config.RepoReadiness (#200).
	repoReadiness RepoReadinessFunc
	// repoEvals / repoHotspots mirror Config.RepoEvals / RepoHotspots (#201).
	repoEvals    RepoEvalsFunc
	repoHotspots RepoHotspotsFunc
	// version mirrors Config.Version (#212).
	version string
	// dispatch mirrors Config.Dispatch (#213).
	dispatch DispatchClient
	// changes mirrors Config.Changes (#215).
	changes ChangesClient
	// playbooks / steeringPhrases mirror Config.Playbooks /
	// Config.SteeringPhrases (#218).
	playbooks       PlaybooksClient
	steeringPhrases []config.SteeringPhrase
	// clearAllInFlight is set while a DELETE /api/v1/workspaces clear runs in
	// the background, so a second request is refused instead of starting a
	// second clear over the same tree (CORE-114).
	clearAllInFlight atomic.Bool
}

// New constructs a Server from a Config. Snapshot and RefreshChan must be non-nil.
func New(cfg Config) *Server {
	client := cfg.Client
	if client == nil {
		client = noopClient{}
	}
	skillsClient := cfg.SkillsClient
	if skillsClient == nil {
		skillsClient = noopSkillsClient{}
	}
	s := &Server{
		router:         chi.NewRouter(),
		snapshot:       cfg.Snapshot,
		refreshChan:    cfg.RefreshChan,
		logFile:        cfg.LogFile,
		client:         client,
		fetchIssue:     cfg.FetchIssue,
		projectManager: cfg.ProjectManager,
		scopeManager:   cfg.ScopeManager,
		bc:             newBroadcaster(),
		streamsDone:    make(chan struct{}),
		apiToken:       cfg.APIToken,
		tokens:         cfg.Tokens,
		actionTokens:   cfg.ActionTokenStore,
		skills:         skillsClient,
		rules:          cfg.Rules,
		memory:         cfg.Memory,
		guardrails:     cfg.Guardrails,
		settingsLayers: cfg.SettingsLayers,
		depsAnalyzer:   cfg.DepsAnalyzer,

		mergeStrategy:       cfg.MergeStrategy,
		mergeBlockLabels:    cfg.MergeBlockLabels,
		allowUncheckedMerge: cfg.AllowUncheckedMerge,
		readiness:           cfg.Readiness,
		metrics:             cfg.Metrics,
		reportClientError:   cfg.ReportClientError,
		webhooks:            cfg.Webhooks,
		prEventWebhooks:     cfg.PREventWebhooks,
		clientErrors:        cfg.ClientErrorLimiter,
		sessions:            cfg.Sessions,
		repoReadiness:       cfg.RepoReadiness,
		repoEvals:           cfg.RepoEvals,
		repoHotspots:        cfg.RepoHotspots,
		version:             cfg.Version,
		dispatch:            cfg.Dispatch,
		changes:             cfg.Changes,
		playbooks:           cfg.Playbooks,
		steeringPhrases:     cfg.SteeringPhrases,
	}
	if s.clientErrors == nil {
		s.clientErrors = DefaultClientErrorLimiter
	}
	// Root-router middleware: applies to every response class (200s, 401s,
	// the SPA fallback, and SSE), unlike the bearer-auth group which only
	// wraps the authenticated sub-router. Must be registered before routes()
	// per chi's "middleware before routes" rule.
	s.router.Use(securityHeadersMiddleware)
	// CORE-162: DNS-rebinding guard, ahead of every route (SPA, static files,
	// /api, SSE). Unauthenticated mode only — see hostGuard for why token
	// mode does not need it.
	if s.apiToken == "" {
		s.router.Use(hostGuard(cfg.BindHost, cfg.AllowedHosts))
	}
	s.routes()
	return s
}

// Validate checks that all required Config fields are set.
// Call before starting the HTTP listener.
func (s *Server) Validate() error {
	var missing []string
	if s.snapshot == nil {
		missing = append(missing, "Snapshot")
	}
	if s.refreshChan == nil {
		missing = append(missing, "RefreshChan")
	}
	if len(missing) > 0 {
		return fmt.Errorf("server: missing required Config fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Notify signals all active SSE clients to push the current state immediately.
func (s *Server) Notify() {
	s.bc.notify()
}

func spaHandler() http.Handler {
	fs := spaFS()
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// index.html must never be cached: it references hashed JS/CSS assets,
		// and a stale copy would load old bundles after a binary rebuild.
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		f, err := fs.Open(r.URL.Path)
		if err != nil {
			// File not found — serve index.html for React Router client-side routing.
			u := *r.URL
			u.Path = "/"
			r2 := *r
			r2.URL = &u
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			fileServer.ServeHTTP(w, &r2)
			return
		}
		_ = f.Close()
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// API routes are nested under /api so method-not-allowed works correctly
	// even when the SPA catch-all is registered at the root level.
	s.router.Route("/api/v1", func(r chi.Router) {
		// Health check is unauthenticated so load balancers can reach it.
		r.Get("/health", s.handleHealth)
		// Readiness (CORE-043): unauthenticated for the same reason as
		// /health — container and load-balancer probes carry no token.
		r.Get("/ready", s.handleReady)
		r.Post("/agent-actions/{identifier}/comment", s.handleAgentComment)
		r.Post("/agent-actions/{identifier}/comment_pr", s.handleAgentCommentPR)
		r.Post("/agent-actions/{identifier}/merge_pr", s.handleAgentMergePR)
		r.Post("/agent-actions/{identifier}/create-issue", s.handleAgentCreateIssue)
		r.Post("/agent-actions/{identifier}/move-state", s.handleAgentMoveState)
		r.Post("/agent-actions/{identifier}/provide-input", s.handleAgentProvideInput)
		// #204: guardrail shims and hooks, gated by the run's guardrail token.
		r.Post("/agent-actions/{identifier}/guardrail/events", s.handleGuardrailEvent)
		r.Post("/agent-actions/{identifier}/guardrail/approvals", s.handleGuardrailApprovalRequest)
		r.Get("/agent-actions/{identifier}/guardrail/approvals/{id}", s.handleGuardrailApprovalStatus)
		// Tracker webhooks (#131): signed by the webhook secret, not the
		// bearer token.
		r.Post("/webhooks/{tracker}", s.handleWebhook)

		// If an API token is configured, all remaining routes require it.
		// Use r.Group to create a sub-router so middleware is applied only to
		// authenticated routes without violating chi's "middleware before routes" rule.
		r.Group(func(r chi.Router) {
			s.useAuth(r)

			r.Get("/whoami", s.handleWhoami) // #214
			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleRevokeToken)
			r.Post("/tokens/{id}/rotate", s.handleRotateToken)
			r.Get("/state", s.handleState)
			r.Get("/summary", s.handleSummary)                                     // #212: ETag'd poll target for supervisors
			r.Get("/feed", s.handleFeed)                                           // #213: the event feed, by cursor
			r.Post("/dispatch/pause", s.handlePauseDispatch)                       // #213
			r.Post("/dispatch/resume", s.handleResumeDispatch)                     // #213
			r.Get("/history", s.handleHistory)                                     // #186
			r.Get("/history/prompt-versions", s.handleHistoryPromptVersions)       // #186
			r.Get("/repo/readiness", s.handleRepoReadiness)                        // #200
			r.Get("/repo/evals", s.handleRepoEvals)                                // #201
			r.Get("/repo/hotspots", s.handleRepoHotspots)                          // #201
			r.Post("/repo/readiness/verify-skill-issue", s.handleVerifySkillIssue) // #218
			r.Get("/playbooks", s.handleListPlaybooks)                             // #218
			r.Get("/steering/phrases", s.handleSteeringPhrases)                    // #218
			r.Get("/events", s.handleEvents)
			r.Get("/sessions", s.handleListSessions)
			r.Post("/sessions/{id}/message", s.handleSessionMessage)
			r.Get("/sessions/{id}/transcript", s.handleSessionTranscript)
			r.Post("/sessions", s.handleStartSession)
			r.Delete("/sessions/{id}", s.handleKillSession)
			r.Get("/sessions/{id}/screen", s.handleSessionScreen)
			r.Post("/sessions/{id}/keys", s.handleSessionKeys)
			r.Get("/issues", s.handleIssues)
			r.Get("/issues/{identifier}", s.handleIssueDetail)
			r.Get("/issues/{identifier}/logs", s.handleIssueLogs)
			r.Get("/issues/{identifier}/log-stream", s.handleIssueLogStream)
			r.Get("/issues/{identifier}/sublogs", s.handleSubLogs)
			r.Get("/issues/{identifier}/sublog-stream", s.handleSubLogStream)
			r.Get("/issues/{identifier}/context", s.handleIssueContext)       // #215
			r.Get("/issues/{identifier}/diff", s.handleIssueDiff)             // #215: hunks need operator
			r.Get("/issues/{identifier}/decisions", s.handleIssueDecisions)   // #218
			r.Post("/issues/{identifier}/playbook", s.handleSetIssuePlaybook) // #218
			r.Delete("/issues/{identifier}/logs", s.handleClearIssueLogs)
			r.Delete("/issues/{identifier}/sublogs", s.handleClearIssueSubLogs)
			r.Delete("/issues/{identifier}/sublogs/{sessionId}", s.handleClearSessionSublog)
			r.Get("/logs/identifiers", s.handleLogIdentifiers)
			r.Delete("/logs", s.handleClearAllLogs)
			r.Delete("/issues/{identifier}", s.handleCancelIssue)
			r.Post("/issues/{identifier}/cancel", s.handleCancelIssue)
			r.Post("/issues/{identifier}/resume", s.handleResumeIssue)
			r.Post("/issues/{identifier}/reanalyze", s.handleReanalyzeIssue)
			r.Post("/issues/{identifier}/terminate", s.handleTerminateIssue)
			r.Post("/issues/{identifier}/ai-review", s.handleAIReview)
			r.Patch("/issues/{identifier}/state", s.handleUpdateIssueState)
			r.Post("/issues/{identifier}/profile", s.handleSetIssueProfile)
			r.Post("/issues/{identifier}/backend", s.handleSetIssueBackend)
			r.Post("/issues/{identifier}/provide-input", s.handleProvideInput)
			r.Post("/issues/{identifier}/dismiss-input", s.handleDismissInput)
			r.Post("/issues/{identifier}/comment", s.handleIssueComment)
			r.Post("/issues/{identifier}/deps-override", s.handleSetDepsOverride)
			r.Post("/issues/{identifier}/failures/ack", s.handleAckFailures)
			r.Delete("/issues/{identifier}/deps-override", s.handleClearDepsOverride)
			// M3-close V1: operator clear of an agent-backend breaker.
			r.Post("/backend-health/clear", s.handleClearBackendBreaker)
			r.Post("/outbox/{id}/retry", s.handleRetryOutboxEntry)
			r.Delete("/outbox/{id}", s.handleDropOutboxEntry)
			r.Post("/settings/inline-input", s.handleSetInlineInput)
			// CORE-048: web client error reports (8 KiB cap, rate-limited).
			r.Post("/client-errors", s.handleClientError)
			r.Get("/logs", s.handleLogs)
			r.Post("/refresh", s.handleRefresh)
			r.Get("/projects", s.handleListProjects)
			r.Get("/projects/filter", s.handleGetProjectFilter)
			r.Put("/projects/filter", s.handleSetProjectFilter)
			r.Get("/tracker/scope", s.handleGetTrackerScope)                // #182
			r.Put("/tracker/scope", s.handleSetTrackerScope)                // #182
			r.Get("/tracker/labels", s.handleTrackerLabels)                 // #182
			r.Get("/tracker/users", s.handleTrackerUsers)                   // #182
			r.Get("/tracker/teams", s.handleTrackerTeams)                   // #198
			r.Post("/tracker/filter/preview", s.handleTrackerFilterPreview) // #198
			r.Post("/settings/workers", s.handleSetWorkers)
			r.Delete("/workspaces", s.handleClearAllWorkspaces)
			r.Post("/settings/workspace/auto-clear", s.handleSetAutoClearWorkspace)
			r.Post("/settings/deps-analysis-mode", s.handleSetDepsAnalysisMode)
			r.Get("/settings/models", s.handleListModels)
			r.Post("/settings/models/refresh", s.handleRefreshModels)
			r.Get("/settings/reviewer", s.handleGetReviewer)
			r.Put("/settings/reviewer", s.handleSetReviewer)
			r.Get("/settings/profiles", s.handleListProfiles)
			r.Put("/settings/profiles/{name}", s.handleUpsertProfile)
			r.Delete("/settings/profiles/{name}", s.handleDeleteProfile)
			r.Put("/settings/automations", s.handleSetAutomations)
			r.Post("/automations/{id}/test", s.handleTestAutomation)
			r.Put("/settings/tracker/states", s.handleUpdateTrackerStates)
			r.Put("/settings/tracker/failed-state", s.handleSetFailedState)
			r.Put("/settings/agent/max-retries", s.handleSetMaxRetries)
			r.Put("/settings/agent/max-switches-per-issue-per-window", s.handleSetMaxSwitches)
			r.Put("/settings/agent/switch-window-hours", s.handleSetSwitchWindowHours)
			r.Post("/settings/ssh-hosts", s.handleAddSSHHost)
			r.Delete("/settings/ssh-hosts/{host}", s.handleRemoveSSHHost)
			r.Put("/settings/dispatch-strategy", s.handleSetDispatchStrategy)
			// Schema-driven Settings page (#158).
			r.Get("/settings/schema", s.handleSettingsSchema)
			r.Get("/settings/layers", s.handleGetSettingsLayers)
			r.Put("/settings/layers/{layer}", s.handleSaveSettingsLayer)

			// Dependency analysis (Phase 2.3 of v0.2.0 todolist6).
			r.Post("/deps/analyze", s.handleDepsAnalyzeEnqueue)
			r.Get("/deps/analyze/{jobId}", s.handleDepsAnalyzeStatus)
			r.Delete("/deps/analyze/{jobId}", s.handleDepsAnalyzeCancel)

			// Skills inventory + analytics (T-87, T-95/T-96, T-102).
			// Rules editor (#157).
			r.Get("/rules", s.handleGetRules)
			r.Get("/rules/preview", s.handlePreviewRules)
			r.Put("/rules/{layer}", s.handleSaveRules)
			// Memory page (#202).
			r.Get("/memory", s.handleListMemory)
			r.Get("/memory/recall", s.handleRecallMemory) // #216
			r.Post("/memory", s.handleCreateMemory)
			r.Put("/memory/{id}", s.handleUpdateMemory)
			r.Delete("/memory/{id}", s.handleDeleteMemory)
			r.Post("/memory/{id}/pin", s.handlePinMemory)
			r.Post("/memory/{id}/promote", s.handlePromoteMemory)
			r.Post("/memory/{id}/approve", s.handleApproveLesson) // #203
			r.Post("/memory/{id}/reject", s.handleRejectLesson)   // #203
			// #217: lesson to rule, enforcement suggestions.
			r.Post("/memory/{id}/promote-to-rule", s.handlePromoteLessonToRule)
			r.Post("/memory/{id}/enforcement/accept", s.handleAcceptLessonEnforcement)
			r.Post("/memory/{id}/enforcement/dismiss", s.handleDismissLessonEnforcement)
			r.Get("/skills/inventory", s.handleSkillsInventory)
			r.Post("/skills/scan", s.handleSkillsScan)
			r.Get("/skills/issues", s.handleSkillsIssues)
			r.Post("/skills/fix", s.handleSkillsFix)
			r.Get("/skills/analytics", s.handleSkillsAnalytics)
			r.Get("/skills/analytics/recommendations", s.handleSkillsAnalyticsRecommendations)
			// Backend-aware references (#123): editor check + profile backends.
			r.Post("/skills/check-refs", s.handleSkillsCheckRefs)
			r.Get("/skills/profile-backends", s.handleSkillsProfileBackends)

			r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			})
		})
	})

	// Prometheus metrics (CORE-045). Always routed so a disabled endpoint
	// answers 404 instead of falling through to the SPA shell; behind the
	// bearer token in token mode, and the CSRF guard (plus the root Host
	// guard) in unauthenticated mode — never in the unauthenticated set.
	s.router.Group(func(r chi.Router) {
		s.useAuth(r)
		r.Get("/metrics", s.handleMetrics)
	})

	// React SPA: serves all non-API paths from the embedded web/dist.
	// Falls back to index.html so React Router client-side routing works.
	s.router.Handle("/*", spaHandler())
}

// useAuth installs the authentication and scope middlewares of an
// authenticated group (#214): the bearer check in token mode; in
// server.allow_unauthenticated mode the CSRF guard (CORE-041) and an admin
// principal for every caller. scopeMiddleware then enforces routeScopes in
// both modes.
func (s *Server) useAuth(r chi.Router) {
	if s.apiToken != "" {
		r.Use(s.bearerAuthMiddleware)
	} else {
		// server.allow_unauthenticated: no bearer check, so refuse
		// cross-site state-changing requests instead (CORE-041).
		r.Use(crossOriginGuardMiddleware)
		r.Use(unauthenticatedAdminMiddleware)
	}
	r.Use(scopeMiddleware)
}

// handleMetrics serves the injected Prometheus handler, or 404 when
// server.metrics.enabled is off.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, "not_found", "metrics are disabled (set server.metrics.enabled: true)")
		return
	}
	s.metrics.ServeHTTP(w, r)
}

// handleHealth returns a lightweight 200 OK for load balancer probes.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
