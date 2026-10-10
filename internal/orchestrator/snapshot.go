package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/domain"
)

// Snapshot returns a consistent copy of the current orchestrator state.
// Safe to call from any goroutine.
//
// issueProfiles are stored in o.issueProfiles (written by SetIssueProfile from
// any goroutine) rather than in the event-loop State, so they are not
// automatically included in lastSnap. We overlay them here so callers — in
// particular fetchIssues in main.go — see the live assignments without waiting
// for the next event-loop tick to rebuild the snapshot.
// IsAutomationProducersPaused returns the paused-producers flag from the most
// recently published snapshot without paying the deep-copy cost of Snapshot().
// Producer goroutines (cron, poll-event, input-required) call this to short-
// circuit before issuing a tracker fetch when backpressure has already paused
// dispatch — saving 16+ map clones per check on a busy daemon.
// v0.2.0 audit P2-1.
func (o *Orchestrator) IsAutomationProducersPaused() bool {
	o.snapMu.RLock()
	defer o.snapMu.RUnlock()
	return o.lastSnap.AutomationQueueBackpressure.PausedProducers
}

func (o *Orchestrator) Snapshot() State {
	o.snapMu.RLock()
	snap := o.lastSnap
	o.snapMu.RUnlock()
	snap = snap.Clone()

	o.issueProfilesMu.RLock()
	if len(o.issueProfiles) > 0 {
		merged := make(map[string]string, len(snap.IssueProfiles)+len(o.issueProfiles))
		maps.Copy(merged, snap.IssueProfiles)
		for k, v := range o.issueProfiles {
			if v == "" {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		snap.IssueProfiles = merged
	}
	o.issueProfilesMu.RUnlock()

	o.issueBackendsMu.RLock()
	if len(o.issueBackends) > 0 {
		merged := make(map[string]string, len(snap.IssueBackends)+len(o.issueBackends))
		maps.Copy(merged, snap.IssueBackends)
		for k, v := range o.issueBackends {
			if v == "" {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		snap.IssueBackends = merged
	}
	o.issueBackendsMu.RUnlock()

	snap.PersistWriteErrors = o.persistWriteErrors.Load()
	return snap
}

const maxHistory = 200

// writeLedgerFile is the single write path for every orchestrator ledger
// (history, paused, pause reasons, input-required, automation queue,
// auto-switched, deps overrides). It delegates to atomicfs.WriteFile —
// temp file in the same directory, fsync, chmod, rename, then a best-effort
// parent-directory fsync — so a reader (or a restart after a crash) sees
// either the previous complete file or the new complete file, never a torn
// one. The parent-dir fsync is best-effort (atomicfs swallows ENOTSUP /
// EINVAL), so this is not a crash-proof durability claim for the directory
// entry. CORE-037.
//
// o.persistWriteFile is a test seam (a fake writer that blocks, fails or
// counts); nil means atomicfs.WriteFile.
func (o *Orchestrator) writeLedgerFile(path string, data []byte, perm fs.FileMode) error {
	if o.persistWriteFile != nil {
		return o.persistWriteFile(path, data, perm)
	}
	return atomicfs.WriteFile(path, data, perm)
}

// SetHistoryFile sets the path for persisting completed runs across restarts.
// Must be called before Run; calling after Run starts is a no-op with a logged error.
// If path is empty, disk persistence is disabled.
func (o *Orchestrator) SetHistoryFile(path string) {
	if o.started.Load() {
		slog.Error("orchestrator: SetHistoryFile called after Run started; ignoring", "path", path)
		return
	}
	o.historyMu.Lock()
	o.historyFile = path
	o.historyMu.Unlock()
}

// SetHistoryKey sets the project-scoping key used to tag and filter history entries.
// Format: "<tracker-kind>:<project-slug>" (e.g. "github:org/repo").
// Entries written with a different (non-empty) key are skipped on load.
// Must be called before Run; calling after Run starts is a no-op with a logged error.
func (o *Orchestrator) SetHistoryKey(key string) {
	if o.started.Load() {
		slog.Error("orchestrator: SetHistoryKey called after Run started; ignoring", "key", key)
		return
	}
	o.historyMu.Lock()
	o.historyKey = key
	o.historyMu.Unlock()
}

// loadHistoryFromDisk reads the history file (if set) and populates completedRuns.
// Called once at startup before the event loop begins.
func (o *Orchestrator) loadHistoryFromDisk() {
	o.historyMu.Lock()
	defer o.historyMu.Unlock()
	if o.historyFile == "" {
		return
	}
	data, err := os.ReadFile(o.historyFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load history file", "path", o.historyFile, "error", err)
		}
		return
	}
	var runs []CompletedRun
	if err := json.Unmarshal(data, &runs); err != nil {
		slog.Warn("orchestrator: failed to parse history file", "path", o.historyFile, "error", err)
		return
	}
	// Filter to only this project's runs. Legacy entries (empty ProjectKey) are
	// kept so that history written before scoping was added is not dropped.
	if o.historyKey != "" {
		filtered := runs[:0]
		for _, r := range runs {
			if r.ProjectKey == "" || r.ProjectKey == o.historyKey {
				filtered = append(filtered, r)
			}
		}
		runs = filtered
	}
	o.completedRuns = runs
	slog.Info("orchestrator: loaded history", "path", o.historyFile, "entries", len(runs))
}

// addCompletedRun appends a finished run to the in-memory history ring buffer
// and persists the ring buffer to disk when a history file is configured.
//
// INVARIANT: must only be called from the single event-loop goroutine (onTick
// and its callees). The event loop is the sole writer of completedRuns; the
// historyMu lock exists to synchronise concurrent readers such as the SSE and
// REST handlers, and ClearHistory.
//
// CORE-151: the history version is marshalled AND submitted to the history
// ledger while historyMu is held, and ClearHistory submits its removal under
// the same lock. Submission order therefore equals history order, and the
// ledger writes versions in submission order, so a clear can never be undone
// by a write of pre-clear history that was already in flight. The disk I/O
// itself (settle) runs after historyMu is released, so readers are never
// blocked by it.
func (o *Orchestrator) addCompletedRun(run CompletedRun) {
	o.historyMu.Lock()
	o.completedRuns = append(o.completedRuns, run)
	if len(o.completedRuns) > maxHistory {
		o.completedRuns = o.completedRuns[len(o.completedRuns)-maxHistory:]
	}
	path := o.historyFile
	submitted := false
	if path != "" {
		data, err := json.Marshal(o.completedRuns)
		if err != nil {
			slog.Warn("orchestrator: failed to marshal history entries", "error", err)
		} else {
			submitted = o.ledger(ledgerHistory).submit(path, data, 0o644)
		}
	}
	o.historyMu.Unlock()
	if submitted {
		o.ledger(ledgerHistory).settle()
	}
}

// SetPausedFile sets the path for persisting PausedIdentifiers across restarts.
// Must be called before Run.
func (o *Orchestrator) SetPausedFile(path string) {
	o.pausedMu.Lock()
	o.pausedFile = path
	o.pausedMu.Unlock()
}

// loadPausedFromDisk reads the paused file and pre-populates state.PausedIdentifiers.
// Called once at startup. state is the freshly-initialised event-loop State.
// Supports both the new format (map[identifier]issueID) and the legacy format
// ([]string of identifiers), storing an empty UUID for legacy entries.
func (o *Orchestrator) loadPausedFromDisk(state State) State {
	o.pausedMu.RLock()
	path := o.pausedFile
	o.pausedMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load paused file", "path", path, "error", err)
		}
		return state
	}
	// Try new format: {"identifier": "issueUUID", ...}
	var newFmt map[string]string
	if err := json.Unmarshal(data, &newFmt); err == nil {
		maps.Copy(state.PausedIdentifiers, newFmt)
		// Pre-populate PrevActiveIdentifiers so the first-tick auto-resume guard
		// treats these as "was already active before daemon start" and does not
		// clear the pause. Without this, the empty PrevActiveIdentifiers on startup
		// causes every disk-persisted pause to be auto-resumed on the first tick —
		// this happens whenever WORKFLOW.md is written (e.g. BumpWorkers), which
		// triggers the file watcher and restarts the orchestrator.
		for id := range newFmt {
			state.PrevActiveIdentifiers[id] = struct{}{}
		}
		slog.Info("orchestrator: loaded paused identifiers", "path", path, "count", len(newFmt))
		return state
	}
	// Fallback: legacy format ["identifier1", "identifier2"]
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		slog.Warn("orchestrator: failed to parse paused file", "path", path, "error", err)
		return state
	}
	for _, id := range ids {
		state.PausedIdentifiers[id] = "" // UUID unknown from legacy format — Discard won't auto-move to Backlog
		state.PrevActiveIdentifiers[id] = struct{}{}
	}
	slog.Info("orchestrator: loaded paused identifiers (legacy format)", "path", path, "count", len(ids))
	return state
}

// SetInputRequiredFile sets the path for persisting input-required waiting
// entries and pending resumes across restarts.
// Must be called before Run.
func (o *Orchestrator) SetInputRequiredFile(path string) {
	o.inputRequiredMu.Lock()
	o.inputRequiredFile = path
	o.inputRequiredMu.Unlock()
}

// inputRequiredDisk is the JSON-serializable form of InputRequiredEntry.
type inputRequiredDisk struct {
	IssueID            string `json:"issue_id"`
	Identifier         string `json:"identifier"`
	SessionID          string `json:"session_id"`
	Context            string `json:"context"`
	BranchName         string `json:"branch_name,omitempty"`
	Backend            string `json:"backend"`
	Command            string `json:"command"`
	WorkerHost         string `json:"worker_host,omitempty"`
	ProfileName        string `json:"profile_name,omitempty"`
	QuestionCommentKey string `json:"question_comment_key,omitempty"`
	QuestionCommentID  string `json:"question_comment_id,omitempty"`
	QuestionAuthorID   string `json:"question_author_id,omitempty"`
	QuestionAuthorName string `json:"question_author_name,omitempty"`
	QueuedAt           string `json:"queued_at"`
	// LastReplyCheckAt persists the reply-check fairness ordering across
	// restarts and config reloads. Without it every reload zeroed the key for
	// every entry, so selectTrackerReplyCheckBatch fell through to its
	// identifier tie-break and restarted at the alphabetically-first five —
	// on a backlog larger than the budget, entries sorting later were never
	// checked at all. That is the exact starvation the ordering exists to
	// prevent, and an operator iterating on WORKFLOW.md reloads often.
	LastReplyCheckAt string `json:"last_reply_check_at,omitempty"`
}

type pendingInputResumeDisk struct {
	IssueID            string `json:"issue_id"`
	Identifier         string `json:"identifier"`
	SessionID          string `json:"session_id"`
	Context            string `json:"context"`
	UserMessage        string `json:"user_message"`
	BranchName         string `json:"branch_name,omitempty"`
	Backend            string `json:"backend"`
	Command            string `json:"command"`
	WorkerHost         string `json:"worker_host,omitempty"`
	ProfileName        string `json:"profile_name,omitempty"`
	QuestionCommentKey string `json:"question_comment_key,omitempty"`
	QuestionCommentID  string `json:"question_comment_id,omitempty"`
	QuestionAuthorID   string `json:"question_author_id,omitempty"`
	QuestionAuthorName string `json:"question_author_name,omitempty"`
	QueuedAt           string `json:"queued_at"`
}

type inputRequiredStateDisk struct {
	Awaiting      map[string]inputRequiredDisk      `json:"awaiting,omitempty"`
	PendingResume map[string]pendingInputResumeDisk `json:"pending_resume,omitempty"`
}

type automationQueueStateDisk struct {
	Entries      map[string]*AutomationQueueEntry `json:"entries,omitempty"`
	Order        []string                         `json:"order,omitempty"`
	Backpressure AutomationQueueBackpressure      `json:"backpressure,omitempty"`
	// AUTO-4 — persist the dependency-audit ledger and its transition seq
	// alongside the queue so a blocker that reaches terminal while the daemon
	// is down still fires blockers_resolved after restart. These are ADDITIVE,
	// omitempty fields: they do NOT bump QueuePersistenceSchemaVersion, so files
	// written by an older daemon (which lack these keys) load cleanly with a nil
	// ledger + zero seq — exactly today's behavior — instead of being
	// quarantined on upgrade. The envelope sha256 is computed over whatever
	// payload bytes were written, so old and new payload shapes each verify.
	DependencyAudit         map[string]*DependencyAuditEntry `json:"dependency_audit,omitempty"`
	DependencyTransitionSeq int64                            `json:"dependency_transition_seq,omitempty"`
}

// saveInputRequiredToDisk writes InputRequiredIssues and PendingInputResumes to disk.
func (o *Orchestrator) saveInputRequiredToDisk(entries map[string]*InputRequiredEntry, pending map[string]*PendingInputResumeEntry) {
	o.inputRequiredMu.RLock()
	path := o.inputRequiredFile
	o.inputRequiredMu.RUnlock()
	if path == "" {
		return
	}
	awaitingDisk := make(map[string]inputRequiredDisk, len(entries))
	for k, v := range entries {
		awaitingDisk[k] = inputRequiredDisk{
			IssueID:            v.IssueID,
			Identifier:         v.Identifier,
			SessionID:          v.SessionID,
			Context:            v.Context,
			BranchName:         v.BranchName,
			Backend:            v.Backend,
			Command:            v.Command,
			WorkerHost:         v.WorkerHost,
			ProfileName:        v.ProfileName,
			QuestionCommentKey: v.QuestionCommentKey,
			QuestionCommentID:  v.QuestionCommentID,
			QuestionAuthorID:   v.QuestionAuthorID,
			QuestionAuthorName: v.QuestionAuthorName,
			QueuedAt:           v.QueuedAt.Format(time.RFC3339),
			LastReplyCheckAt:   formatOptionalTime(v.LastReplyCheckAt),
		}
	}
	pendingDisk := make(map[string]pendingInputResumeDisk, len(pending))
	for k, v := range pending {
		pendingDisk[k] = pendingInputResumeDisk{
			IssueID:            v.IssueID,
			Identifier:         v.Identifier,
			SessionID:          v.SessionID,
			Context:            v.Context,
			UserMessage:        v.UserMessage,
			BranchName:         v.BranchName,
			Backend:            v.Backend,
			Command:            v.Command,
			WorkerHost:         v.WorkerHost,
			ProfileName:        v.ProfileName,
			QuestionCommentKey: v.QuestionCommentKey,
			QuestionCommentID:  v.QuestionCommentID,
			QuestionAuthorID:   v.QuestionAuthorID,
			QuestionAuthorName: v.QuestionAuthorName,
			QueuedAt:           v.QueuedAt.Format(time.RFC3339),
		}
	}
	data, err := json.Marshal(inputRequiredStateDisk{
		Awaiting:      awaitingDisk,
		PendingResume: pendingDisk,
	})
	if err != nil {
		slog.Warn("orchestrator: failed to marshal input-required entries", "error", err)
		return
	}
	o.persistLedger(ledgerInputRequired, path, data, 0o644)
}

// loadInputRequiredFromDisk reads the input-required file and pre-populates
// state.InputRequiredIssues and state.PendingInputResumes.
func (o *Orchestrator) loadInputRequiredFromDisk(state State) State {
	o.inputRequiredMu.RLock()
	path := o.inputRequiredFile
	o.inputRequiredMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load input-required file", "path", path, "error", err)
		}
		return state
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("orchestrator: failed to parse input-required file", "path", path, "error", err)
		return state
	}

	var awaiting map[string]inputRequiredDisk
	var pending map[string]pendingInputResumeDisk
	if _, ok := raw["awaiting"]; ok || raw["pending_resume"] != nil {
		var disk inputRequiredStateDisk
		if err := json.Unmarshal(data, &disk); err != nil {
			slog.Warn("orchestrator: failed to parse input-required state file", "path", path, "error", err)
			return state
		}
		awaiting = disk.Awaiting
		pending = disk.PendingResume
	} else {
		if err := json.Unmarshal(data, &awaiting); err != nil {
			slog.Warn("orchestrator: failed to parse legacy input-required file", "path", path, "error", err)
			return state
		}
	}

	for k, v := range awaiting {
		queuedAt, _ := time.Parse(time.RFC3339, v.QueuedAt)
		state.InputRequiredIssues[k] = &InputRequiredEntry{
			IssueID:            v.IssueID,
			Identifier:         v.Identifier,
			SessionID:          v.SessionID,
			Context:            v.Context,
			BranchName:         v.BranchName,
			Backend:            v.Backend,
			Command:            v.Command,
			WorkerHost:         v.WorkerHost,
			ProfileName:        v.ProfileName,
			QuestionCommentKey: v.QuestionCommentKey,
			QuestionCommentID:  v.QuestionCommentID,
			QuestionAuthorID:   v.QuestionAuthorID,
			QuestionAuthorName: v.QuestionAuthorName,
			QueuedAt:           queuedAt,
			LastReplyCheckAt:   parseOptionalTime(v.LastReplyCheckAt),
		}
	}
	for k, v := range pending {
		queuedAt, _ := time.Parse(time.RFC3339, v.QueuedAt)
		state.PendingInputResumes[k] = &PendingInputResumeEntry{
			IssueID:            v.IssueID,
			Identifier:         v.Identifier,
			SessionID:          v.SessionID,
			Context:            v.Context,
			UserMessage:        v.UserMessage,
			BranchName:         v.BranchName,
			Backend:            v.Backend,
			Command:            v.Command,
			WorkerHost:         v.WorkerHost,
			ProfileName:        v.ProfileName,
			QuestionCommentKey: v.QuestionCommentKey,
			QuestionCommentID:  v.QuestionCommentID,
			QuestionAuthorID:   v.QuestionAuthorID,
			QuestionAuthorName: v.QuestionAuthorName,
			QueuedAt:           queuedAt,
		}
	}
	// gaps_11 G-2 — mirror loadPausedFromDisk: treat persistence-restored
	// identifiers as "active before daemon start" so the absent-issue
	// janitor's two-tick grace window spans the restart boundary instead of
	// pruning restored entries after a single observed absence.
	for k, v := range awaiting {
		state.PrevActiveIdentifiers[inputRequiredPreloadIdent(v.Identifier, k)] = struct{}{}
	}
	for k, v := range pending {
		state.PrevActiveIdentifiers[inputRequiredPreloadIdent(v.Identifier, k)] = struct{}{}
	}
	slog.Info("orchestrator: loaded input-required entries", "path", path, "awaiting", len(awaiting), "pending_resume", len(pending))
	return state
}

// inputRequiredPreloadIdent picks the identifier to seed into
// PrevActiveIdentifiers for a persistence-restored entry: the entry's own
// Identifier when present, else the map key (legacy shapes keyed by
// identifier without carrying the field). gaps_11 G-2.
func inputRequiredPreloadIdent(identifier, key string) string {
	if identifier != "" {
		return identifier
	}
	return key
}

// SetAutomationQueueFile sets the path for persisting automation queue entries.
// Must be called before Run.
func (o *Orchestrator) SetAutomationQueueFile(path string) {
	if o.started.Load() {
		slog.Error("orchestrator: SetAutomationQueueFile called after Run started; ignoring", "path", path)
		return
	}
	o.automationQueueMu.Lock()
	o.automationQueueFile = path
	o.automationQueueMu.Unlock()
}

func (o *Orchestrator) saveAutomationQueueToDisk(entries map[string]*AutomationQueueEntry, order []string, backpressure AutomationQueueBackpressure, dependencyAudit map[string]*DependencyAuditEntry, dependencyTransitionSeq int64) {
	o.automationQueueMu.RLock()
	path := o.automationQueueFile
	o.automationQueueMu.RUnlock()
	if path == "" {
		return
	}
	disk := automationQueueStateDisk{
		Entries:                 copyAutomationQueueMap(entries),
		Order:                   append([]string(nil), order...),
		Backpressure:            backpressure,
		DependencyAudit:         copyDependencyAuditMap(dependencyAudit),
		DependencyTransitionSeq: dependencyTransitionSeq,
	}
	payload, err := json.Marshal(disk)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal automation queue", "error", err)
		return
	}
	// todolist4 A.2 — wrap the payload in the versioned envelope so future
	// readers can detect schema drift, corruption, or daemon-instance
	// mismatch instead of silently consuming stale state.
	data, err := EncodeQueueEnvelope(payload, o.daemonInstanceID)
	if err != nil {
		slog.Warn("orchestrator: failed to envelope automation queue", "error", err)
		return
	}
	o.persistLedger(ledgerAutomationQueue, path, data, 0o600)
}

func (o *Orchestrator) loadAutomationQueueFromDisk(state State) State {
	o.automationQueueMu.RLock()
	path := o.automationQueueFile
	o.automationQueueMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load automation queue file", "path", path, "error", err)
		}
		return state
	}
	// todolist4 A.2 — try the v2 envelope first; fall back to v1 raw payload
	// for backward compatibility with files written before the envelope wrap.
	payload := data
	if IsQueueEnvelopeShape(data) {
		envelope, reason, decodeErr := DecodeQueueEnvelope(data, o.daemonInstanceID)
		if decodeErr == nil {
			payload = envelope
			if reason != "" {
				slog.Warn("orchestrator: queue envelope warning", "path", path, "reason", reason)
			}
		} else if errors.Is(decodeErr, ErrQueueEnvelopeQuarantined) {
			quarantinePath := o.writeQuarantineCopy(path, data) // CORE-173 c
			slog.Warn("orchestrator: automation queue envelope quarantined", "path", path, "quarantine", quarantinePath, "error", decodeErr)
			return state
		}
	}
	var disk automationQueueStateDisk
	if err := json.Unmarshal(payload, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse automation queue file", "path", path, "error", err)
		return state
	}
	ensureAutomationQueueState(&state)
	state.AutomationQueue = make(map[string]*AutomationQueueEntry, len(disk.Entries))
	state.AutomationQueueOrder = nil
	seen := make(map[string]struct{}, len(disk.Entries))
	for key, entry := range disk.Entries {
		if entry == nil {
			slog.Warn("orchestrator: dropping malformed automation queue entry", "path", path, "id", key, "reason", "nil_entry")
			continue
		}
		if entry.ID == "" {
			entry.ID = key
		}
		if entry.ID == "" || entry.AutomationID == "" || entry.TriggerType == "" {
			slog.Warn("orchestrator: dropping malformed automation queue entry", "path", path, "id", key, "reason", "missing_required_fields")
			continue
		}
		cp := *entry
		cp.Issue = copyDomainIssue(entry.Issue)
		state.AutomationQueue[cp.ID] = &cp
		// gaps_11 G-2 — same persistence-replay protection as the paused and
		// input-required loaders: restored queue identifiers count as active
		// before daemon start so the absent-issue janitor grants them the
		// full two-tick grace window after restart.
		if cp.Issue.Identifier != "" {
			state.PrevActiveIdentifiers[cp.Issue.Identifier] = struct{}{}
		}
	}
	for _, id := range disk.Order {
		if _, ok := state.AutomationQueue[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		state.AutomationQueueOrder = append(state.AutomationQueueOrder, id)
		seen[id] = struct{}{}
	}
	missingOrder := make([]string, 0)
	for id := range state.AutomationQueue {
		if _, ok := seen[id]; !ok {
			missingOrder = append(missingOrder, id)
		}
	}
	slices.Sort(missingOrder)
	state.AutomationQueueOrder = append(state.AutomationQueueOrder, missingOrder...)
	currentMaxLength := state.AutomationQueueBackpressure.MaxLength
	state.AutomationQueueBackpressure = disk.Backpressure
	state.AutomationQueueBackpressure.MaxLength = currentMaxLength
	refreshAutomationQueueBackpressure(&state)

	// AUTO-4 — restore the dependency-audit ledger and its transition seq so a
	// blocker that reached terminal while the daemon was down still fires
	// blockers_resolved after restart. Rows are deep-copied on load (the disk
	// entries are *DependencyAuditEntry pointers) so the restored map aliases
	// nothing. Old payloads simply lack these keys → nil ledger + zero seq,
	// which is today's behavior.
	if len(disk.DependencyAudit) > 0 {
		state.DependencyAudit = copyDependencyAuditMapForRestore(disk.DependencyAudit)
	}
	state.DependencyTransitionSeq = disk.DependencyTransitionSeq
	// AUTO-4 aggravator — pendingBlockersResolvedStates (consulted by
	// reconcileDependencyRefresh) treats DependencyTransitionSeq ==
	// LastBlockersResolvedAuditSeq as "nothing pending" and skips the batch
	// (both are 0 after a fresh restart), which would skip the one scan
	// needed to notice blockers that closed while the daemon was down. Seed
	// the watermark one behind the restored seq so the next pass runs
	// exactly once, then re-converges (the apply handler sets
	// LastBlockersResolvedAuditSeq = the launch-time seq it was given).
	if len(state.DependencyAudit) > 0 {
		state.LastBlockersResolvedAuditSeq = state.DependencyTransitionSeq - 1
	}

	slog.Info("orchestrator: loaded automation queue", "path", path, "entries", len(state.AutomationQueue), "dependency_audit", len(state.DependencyAudit))
	return state
}

// copyRunningMap returns a deep copy of a map[string]*RunEntry.
// Each RunEntry value is copied by value so that external goroutines reading
// the snapshot cannot observe in-progress mutations by the event loop
// (TurnCount, TotalTokens, LastMessage, etc.). WorkerCancel is intentionally
// omitted from the copy — snapshot readers must never cancel a live worker.
func copyRunningMap(m map[string]*RunEntry) map[string]*RunEntry {
	cp := make(map[string]*RunEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		e := *v              // copy struct value
		e.WorkerCancel = nil // not safe to share across goroutines
		e.run = nil          // the worker's handle stays with the live entry
		e.Issue = copyDomainIssue(v.Issue)
		cp[k] = &e
	}
	return cp
}

// copyRetryMap returns a copy of a map[string]*RetryEntry with each entry
// copied by value (RetryEntry.Error is an immutable *string, shared).
func copyRetryMap(m map[string]*RetryEntry) map[string]*RetryEntry {
	cp := make(map[string]*RetryEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		e := *v
		cp[k] = &e
	}
	return cp
}

func copyAutomationQueueMap(m map[string]*AutomationQueueEntry) map[string]*AutomationQueueEntry {
	cp := make(map[string]*AutomationQueueEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		entry := *v
		entry.Issue = copyDomainIssue(v.Issue)
		entry.Trigger.ResolvedBlockers = copyBlockerRefs(v.Trigger.ResolvedBlockers)
		entry.Trigger.PreviouslyBlockedBy = copyBlockerRefs(v.Trigger.PreviouslyBlockedBy)
		cp[k] = &entry
	}
	return cp
}

func copyDependencyAuditMap(m map[string]*DependencyAuditEntry) map[string]*DependencyAuditEntry {
	cp := make(map[string]*DependencyAuditEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		entry := *v
		entry.Sources = append([]DependencyAuditSource(nil), v.Sources...)
		entry.BlockedBy = copyBlockerRefs(v.BlockedBy)
		entry.UnresolvedBlockers = copyBlockerRefs(v.UnresolvedBlockers)
		entry.ResolvedBlockers = copyBlockerRefs(v.ResolvedBlockers)
		cp[k] = &entry
	}
	return cp
}

// copyDependencyAuditMapForRestore deep-copies the persisted dependency-audit
// ledger and clears the transient in-flight latch. Used only on the envelope
// restore path — a daemon that crashed mid-refresh must not come back up with
// rows marked in-flight, because nothing would ever clear them.
func copyDependencyAuditMapForRestore(m map[string]*DependencyAuditEntry) map[string]*DependencyAuditEntry {
	cp := copyDependencyAuditMap(m)
	for _, entry := range cp {
		if entry == nil {
			continue
		}
		entry.InFlight = false
	}
	return cp
}

func copyIssueStatusHistoryMap(m map[string][]IssueStatusChange) map[string][]IssueStatusChange {
	cp := make(map[string][]IssueStatusChange, len(m))
	for k, v := range m {
		cp[k] = append([]IssueStatusChange(nil), v...)
	}
	return cp
}

// copyInferredDepsMap deep-copies State.InferredDeps. Entries are plain
// values (InferredDepEntry has no pointer/map fields), so copying the map and
// each per-target slice is sufficient — mirrors copyIssueStatusHistoryMap.
func copyInferredDepsMap(m map[string][]InferredDepEntry) map[string][]InferredDepEntry {
	cp := make(map[string][]InferredDepEntry, len(m))
	for k, v := range m {
		cp[k] = append([]InferredDepEntry(nil), v...)
	}
	return cp
}

// copyDependencyCycles deep-copies State.DependencyCycles. Each
// DependencyCycle's Members slice is independently copied so a mutation on
// the live event-loop slice cannot leak into an already-published snapshot.
// A nil input returns an empty, non-nil slice — matching the map-copy
// siblings above (copyRunningMap, copyAutomationQueueMap, ...), which all
// `make(..., len(m))` regardless of whether the source was nil, so JSON
// marshaling of a snapshot with no cycles emits `[]` rather than `null`.
func copyDependencyCycles(cycles []DependencyCycle) []DependencyCycle {
	cp := make([]DependencyCycle, len(cycles))
	for i, c := range cycles {
		cp[i] = c
		cp[i].Members = append([]string(nil), c.Members...)
	}
	return cp
}

// copyDependencyAttention deep-copies State.DependencyAttention. Each
// entry's Blockers slice is independently copied, mirroring
// copyDependencyCycles — including the nil-in/empty-out behavior.
func copyDependencyAttention(entries []DependencyAttentionEntry) []DependencyAttentionEntry {
	cp := make([]DependencyAttentionEntry, len(entries))
	for i, e := range entries {
		cp[i] = e
		cp[i].Blockers = append([]string(nil), e.Blockers...)
	}
	return cp
}

func copyDomainIssue(issue domain.Issue) domain.Issue {
	cp := issue
	cp.Labels = append([]string(nil), issue.Labels...)
	cp.BlockedBy = copyBlockerRefs(issue.BlockedBy)
	cp.Comments = append([]domain.Comment(nil), issue.Comments...)
	return cp
}

func copyBlockerRefs(refs []domain.BlockerRef) []domain.BlockerRef {
	return append([]domain.BlockerRef(nil), refs...)
}

// SetAutoSwitchedFile sets the path for persisting auto-switched profile/backend
// overrides across restarts. Gap §5.3. Must be called before Run.
func (o *Orchestrator) SetAutoSwitchedFile(path string) {
	o.autoSwitchedMu.Lock()
	o.autoSwitchedFile = path
	o.autoSwitchedMu.Unlock()
}

// quarantineLedgerFile preserves an unreadable ledger file (corrupt, or a
// version this daemon does not know) as <path>.quarantine before the
// daemon's next save overwrites it, the way the automation-queue loader
// does (M3-close BH-M3-7/V3). The daemon then starts from an empty ledger
// for that file; the quarantined copy is for the operator (or a newer
// daemon) to recover.
func (o *Orchestrator) quarantineLedgerFile(path string, data []byte, reason error) {
	quarantinePath := o.writeQuarantineCopy(path, data)
	slog.Warn("orchestrator: unreadable ledger file quarantined; starting empty",
		"path", path, "quarantine", quarantinePath, "error", reason)
}

// maxQuarantineCopies bounds how many quarantine copies of one state file
// are kept (CORE-173 c); the oldest beyond it are removed.
const maxQuarantineCopies = 5

// writeQuarantineCopy saves data as <path>.quarantine.<UTC timestamp> — a
// new file per quarantine, so a second bad file no longer overwrites the
// first (CORE-173 c) — and prunes the copies of path down to the newest
// maxQuarantineCopies. The timestamp sorts lexicographically. Returns the
// copy's path ("" when it could not be written; the failure is logged).
func (o *Orchestrator) writeQuarantineCopy(path string, data []byte) string {
	base := path + ".quarantine." + time.Now().UTC().Format("20060102T150405.000000000Z")
	quarantinePath := base
	for i := 1; ; i++ { // same-nanosecond collision: never overwrite
		if _, err := os.Lstat(quarantinePath); os.IsNotExist(err) {
			break
		}
		quarantinePath = fmt.Sprintf("%s-%d", base, i)
	}
	if err := o.writeLedgerFile(quarantinePath, data, 0o600); err != nil {
		slog.Warn("orchestrator: failed to write quarantine file", "path", quarantinePath, "error", err)
		return ""
	}
	copies, err := filepath.Glob(escapeGlob(path) + ".quarantine.*")
	if err != nil {
		return quarantinePath
	}
	slices.Sort(copies)
	for _, old := range copies[:max(0, len(copies)-maxQuarantineCopies)] {
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to prune quarantine copy", "path", old, "error", err)
		}
	}
	return quarantinePath
}

// escapeGlob escapes filepath.Match metacharacters in a literal path.
func escapeGlob(p string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(p)
}

// autoSwitchedRecord is the wire shape persisted to autoSwitchedFile.
// Profile is required (always set when AutoResume fires); Backend is
// optional (only set when the rule's SwitchToBackend was non-empty).
type autoSwitchedRecord struct {
	Profile    string     `json:"profile"`
	Backend    string     `json:"backend,omitempty"`
	SwitchedAt *time.Time `json:"switched_at,omitempty"`
	// Provenance (CORE-055), additive and optional: a file written before
	// these existed loads with an empty source, rendered as "unknown".
	Source      string `json:"source,omitempty"`
	FromBackend string `json:"from_backend,omitempty"`
	FromProfile string `json:"from_profile,omitempty"`
	Reason      string `json:"reason,omitempty"`
	FromKey     string `json:"from_key,omitempty"`
}

// autoSwitchedFileVersion is the current auto_switched.json envelope
// version (CORE-052). Version 1 was a flat map[identifier]autoSwitchedRecord.
const autoSwitchedFileVersion = 2

// autoSwitchedEnvelope is the v2 wire shape of auto_switched.json. The v1
// file was a flat map whose loader treated EVERY top-level key as an issue,
// so new top-level keys could not be added to it without becoming phantom
// issue records; the envelope nests the overrides instead.
type autoSwitchedEnvelope struct {
	Version         int                           `json:"version"`
	Overrides       map[string]autoSwitchedRecord `json:"overrides"`
	History         map[string][]time.Time        `json:"switch_history"`
	Cooldowns       map[string]time.Time          `json:"cooldowns"`
	CapCommentUntil map[string]time.Time          `json:"cap_comment_until"`
}

// decodeAutoSwitchedFile accepts the v2 envelope and the legacy v1 flat map
// (no "version" key), which is migrated in memory; the next save rewrites
// the file in the v2 shape.
func decodeAutoSwitchedFile(data []byte) (autoSwitchedEnvelope, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return autoSwitchedEnvelope{}, err
	}
	if v, ok := probe["version"]; ok {
		var env autoSwitchedEnvelope
		if err := json.Unmarshal(v, &env.Version); err == nil {
			// M3-close V3: only the version this daemon writes is read; a
			// future (or bogus) version is rejected, not read as v2.
			if env.Version != autoSwitchedFileVersion {
				return autoSwitchedEnvelope{}, fmt.Errorf("orchestrator: unsupported auto_switched.json version %d (want %d)", env.Version, autoSwitchedFileVersion)
			}
			if err := json.Unmarshal(data, &env); err != nil {
				return autoSwitchedEnvelope{}, err
			}
			return env, nil
		}
	}
	var legacy map[string]autoSwitchedRecord
	if err := json.Unmarshal(data, &legacy); err != nil {
		return autoSwitchedEnvelope{}, err
	}
	return autoSwitchedEnvelope{Version: 1, Overrides: legacy}, nil
}

// loadAutoSwitchedFromDisk reads the auto-switched file and pre-populates
// state.IssueProfiles, state.IssueBackends, state.AutoSwitchedIdentifiers,
// state.AutoSwitchedAt and the switch bookkeeping (CORE-052). Called once at
// startup, before the event loop's first select. Errors are logged and
// swallowed; a missing or malformed file should not block daemon startup.
func (o *Orchestrator) loadAutoSwitchedFromDisk(state State) State {
	o.autoSwitchedMu.RLock()
	path := o.autoSwitchedFile
	o.autoSwitchedMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load auto-switched file", "path", path, "error", err)
		}
		return state
	}
	env, err := decodeAutoSwitchedFile(data)
	if err != nil {
		// BH-M3-7: keep the unreadable file before the next save overwrites it.
		o.quarantineLedgerFile(path, data, err)
		return state
	}
	o.applyAutoSwitchedEnvelope(&state, env, time.Now())
	slog.Info("orchestrator: loaded auto-switched overrides", "path", path,
		"count", len(env.Overrides), "file_version", env.Version,
		"switch_history", len(state.SwitchHistory), "cooldowns", len(state.RateLimitCooldowns))
	return state
}

// applyAutoSwitchedEnvelope merges a decoded file into state, pruning
// switch stamps older than the switch window and expired cooldown and
// cap-comment entries (they can no longer affect a decision).
func (o *Orchestrator) applyAutoSwitchedEnvelope(state *State, env autoSwitchedEnvelope, now time.Time) {
	if state.IssueProfiles == nil {
		state.IssueProfiles = make(map[string]string)
	}
	if state.IssueBackends == nil {
		state.IssueBackends = make(map[string]string)
	}
	if state.AutoSwitchedIdentifiers == nil {
		state.AutoSwitchedIdentifiers = make(map[string]struct{})
	}
	if state.AutoSwitchedAt == nil {
		state.AutoSwitchedAt = make(map[string]time.Time)
	}
	for id, rec := range env.Overrides {
		state.IssueProfiles[id] = rec.Profile
		if rec.Backend != "" {
			state.IssueBackends[id] = rec.Backend
		}
		state.AutoSwitchedIdentifiers[id] = struct{}{}
		if rec.SwitchedAt != nil && !rec.SwitchedAt.IsZero() {
			state.AutoSwitchedAt[id] = *rec.SwitchedAt
		}
		if rec.Source != "" {
			if state.AutoSwitchInfo == nil {
				state.AutoSwitchInfo = make(map[string]AutoSwitchRecord)
			}
			info := AutoSwitchRecord{
				Source: rec.Source, FromBackend: rec.FromBackend, FromProfile: rec.FromProfile,
				ToBackend: rec.Backend, ToProfile: rec.Profile, Reason: rec.Reason, FromKey: rec.FromKey,
			}
			if rec.SwitchedAt != nil {
				info.SwitchedAt = *rec.SwitchedAt
			}
			state.AutoSwitchInfo[id] = info
		}
	}
	windowStart := now.Add(-o.rateLimitSwitchWindowDuration())
	for issueID, stamps := range env.History {
		var kept []time.Time
		for _, t := range stamps {
			if !t.Before(windowStart) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			continue
		}
		if state.SwitchHistory == nil {
			state.SwitchHistory = make(map[string][]time.Time)
		}
		state.SwitchHistory[issueID] = kept
	}
	for key, until := range env.Cooldowns {
		if !until.After(now) {
			continue
		}
		if state.RateLimitCooldowns == nil {
			state.RateLimitCooldowns = make(map[string]time.Time)
		}
		state.RateLimitCooldowns[key] = until
	}
	for issueID, until := range env.CapCommentUntil {
		if !until.After(now) {
			continue
		}
		if state.RateLimitCapCommentUntil == nil {
			state.RateLimitCapCommentUntil = make(map[string]time.Time)
		}
		state.RateLimitCapCommentUntil[issueID] = until
	}
}

// saveAutoSwitchedToDisk writes the auto-switched overrides and the switch
// bookkeeping to disk as the v2 envelope. Called from the event loop after
// any mutation to either (auto-switch fire, clear-on-success, revert, a
// recorded switch / cooldown / cap-comment claim, a pruning tick). It
// marshals from state on the calling (event-loop) goroutine and hands only
// bytes to the ordered, dirty-checked ledger writer (CORE-038), so an
// unchanged file costs no write.
func (o *Orchestrator) saveAutoSwitchedToDisk(state *State) {
	o.autoSwitchedMu.RLock()
	path := o.autoSwitchedFile
	o.autoSwitchedMu.RUnlock()
	if path == "" || state == nil {
		return
	}
	env := autoSwitchedEnvelope{
		Version:         autoSwitchedFileVersion,
		Overrides:       make(map[string]autoSwitchedRecord, len(state.AutoSwitchedIdentifiers)),
		History:         make(map[string][]time.Time, len(state.SwitchHistory)),
		Cooldowns:       make(map[string]time.Time, len(state.RateLimitCooldowns)),
		CapCommentUntil: make(map[string]time.Time, len(state.RateLimitCapCommentUntil)),
	}
	for id := range state.AutoSwitchedIdentifiers {
		rec := autoSwitchedRecord{Profile: state.IssueProfiles[id]}
		if b, ok := state.IssueBackends[id]; ok {
			rec.Backend = b
		}
		if t, ok := state.AutoSwitchedAt[id]; ok && !t.IsZero() {
			t = t.UTC()
			rec.SwitchedAt = &t
		}
		if info, ok := state.AutoSwitchInfo[id]; ok {
			rec.Source, rec.FromBackend, rec.FromProfile = info.Source, info.FromBackend, info.FromProfile
			rec.Reason, rec.FromKey = info.Reason, info.FromKey
		}
		env.Overrides[id] = rec
	}
	for id, stamps := range state.SwitchHistory {
		utc := make([]time.Time, len(stamps))
		for i, t := range stamps {
			utc[i] = t.UTC()
		}
		env.History[id] = utc
	}
	for k, t := range state.RateLimitCooldowns {
		env.Cooldowns[k] = t.UTC()
	}
	for k, t := range state.RateLimitCapCommentUntil {
		env.CapCommentUntil[k] = t.UTC()
	}
	data, err := json.Marshal(env)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal auto-switched overrides", "error", err)
		return
	}
	o.persistLedger(ledgerAutoSwitched, path, data, 0o644)
}

func pauseReasonsPath(pausedFile string) string {
	if pausedFile == "" {
		return ""
	}
	return pausedFile + ".reasons.json"
}

// savePauseReasonsToDisk persists why each identifier is paused. Failure is
// logged, not fatal: a lost reason degrades diagnostics, never correctness —
// the pause itself lives in the paused file.
func (o *Orchestrator) savePauseReasonsToDisk(reasons map[string]string) {
	o.pausedMu.RLock()
	path := pauseReasonsPath(o.pausedFile)
	o.pausedMu.RUnlock()
	if path == "" {
		return
	}
	// storeSnap runs on every event-loop turn, and each save is an atomic
	// write (temp + fsync + rename). Almost every daemon has no paused issues
	// at all, so skip the write entirely when there is nothing to record AND
	// no stale file to clear — otherwise this costs a file write per turn to
	// persist an empty map.
	//
	// The "no stale file" half is load-bearing: when the last pause is
	// cleared the map goes empty and the file MUST be rewritten, or a
	// restart would resurrect reasons for issues that are no longer paused.
	//
	// CORE-038: the stat only matters before this process has submitted any
	// reasons. Once it has, the ledger's dirty check decides — and must: an
	// earlier non-empty version may still be pending (not yet on disk), so
	// "no file yet" no longer implies "nothing stale to clear".
	if len(reasons) == 0 && !o.ledger(ledgerPauseReasons).hasSubmitted() {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return
		}
	}
	data, err := json.Marshal(reasons)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal pause reasons", "error", err)
		return
	}
	o.persistLedger(ledgerPauseReasons, path, data, 0o644)
}

// loadPauseReasonsFromDisk restores pause reasons, dropping any whose
// identifier is no longer paused so the map cannot outlive its pauses.
func (o *Orchestrator) loadPauseReasonsFromDisk(state State) State {
	o.pausedMu.RLock()
	path := pauseReasonsPath(o.pausedFile)
	o.pausedMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path) //nolint:gosec // daemon-controlled path
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load pause reasons file", "path", path, "error", err)
		}
		return state
	}
	var reasons map[string]string
	if err := json.Unmarshal(data, &reasons); err != nil {
		slog.Warn("orchestrator: failed to parse pause reasons file, continuing without reasons",
			"path", path, "error", err)
		return state
	}
	if state.PauseReasons == nil {
		state.PauseReasons = make(map[string]string, len(reasons))
	}
	for ident, reason := range reasons {
		if _, stillPaused := state.PausedIdentifiers[ident]; stillPaused {
			state.PauseReasons[ident] = reason
		}
	}
	return state
}

func (o *Orchestrator) savePausedToDisk(paused map[string]string) {
	o.pausedMu.RLock()
	path := o.pausedFile
	o.pausedMu.RUnlock()
	if path == "" {
		return
	}
	data, err := json.Marshal(paused)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal paused identifiers", "error", err)
		return
	}
	o.persistLedger(ledgerPaused, path, data, 0o644)
}

// RunHistory returns a snapshot of recently completed runs (newest last).
func (o *Orchestrator) RunHistory() []CompletedRun {
	o.historyMu.RLock()
	defer o.historyMu.RUnlock()
	result := make([]CompletedRun, len(o.completedRuns))
	copy(result, o.completedRuns)
	return result
}

func (o *Orchestrator) storeSnap(s State) {
	// Deep-copy every map field so that lastSnap contains independent copies.
	// The event loop mutates state.* maps without holding snapMu (they are its
	// private data). External goroutines read lastSnap.* under snapMu. Sharing
	// the same underlying maps would be a data race; separate copies prevent it.
	snap := s.Clone()

	o.snapMu.Lock()
	o.lastSnap = snap
	o.snapMu.Unlock()

	o.savePausedToDisk(snap.PausedIdentifiers)
	// Reasons persist alongside the pauses they explain, from the SAME place.
	// Persisting only at the individual pause sites missed the one that
	// matters most: the transition-failed reason is set in the worker-exit
	// handler, which has no savePaused call of its own, so the reason was
	// recorded in memory and lost on restart — leaving a recoverable pause
	// indistinguishable from a user cancel again, which is the whole point of
	// #42-F. Persisting here covers every pause site by construction.
	o.savePauseReasonsToDisk(snap.PauseReasons)
	o.saveInputRequiredToDisk(snap.InputRequiredIssues, snap.PendingInputResumes)
	o.saveAutomationQueueToDisk(snap.AutomationQueue, snap.AutomationQueueOrder, snap.AutomationQueueBackpressure, snap.DependencyAudit, snap.DependencyTransitionSeq)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// copyInputRequiredMap deep-copies the input-required queue.
//
// maps.Clone is a SHALLOW clone: on a map of pointers it duplicates the map
// but shares every *InputRequiredEntry with the event loop. That was
// harmless while the loop only ever inserted and deleted whole entries, and
// became a data race the moment checkTrackerReplies started stamping
// LastReplyCheckAt in place on the entry it had just selected — a race
// reproduced against Snapshot's clone. Copying the struct value keeps
// CLAUDE.md's rule that a snapshot handed to HTTP handler goroutines shares
// nothing mutable with the loop.
func copyInputRequiredMap(m map[string]*InputRequiredEntry) map[string]*InputRequiredEntry {
	cp := make(map[string]*InputRequiredEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		e := *v // copy struct value; InputRequiredEntry has no reference fields
		cp[k] = &e
	}
	return cp
}

// copyPendingInputResumeMap deep-copies the pending-resume queue, for the
// same reason as copyInputRequiredMap. No field on it is mutated in place
// today, but it is the same pointer-map shape reached by the same snapshot
// path, and the next in-place write would be the same silent race.
func copyPendingInputResumeMap(m map[string]*PendingInputResumeEntry) map[string]*PendingInputResumeEntry {
	cp := make(map[string]*PendingInputResumeEntry, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		e := *v
		cp[k] = &e
	}
	return cp
}

// formatOptionalTime renders t for disk, mapping the zero value to "" so a
// never-set timestamp round-trips as never-set rather than as year 1.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// parseOptionalTime is formatOptionalTime's inverse. An empty or unparseable
// value yields the zero time, which sorts first — a never-checked entry gets
// priority, the safe direction for a fairness key.
func parseOptionalTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
