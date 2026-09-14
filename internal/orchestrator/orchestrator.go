package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// maxWorkersCap is the absolute upper bound on MaxConcurrentAgents.
// Time-based worker constants (hookFallbackTimeout, postRunTimeout) are
// defined in worker.go alongside their call sites; maxTransitionAttempts is
// defined in write_sink.go alongside directWriteSink, the only place that
// retry loop runs.
const maxWorkersCap = 50

// Orchestrator is the single-goroutine state machine that owns all dispatch state.
type Orchestrator struct {
	// DryRun disables actual agent execution: issues are claimed but no worker
	// subprocess is started. Set ITERVOX_DRY_RUN=1 or assign before calling Run.
	DryRun bool

	cfg       *config.Config
	tracker   tracker.Tracker
	runner    agent.Runner
	workspace workspace.Provider // nil is safe — workspace ops skipped (useful in tests)
	// sink is the write path for completion/failed-state transitions and
	// worker-exit outcome comments (see write_sink.go's WriteSink doc
	// comment for the full call-site list). New() defaults it to a
	// directWriteSink wrapping tr, preserving old behavior; cmd/itervox
	// (Task 3) calls SetWriteSink to swap in an outbox-backed sink when
	// cfg.Tracker.Outbox is true. Nil-safe via writeSink() — tests across
	// this package construct &Orchestrator{...} directly (bypassing New),
	// same convention as Logger/logger().
	sink WriteSink
	// outbox is the write-ahead outbox handle used for the tick-top overlay
	// and reconciliation (see outbox_overlay.go). It is read-only from the
	// event loop's perspective — the loop calls PendingFor (read) and Drop
	// (reconciliation's own internally-mutex-guarded mutation; Drop is not
	// an orchestrator.State mutation, it mutates outbox.Outbox's own state
	// under its own mutex, same as MarkFlushed/MarkFailed do for the
	// flusher goroutine). Nil is the default and fully safe: cfg.Tracker.Outbox
	// = false (the kill switch) means cmd/itervox never calls SetOutbox, so
	// every outbox_overlay.go read/reconcile call no-ops. Set once via
	// SetOutbox before Run() — same "must be set before Run" convention as
	// SetWriteSink (see its doc comment for why that's safe without a lock).
	outbox  *outbox.Outbox
	logBuf  *logbuffer.Buffer // nil is safe — log buffering disabled
	events  chan OrchestratorEvent
	refresh chan struct{} // signals an immediate re-poll (e.g. from the web dashboard)
	// OnDispatch is an optional hook called (in-goroutine) when an issue is dispatched.
	OnDispatch func(issueID string)
	// OnStateChange is called after every state snapshot update (see storeSnap).
	// Both fields must be set before calling Run — Run spawns goroutines that
	// read them, so the Go memory model's happens-before guarantee (set before
	// goroutine start) ensures visibility without any additional synchronisation.
	OnStateChange func()
	// Logger, when set, is used in place of the global slog.Default() for the
	// worker-completion / dispatch-success log lines (see logger(), and its
	// two call sites: worker.go's "worker: completed" and event_loop.go's
	// "orchestrator: worker succeeded, claim released"). Nil-safe: unset
	// falls back to slog.Default(), matching pre-existing behavior. Must be
	// set before calling Run() (same visibility rule as OnDispatch/
	// OnStateChange above — worker goroutines read it, so Go's happens-before
	// guarantee for "set before goroutine start" covers it).
	//
	// wave-2 polish Task 4 / #50 — exists specifically so tests can capture
	// this log output through a logger instance scoped to their own
	// Orchestrator, instead of mutating the process-global slog.Default().
	// runWorker's completion log runs in a worker goroutine that Run() does
	// not join on ctx cancellation (only the event-loop goroutine is
	// awaited), so a test that used slog.SetDefault + restore could have a
	// still-running worker goroutine from test N write into test N+1's
	// freshly-swapped default logger after N returns —
	// TestWorkerCompletedLogHasCorrectTokenValues's documented flake. An
	// injected per-Orchestrator Logger closes over its own buffer, so a
	// stray late write lands in the SAME test's (already-read, harmless)
	// buffer rather than a different test's.
	Logger *slog.Logger

	snapMu     sync.RWMutex
	lastSnap   State
	sshHostIdx int // round-robin index for SSH host selection; only accessed in event loop

	// cfgMu guards cfg fields mutated at runtime from HTTP handler goroutines.
	// The CANONICAL list of guarded fields lives in
	// `cfg_mu_audit_test.go::AllowedMutableCfgFields` (gap §7.1) — that test
	// fails the build if a new `o.cfg.X = ...` assignment is added without
	// being added to the allowlist. Browse it for the full enumeration.
	// Quick reference (kept loosely in sync; trust the audit test):
	// cfg.Agent.{MaxConcurrentAgents, Profiles, MaxRetries,
	// MaxSwitchesPerIssuePerWindow, SwitchWindowHours, SwitchRevertHours,
	// RateLimitErrorPatterns, SSHHosts, SSHHostDescriptions, DispatchStrategy,
	// ReviewerProfile, AutoReview, InlineInput};
	// cfg.Tracker.{ActiveStates, TerminalStates, CompletionState, FailedState};
	// cfg.Workspace.AutoClearWorkspace; cfg.Automations.
	// All other cfg fields are read-only after startup and need no lock.
	cfgMu sync.RWMutex

	// sshHostDescs maps SSH host address → optional human label.
	// Managed at runtime via AddSSHHostCfg / RemoveSSHHostCfg.
	// Protected by cfgMu alongside cfg.Agent.SSHHosts.
	sshHostDescs map[string]string

	// historyMu guards completedRuns, historyFile, and historyKey only.
	historyMu     sync.RWMutex
	completedRuns []CompletedRun
	historyFile   string // optional path for persisting completedRuns to disk
	historyKey    string // project key used to scope history entries; format "<kind>:<slug>"

	// pausedMu guards pausedFile, which is an unrelated concern from history.
	pausedMu   sync.RWMutex
	pausedFile string // optional path for persisting PausedIdentifiers across restarts

	// autoSwitchedMu guards autoSwitchedFile. Gap §5.3 — persistence for
	// state.IssueProfiles + state.IssueBackends overrides set by the
	// rate_limited automation auto-switch. Without this, a daemon crash
	// mid-flight loses the override and the next dispatch picks up the
	// original (rate-limited) profile, looping back into the same failure.
	autoSwitchedMu   sync.RWMutex
	autoSwitchedFile string

	// inputRequiredMu guards inputRequiredFile.
	inputRequiredMu   sync.RWMutex
	inputRequiredFile string // optional path for persisting InputRequiredIssues across restarts

	// automationQueueMu guards automationQueueFile only. Queue entries remain
	// owned by the single event-loop State.
	automationQueueMu   sync.RWMutex
	automationQueueFile string // optional path for persisting AutomationQueue across restarts

	// depsOverridesMu guards depsOverridesFile only. State.DepsOverrides
	// itself remains owned by the single event-loop State.
	// unified-dependency-graph Task 6.
	depsOverridesMu   sync.RWMutex
	depsOverridesFile string // optional path for persisting DepsOverrides across restarts
	// daemonInstanceID stamps the queue persistence envelope so a reader can
	// distinguish state written by this daemon from state inherited from
	// another (todolist4 A.2). Set once at construction; reads are lock-free.
	daemonInstanceID string

	// workerCancelsMu guards workerCancels, which is written by dispatch (event
	// loop goroutine) and read by cancelRunningWorker (any goroutine).
	// This is separate from lastSnap.Running because snapshot copies intentionally
	// omit WorkerCancel to avoid sharing cancel funcs across goroutines unsafely.
	workerCancelsMu sync.Mutex
	workerCancels   map[string]context.CancelFunc // identifier → cancel func

	// userCancelledMu guards userCancelledIDs, which is written by CancelIssue
	// (any goroutine) and read by handleEvent (event loop goroutine).
	userCancelledMu  sync.Mutex
	userCancelledIDs map[string]struct{} // keyed by identifier (e.g. "TIPRD-25")

	// userTerminatedMu guards userTerminatedIDs, which is written by TerminateIssue
	// (any goroutine) and read by handleEvent (event loop goroutine).
	userTerminatedMu  sync.Mutex
	userTerminatedIDs map[string]struct{} // like userCancelledIDs but releases claim without pausing

	// issueProfilesMu guards issueProfiles AND reviewerInjectedProfiles —
	// they're tracked together because reviewer-dispatch writes both under the
	// same lock. issueProfiles is written by SetIssueProfile (any goroutine)
	// and read by dispatch (event loop goroutine) and Snapshot.
	// RWMutex allows concurrent Snapshot() calls without serialising each other.
	issueProfilesMu sync.RWMutex
	issueProfiles   map[string]string // identifier → profile name

	// reviewerInjectedProfiles marks identifiers whose issueProfiles entry was
	// set by dispatchReviewerForIssue (T-21). On reviewer-run TerminalSucceeded
	// we clear ONLY entries we marked here, leaving user-set overrides intact.
	reviewerInjectedProfiles map[string]struct{}

	// issueBackendsMu guards issueBackends, which is written by SetIssueBackend
	// (any goroutine) and read by dispatch (event loop goroutine) and Snapshot.
	issueBackendsMu sync.RWMutex
	issueBackends   map[string]string // identifier → "claude"|"codex"

	// commentCountsMu guards commentCounts, which is written by
	// BumpCommentCount (any goroutine, called from the HTTP handler that
	// serves /api/v1/agent-actions/{identifier}/comment) and read by
	// CommentCountFor when populating snapshot rows. Per-identifier counters
	// are reset to zero by ResetCommentCount when the run terminates.
	commentCountsMu sync.RWMutex
	commentCounts   map[string]int // identifier → live comment count

	// automationsMu guards inputRequiredAutomations and runFailedAutomations so
	// the automations goroutine can hot-reload them via SetInputRequiredAutomations
	// / SetRunFailedAutomations concurrently with event-loop reads.
	automationsMu sync.RWMutex

	// inputRequiredAutomations is the compiled set of helper-agent rules that
	// react to blocked runs. Read by the event loop via snapshot helpers so
	// hot-reload from the automations goroutine is race-safe.
	inputRequiredAutomations []InputRequiredAutomation

	// runFailedAutomations is the compiled set of helper-agent rules that react
	// to terminal worker failures after retries are exhausted.
	runFailedAutomations []RunFailedAutomation

	// prOpenedAutomations (gap B) is the compiled set of rules that react to
	// the worker's pr_opened signal. Race-safe via the same automationsMu
	// guard as the other registries.
	prOpenedAutomations []PROpenedAutomation

	// prMergedAutomations (P1) mirrors prOpenedAutomations for the merge-side
	// trigger. Compiled rules are installed via SetPRMergedAutomations.
	prMergedAutomations []PRMergedAutomation

	// trackerCommentAutomations (P0-B) holds compiled tracker_comment_added
	// rules so the body-filter dispatch helper can short-circuit comments
	// that fail body_contains / body_regex matching.
	trackerCommentAutomations []TrackerCommentAutomation

	// rateLimitedAutomations (gap E) is the compiled set of rules that fire
	// when an exhausted-retry exit is classified rate-limit-driven. These
	// share the automationsMu lock with the other registries.
	rateLimitedAutomations []RateLimitedAutomation

	// blockersResolvedAutomations is the compiled set of rules that fire when
	// dependency audit observes a blocked issue becoming unblocked.
	blockersResolvedAutomations []BlockersResolvedAutomation

	// switchHistoryMu guards switchHistory which records every successful
	// rate_limited switch so the per-issue cap (cfg.Agent.MaxSwitchesPerIssuePerWindow
	// over cfg.Agent.SwitchWindowHours) can reject further switches once
	// the cap is reached.
	switchHistoryMu sync.Mutex
	switchHistory   map[string][]time.Time // issueID → fire timestamps

	// rateLimitCooldownMu guards rateLimitCooldown which records the time
	// until which a (issueID, profile) tuple is muted from re-firing the
	// rate_limited rule.
	rateLimitCooldownMu sync.Mutex
	rateLimitCooldown   map[string]time.Time // key="<issueID>|<profile>" → until

	// rateLimitCapCommentMu guards rateLimitCapCommentUntil, which deduplicates
	// managed tracker comments when a per-issue rate_limited switch cap blocks
	// repeated recovery attempts within the same rolling window.
	rateLimitCapCommentMu    sync.Mutex
	rateLimitCapCommentUntil map[string]time.Time // issueID → next time a cap comment may be posted

	// agentLogDir, when non-empty, is passed to RunTurn as CLAUDE_CODE_LOG_DIR
	// so Claude Code writes full session logs (including sub-agents) to disk.
	// Set via SetAgentLogDir before calling Run.
	agentLogDir string

	// depsSidecarCache is the mtime-cached reader over
	// `.itervox/dependencies.json`, read once per tick by onTick to feed
	// ReconcileInferredDeps. nil is safe (no sidecar path configured — e.g.
	// tests, or a daemon with no deps_analyzer_profile) and yields no
	// inferred edges. Set via SetDepsSidecarPath before calling Run; only
	// read from the event-loop goroutine. unified-dependency-graph Task 4.
	depsSidecarCache *depsanalysis.SidecarCache

	// agentActionBaseURL is the daemon base URL exposed to local worker actions.
	// Set via SetAgentActionBaseURL before Run.
	agentActionBaseURL string

	// agentActionTokens issues and validates short-lived per-run action grants.
	// Set via SetAgentActionTokens before Run.
	agentActionTokens *agentactions.Store

	// appSessionID is a unique ID for this daemon invocation, used to group
	// all history entries produced during a single run of the binary.
	// Set via SetAppSessionID before calling Run.
	appSessionID string

	// autoClearWg tracks in-flight auto-clear workspace goroutines so Run can
	// wait for them before returning.
	autoClearWg sync.WaitGroup

	// discardWg tracks in-flight asyncDiscardAndTransition / asyncDiscardAndTransitionTo
	// goroutines so Run can wait for them before returning.
	discardWg sync.WaitGroup

	// commentWg tracks every fire-and-forget tracker-comment goroutine, so
	// Run cannot return while one is still writing: the two in event_loop.go
	// (post-user-input comment, post-input-required-question comment) and the
	// two rate-limit notices in automation_rate_limited.go (auto-switch,
	// cap-exhausted). Without this, Run could return mid-write and drop a
	// comment the user expected persisted. T-44 (gaps_280426 02.G-01).
	//
	// Under the write-ahead outbox these are no longer just API calls — they
	// enqueue DURABLE entries, so an untracked one can also persist
	// .itervox/outbox.json after run() has returned and main()'s reload loop
	// has opened a second handle on the same file, which is a whole-file
	// rewrite from stale memory.
	commentWg sync.WaitGroup

	// transitionFailed marks issues whose completion-state tracker write
	// failed, so the event loop can record PauseReasonTransitionFailed rather
	// than mislabelling it a user cancel (#42-F).
	transitionFailed transitionFailedSet

	// depsRefreshWg tracks the in-flight dependency-refresh goroutine so Run
	// can wait for it before returning.
	depsRefreshWg sync.WaitGroup

	// runCtx is the context passed to Run. Stored atomically so DispatchReviewer
	// can read it safely from any goroutine without a mutex.
	runCtx atomic.Pointer[context.Context]

	// started is set to true at the beginning of Run. It guards SetHistoryFile
	// and SetHistoryKey: calling either after Run starts is a programming error
	// (those fields are only read under historyMu from the event-loop goroutine,
	// and loadHistoryFromDisk has already consumed them by the time Run returns).
	started atomic.Bool
}

// New constructs an Orchestrator ready to Run. wm may be nil (workspace ops skipped).
func New(cfg *config.Config, tr tracker.Tracker, runner agent.Runner, wm workspace.Provider) *Orchestrator {
	sshHostDescs := maps.Clone(cfg.Agent.SSHHostDescriptions)
	if sshHostDescs == nil {
		sshHostDescs = make(map[string]string)
	}
	return &Orchestrator{
		cfg:                      cfg,
		tracker:                  tr,
		runner:                   runner,
		workspace:                wm,
		sink:                     NewDirectWriteSink(tr),
		events:                   make(chan OrchestratorEvent, 64),
		refresh:                  make(chan struct{}, 1),
		workerCancels:            make(map[string]context.CancelFunc),
		userCancelledIDs:         make(map[string]struct{}),
		userTerminatedIDs:        make(map[string]struct{}),
		issueProfiles:            make(map[string]string),
		reviewerInjectedProfiles: make(map[string]struct{}),
		issueBackends:            make(map[string]string),
		commentCounts:            make(map[string]int),
		sshHostDescs:             sshHostDescs,
		daemonInstanceID:         newDaemonInstanceID(),
	}
}

// logger returns o.Logger when set, otherwise slog.Default() — see Logger's
// doc comment for why worker-goroutine completion logging goes through this
// instead of calling the slog package functions directly.
func (o *Orchestrator) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// writeSink returns o.sink when set (New's default, or an override from
// SetWriteSink), otherwise falls back to a directWriteSink wrapping
// o.tracker — the same nil-safe fallback convention as logger() above,
// needed because many tests in this package construct &Orchestrator{...}
// directly rather than via New().
func (o *Orchestrator) writeSink() WriteSink {
	if o.sink != nil {
		return o.sink
	}
	return NewDirectWriteSink(o.tracker)
}

// SetWriteSink overrides the orchestrator's write path for completion/
// failed-state transitions and worker-exit outcome comments (see
// write_sink.go's WriteSink doc comment). cmd/itervox calls this to route
// through an outbox-backed sink when cfg.Tracker.Outbox is true (Task 3);
// tests call it to inject a fake sink. Must be set before calling Run()
// (same visibility rule as OnDispatch/OnStateChange/Logger — see Logger's
// doc comment for why: Run spawns goroutines that read it, and Go's
// happens-before guarantee for "set before goroutine start" is what makes
// that safe without extra synchronization).
func (o *Orchestrator) SetWriteSink(sink WriteSink) {
	o.sink = sink
}

// SetOutbox gives the orchestrator the write-ahead outbox handle used for
// the tick-top overlay and reconciliation (outbox_overlay.go). cmd/itervox
// calls this whenever cfg.Tracker.Outbox is true, right alongside
// SetWriteSink(NewOutboxWriteSink(ob)) — same handle, two different reasons
// the event loop needs it (writing new entries goes through the sink;
// reading/reconciling pending entries goes through this accessor). Must be
// set before calling Run() (same visibility rule as SetWriteSink — see its
// doc comment). Leaving it unset (nil) is the kill-switch-off default and is
// fully safe: every read site in outbox_overlay.go treats a nil o.outbox as
// "nothing pending".
func (o *Orchestrator) SetOutbox(ob *outbox.Outbox) {
	o.outbox = ob
}

// newDaemonInstanceID returns a process-unique tag for the queue persistence
// envelope. PID + boot timestamp are sufficient — the envelope is local to a
// single repo and not synchronised across daemons. todolist4 A.2.
func newDaemonInstanceID() string {
	return fmt.Sprintf("itervox-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// BumpCommentCount increments the per-identifier comment counter (T-6).
// Called from the HTTP handler that fans out a comment action; safe for any
// goroutine. The counter is read-merged into RunningRow.CommentCount /
// HistoryRow.CommentCount at snapshot conversion time.
func (o *Orchestrator) BumpCommentCount(identifier string) {
	if identifier == "" {
		return
	}
	o.commentCountsMu.Lock()
	if o.commentCounts == nil {
		o.commentCounts = make(map[string]int)
	}
	o.commentCounts[identifier]++
	o.commentCountsMu.Unlock()
}

// CommentCountFor returns the current count for the identifier (zero if the
// identifier has not commented). Safe for any goroutine.
func (o *Orchestrator) CommentCountFor(identifier string) int {
	o.commentCountsMu.RLock()
	defer o.commentCountsMu.RUnlock()
	return o.commentCounts[identifier]
}

// ResetCommentCount clears the counter for the identifier. Called when a
// run terminates so the next run starts fresh.
func (o *Orchestrator) ResetCommentCount(identifier string) {
	o.commentCountsMu.Lock()
	delete(o.commentCounts, identifier)
	o.commentCountsMu.Unlock()
}

// SetAgentLogDir configures the directory where agent session logs are written
// via CLAUDE_CODE_LOG_DIR. Per-issue logs are stored in {dir}/{identifier}/.
// Must be called before Run.
func (o *Orchestrator) SetAgentLogDir(dir string) {
	o.agentLogDir = dir
}

// SetAgentActionBaseURL configures the daemon base URL exposed to local workers.
// Must be called before Run.
func (o *Orchestrator) SetAgentActionBaseURL(url string) {
	o.agentActionBaseURL = url
}

// SetAgentActionTokens configures the short-lived action grant store.
// Must be called before Run.
func (o *Orchestrator) SetAgentActionTokens(store *agentactions.Store) {
	o.agentActionTokens = store
}

// SetAppSessionID sets the unique ID for this daemon invocation.
// Must be called before Run.
func (o *Orchestrator) SetAppSessionID(id string) {
	o.appSessionID = id
}

// AgentLogDir returns the configured agent log directory (empty = disabled).
func (o *Orchestrator) AgentLogDir() string {
	return o.agentLogDir
}

// Refresh triggers an immediate re-poll on the next select iteration.
// Safe to call from any goroutine; non-blocking (drops the signal if one is already pending).
func (o *Orchestrator) Refresh() {
	select {
	case o.refresh <- struct{}{}:
	default:
	}
}

// cancelAndCleanupWorker cancels the worker context for the given issue
// identifier and removes the entry from the workerCancels map atomically.
//
// This MUST be the only path that mutates workerCancels post-startup. The
// previous design coupled cleanup to the EventWorkerExited event reaching
// the event loop — but reconcile-driven cancellations send that event with
// a 100ms send-timeout, which under load drops the event and leaves the
// cancel func pinned in the map until process exit (a slow leak per
// stalled or reconciled worker). Calling this helper makes cleanup
// independent of event delivery.
//
// Safe to call from any goroutine. If the identifier is not in the map
// (already cleaned up), this is a no-op.
func (o *Orchestrator) cancelAndCleanupWorker(identifier string) {
	o.workerCancelsMu.Lock()
	cancel, ok := o.workerCancels[identifier]
	if ok {
		delete(o.workerCancels, identifier)
	}
	o.workerCancelsMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

// SetMaxWorkers updates the maximum number of concurrent agents at runtime.
// The value is clamped to [1, maxWorkersCap]. Safe to call from any goroutine.
func (o *Orchestrator) SetMaxWorkers(n int) {
	if n < 1 {
		n = 1
	}
	if n > maxWorkersCap {
		n = maxWorkersCap
	}
	o.cfgMu.Lock()
	o.cfg.Agent.MaxConcurrentAgents = n
	o.cfgMu.Unlock()
	slog.Info("orchestrator: max workers updated", "max_concurrent_agents", n)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// BumpMaxWorkers atomically applies a delta to MaxConcurrentAgents under cfgMu,
// clamping the result to [1, maxWorkersCap]. Returns the new value.
// Safe to call from any goroutine.
func (o *Orchestrator) BumpMaxWorkers(delta int) int {
	o.cfgMu.Lock()
	next := max(1, min(o.cfg.Agent.MaxConcurrentAgents+delta, maxWorkersCap))
	o.cfg.Agent.MaxConcurrentAgents = next
	o.cfgMu.Unlock()
	slog.Info("orchestrator: max workers bumped", "delta", delta, "max_concurrent_agents", next)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
	return next
}

// MaxWorkers returns the current maximum concurrent agents setting.
// Safe to call from any goroutine.
func (o *Orchestrator) MaxWorkers() int {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.MaxConcurrentAgents
}

// SetMaxRetriesCfg updates the per-issue retry budget at runtime. A value of 0
// is interpreted as "unlimited retries" by the event loop's exhaustion check
// (see event_loop.go). Negative values are clamped to 0.
// Safe to call from any goroutine.
func (o *Orchestrator) SetMaxRetriesCfg(n int) {
	if n < 0 {
		n = 0
	}
	o.cfgMu.Lock()
	o.cfg.Agent.MaxRetries = n
	o.cfgMu.Unlock()
	slog.Info("orchestrator: max_retries updated", "max_retries", n)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// MaxRetriesCfg returns the current per-issue retry budget.
// Safe to call from any goroutine.
func (o *Orchestrator) MaxRetriesCfg() int {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.MaxRetries
}

// SetFailedStateCfg updates the tracker state issues are moved to when the
// retry budget is exhausted. Empty string means "pause instead of move".
// The caller is responsible for validating that the state exists in the
// tracker's known state set; this setter does not validate.
// Safe to call from any goroutine.
func (o *Orchestrator) SetFailedStateCfg(stateName string) {
	o.cfgMu.Lock()
	o.cfg.Tracker.FailedState = stateName
	o.cfgMu.Unlock()
	slog.Info("orchestrator: failed_state updated", "failed_state", stateName)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// FailedStateCfg returns the current failed-state name (empty = pause).
// Safe to call from any goroutine.
func (o *Orchestrator) FailedStateCfg() string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Tracker.FailedState
}

// MaxSwitchesPerIssuePerWindowCfg returns the per-issue rate-limited switch
// cap. 0 means "unlimited" (operator opt-out). Gap E. Safe to call from any
// goroutine.
func (o *Orchestrator) MaxSwitchesPerIssuePerWindowCfg() int {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.MaxSwitchesPerIssuePerWindow
}

// SwitchWindowHoursCfg returns the rolling-window duration over which
// switches are counted. Gap E.
func (o *Orchestrator) SwitchWindowHoursCfg() int {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.SwitchWindowHours
}

// SetMaxSwitchesPerIssuePerWindowCfg updates the cap at runtime. Negative
// values are clamped to 0 (= unlimited). Gap E.
func (o *Orchestrator) SetMaxSwitchesPerIssuePerWindowCfg(n int) {
	if n < 0 {
		n = 0
	}
	o.cfgMu.Lock()
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = n
	o.cfgMu.Unlock()
	slog.Info("orchestrator: max_switches_per_issue_per_window updated", "cap", n)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// SetSwitchWindowHoursCfg updates the rolling-window duration. Values <= 0
// are normalised to 6h. Gap E.
func (o *Orchestrator) SetSwitchWindowHoursCfg(h int) {
	if h <= 0 {
		h = 6
	}
	o.cfgMu.Lock()
	o.cfg.Agent.SwitchWindowHours = h
	o.cfgMu.Unlock()
	slog.Info("orchestrator: switch_window_hours updated", "hours", h)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// SetAutoClearWorkspaceCfg toggles automatic workspace removal after a task succeeds.
// Returns an error when the change would conflict with auto-review.
// Safe to call from any goroutine.
func (o *Orchestrator) SetAutoClearWorkspaceCfg(enabled bool) error {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	if err := config.ValidateAutoClearAutoReview(
		enabled,
		o.cfg.Agent.ReviewerProfile,
		o.cfg.Agent.AutoReview,
	); err != nil {
		return err
	}
	o.cfg.Workspace.AutoClearWorkspace = enabled
	return nil
}

// ClearHistory wipes the in-memory completed-run ring buffer and deletes the
// on-disk history file. Safe to call from any goroutine.
func (o *Orchestrator) ClearHistory() {
	o.historyMu.Lock()
	o.completedRuns = nil
	path := o.historyFile
	o.historyMu.Unlock()
	if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to remove history file", "path", path, "error", err)
		}
	}
}

// AutoClearWorkspaceCfg returns the current auto-clear workspace setting.
// Safe to call from any goroutine.
func (o *Orchestrator) AutoClearWorkspaceCfg() bool {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Workspace.AutoClearWorkspace
}

func (o *Orchestrator) SetInlineInputCfg(enabled bool) {
	o.cfgMu.Lock()
	o.cfg.Agent.InlineInput = enabled
	o.cfgMu.Unlock()
}

func (o *Orchestrator) InlineInputCfg() bool {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.InlineInput
}

// AvailableModelsCfg returns the available models from the config.
// Read-only after startup — no lock needed.
func (o *Orchestrator) AvailableModelsCfg() map[string][]config.ModelOption {
	return o.cfg.Agent.AvailableModels
}

// ReviewerCfg returns the reviewer profile name and auto-review flag under cfgMu.
func (o *Orchestrator) ReviewerCfg() (profile string, autoReview bool) {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.ReviewerProfile, o.cfg.Agent.AutoReview
}

// reviewerChainCfg returns the reviewer chain read under cfgMu.
//
// cfg.Agent.ReviewerProfile is on the cfgMu allowlist and has a live runtime
// writer — SetReviewerCfg, reachable from the PUT /settings/reviewer handler
// goroutine. ReviewerProfileChain reads that field whenever the plural form
// is unset, which is the default single-reviewer shape, so every unlocked
// call was an unsynchronized string-header read racing that writer. The
// returned slice is freshly allocated inside ReviewerProfileChain, so it is
// safe to use after the lock is released.
func (o *Orchestrator) reviewerChainCfg() []string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return ReviewerProfileChain(o.cfg)
}

// reviewVerdictRelPathCfg is reviewVerdictRelPathFor read under cfgMu. Called
// from runWorker — a worker goroutine — which must never touch o.cfg
// directly; see reviewerChainCfg for the writer it races.
func (o *Orchestrator) reviewVerdictRelPathCfg(identifier, profileName string) string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return reviewVerdictRelPathFor(o.cfg, identifier, profileName)
}

// DepsAnalyzerProfileCfg returns the configured deps-analyzer profile name
// under cfgMu. Empty means the analyzer is disabled and the dashboard's
// "Analyze dependencies" button stays disabled. v0.2.0 todolist6.
func (o *Orchestrator) DepsAnalyzerProfileCfg() string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.DepsAnalyzerProfile
}

// AgentProfileCfg returns a copy of the named agent profile under cfgMu. The
// deps-analyzer service uses this to resolve SOUL/INSTRUCTIONS at job-run
// time without snapshotting the full profile map. v0.2.0 todolist6.
func (o *Orchestrator) AgentProfileCfg(name string) (config.AgentProfile, bool) {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	p, ok := o.cfg.Agent.Profiles[name]
	return p, ok
}

// ResolveDepsAnalyzerProfileCfg atomically reads the configured
// deps-analyzer profile name AND resolves it against cfg.Agent.Profiles
// under a single cfgMu critical section. Equivalent to calling
// DepsAnalyzerProfileCfg() followed by AgentProfileCfg(name), except those
// two separate cfgMu acquisitions can observe a torn read: a
// SetDepsAnalyzerProfileCfg (or a profiles.* PUT) landing between the two
// calls can pair an old profile name with a newer/absent profile map entry
// (or vice versa), which self-heals on the next tick but produces a
// spurious "profile does not resolve" warning in the interim (#52 deferral —
// "resolveProfile reads two cfgMu sections non-atomically"). Callers that
// need both values together (deps_auto_analyze.go's startDepsAutoAnalyze)
// should use this instead of the two single-field accessors.
func (o *Orchestrator) ResolveDepsAnalyzerProfileCfg() (name string, profile config.AgentProfile, ok bool) {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	name = o.cfg.Agent.DepsAnalyzerProfile
	if name == "" {
		return "", config.AgentProfile{}, false
	}
	profile, ok = o.cfg.Agent.Profiles[name]
	return name, profile, ok
}

// SetDepsAnalyzerProfileCfg updates the deps-analyzer profile name at runtime
// under cfgMu. Settings UI surface; Phase 2.3. v0.2.0 todolist6.
func (o *Orchestrator) SetDepsAnalyzerProfileCfg(profile string) error {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	if err := config.ValidateDepsAnalyzerProfile(o.cfg.Agent.Profiles, profile); err != nil {
		return err
	}
	o.cfg.Agent.DepsAnalyzerProfile = profile
	return nil
}

// SetReviewerCfg sets the reviewer profile name and auto-review flag under cfgMu.
// Returns an error when the change would conflict with auto-clear.
func (o *Orchestrator) SetReviewerCfg(profile string, autoReview bool) error {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	if err := config.ValidateReviewerAutoReview(profile, autoReview); err != nil {
		return err
	}
	if err := config.ValidateReviewerProfile(o.cfg.Agent.Profiles, profile); err != nil {
		return err
	}
	if err := config.ValidateAutoClearAutoReview(
		o.cfg.Workspace.AutoClearWorkspace,
		profile,
		autoReview,
	); err != nil {
		return err
	}
	o.cfg.Agent.ReviewerProfile = profile
	o.cfg.Agent.AutoReview = autoReview
	return nil
}

// ProfilesCfg returns a shallow copy of cfg.Agent.Profiles under cfgMu.
// Safe to call from any goroutine.
func (o *Orchestrator) ProfilesCfg() map[string]config.AgentProfile {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	cp := make(map[string]config.AgentProfile, len(o.cfg.Agent.Profiles))
	maps.Copy(cp, o.cfg.Agent.Profiles)
	return cp
}

// SetProfilesCfg atomically replaces cfg.Agent.Profiles under cfgMu.
// Safe to call from any goroutine.
func (o *Orchestrator) SetProfilesCfg(p map[string]config.AgentProfile) {
	o.cfgMu.Lock()
	o.cfg.Agent.Profiles = p
	o.cfgMu.Unlock()
}

// TrackerStatesCfg returns copies of cfg.Tracker.ActiveStates, TerminalStates,
// and CompletionState under cfgMu.
// Safe to call from any goroutine.
func (o *Orchestrator) TrackerStatesCfg() (active, terminal []string, completion string) {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return append([]string{}, o.cfg.Tracker.ActiveStates...),
		append([]string{}, o.cfg.Tracker.TerminalStates...),
		o.cfg.Tracker.CompletionState
}

// SetTrackerStatesCfg atomically updates ActiveStates, TerminalStates, and
// CompletionState under cfgMu.
// Safe to call from any goroutine.
func (o *Orchestrator) SetTrackerStatesCfg(active, terminal []string, completion string) {
	o.cfgMu.Lock()
	o.cfg.Tracker.ActiveStates = active
	o.cfg.Tracker.TerminalStates = terminal
	o.cfg.Tracker.CompletionState = completion
	o.cfgMu.Unlock()
}

// AutomationsCfg returns a deep copy of cfg.Automations under cfgMu.
// Safe to call from any goroutine. Automation slices inside each config are
// copied to decouple the caller from future in-place mutations.
func (o *Orchestrator) AutomationsCfg() []config.AutomationConfig {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	if len(o.cfg.Automations) == 0 {
		return nil
	}
	out := make([]config.AutomationConfig, len(o.cfg.Automations))
	for i, a := range o.cfg.Automations {
		cp := a
		if len(a.Filter.States) > 0 {
			cp.Filter.States = append([]string{}, a.Filter.States...)
		}
		if len(a.Filter.LabelsAny) > 0 {
			cp.Filter.LabelsAny = append([]string{}, a.Filter.LabelsAny...)
		}
		out[i] = cp
	}
	return out
}

// SetAutomationsCfg atomically replaces cfg.Automations under cfgMu.
// Safe to call from any goroutine. The caller is responsible for re-registering
// input-required and run-failed automation sets via SetInputRequiredAutomations
// and SetRunFailedAutomations so the event loop picks up the new rules.
func (o *Orchestrator) SetAutomationsCfg(cfgs []config.AutomationConfig) {
	o.cfgMu.Lock()
	o.cfg.Automations = cfgs
	o.cfgMu.Unlock()
}

// SSHHostsCfg returns a copy of the current SSH host list and descriptions map.
// Safe to call from any goroutine.
func (o *Orchestrator) SSHHostsCfg() (hosts []string, descs map[string]string) {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return append([]string{}, o.cfg.Agent.SSHHosts...), maps.Clone(o.sshHostDescs)
}

// AddSSHHostCfg adds a host to the SSH pool at runtime. If the host already
// exists, its description is updated. Safe to call from any goroutine.
func (o *Orchestrator) AddSSHHostCfg(host, description string) {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	if o.cfg.Agent.SSHHostDescriptions == nil {
		o.cfg.Agent.SSHHostDescriptions = make(map[string]string)
	}
	for _, h := range o.cfg.Agent.SSHHosts {
		if h == host {
			if description == "" {
				delete(o.sshHostDescs, host)
				delete(o.cfg.Agent.SSHHostDescriptions, host)
			} else {
				o.sshHostDescs[host] = description
				o.cfg.Agent.SSHHostDescriptions[host] = description
			}
			return
		}
	}
	o.cfg.Agent.SSHHosts = append(o.cfg.Agent.SSHHosts, host)
	if description != "" {
		o.sshHostDescs[host] = description
		o.cfg.Agent.SSHHostDescriptions[host] = description
	}
}

// RemoveSSHHostCfg removes a host from the SSH pool at runtime.
// Safe to call from any goroutine.
func (o *Orchestrator) RemoveSSHHostCfg(host string) {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	result := o.cfg.Agent.SSHHosts[:0:0]
	for _, h := range o.cfg.Agent.SSHHosts {
		if h != host {
			result = append(result, h)
		}
	}
	o.cfg.Agent.SSHHosts = result
	delete(o.sshHostDescs, host)
	delete(o.cfg.Agent.SSHHostDescriptions, host)
}

// DispatchStrategyCfg returns the active dispatch strategy.
// Safe to call from any goroutine.
func (o *Orchestrator) DispatchStrategyCfg() string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return o.cfg.Agent.DispatchStrategy
}

// SetDispatchStrategyCfg sets the dispatch strategy at runtime.
// Safe to call from any goroutine.
func (o *Orchestrator) SetDispatchStrategyCfg(strategy string) {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	o.cfg.Agent.DispatchStrategy = strategy
}

// SetLogBuffer attaches a log buffer so worker output is captured per-identifier
// for display in the interactive TUI.
func (o *Orchestrator) SetLogBuffer(buf *logbuffer.Buffer) {
	o.logBuf = buf
}

// SetDepsSidecarPath installs the mtime-cached sidecar reader the event loop
// consults every tick to reconcile inferred dependency edges. Call before Run;
// not calling it leaves depsSidecarCache nil, which sidecarEdges() treats as
// "no inferred edges" rather than panicking. unified-dependency-graph Task 4.
func (o *Orchestrator) SetDepsSidecarPath(path string) {
	o.depsSidecarCache = depsanalysis.NewSidecarCache(path)
}

// sidecarEdges returns the current inferred-edge set from the sidecar cache.
// nil-safe: a nil depsSidecarCache (no path configured) or a missing/invalid
// sidecar file both yield an empty slice rather than a panic or nil map read.
func (o *Orchestrator) sidecarEdges() []depsanalysis.InferredEdge {
	if o.depsSidecarCache == nil {
		return nil
	}
	sc := o.depsSidecarCache.Latest()
	if sc == nil {
		return nil
	}
	return sc.Edges
}
