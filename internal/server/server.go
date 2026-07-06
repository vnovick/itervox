package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/domain"

	"github.com/go-chi/chi/v5"
)

// errNotConfigured is returned by no-op callback stubs installed in New()
// for optional Config fields that were left nil by the caller.
var errNotConfigured = errors.New("not configured")

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
	// AutomationID / TriggerType propagate the automation context onto
	// completed runs so that the Activity tab and Timeline filter chip can
	// scope history per-automation. Empty for manual runs.
	AutomationID string `json:"automationId,omitempty"`
	TriggerType  string `json:"triggerType,omitempty"`
	// CommentCount: comments posted during this run (T-6 surface).
	CommentCount int `json:"commentCount,omitempty"`
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
	SetProjectFilter(slugs []string)
	GetProjectFilter() []string
}

// OrchestratorClient abstracts the orchestrator and workflow operations called
// by HTTP handlers. A nil value in Config is replaced with noopClient.
// PRMergedEmitter is an optional capability some OrchestratorClient
// implementations expose: the daemon-side merge_pr handler invokes it on a
// successful gh merge so pr_merged automations fire downstream. Discovered
// via type assertion to avoid bloating the main interface.
type PRMergedEmitter interface {
	EmitPRMerged(ctx context.Context, identifier, prURL string, prNumber int, mergedSHA, baseRef string) error
}

type OrchestratorClient interface {
	FetchIssues(ctx context.Context) ([]TrackerIssue, error)
	CancelIssue(identifier string) bool
	ResumeIssue(identifier string) bool
	TerminateIssue(identifier string) bool
	ReanalyzeIssue(identifier string) bool
	FetchLogs(identifier string) []string
	ClearLogs(identifier string) error
	ClearAllLogs() error
	ClearIssueSubLogs(identifier string) error
	ClearSessionSublog(identifier, sessionID string) error
	FetchSubLogs(ctx context.Context, identifier string) ([]domain.IssueLogEntry, error)
	DispatchReviewer(identifier string) error
	CommentOnIssue(ctx context.Context, identifier, body string) error
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
	AvailableModels() map[string][]ModelOption
	ReviewerConfig() (profile string, autoReview bool)
	SetReviewerConfig(profile string, autoReview bool) error
	UpsertProfile(name string, def ProfileDef, originalName string) error
	DeleteProfile(name string) error
	SetAutomations(automations []AutomationDef) error
	SetAutoClearWorkspace(enabled bool) error
	ClearAllWorkspaces() error
	FetchLogIdentifiers() []string
	UpdateTrackerStates(active, terminal []string, completion string) error
	AddSSHHost(host, description string) error
	RemoveSSHHost(host string) error
	SetDispatchStrategy(strategy string) error
	ProvideInput(identifier, message string) bool
	DismissInput(identifier string) bool
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
}

// noopClient implements OrchestratorClient with harmless defaults.
// Boolean methods return false; error methods return errNotConfigured.
type noopClient struct{}

func (noopClient) FetchIssues(context.Context) ([]TrackerIssue, error) { return nil, errNotConfigured }
func (noopClient) CancelIssue(string) bool                             { return false }
func (noopClient) ResumeIssue(string) bool                             { return false }
func (noopClient) TerminateIssue(string) bool                          { return false }
func (noopClient) ReanalyzeIssue(string) bool                          { return false }
func (noopClient) FetchLogs(string) []string                           { return nil }
func (noopClient) ClearLogs(string) error                              { return errNotConfigured }
func (noopClient) ClearAllLogs() error                                 { return errNotConfigured }
func (noopClient) ClearIssueSubLogs(string) error                      { return errNotConfigured }
func (noopClient) ClearSessionSublog(string, string) error             { return errNotConfigured }
func (noopClient) FetchSubLogs(context.Context, string) ([]domain.IssueLogEntry, error) {
	return nil, nil
}
func (noopClient) DispatchReviewer(string) error                        { return errNotConfigured }
func (noopClient) CommentOnIssue(context.Context, string, string) error { return errNotConfigured }
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
func (noopClient) AvailableModels() map[string][]ModelOption              { return nil }
func (noopClient) ReviewerConfig() (string, bool)                         { return "", false }
func (noopClient) SetReviewerConfig(string, bool) error                   { return nil }
func (noopClient) UpsertProfile(string, ProfileDef, string) error         { return errNotConfigured }
func (noopClient) DeleteProfile(string) error                             { return errNotConfigured }
func (noopClient) SetAutomations([]AutomationDef) error                   { return errNotConfigured }
func (noopClient) SetAutoClearWorkspace(bool) error                       { return errNotConfigured }
func (noopClient) ClearAllWorkspaces() error                              { return errNotConfigured }
func (noopClient) FetchLogIdentifiers() []string                          { return nil }
func (noopClient) UpdateTrackerStates([]string, []string, string) error   { return errNotConfigured }
func (noopClient) AddSSHHost(string, string) error                        { return errNotConfigured }
func (noopClient) RemoveSSHHost(string) error                             { return errNotConfigured }
func (noopClient) SetDispatchStrategy(string) error                       { return errNotConfigured }
func (noopClient) ProvideInput(string, string) bool                       { return false }
func (noopClient) DismissInput(string) bool                               { return false }
func (noopClient) SetInlineInput(bool) error                              { return errNotConfigured }
func (noopClient) BumpCommentCount(string)                                {}
func (noopClient) TestAutomation(context.Context, string, string) error   { return errNotConfigured }

// FuncClient builds an OrchestratorClient from individual function fields.
// Any nil field falls back to the noopClient default. Intended for tests.
type FuncClient struct {
	FetchIssuesFn                     func(context.Context) ([]TrackerIssue, error)
	CancelIssueFn                     func(string) bool
	ResumeIssueFn                     func(string) bool
	TerminateIssueFn                  func(string) bool
	ReanalyzeIssueFn                  func(string) bool
	FetchLogsFn                       func(string) []string
	ClearLogsFn                       func(string) error
	ClearAllLogsFn                    func() error
	ClearIssueSubLogsFn               func(string) error
	ClearSessionSublogFn              func(string, string) error
	DispatchReviewerFn                func(string) error
	CommentOnIssueFn                  func(context.Context, string, string) error
	CreateIssueFn                     func(context.Context, string, string, string, string) (*domain.Issue, error)
	UpdateIssueStateFn                func(context.Context, string, string) error
	SetWorkersFn                      func(int) error
	BumpWorkersFn                     func(int) (int, error)
	SetMaxRetriesFn                   func(int) error
	MaxRetriesFn                      func() int
	SetFailedStateFn                  func(string) error
	FailedStateFn                     func() string
	SetMaxSwitchesPerIssuePerWindowFn func(int) error
	MaxSwitchesPerIssuePerWindowFn    func() int
	SetSwitchWindowHoursFn            func(int) error
	SwitchWindowHoursFn               func() int
	SetIssueProfileFn                 func(string, string)
	SetIssueBackendFn                 func(string, string)
	ProfileDefsFn                     func() map[string]ProfileDef
	AvailableModelsFn                 func() map[string][]ModelOption
	ReviewerConfigFn                  func() (string, bool)
	SetReviewerConfigFn               func(string, bool) error
	UpsertProfileFn                   func(string, ProfileDef, string) error
	DeleteProfileFn                   func(string) error
	SetAutomationsFn                  func([]AutomationDef) error
	SetAutoClearWorkspaceFn           func(bool) error
	ClearAllWorkspacesFn              func() error
	FetchLogIdentifiersFn             func() []string
	UpdateTrackerStatesFn             func([]string, []string, string) error
	FetchSubLogsFn                    func(context.Context, string) ([]domain.IssueLogEntry, error)
	AddSSHHostFn                      func(string, string) error
	RemoveSSHHostFn                   func(string) error
	SetDispatchStrategyFn             func(string) error
	SetInlineInputFn                  func(bool) error
	ProvideInputFn                    func(string, string) bool
	DismissInputFn                    func(string) bool
	BumpCommentCountFn                func(string)
	TestAutomationFn                  func(context.Context, string, string) error
}

func (c *FuncClient) FetchIssues(ctx context.Context) ([]TrackerIssue, error) {
	if c.FetchIssuesFn != nil {
		return c.FetchIssuesFn(ctx)
	}
	return nil, errNotConfigured
}
func (c *FuncClient) CancelIssue(id string) bool {
	if c.CancelIssueFn != nil {
		return c.CancelIssueFn(id)
	}
	return false
}
func (c *FuncClient) ResumeIssue(id string) bool {
	if c.ResumeIssueFn != nil {
		return c.ResumeIssueFn(id)
	}
	return false
}
func (c *FuncClient) TerminateIssue(id string) bool {
	if c.TerminateIssueFn != nil {
		return c.TerminateIssueFn(id)
	}
	return false
}
func (c *FuncClient) ReanalyzeIssue(id string) bool {
	if c.ReanalyzeIssueFn != nil {
		return c.ReanalyzeIssueFn(id)
	}
	return false
}
func (c *FuncClient) FetchLogs(id string) []string {
	if c.FetchLogsFn != nil {
		return c.FetchLogsFn(id)
	}
	return nil
}
func (c *FuncClient) ClearLogs(id string) error {
	if c.ClearLogsFn != nil {
		return c.ClearLogsFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearAllLogs() error {
	if c.ClearAllLogsFn != nil {
		return c.ClearAllLogsFn()
	}
	return errNotConfigured
}
func (c *FuncClient) ClearIssueSubLogs(id string) error {
	if c.ClearIssueSubLogsFn != nil {
		return c.ClearIssueSubLogsFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearSessionSublog(id, sessionID string) error {
	if c.ClearSessionSublogFn != nil {
		return c.ClearSessionSublogFn(id, sessionID)
	}
	return errNotConfigured
}
func (c *FuncClient) FetchSubLogs(ctx context.Context, id string) ([]domain.IssueLogEntry, error) {
	if c.FetchSubLogsFn != nil {
		return c.FetchSubLogsFn(ctx, id)
	}
	return nil, nil
}
func (c *FuncClient) DispatchReviewer(id string) error {
	if c.DispatchReviewerFn != nil {
		return c.DispatchReviewerFn(id)
	}
	return errNotConfigured
}
func (c *FuncClient) CommentOnIssue(ctx context.Context, identifier, body string) error {
	if c.CommentOnIssueFn != nil {
		return c.CommentOnIssueFn(ctx, identifier, body)
	}
	return errNotConfigured
}
func (c *FuncClient) CreateIssue(ctx context.Context, identifier, title, body, state string) (*domain.Issue, error) {
	if c.CreateIssueFn != nil {
		return c.CreateIssueFn(ctx, identifier, title, body, state)
	}
	return nil, errNotConfigured
}
func (c *FuncClient) UpdateIssueState(ctx context.Context, id, state string) error {
	if c.UpdateIssueStateFn != nil {
		return c.UpdateIssueStateFn(ctx, id, state)
	}
	return errNotConfigured
}
func (c *FuncClient) SetWorkers(n int) error {
	if c.SetWorkersFn != nil {
		return c.SetWorkersFn(n)
	}
	return nil
}
func (c *FuncClient) BumpWorkers(delta int) (int, error) {
	if c.BumpWorkersFn != nil {
		return c.BumpWorkersFn(delta)
	}
	return 0, nil
}
func (c *FuncClient) SetMaxRetries(n int) error {
	if c.SetMaxRetriesFn != nil {
		return c.SetMaxRetriesFn(n)
	}
	return nil
}
func (c *FuncClient) MaxRetries() int {
	if c.MaxRetriesFn != nil {
		return c.MaxRetriesFn()
	}
	return 0
}
func (c *FuncClient) SetFailedState(s string) error {
	if c.SetFailedStateFn != nil {
		return c.SetFailedStateFn(s)
	}
	return nil
}
func (c *FuncClient) FailedState() string {
	if c.FailedStateFn != nil {
		return c.FailedStateFn()
	}
	return ""
}
func (c *FuncClient) SetMaxSwitchesPerIssuePerWindow(n int) error {
	if c.SetMaxSwitchesPerIssuePerWindowFn != nil {
		return c.SetMaxSwitchesPerIssuePerWindowFn(n)
	}
	return nil
}
func (c *FuncClient) MaxSwitchesPerIssuePerWindow() int {
	if c.MaxSwitchesPerIssuePerWindowFn != nil {
		return c.MaxSwitchesPerIssuePerWindowFn()
	}
	return 0
}
func (c *FuncClient) SetSwitchWindowHours(h int) error {
	if c.SetSwitchWindowHoursFn != nil {
		return c.SetSwitchWindowHoursFn(h)
	}
	return nil
}
func (c *FuncClient) SwitchWindowHours() int {
	if c.SwitchWindowHoursFn != nil {
		return c.SwitchWindowHoursFn()
	}
	return 0
}
func (c *FuncClient) SetIssueProfile(id, profile string) {
	if c.SetIssueProfileFn != nil {
		c.SetIssueProfileFn(id, profile)
	}
}
func (c *FuncClient) SetIssueBackend(id, backend string) {
	if c.SetIssueBackendFn != nil {
		c.SetIssueBackendFn(id, backend)
	}
}
func (c *FuncClient) ProfileDefs() map[string]ProfileDef {
	if c.ProfileDefsFn != nil {
		return c.ProfileDefsFn()
	}
	return nil
}
func (c *FuncClient) AvailableModels() map[string][]ModelOption {
	if c.AvailableModelsFn != nil {
		return c.AvailableModelsFn()
	}
	return nil
}
func (c *FuncClient) ReviewerConfig() (string, bool) {
	if c.ReviewerConfigFn != nil {
		return c.ReviewerConfigFn()
	}
	return "", false
}
func (c *FuncClient) SetReviewerConfig(profile string, autoReview bool) error {
	if c.SetReviewerConfigFn != nil {
		return c.SetReviewerConfigFn(profile, autoReview)
	}
	return nil
}
func (c *FuncClient) UpsertProfile(name string, def ProfileDef, originalName string) error {
	if c.UpsertProfileFn != nil {
		return c.UpsertProfileFn(name, def, originalName)
	}
	return errNotConfigured
}
func (c *FuncClient) DeleteProfile(name string) error {
	if c.DeleteProfileFn != nil {
		return c.DeleteProfileFn(name)
	}
	return errNotConfigured
}
func (c *FuncClient) SetAutomations(automations []AutomationDef) error {
	if c.SetAutomationsFn != nil {
		return c.SetAutomationsFn(automations)
	}
	return errNotConfigured
}
func (c *FuncClient) SetAutoClearWorkspace(enabled bool) error {
	if c.SetAutoClearWorkspaceFn != nil {
		return c.SetAutoClearWorkspaceFn(enabled)
	}
	return errNotConfigured
}
func (c *FuncClient) ClearAllWorkspaces() error {
	if c.ClearAllWorkspacesFn != nil {
		return c.ClearAllWorkspacesFn()
	}
	return errNotConfigured
}
func (c *FuncClient) FetchLogIdentifiers() []string {
	if c.FetchLogIdentifiersFn != nil {
		return c.FetchLogIdentifiersFn()
	}
	return nil
}
func (c *FuncClient) UpdateTrackerStates(active, terminal []string, completion string) error {
	if c.UpdateTrackerStatesFn != nil {
		return c.UpdateTrackerStatesFn(active, terminal, completion)
	}
	return errNotConfigured
}
func (c *FuncClient) AddSSHHost(host, description string) error {
	if c.AddSSHHostFn != nil {
		return c.AddSSHHostFn(host, description)
	}
	return errNotConfigured
}
func (c *FuncClient) RemoveSSHHost(host string) error {
	if c.RemoveSSHHostFn != nil {
		return c.RemoveSSHHostFn(host)
	}
	return errNotConfigured
}
func (c *FuncClient) SetDispatchStrategy(strategy string) error {
	if c.SetDispatchStrategyFn != nil {
		return c.SetDispatchStrategyFn(strategy)
	}
	return errNotConfigured
}
func (c *FuncClient) ProvideInput(identifier, message string) bool {
	if c.ProvideInputFn != nil {
		return c.ProvideInputFn(identifier, message)
	}
	return false
}
func (c *FuncClient) DismissInput(identifier string) bool {
	if c.DismissInputFn != nil {
		return c.DismissInputFn(identifier)
	}
	return false
}
func (c *FuncClient) SetInlineInput(enabled bool) error {
	if c.SetInlineInputFn != nil {
		return c.SetInlineInputFn(enabled)
	}
	return errNotConfigured
}
func (c *FuncClient) BumpCommentCount(identifier string) {
	if c.BumpCommentCountFn != nil {
		c.BumpCommentCountFn(identifier)
	}
}
func (c *FuncClient) TestAutomation(ctx context.Context, automationID, identifier string) error {
	if c.TestAutomationFn != nil {
		return c.TestAutomationFn(ctx, automationID, identifier)
	}
	return errNotConfigured
}

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
	// ProjectName is a human-readable label for the project this daemon is
	// serving. Populated from the tracker project slug when available, else
	// the directory basename of the WORKFLOW.md file. Rendered in the web
	// UI header so multi-daemon / multi-repo users can tell which instance
	// they are looking at.
	ProjectName string `json:"projectName,omitempty"`
	// ActiveProjectFilter is the current runtime project filter slugs.
	// nil/absent means "using WORKFLOW.md default"; empty array means "all issues".
	ActiveProjectFilter []string `json:"activeProjectFilter,omitempty"`
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
	// BacklogStates are always-fetched states shown as the leftmost board column.
	BacklogStates []string `json:"backlogStates,omitempty"`
	// PausedWithPR maps paused issue identifiers to a known open-PR URL.
	// Itervox does NOT auto-pause on an existing open PR — workers continue
	// on the PR branch instead (v0.2.0 audit D2). The daemon currently emits
	// no entries; the field is retained for wire-schema stability (Zod marks
	// it optional) and for snapshot producers that do record PR URLs (e.g.
	// tests driving comment_pr's GitHub-PR routing).
	PausedWithPR map[string]string `json:"pausedWithPR,omitempty"`
	// PollIntervalMs is the configured tracker poll interval in milliseconds.
	// The TUI uses this to derive a safe background refresh rate.
	PollIntervalMs int `json:"pollIntervalMs,omitempty"`
	// AutoClearWorkspace indicates whether workspace directories are
	// automatically deleted after a task succeeds.
	AutoClearWorkspace bool `json:"autoClearWorkspace,omitempty"`
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
	// InlineInput indicates whether agent input-required signals are posted as
	// tracker comments (true) or queued in the dashboard UI (false).
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
	// AutomationDropsSelfReentryTotal is the monotonic count of input_required
	// automation dispatches suppressed by the self-reentry guard (the previous
	// worker on the issue was itself automation-launched). Surfaced on the
	// dashboard's LiveOpsStrip so operators can distinguish "guarded loop" from
	// "automation never fired". omitempty: absent until the first drop.
	// gaps_11 G-11.
	AutomationDropsSelfReentryTotal uint64                   `json:"automationDropsSelfReentryTotal,omitempty"`
	DependencyAudit                 []DependencyAuditRow     `json:"dependencyAudit,omitempty"`
	DependencyGraphNodes            []DependencyGraphNodeRow `json:"dependencyGraphNodes,omitempty"`
	DependencyGraphEdges            []DependencyGraphEdgeRow `json:"dependencyGraphEdges,omitempty"`
	// DepsAnalyzerProfile is the configured agent.deps_analyzer_profile (Phase
	// 1.1). The dashboard gates the "Analyze dependencies" button on this being
	// non-empty + the named profile existing + enabled.
	DepsAnalyzerProfile string `json:"depsAnalyzerProfile,omitempty"`
	// DepsLastAnalyzedAt is the GeneratedAt timestamp from
	// `.itervox/dependencies.json`. Absent when the sidecar is missing or
	// outdated. Surfaced as a "Last analyzed N ago" label in the Deps toolbar.
	DepsLastAnalyzedAt *time.Time `json:"depsLastAnalyzedAt,omitempty"`
	// ConfigInvalid surfaces an in-flight WORKFLOW.md validation failure to
	// the dashboard / TUI banner. nil/absent means the daemon is reading a
	// valid config; non-nil means the most recent reload tick failed and the
	// daemon is running on the previously-valid config while exponentially
	// backing off retries (T-26).
	ConfigInvalid *ConfigInvalidStatus `json:"configInvalid,omitempty"`
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
	EdgesFound    int        `json:"edgesFound,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// DepsAnalyzer is the optional service backing the `/api/v1/deps/analyze`
// endpoints (Phase 2.3 of v0.2.0 todolist6). A nil DepsAnalyzer in Config
// makes both endpoints return 503.
type DepsAnalyzer interface {
	// EnqueueAnalysis kicks off (or returns the in-flight) analyzer job for the
	// given profile. Empty `profile` falls back to the configured
	// agent.deps_analyzer_profile.
	EnqueueAnalysis(profile string) (jobID string, queuedAt time.Time, err error)
	// Status returns the analyzer job with the given ID, or false when absent.
	Status(jobID string) (DepsAnalyzeJobRow, bool)
	// DefaultProfile returns the configured agent.deps_analyzer_profile, or
	// empty when the analyzer is disabled.
	DefaultProfile() string
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
	IneligibleReason string                 `json:"ineligibleReason,omitempty"`
	// AgentProfile is the name of the per-issue agent profile override, if any.
	AgentProfile string `json:"agentProfile,omitempty"`
	// AgentBackend is the per-issue backend override, if any ("claude" or "codex").
	AgentBackend string `json:"agentBackend,omitempty"`
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

// broadcaster fans out state-change notifications to multiple SSE clients.
type broadcaster struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{clients: make(map[chan struct{}]struct{})}
}

func (b *broadcaster) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broadcaster) unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

func (b *broadcaster) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// close wakes up every subscribed SSE handler so they can exit promptly on
// graceful shutdown. Each subscriber's channel is removed from the clients
// map so a duplicate notify() doesn't double-send. Safe to call multiple
// times. G-03 (gaps_280426_2).
func (b *broadcaster) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		// Send before delete so any already-blocked receiver wakes; the
		// `default` branch covers handlers that have already drained.
		select {
		case ch <- struct{}{}:
		default:
		}
		delete(b.clients, ch)
	}
}

// Shutdown wakes up SSE subscribers so they can exit on graceful daemon
// shutdown. Pair with the http.Server's own Shutdown call — chi cancels
// per-request contexts which is the primary exit signal; this helper is
// belt-and-suspenders ordering documentation: orchestrator stop should
// precede server stop, and server stop should call Shutdown to release
// any subscriber holding a stale snapshot pointer. G-03.
func (s *Server) Shutdown() {
	if s == nil || s.bc == nil {
		return
	}
	s.bc.close()
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
	// APIToken, when non-empty, enables bearer-token authentication on all
	// /api/ routes except /api/v1/health. Requests must include the header
	// "Authorization: Bearer <token>".
	APIToken string
	// ActionTokenStore validates short-lived per-run grants for agent action routes.
	ActionTokenStore *agentactions.Store
	// SkillsClient exposes the skills-inventory surface (T-87). Nil → noop.
	SkillsClient SkillsClient
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
	bc             *broadcaster
	apiToken       string
	actionTokens   *agentactions.Store
	skills         SkillsClient
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
		bc:             newBroadcaster(),
		apiToken:       cfg.APIToken,
		actionTokens:   cfg.ActionTokenStore,
		skills:         skillsClient,
		depsAnalyzer:   cfg.DepsAnalyzer,

		mergeStrategy:       cfg.MergeStrategy,
		mergeBlockLabels:    cfg.MergeBlockLabels,
		allowUncheckedMerge: cfg.AllowUncheckedMerge,
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
		r.Post("/agent-actions/{identifier}/comment", s.handleAgentComment)
		r.Post("/agent-actions/{identifier}/comment_pr", s.handleAgentCommentPR)
		r.Post("/agent-actions/{identifier}/merge_pr", s.handleAgentMergePR)
		r.Post("/agent-actions/{identifier}/create-issue", s.handleAgentCreateIssue)
		r.Post("/agent-actions/{identifier}/move-state", s.handleAgentMoveState)
		r.Post("/agent-actions/{identifier}/provide-input", s.handleAgentProvideInput)

		// If an API token is configured, all remaining routes require it.
		// Use r.Group to create a sub-router so middleware is applied only to
		// authenticated routes without violating chi's "middleware before routes" rule.
		r.Group(func(r chi.Router) {
			if s.apiToken != "" {
				r.Use(s.bearerAuthMiddleware)
			}

			r.Get("/state", s.handleState)
			r.Get("/events", s.handleEvents)
			r.Get("/issues", s.handleIssues)
			r.Get("/issues/{identifier}", s.handleIssueDetail)
			r.Get("/issues/{identifier}/logs", s.handleIssueLogs)
			r.Get("/issues/{identifier}/log-stream", s.handleIssueLogStream)
			r.Get("/issues/{identifier}/sublogs", s.handleSubLogs)
			r.Get("/issues/{identifier}/sublog-stream", s.handleSubLogStream)
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
			r.Post("/settings/inline-input", s.handleSetInlineInput)
			r.Get("/logs", s.handleLogs)
			r.Post("/refresh", s.handleRefresh)
			r.Get("/projects", s.handleListProjects)
			r.Get("/projects/filter", s.handleGetProjectFilter)
			r.Put("/projects/filter", s.handleSetProjectFilter)
			r.Post("/settings/workers", s.handleSetWorkers)
			r.Delete("/workspaces", s.handleClearAllWorkspaces)
			r.Post("/settings/workspace/auto-clear", s.handleSetAutoClearWorkspace)
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

			// Dependency analysis (Phase 2.3 of v0.2.0 todolist6).
			r.Post("/deps/analyze", s.handleDepsAnalyzeEnqueue)
			r.Get("/deps/analyze/{jobId}", s.handleDepsAnalyzeStatus)

			// Skills inventory + analytics (T-87, T-95/T-96, T-102).
			r.Get("/skills/inventory", s.handleSkillsInventory)
			r.Post("/skills/scan", s.handleSkillsScan)
			r.Get("/skills/issues", s.handleSkillsIssues)
			r.Post("/skills/fix", s.handleSkillsFix)
			r.Get("/skills/analytics", s.handleSkillsAnalytics)
			r.Get("/skills/analytics/recommendations", s.handleSkillsAnalyticsRecommendations)

			r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			})
		})
	})

	// React SPA: serves all non-API paths from the embedded web/dist.
	// Falls back to index.html so React Router client-side routing works.
	s.router.Handle("/*", spaHandler())
}

// handleHealth returns a lightweight 200 OK for load balancer probes.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// bearerAuthMiddleware rejects requests that do not carry a valid
// "Authorization: Bearer <token>" header matching s.apiToken.
func (s *Server) bearerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) || strings.TrimPrefix(auth, prefix) != s.apiToken {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
