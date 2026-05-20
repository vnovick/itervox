package orchestrator

import (
	"context"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// EventType identifies the kind of OrchestratorEvent.
type EventType string

// OrchestratorEvent type constants sent over the events channel.
const (
	EventWorkerExited    EventType = "WorkerExited"
	EventWorkerUpdate    EventType = "WorkerUpdate"
	EventForceReanalyze  EventType = "ForceReanalyze"
	EventResumeIssue     EventType = "ResumeIssue"
	EventTerminatePaused EventType = "TerminatePaused"
	// EventDiscardComplete is sent by the UpdateIssueState goroutine (spawned by
	// EventTerminatePaused) once the label transition is confirmed. Until this
	// event is processed, the issue stays in DiscardingIdentifiers which blocks
	// dispatch — preventing the TUI's background TriggerPoll from re-picking the
	// issue before the "In Progress" label has been removed.
	EventDiscardComplete EventType = "DiscardComplete"
	// EventTerminateRunning is sent by TerminateIssue when the target issue has
	// a live worker. Processing it inside the event loop linearises the terminate
	// with EventWorkerExited, closing the TOCTOU window where a natural worker
	// exit races with a user-initiated cancel (GO-R5-3).
	EventTerminateRunning EventType = "TerminateRunning"
	// EventCancelRetry is sent by CancelIssue when the target issue is in the
	// retry queue (no live worker). The event loop removes the retry entry,
	// releases the claim, and moves the issue to PausedIdentifiers.
	EventCancelRetry EventType = "CancelRetry"
	// EventProvideInput is sent by ProvideInput when the user provides a message
	// for an input-required issue. The event loop removes the entry from
	// InputRequiredIssues and dispatches a resumed worker.
	EventProvideInput EventType = "ProvideInput"
	// EventDismissInput is sent by DismissInput when the user dismisses an
	// input-required issue without providing input. Moves to PausedIdentifiers.
	EventDismissInput EventType = "DismissInput"
	// EventDispatchReviewer is sent by DispatchReviewer (manual trigger) to
	// dispatch a reviewer worker through the event loop so state mutations
	// happen in the single event-loop goroutine.
	EventDispatchReviewer EventType = "DispatchReviewer"
	// EventInputRequiredCommentRecorded is sent after the tracker successfully
	// creates the input-required question comment so the event loop can persist
	// the exact tracker comment ID and author identity locally.
	EventInputRequiredCommentRecorded EventType = "InputRequiredCommentRecorded"
	// EventDispatchAutomation is sent by cron automations to dispatch a helper
	// worker through the event loop using a selected profile plus extra
	// automation instructions.
	EventDispatchAutomation EventType = "DispatchAutomation"
)

// OrchestratorEvent is sent over the event channel to the orchestrator loop.
type OrchestratorEvent struct { //nolint:revive
	Type               EventType
	IssueID            string // tracker UUID (e.g. "abc123"); used by WorkerExited/Update events
	Identifier         string // human identifier (e.g. "ENG-1"); used by ForceReanalyze events
	RunEntry           *RunEntry
	RetryEntry         *RetryEntry
	Error              error
	Message            string              // user-provided text for EventProvideInput
	ReviewerProfile    string              // profile name for EventDispatchReviewer
	InputRequiredEntry *InputRequiredEntry // used by TerminalInputRequired
	Comment            *domain.Comment     // used by EventInputRequiredCommentRecorded
	Issue              *domain.Issue       // used by EventDispatchAutomation
	Automation         *AutomationDispatch // used by EventDispatchAutomation
}

// TerminalReason classifies why a worker stopped.
type TerminalReason string

// Terminal reason constants for RunEntry.TerminalReason.
const (
	TerminalSucceeded                TerminalReason = "succeeded"
	TerminalFailed                   TerminalReason = "failed"
	TerminalCanceledByReconciliation TerminalReason = "canceled_by_reconciliation"
	// TerminalStalled is used when a worker is killed by stall detection.
	// Claim and retry management are handled inline by ReconcileStalls; the
	// event loop only records history when it sees this reason.
	TerminalStalled TerminalReason = "stalled"
	// TerminalInputRequired is used when the agent signals it needs human input
	// (permission prompt, missing API key, etc.). The issue is moved to the
	// InputRequiredIssues queue instead of being retried or marked as succeeded.
	TerminalInputRequired TerminalReason = "input_required"
)

// ResumeContext, when non-nil, configures a worker to continue an existing
// agent session via --resume instead of starting a fresh one. It unifies two
// resume flows that used to be handled separately:
//
//   - Pause/resume: SessionID set, UserMessage empty. The worker re-renders
//     the WORKFLOW.md prompt normally; the agent picks up where it left off.
//   - Input-required resume: SessionID set, UserMessage set to the user's
//     reply. The worker substitutes UserMessage for the rendered prompt, caps
//     the run at a single turn, and skips PR detection (the worktree and
//     branch already exist from the original dispatch).
//
// When nil, the worker performs a normal fresh dispatch.
type ResumeContext struct {
	SessionID    string // agent session ID for --resume <id>
	UserMessage  string // latest human reply
	InputContext string // the agent question/request that prompted the reply
}

// InputRequiredEntry holds context for an issue whose agent is blocked waiting
// for human input. Stored in State.InputRequiredIssues until the user provides
// input (via ProvideInput) or dismisses it (via DismissInput).
type InputRequiredEntry struct {
	IssueID            string
	Identifier         string
	SessionID          string // for --resume
	Context            string // what the agent was waiting for (from FailureText/ResultText)
	BranchName         string // actual branch/worktree checkout to reuse on resume
	Backend            string // which runner was used
	Command            string // agent command (for resume on same runner)
	WorkerHost         string // SSH host (for resume on same host)
	ProfileName        string // active profile
	QuestionCommentID  string // exact tracker comment ID for the agent question
	QuestionAuthorID   string // exact tracker author ID for the agent question
	QuestionAuthorName string // display author for the agent question
	QueuedAt           time.Time
}

// PendingInputResumeEntry holds a user reply that has been accepted but not
// yet durably consumed by a resumed worker turn. This state is persisted so a
// daemon restart between "reply accepted" and "resumed worker produced output"
// can continue the same agent session and host/backend selection.
type PendingInputResumeEntry struct {
	IssueID            string
	Identifier         string
	SessionID          string
	Context            string
	UserMessage        string
	BranchName         string
	Backend            string
	Command            string
	WorkerHost         string
	ProfileName        string
	QuestionCommentID  string
	QuestionAuthorID   string
	QuestionAuthorName string
	QueuedAt           time.Time
}

// RunEntry tracks a live agent worker goroutine.
type RunEntry struct {
	Issue domain.Issue
	// SessionID is the per-run log session ID generated by the orchestrator.
	// Used purely for log correlation in the Timeline. NOT the agent's session
	// ID — see AgentSessionID for that.
	SessionID string
	// AgentSessionID is the real session ID assigned by the agent backend
	// (Claude Code or Codex) that can be used with --resume / `exec resume`
	// to continue the same conversation. Empty until the first turn completes
	// and the agent reports its session ID.
	AgentSessionID string
	WorkerHost     string // SSH host used for this worker, empty = local
	Backend        string // e.g. "claude", "codex", or "" when unknown
	ProfileName    string // resolved agent profile name, empty = default
	Kind           string // "worker" (default) | "reviewer" | "automation"
	// AutomationID is the rule ID that dispatched this run; empty for
	// manually dispatched runs. Set once at dispatch and never mutated.
	AutomationID string
	// TriggerType is the automation trigger ("cron", "input_required",
	// "run_failed", "test"). Empty for manual runs.
	TriggerType string
	// CommentCount counts comment-action invocations recorded for this
	// run; surfaced on the issue card (T-6).
	CommentCount int
	// PendingInputResume is true while this run is consuming a locally
	// persisted human reply from PendingInputResumes. The event loop uses it to
	// clear the pending-resume record only after the resumed run has actually
	// started producing output or has exited.
	PendingInputResume bool
	BranchName         string // actual resolved branch used for the worktree (may differ from issue.BranchName when a PR branch was used)
	PRURL              string // URL of the PR created or continued during this run (empty if none)
	TerminalReason     TerminalReason
	LastEventAt        *time.Time // when last EventWorkerUpdate was received
	LastMessage        string
	InputTokens        int
	OutputTokens       int
	TotalTokens        int
	TurnCount          int
	RetryAttempt       *int
	StartedAt          time.Time
	WorkerCancel       context.CancelFunc
}

// CompletedRun is a snapshot of a finished worker session, kept in the history ring buffer.
type CompletedRun struct {
	Identifier   string
	Title        string
	StartedAt    time.Time
	FinishedAt   time.Time
	ElapsedMs    int64
	TurnCount    int
	TotalTokens  int
	InputTokens  int
	OutputTokens int
	Status       string // "succeeded" | "failed" | "cancelled" | "stalled" | "input_required"
	WorkerHost   string
	Backend      string
	ProfileName  string `json:"profileName,omitempty"`
	SessionID    string
	// ProjectKey scopes this run to a specific project so that a shared
	// history file does not leak runs across projects. Format: "<kind>:<slug>".
	// Empty string means "unscoped" (legacy entries written before this field
	// was added); these are retained so existing history is not silently dropped.
	Kind         string // "worker" (default) | "reviewer" | "automation"
	ProjectKey   string
	AppSessionID string // daemon-invocation grouping key; empty for legacy entries
	// AutomationID / TriggerType propagate the automation context onto
	// completed runs so dashboards can attribute history to a specific
	// rule. Empty for manual runs.
	AutomationID string `json:"automationID,omitempty"`
	TriggerType  string `json:"triggerType,omitempty"`
	// CommentCount captures how many comment actions the run posted.
	CommentCount int `json:"commentCount,omitempty"`
}

// RetryEntry represents a scheduled retry for an issue.
type RetryEntry struct {
	IssueID    string
	Identifier string
	Attempt    int
	DueAt      time.Time
	Error      *string
}

// PausedSessionInfo captures the runtime context of a paused worker so that
// on resume the same agent session can be continued via --resume (Claude) or
// `exec resume` (Codex). Captured from the live RunEntry at pause time.
//
// All fields are optional. The most important is SessionID — if it is empty,
// the resume path falls back to a fresh dispatch.
type PausedSessionInfo struct {
	IssueID     string
	SessionID   string
	WorkerHost  string
	Backend     string
	Command     string
	ProfileName string
}

// State is the single in-memory authority for all orchestrator runtime data.
type State struct {
	PollIntervalMs      int
	MaxConcurrentAgents int
	// ActiveStates and TerminalStates are snapshotted from cfg at the start of
	// each tick under cfgMu so the event loop can compare issue states lock-free
	// throughout a tick. These are the cfg fields governed by cfgMu.
	ActiveStates   []string
	TerminalStates []string
	Running        map[string]*RunEntry
	Claimed        map[string]struct{}
	RetryAttempts  map[string]*RetryEntry
	// PausedIdentifiers tracks issues paused by user kill.
	// Key: identifier (e.g. "TIPRD-25"), Value: issue UUID (empty when loaded
	// from an old disk snapshot that predates UUID persistence).
	// Paused issues are not re-dispatched until explicitly resumed.
	PausedIdentifiers map[string]string
	// PausedSessions stores per-issue session-resume info captured at pause time.
	// When a user pauses a running issue, the orchestrator captures the live
	// RunEntry's SessionID, WorkerHost, Backend, Command, and ProfileName so that
	// on resume the same agent session can be continued via --resume / `exec resume`
	// instead of starting from scratch.
	//
	// Key: identifier (mirrors PausedIdentifiers). Absence means no session info
	// was captured (e.g. issue was paused while in retry queue, or loaded from a
	// disk snapshot from an older daemon version) — resume falls back to fresh
	// dispatch via the normal runWorker path.
	//
	// Cleared when the issue is resumed, terminated, or transitions out of paused.
	// Not persisted to disk: session IDs are ephemeral and have no meaning across
	// daemon restarts (the agent CLI's session storage is local to the machine).
	PausedSessions map[string]*PausedSessionInfo
	// IssueProfiles maps issue identifier to an agent profile name override.
	// When set for an issue, the named profile's Command is used instead of
	// the default cfg.Agent.Command when dispatching that issue.
	IssueProfiles map[string]string
	// IssueBackends maps issue identifier to a backend override ("claude" or "codex").
	// When set, overrides the profile and config backend for dispatch.
	IssueBackends map[string]string
	// AutoSwitchedIdentifiers tracks issues whose IssueProfiles / IssueBackends
	// override was set by a `rate_limited` automation auto-switch (gap E)
	// rather than by an explicit operator action via SetIssueProfile /
	// SetIssueBackend. On a successful worker exit the override is cleared so
	// subsequent runs revert to the natural profile — but operator-set
	// overrides survive. Gap §1.3.
	AutoSwitchedIdentifiers map[string]struct{}
	// AutoSwitchedAt records the wall-clock time of each auto-switch fire.
	// Used by the time-based revert path: when `cfg.Agent.SwitchRevertHours`
	// > 0 the orchestrator's onTick reverts overrides whose age has
	// exceeded the TTL, even if no successful exit cleared them. Gap §6.2.
	// Keys mirror AutoSwitchedIdentifiers; the two maps stay in sync.
	AutoSwitchedAt map[string]time.Time
	// PausedOpenPRs tracks issues that were auto-paused because an open PR was detected.
	// Key: issue identifier, Value: open PR URL.
	PausedOpenPRs map[string]string
	// ForceReanalyze holds identifiers queued for forced PR re-analysis.
	// These bypass the "existing open PR = skip" guard on next dispatch.
	ForceReanalyze map[string]struct{}
	// PrevActiveIdentifiers is the set of issue identifiers that were fetched
	// as active on the previous tick. Used by the auto-resume guard to
	// distinguish "issue came back to active after being absent" (safe to
	// auto-resume) from "issue was already active when user paused it"
	// (must not auto-resume — wait until it leaves active and returns).
	PrevActiveIdentifiers map[string]struct{}
	// DiscardingIdentifiers holds identifiers of issues whose EventTerminatePaused
	// has been processed but whose UpdateIssueState goroutine has not yet
	// completed. Issues in this set are ineligible for dispatch, preventing the
	// TUI's background TriggerPoll from re-picking them before the label update
	// (e.g. removing "In Progress") finishes. Cleared by EventDiscardComplete.
	DiscardingIdentifiers map[string]struct{}
	// InputRequiredIssues tracks issues whose agent signalled that it needs
	// human input to continue. Key: identifier. These issues are not dispatched
	// until the user provides input or dismisses.
	InputRequiredIssues map[string]*InputRequiredEntry
	// PendingInputResumes tracks replies that were accepted locally or detected
	// in the tracker, but have not yet been durably consumed by a resumed
	// worker. Key: identifier.
	PendingInputResumes map[string]*PendingInputResumeEntry
}

// NewState initialises a State from a config snapshot.
func NewState(cfg *config.Config) State {
	return State{
		PollIntervalMs:          cfg.Polling.IntervalMs,
		MaxConcurrentAgents:     cfg.Agent.MaxConcurrentAgents,
		ActiveStates:            append([]string{}, cfg.Tracker.ActiveStates...),
		TerminalStates:          append([]string{}, cfg.Tracker.TerminalStates...),
		Running:                 make(map[string]*RunEntry),
		Claimed:                 make(map[string]struct{}),
		RetryAttempts:           make(map[string]*RetryEntry),
		PausedIdentifiers:       make(map[string]string),
		PausedSessions:          make(map[string]*PausedSessionInfo),
		IssueProfiles:           make(map[string]string),
		IssueBackends:           make(map[string]string),
		AutoSwitchedIdentifiers: make(map[string]struct{}),
		AutoSwitchedAt:          make(map[string]time.Time),
		PausedOpenPRs:           make(map[string]string),
		ForceReanalyze:          make(map[string]struct{}),
		PrevActiveIdentifiers:   make(map[string]struct{}),
		DiscardingIdentifiers:   make(map[string]struct{}),
		InputRequiredIssues:     make(map[string]*InputRequiredEntry),
		PendingInputResumes:     make(map[string]*PendingInputResumeEntry),
	}
}
