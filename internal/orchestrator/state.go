package orchestrator

import (
	"context"
	"time"

	"github.com/vnovick/itervox/internal/agent"
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
	// EventDispatchAutomation is sent by cron automations to dispatch a helper
	// worker through the event loop using a selected profile plus extra
	// automation instructions.
	EventDispatchAutomation EventType = "DispatchAutomation"
	// EventIssueStatusChanged records a tracker state transition observed from
	// a goroutine or HTTP handler. The event loop owns the status ledger.
	EventIssueStatusChanged EventType = "IssueStatusChanged"
	// EventDependencyAuditRefreshed carries the result of one off-loop
	// dependency-refresh batch. The worker performs tracker I/O only; every
	// State mutation happens in the event loop when this event is handled.
	EventDependencyAuditRefreshed EventType = "DependencyAuditRefreshed"
	// EventSetDepsOverride is sent by SetDepsOverride when an operator
	// dismisses (or restores) an LLM-inferred dependency edge's gating effect
	// on a target identifier. The event loop mutates state.DepsOverrides and
	// recomputes that identifier's InferredDeps entries in place so the
	// dispatch guard sees the effect on the very next snapshot, without
	// waiting for the next tick's ReconcileInferredDeps pass.
	// unified-dependency-graph Task 6.
	EventSetDepsOverride EventType = "SetDepsOverride"
	// EventFailureRecorded carries one RecentFailures entry from an off-loop
	// producer (outbox flusher, ledger writer, recovered panic, web client
	// error report). Sent non-blocking via RecordFailure; a dropped event is
	// counted, never re-sent. The loop appends it (CORE-046).
	EventFailureRecorded EventType = "FailureRecorded"
	// EventDrain is sent by RequestDrain (CORE-057) when the daemon begins a
	// graceful drain for shutdown or a WORKFLOW.md reload. The loop sets
	// State.Draining; from then on nothing new is admitted while in-flight
	// workers run to completion.
	EventDrain EventType = "Drain"
	// EventAckFailures records an operator acknowledgement of an issue's
	// worker failures up to a time (CORE-175).
	EventAckFailures EventType = "AckFailures"
)

// OrchestratorEvent is sent over the event channel to the orchestrator loop.
type OrchestratorEvent struct { //nolint:revive
	Type               EventType
	IssueID            string // tracker UUID (e.g. "abc123"); used by WorkerExited/Update events
	Identifier         string // human identifier (e.g. "ENG-1"); used by ForceReanalyze events
	RunEntry           *RunEntry
	RetryEntry         *RetryEntry
	Error              error
	Message            string                   // user-provided text for EventProvideInput
	ReviewerProfile    string                   // profile name for EventDispatchReviewer
	InputRequiredEntry *InputRequiredEntry      // used by TerminalInputRequired
	Issue              *domain.Issue            // used by EventDispatchAutomation
	Automation         *AutomationDispatch      // used by EventDispatchAutomation
	StatusChange       *IssueStatusChange       // used by EventIssueStatusChanged
	DependencyRefresh  *DependencyRefreshResult // used by EventDependencyAuditRefreshed
	Enabled            bool                     // true = set override, false = clear; used by EventSetDepsOverride
	Failure            *FailureRecord           // used by EventFailureRecorded
	// Limit is the vendor limit signal of a failed turn (CORE-050/051):
	// set on TerminalRateLimited, and on TerminalFailed when the turn saw an
	// advisory throttle whose vendor delay the retry should honour.
	Limit *agent.LimitSignal
	// AckUpTo is the acknowledged-up-to time of an EventAckFailures.
	AckUpTo time.Time
	// DiscardGen identifies the discard operation an EventDiscardComplete
	// finishes (CORE-109); it must match the marker's Gen to clear it.
	DiscardGen uint64
}

// DiscardMarker is one State.DiscardingIdentifiers entry (CORE-109): the
// generation of the discard operation that set it and when it was set.
type DiscardMarker struct {
	Gen uint64
	At  time.Time
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
	// TerminalRateLimited is used when a failed turn carries a quota limit
	// signal (agent.LimitSignal.Terminal, CORE-050): the backend cannot serve
	// the issue again before the limit resets. The event loop evaluates the
	// rate_limited fallback immediately, without consuming a retry, and falls
	// through to the normal failed path when no fallback is eligible
	// (CORE-051).
	TerminalRateLimited TerminalReason = "rate_limited"
	// TerminalStackUnavailable is used when a dependent was admitted because
	// its only blocker is in review (dependencies.stacked_prs) but its
	// worktree could not be stacked on the blocker's branch: running it on
	// base_branch would build on code the blocker has not landed yet. No
	// agent ran; the event loop records the miss in State.StackUnavailable
	// so the issue waits for the blocker like any other.
	TerminalStackUnavailable TerminalReason = "stack_unavailable"
)

// Pause reasons recorded in State.PauseReasons (issue #42-F).
//
// The distinction that motivated these: PauseReasonTransitionFailed is a
// TRANSIENT infrastructure failure — the agent's work succeeded and only the
// tracker write did not — whereas PauseReasonUserCancelled is a deliberate
// human decision. Both used to be recorded identically, so an operator could
// not tell which pauses were safe to resume in bulk, and a transition failure
// sat waiting on a human who had no way to know it was waiting.
const (
	// PauseReasonUserCancelled — a human cancelled the run or its retry.
	PauseReasonUserCancelled = "user_cancelled"
	// PauseReasonUserDismissedInput — a human dismissed an input-required
	// prompt without answering it.
	PauseReasonUserDismissedInput = "user_dismissed_input"
	// PauseReasonRetriesExhausted — retries ran out and no failed_state is
	// configured to move the issue to.
	PauseReasonRetriesExhausted = "retries_exhausted"
	// PauseReasonTransitionFailed — the agent finished successfully but the
	// completion-state tracker write failed after its retries. Distinct from
	// user_cancelled precisely because it is recoverable: the work is done.
	PauseReasonTransitionFailed = "transition_failed"
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
	IssueID     string
	Identifier  string
	SessionID   string // for --resume
	Context     string // what the agent was waiting for (from FailureText/ResultText)
	BranchName  string // actual branch/worktree checkout to reuse on resume
	Backend     string // which runner was used
	Command     string // agent command (for resume on same runner)
	WorkerHost  string // SSH host (for resume on same host)
	ProfileName string // active profile
	// QuestionCommentKey is the idempotency key the agent's question was (or
	// will be) posted under. It identifies the question BEFORE delivery: on
	// Linear it is the comment's id, on GitHub a hidden body marker. When set
	// it is authoritative — reply detection never falls back to the legacy
	// id/prefix match, because that could pick up a previous round's question.
	QuestionCommentKey string
	QuestionCommentID  string // exact tracker comment ID for the agent question
	QuestionAuthorID   string // exact tracker author ID for the agent question
	QuestionAuthorName string // display author for the agent question
	QueuedAt           time.Time
	// LastReplyCheckAt is when checkTrackerReplies last spent a
	// FetchIssueDetail on this entry. It orders the per-tick budget
	// least-recently-checked first, so every entry is still reached in
	// bounded time no matter how large the backlog grows. Zero means never
	// checked, which sorts first.
	LastReplyCheckAt time.Time
	// QuestionStuckWarned records that the Warn for a degraded, undelivered
	// question has been logged, so it fires once per entry rather than on
	// every tick (CORE-123). Event-loop owned; session-scoped.
	QuestionStuckWarned bool
}

// PendingInputResumeEntry holds a user reply that has been accepted but not
// yet durably consumed by a resumed worker turn. This state is persisted so a
// daemon restart between "reply accepted" and "resumed worker produced output"
// can continue the same agent session and host/backend selection.
type PendingInputResumeEntry struct {
	IssueID     string
	Identifier  string
	SessionID   string
	Context     string
	UserMessage string
	BranchName  string
	Backend     string
	Command     string
	WorkerHost  string
	ProfileName string
	// QuestionCommentKey is the idempotency key the agent's question was (or
	// will be) posted under. It identifies the question BEFORE delivery: on
	// Linear it is the comment's id, on GitHub a hidden body marker. When set
	// it is authoritative — reply detection never falls back to the legacy
	// id/prefix match, because that could pick up a previous round's question.
	QuestionCommentKey string
	QuestionCommentID  string
	QuestionAuthorID   string
	QuestionAuthorName string
	QueuedAt           time.Time
	// LastResumeAttemptAt is when processPendingInputResumes last spent a
	// FetchIssueDetail on this entry. It orders the per-tick fetch budget
	// least-recently-attempted first: without it the loop re-spent the whole
	// budget on the same lexically-first entries every tick, so one
	// permanently unfetchable issue starved every entry behind it forever.
	// Zero means never attempted, which sorts first.
	LastResumeAttemptAt time.Time
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
	// CostUSD is the latest session-cumulative estimated cost reported by the
	// run's result events (Claude total_cost_usd, CORE-091); nil when unknown.
	CostUSD *float64
	// totalsInput/totalsOutput/totalsCounted record what this run has
	// already contributed to State.Totals (CORE-091). Unexported: event-loop
	// bookkeeping, not part of the snapshot.
	totalsInput   int
	totalsOutput  int
	totalsCounted bool
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
	// Draining is set by the event loop on EventDrain (CORE-057) and never
	// cleared: a drain ends with the generation. While true AvailableSlots
	// is 0 and dispatch()/dispatchReviewerForIssue refuse.
	Draining            bool
	PollIntervalMs      int
	MaxConcurrentAgents int
	// ActiveStates and TerminalStates are snapshotted from cfg at the start of
	// each tick under cfgMu so the event loop can compare issue states lock-free
	// throughout a tick. These are the cfg fields governed by cfgMu.
	ActiveStates   []string
	TerminalStates []string
	// PauseDispatchWhenAnyInState snapshots Agent.PauseDispatchWhenAnyInState
	// at the tick boundary so dispatch guards can read it lock-free. Lowercase,
	// case-folded copies. Empty disables the guard.
	PauseDispatchWhenAnyInState []string
	Running                     map[string]*RunEntry
	// ExitPending holds the IDs of Running entries whose worker has already
	// sent its exit, which the loop has not handled yet. Rebuilt each tick
	// before reconciliation, which leaves these runs to their own exit.
	ExitPending   map[string]bool
	Claimed       map[string]struct{}
	RetryAttempts map[string]*RetryEntry
	// PausedIdentifiers tracks issues paused by user kill.
	// Key: identifier (e.g. "TIPRD-25"), Value: issue UUID (empty when loaded
	// from an old disk snapshot that predates UUID persistence).
	// Paused issues are not re-dispatched until explicitly resumed.
	PausedIdentifiers map[string]string
	// PauseReasons records WHY each paused identifier is paused, keyed the
	// same way as PausedIdentifiers. Issue #42-F: the pause map alone cannot
	// distinguish a deliberate user cancel from a completion-state transition
	// that failed, so every automatic pause looked like a human decision and
	// needed a human to undo. A missing entry means "recorded before reasons
	// existed", which callers must treat as unknown rather than as any
	// specific reason.
	PauseReasons map[string]string
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
	// SwitchHistory records, per tracker issue ID, the time of every
	// rate_limited switch that fired, for the per-issue cap
	// (cfg.Agent.MaxSwitchesPerIssuePerWindow over SwitchWindowHours).
	// RateLimitCooldowns maps "<issueID>|<failed profile>" to the time until
	// which that pair is muted from re-firing a rate_limited rule.
	// RateLimitCapCommentUntil maps issue ID to the next time a cap-exhausted
	// tracker comment may be posted. All three were mutex-guarded
	// Orchestrator fields lost on restart; they are event-loop State now and
	// persisted with the auto-switched overrides (CORE-052).
	SwitchHistory            map[string][]time.Time
	RateLimitCooldowns       map[string]time.Time
	RateLimitCapCommentUntil map[string]time.Time
	// AutoSwitchInfo is the provenance of each automatic override in
	// AutoSwitchedIdentifiers: who switched (a rate_limited automation or
	// agent.backend_fallback), from and to which backend/profile, and why.
	// Keys mirror AutoSwitchedIdentifiers; persisted with them (CORE-055).
	AutoSwitchInfo map[string]AutoSwitchRecord
	// BackendHealth is the per-(backend, worker host) circuit breaker
	// (CORE-053), keyed by BackendHealthKey. Absent means healthy. Written
	// only by the event loop; persisted to backend_health.json.
	BackendHealth map[string]BackendHealthEntry
	// BackendLimitedHolds records the issues the dispatch gate refused
	// because every target backend was limited (the backend_limited
	// ineligible reason), with the breaker key and the earliest time a
	// target may admit it. Derived, event-loop owned, not persisted.
	BackendLimitedHolds map[string]BackendHold
	// ForceReanalyze holds identifiers queued for forced PR re-analysis.
	// These bypass the "existing open PR = skip" guard on next dispatch.
	ForceReanalyze map[string]struct{}
	// PrevActiveIdentifiers is the set of issue identifiers that were fetched
	// as active on the previous tick. Used by the auto-resume guard to
	// distinguish "issue came back to active after being absent" (safe to
	// auto-resume) from "issue was already active when user paused it"
	// (must not auto-resume — wait until it leaves active and returns).
	PrevActiveIdentifiers map[string]struct{}
	// PrevIssueStates stores the last tracker state observed for each issue.
	PrevIssueStates map[string]string
	// IssueStatusHistory stores a bounded status-change ledger per issue.
	IssueStatusHistory map[string][]IssueStatusChange
	// DiscardingIdentifiers holds identifiers of issues whose EventTerminatePaused
	// has been processed but whose UpdateIssueState goroutine has not yet
	// completed. Issues in this set are ineligible for dispatch, preventing the
	// TUI's background TriggerPoll from re-picking them before the label update
	// (e.g. removing "In Progress") finishes. Cleared by the
	// EventDiscardComplete whose DiscardGen matches the marker's Gen, or —
	// when that completion was lost — expired by the janitor after
	// discardMarkerTTL (CORE-109).
	DiscardingIdentifiers map[string]DiscardMarker
	// DiscardGeneration is the last generation handed to a discard marker.
	// Event-loop state: incremented on every insert so a late completion of
	// an older discard cannot clear a newer marker for the same identifier.
	DiscardGeneration uint64
	// FailureAcks is identifier → acknowledged-up-to time for the
	// operator's "acknowledge" on failed/stalled attention items
	// (CORE-175). Event-loop state; not persisted, like the RecentFailures
	// ring it refers to, and pruned when the issue has no worker failure
	// left in that ring.
	FailureAcks map[string]time.Time
	// Totals is the daemon-session token and estimated-cost accounting
	// (CORE-091): event-loop state, not persisted.
	Totals RunTotals
	// InputRequiredIssues tracks issues whose agent signalled that it needs
	// human input to continue. Key: identifier. These issues are not dispatched
	// until the user provides input or dismisses.
	InputRequiredIssues map[string]*InputRequiredEntry
	// PendingInputResumes tracks replies that were accepted locally or detected
	// in the tracker, but have not yet been durably consumed by a resumed
	// worker. Key: identifier.
	PendingInputResumes map[string]*PendingInputResumeEntry
	// AutomationQueue tracks automation triggers that could not start immediately.
	// Key: stable queue key from automationQueueKey.
	AutomationQueue map[string]*AutomationQueueEntry
	// AutomationQueueOrder preserves FIFO order for AutomationQueue keys.
	AutomationQueueOrder []string
	// AutomationQueueBackpressure tracks queue-cap saturation and rejected triggers.
	AutomationQueueBackpressure AutomationQueueBackpressure
	// ReviewVerdicts holds the per-issue reviewer verdicts collected so far in
	// a multi-reviewer chain (#58). Key: issue identifier. Event-loop owned;
	// session-scoped and not persisted.
	ReviewVerdicts map[string][]ReviewVerdict
	// ReviewChainIndex is the position in ReviewerProfileChain of the NEXT
	// reviewer to dispatch for an issue. Absent means no chain is in flight.
	ReviewChainIndex map[string]int
	// ReviewOutcomes is the quorum result once every reviewer in the chain
	// has reported. Key: issue identifier.
	ReviewOutcomes map[string]ReviewOutcome
	// PersistWriteErrors counts failed ledger writes (CORE-038). Not tracked
	// by the event loop: Snapshot() overlays the orchestrator's atomic
	// counter, so it is only meaningful on a Snapshot() result.
	PersistWriteErrors int64
	// DispatchPressure records whether each tick was slot-bound or
	// dependency-bound, so the operator can tell whether raising
	// agent.max_concurrent_agents would actually buy throughput. Session-
	// scoped and not persisted; see dispatch_pressure.go.
	DispatchPressure DispatchPressure
	// LastTrackerError is the most recent tracker failure (CORE-044): a
	// failed poll (op "poll", set in onTick) or a failed failed-state move
	// (op "update_state", reported back on EventDiscardComplete). Written
	// only by the event loop; zero At means none. A successful poll clears a
	// poll error; a write error ages out after trackerWriteErrorTTL.
	LastTrackerError TrackerErrorInfo
	// ConsecutivePollFailures counts consecutive non-rate-limited poll
	// failures; reset by a successful poll. Drives the Warn→Error escalation
	// at PollFailureEscalationThreshold and the /ready probe (CORE-043).
	ConsecutivePollFailures int
	// RecentFailures is the bounded (RecentFailuresCap) ring of operator-
	// relevant failures, oldest first by RecordedAt (CORE-046). Appended only
	// by the event loop — in-loop producers call appendRecentFailure, off-loop
	// producers send EventFailureRecorded via RecordFailure. Not persisted;
	// carried across WORKFLOW.md reloads by cmd/itervox (SeedRecentFailures).
	RecentFailures []FailureRecord
	// DependencyAudit tracks normalized blocker state by issue identifier.
	DependencyAudit map[string]*DependencyAuditEntry
	// DependencyTransitionSeq increments when dependency audit emits a transition.
	DependencyTransitionSeq int64
	// LastBlockersResolvedAuditSeq snapshots DependencyTransitionSeq as
	// observed at the launch of the most recent blockers_resolved refresh
	// batch (see DependencyRefreshResult.SeqAtLaunch). When
	// DependencyTransitionSeq has not advanced since then there is nothing
	// for the next batch to do, so pendingBlockersResolvedStates skips the
	// FetchIssuesByStates call entirely. v0.2.0 audit P1-2.
	LastBlockersResolvedAuditSeq int64
	// DepsRefreshInFlight is the single-flight latch for the off-loop
	// dependency-refresh worker. At most one batch is tracked as live: after
	// a watchdog fire (reclaimStuckDependencyRefresh) the abandoned batch's
	// goroutine is still running and may still be calling the tracker — the
	// watchdog only stops the event loop from TRACKING it, it cannot cancel
	// the goroutine itself. DepsRefreshGeneration is what makes that
	// abandoned goroutine's eventual result safe to discard.
	DepsRefreshInFlight bool
	// DepsRefreshStartedAt is when the in-flight batch began. The event loop
	// uses it as a watchdog: if a result never arrives (dropped event send,
	// panicked worker, hung tracker), the latch is force-cleared and the rows
	// are released for reselection.
	DepsRefreshStartedAt time.Time
	// DepsRefreshBatchSize is how many rows the in-flight batch holds.
	// Surfaced on the snapshot as the "refreshing N" operator signal.
	DepsRefreshBatchSize int
	// DepsRefreshLastDurationMs is the wall-clock of the last completed batch.
	DepsRefreshLastDurationMs int64
	// DepsRefreshGeneration increments on every batch launch and every
	// watchdog fire. A result whose Generation no longer matches belongs to an
	// abandoned batch: it must be dropped without touching the latch or any
	// row, because the current generation owns those rows now.
	DepsRefreshGeneration int64
	// PROpenedDispatched dedups `pr_opened` automation dispatches so a resumed
	// worker, a retry, or a secondary run on the same issue does not re-fire
	// the same `(issue, prURL, automationID)` triple. Lifetime is event-loop
	// owned; pruned by pruneTerminalRuntimeLedgers when the issue reaches a
	// terminal tracker state.
	//
	// Key shape: `<issue.Identifier>|<prURL>|<automationID>`.
	PROpenedDispatched map[string]struct{}

	// PRMergedDispatched is the pr_merged sibling of PROpenedDispatched.
	// Key shape: `<issue.Identifier>|<prURL>|<automationID>`.
	PRMergedDispatched map[string]struct{}

	// AutomationDropsSelfReentryTotal is a monotonic counter incremented every
	// time an `input_required` automation dispatch is suppressed because the
	// previous worker on this issue was itself an automation-driven run
	// (codex-B1). Surfaced on the snapshot for the dashboard's live-ops strip
	// so operators can distinguish "guarded loop" from "automation never
	// fired".
	AutomationDropsSelfReentryTotal uint64

	// AutomationDispatchesPROpenedTotal / AutomationDroppedPROpenedDedupTotal
	// surface pr_opened automation telemetry for the dashboard. codex-B4.
	AutomationDispatchesPROpenedTotal   uint64
	AutomationDroppedPROpenedDedupTotal uint64
	AutomationDispatchesPRMergedTotal   uint64
	AutomationDroppedPRMergedDedupTotal uint64

	// TransportFailureCount counts agent-runner errors classified as
	// transport-level (codex stream disconnected, network resets) so the
	// dashboard's LiveOpsStrip can surface a paused_transport tile.
	// todolist4 A.4.
	TransportFailureCount uint64

	// InferredDeps holds the per-tick reconciliation of LLM-inferred
	// dependency edges (from the depsanalysis sidecar) against the current
	// candidate-issue set and the dependencies gating policy. Key: target
	// (blocked) issue identifier. Recomputed every tick by
	// ReconcileInferredDeps in onTick; unified-dependency-graph Task 4.
	InferredDeps map[string][]InferredDepEntry

	// DepsOverrides holds operator-issued dismissals of the inferred-dependency
	// gating layer, keyed by the target (blocked) issue identifier, valued by
	// the time the override was set. Consulted by ReconcileInferredDeps (via
	// the overrides map argument) so an overridden target's InferredDeps
	// entries report Overridden=true and Gating=false. Mutated only by the
	// EventSetDepsOverride case in the event loop; SetDepsOverride (any
	// goroutine) only sends the event. unified-dependency-graph Task 6.
	DepsOverrides map[string]time.Time

	// StackOnReviewState is tracker.completion_state, lower-cased, when
	// dependencies.stacked_prs is on, and "" otherwise. Snapshotted each tick
	// under cfgMu. An issue whose ONLY unresolved blocker is in this state
	// may dispatch, stacked on that blocker's branch (#73 follow-up).
	StackOnReviewState string
	// StackUnavailable records, per dependent identifier, the review blocker
	// (stackKey) its worktree could not be stacked on. While that blocker is
	// unchanged the dependent waits for it as before; a new blocker or state
	// gives stacking another try. Event-loop state, not persisted.
	StackUnavailable map[string]string

	// PendingReviews holds reviewer dispatches refused while draining
	// (M4-close BH-M4-2), keyed by issue identifier. Persisted to
	// pending_reviews.json and re-dispatched by resumePendingReviews once
	// admission is open again (normally after the restart). Event loop only.
	PendingReviews map[string]PendingReview

	// DependencyCycles holds this tick's strongly-connected-component cycle
	// alerts over the tick graph (sorted by first member). Recomputed every
	// tick by ExtractCycles from the tick graph built in onTick; DetectedAt
	// is carried forward from the previous tick's slice when the exact
	// member set repeats. Derived, event-loop-owned state — no persistence,
	// no events. critical-path-ordering Task 4.
	DependencyCycles []DependencyCycle

	// DependencyAttention holds this tick's operator-attention entries
	// (cycle members plus issues blocked past the escalation window),
	// sorted by Identifier. Recomputed every tick by
	// DeriveDependencyAttention. Derived, event-loop-owned state — no
	// persistence, no events. critical-path-ordering Task 4.
	DependencyAttention []DependencyAttentionEntry

	// CandidateSeen is a snapshot of "what tracker polling saw this tick" —
	// one row per candidate-issue identifier (plus tracker UpdatedAt when
	// known), sorted by Identifier. Recomputed every tick by
	// candidateSeenRows in onTick, right where the freshly fetched `issues`
	// slice is in hand. Derived, event-loop-owned state — no persistence, no
	// events, and nothing else in the orchestrator consumes it; it exists
	// solely so the snapshot carries a real backlog signal for cmd/itervox's
	// deps auto-analyze scheduler. analyzer-autonomy Task 4 fix round.
	CandidateSeen []CandidateSeenRow

	// OutboxSyncing is the set of issue identifiers whose tracker State was
	// overlaid this tick by a pending write-ahead-outbox update_state entry
	// (see internal/orchestrator/outbox_overlay.go and
	// docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md,
	// "Overlay"). Recomputed every tick from scratch — membership does not
	// persist across ticks beyond what the outbox itself still has pending.
	// Derived, event-loop-owned state — no persistence, no events.
	//
	// This is the seam Task 4 (surfaces) reads to set `Syncing: true` on
	// issue rows: /api/v1/issues (server.go's handleIssues) builds its rows
	// from a direct client-side tracker fetch, NOT from this snapshot, so
	// Task 4 must join TrackerIssue rows against Snapshot().OutboxSyncing by
	// Identifier rather than finding a Syncing field already sitting on a
	// server-side issue-row type. Keyed by Identifier (not the tracker's
	// opaque ID) because that is the join key every issue-row surface
	// (TrackerIssue, RunEntry, snapshot maps) already uses.
	OutboxSyncing map[string]struct{}
}

// NewState initialises a State from a config snapshot.
func NewState(cfg *config.Config) State {
	return State{
		PollIntervalMs:              cfg.Polling.IntervalMs,
		MaxConcurrentAgents:         cfg.Agent.MaxConcurrentAgents,
		ActiveStates:                append([]string{}, cfg.Tracker.ActiveStates...),
		TerminalStates:              append([]string{}, cfg.Tracker.TerminalStates...),
		PauseDispatchWhenAnyInState: normalizePauseStates(cfg.Agent.PauseDispatchWhenAnyInState),
		Running:                     make(map[string]*RunEntry),
		Claimed:                     make(map[string]struct{}),
		RetryAttempts:               make(map[string]*RetryEntry),
		PausedIdentifiers:           make(map[string]string),
		PausedSessions:              make(map[string]*PausedSessionInfo),
		IssueProfiles:               make(map[string]string),
		IssueBackends:               make(map[string]string),
		AutoSwitchedIdentifiers:     make(map[string]struct{}),
		AutoSwitchedAt:              make(map[string]time.Time),
		SwitchHistory:               make(map[string][]time.Time),
		RateLimitCooldowns:          make(map[string]time.Time),
		RateLimitCapCommentUntil:    make(map[string]time.Time),
		AutoSwitchInfo:              make(map[string]AutoSwitchRecord),
		BackendHealth:               make(map[string]BackendHealthEntry),
		BackendLimitedHolds:         make(map[string]BackendHold),
		ForceReanalyze:              make(map[string]struct{}),
		PrevActiveIdentifiers:       make(map[string]struct{}),
		PrevIssueStates:             make(map[string]string),
		IssueStatusHistory:          make(map[string][]IssueStatusChange),
		DiscardingIdentifiers:       make(map[string]DiscardMarker),
		InputRequiredIssues:         make(map[string]*InputRequiredEntry),
		PendingInputResumes:         make(map[string]*PendingInputResumeEntry),
		AutomationQueue:             make(map[string]*AutomationQueueEntry),
		AutomationQueueOrder:        []string{},
		AutomationQueueBackpressure: AutomationQueueBackpressure{
			MaxLength: cfg.Agent.MaxAutomationQueueLength,
		},
		DependencyAudit:    make(map[string]*DependencyAuditEntry),
		ReviewVerdicts:     make(map[string][]ReviewVerdict),
		ReviewChainIndex:   make(map[string]int),
		ReviewOutcomes:     make(map[string]ReviewOutcome),
		PROpenedDispatched: make(map[string]struct{}),
		PRMergedDispatched: make(map[string]struct{}),
		InferredDeps:       make(map[string][]InferredDepEntry),
		DepsOverrides:      make(map[string]time.Time),
		StackOnReviewState: stackOnReviewState(cfg),
		StackUnavailable:   make(map[string]string),
		PendingReviews:     make(map[string]PendingReview),
		OutboxSyncing:      make(map[string]struct{}),
	}
}
