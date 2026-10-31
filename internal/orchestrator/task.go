package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// New task (#251): a one-off task created from the dashboard, the Mac app or
// `itervox task`, either as an Itervox-only local issue file or as an issue
// in the project's tracker. Its custom instructions are kept here, keyed by
// identifier, and become a `## Task instructions` block right after the
// playbook in the prompt of every worker run of the issue; the block is a
// prompt-digest component, so run history names the instructions that
// produced a run. They can be edited until the task first starts.
//
// The specs are not State: they are written by HTTP goroutines (create,
// edit) and read once per run by startRun, all under taskBook.mu, like the
// per-issue profile overrides. Creating a task never bypasses the event
// loop: the issue is created in the first active state and the normal tick
// dispatches it (Start only asks for that tick now), so slots, guardrails,
// budgets and dependencies apply as to any other issue. While a create is in
// flight, a tick skips an issue with the same title, so a run can never start
// before the task's instructions are recorded.

// MaxTaskInstructionsBytes bounds a task's custom instructions.
const MaxTaskInstructionsBytes = 16 * 1024

// maxTaskSpecs bounds the task book; the oldest started tasks go first.
const maxTaskSpecs = 500

// taskInstructionsHeading starts a task's prompt block.
const taskInstructionsHeading = "## Task instructions"

// Errors of CreateTask and SetTaskInstructions.
var (
	ErrTaskTitleRequired      = errors.New("orchestrator: a task needs a title")
	ErrTaskInstructionsTooBig = fmt.Errorf("orchestrator: task instructions are longer than %d bytes", MaxTaskInstructionsBytes)
	ErrLocalTasksUnavailable  = errors.New("orchestrator: this tracker cannot hold local tasks")
	ErrTaskNoActiveState      = errors.New("orchestrator: no active state to create the task in")
	ErrTaskNotFound           = errors.New("orchestrator: no task with that identifier")
	ErrTaskStarted            = errors.New("orchestrator: the task has started; its instructions can no longer change")
)

// TaskSpec is what Itervox keeps about a task.
type TaskSpec struct {
	Instructions string `json:"instructions,omitempty"`
	// BaseBranch is the branch the task's worktree starts from and its pull
	// request targets; "" for workspace.base_branch.
	BaseBranch string `json:"baseBranch,omitempty"`
	// Playbook is the playbook chosen at creation, used until an operator
	// override replaces it.
	Playbook string `json:"playbook,omitempty"`
	// Local is set for an Itervox-only task (an issue file).
	Local     bool       `json:"local,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// Block is the task's prompt block, "" when it has no instructions.
func (s TaskSpec) Block() string {
	text := strings.TrimSpace(s.Instructions)
	if text == "" {
		return ""
	}
	return taskInstructionsHeading + "\n\n" + text
}

// TaskRequest is a task to create.
type TaskRequest struct {
	Title        string
	Instructions string
	Profile      string
	Backend      string
	Playbook     string
	BaseBranch   string
	// OnTracker creates the issue in the project's tracker instead of as a
	// local task.
	OnTracker bool
	// Start asks for a dispatch tick at once.
	Start bool
}

// TaskResult is a created task.
type TaskResult struct {
	Issue domain.Issue
	Local bool
	// StartRequested is set when a dispatch tick was requested.
	StartRequested bool
}

// taskBook holds the task specs and the creates in flight.
type taskBook struct {
	mu       sync.Mutex
	specs    map[string]TaskSpec
	creating map[string]int // title → creates in flight
	file     string
}

// localTaskCreator is what a tracker that can hold local tasks implements
// (internal/tracker/local.TaskCreator).
type localTaskCreator interface {
	CreateLocalTask(ctx context.Context, title, body, stateName string) (*domain.Issue, error)
	OwnsLocalTask(issueID string) bool
}

// SetTaskSpecsFile sets where the task specs persist across restarts
// (.itervox/task_specs.json) and loads it. Before Run. A missing file is no
// tasks; a corrupt one is logged and ignored.
func (o *Orchestrator) SetTaskSpecsFile(path string) {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	b.file = path
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load task specs", "path", path, "error", err)
		}
		return
	}
	var disk map[string]TaskSpec
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse task specs", "path", path, "error", err)
		return
	}
	if b.specs == nil {
		b.specs = make(map[string]TaskSpec, len(disk))
	}
	for id, s := range disk {
		if id != "" && len(s.Instructions) <= MaxTaskInstructionsBytes {
			b.specs[id] = s
		}
	}
}

// saveLocked hands the specs to the ledger writer. Caller holds b.mu, so
// submissions reach the writer in the order the book changed.
func (o *Orchestrator) saveTasksLocked() {
	b := &o.tasks
	if b.file == "" {
		return
	}
	data, err := json.Marshal(b.specs)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal task specs", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.file), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create task specs dir", "path", b.file, "error", err)
		return
	}
	o.persistLedger(ledgerTaskSpecs, b.file, data, 0o644)
}

// TaskSpec returns the task kept for identifier.
func (o *Orchestrator) TaskSpec(identifier string) (TaskSpec, bool) {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.specs[identifier]
	return s, ok
}

// TaskSpecs returns a copy of every task kept (snapshot rows).
func (o *Orchestrator) TaskSpecs() map[string]TaskSpec {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.specs)
}

// SetTaskInstructions replaces a task's instructions until it first starts:
// ErrTaskNotFound for an identifier that is not a task, ErrTaskStarted once
// a run of it has started. Any goroutine.
func (o *Orchestrator) SetTaskInstructions(identifier, text string) error {
	text = strings.TrimSpace(text)
	if len(text) > MaxTaskInstructionsBytes {
		return ErrTaskInstructionsTooBig
	}
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.specs[identifier]
	switch {
	case !ok:
		return ErrTaskNotFound
	case s.StartedAt != nil:
		return ErrTaskStarted
	}
	s.Instructions = text
	b.specs[identifier] = s
	o.saveTasksLocked()
	slog.Info("orchestrator: task instructions updated", "identifier", identifier, "bytes", len(text))
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
	return nil
}

// takeTaskForRun returns identifier's task and marks it started, which
// freezes its instructions. Event loop (startRun).
func (o *Orchestrator) takeTaskForRun(identifier string, now time.Time) (TaskSpec, bool) {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.specs[identifier]
	if !ok {
		return TaskSpec{}, false
	}
	if s.StartedAt == nil {
		at := now.UTC()
		s.StartedAt = &at
		b.specs[identifier] = s
		o.saveTasksLocked()
	}
	return s, true
}

// taskCreationPending reports whether a task titled title is being created:
// the tick holds such an issue until its spec is recorded. Event loop.
func (o *Orchestrator) taskCreationPending(title string) bool {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.creating[strings.TrimSpace(title)] > 0
}

// beginTaskCreate marks a create of title in flight; the returned func ends
// it.
func (o *Orchestrator) beginTaskCreate(title string) func() {
	b := &o.tasks
	b.mu.Lock()
	if b.creating == nil {
		b.creating = map[string]int{}
	}
	b.creating[title]++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.creating[title]--; b.creating[title] <= 0 {
			delete(b.creating, title)
		}
		b.mu.Unlock()
	}
}

// recordTask stores a created task's spec, evicting the oldest started
// tasks beyond maxTaskSpecs.
func (o *Orchestrator) recordTask(identifier string, spec TaskSpec) {
	b := &o.tasks
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.specs == nil {
		b.specs = map[string]TaskSpec{}
	}
	b.specs[identifier] = spec
	if over := len(b.specs) - maxTaskSpecs; over > 0 {
		var started []string
		for id, s := range b.specs {
			if s.StartedAt != nil {
				started = append(started, id)
			}
		}
		slices.SortFunc(started, func(a, c string) int { return b.specs[a].CreatedAt.Compare(b.specs[c].CreatedAt) })
		for _, id := range started[:min(over, len(started))] {
			delete(b.specs, id)
		}
	}
	o.saveTasksLocked()
}

// firstActiveState is where a new task is created. cfgMu field.
func (o *Orchestrator) firstActiveState() string {
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	for _, s := range o.cfg.Tracker.ActiveStates {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// trackerSourceID is a tracker issue a new tracker issue can take its
// context (Linear's team) from: a running one, else a candidate; never a
// local task.
func (o *Orchestrator) trackerSourceID(ctx context.Context, owns func(string) bool) string {
	o.snapMu.RLock()
	for _, e := range o.lastSnap.Running {
		if e != nil && e.Issue.ID != "" && !owns(e.Issue.ID) {
			id := e.Issue.ID
			o.snapMu.RUnlock()
			return id
		}
	}
	o.snapMu.RUnlock()
	issues, err := o.tracker.FetchCandidateIssues(ctx)
	if err != nil {
		return ""
	}
	for _, is := range issues {
		if !owns(is.ID) {
			return is.ID
		}
	}
	return ""
}

// taskIssueBody is the description of a task created in the tracker; the
// instructions stay with Itervox.
const taskIssueBody = "Created with Itervox (New task). Its instructions are kept by Itervox and shown in the issue's detail on the dashboard."

// CreateTask creates a task (#251) and records its spec: a local task when
// the tracker can hold one (the local tracker, or Linear/GitHub joined with
// the project's task files), else ErrLocalTasksUnavailable; with OnTracker,
// an issue in the tracker. The issue is created in the first active state;
// the profile and backend become the issue's overrides and the playbook its
// override. Start requests a dispatch tick at once; either way the normal
// dispatch path starts it. Runs on the caller's goroutine (the tracker
// write is a network call for a tracker issue); never on the event loop.
func (o *Orchestrator) CreateTask(ctx context.Context, req TaskRequest) (TaskResult, error) {
	title := strings.TrimSpace(req.Title)
	instructions := strings.TrimSpace(req.Instructions)
	switch {
	case title == "":
		return TaskResult{}, ErrTaskTitleRequired
	case len(instructions) > MaxTaskInstructionsBytes:
		return TaskResult{}, ErrTaskInstructionsTooBig
	}
	state := o.firstActiveState()
	if state == "" {
		return TaskResult{}, ErrTaskNoActiveState
	}
	lc, canLocal := o.tracker.(localTaskCreator)
	if !req.OnTracker && !canLocal {
		return TaskResult{}, ErrLocalTasksUnavailable
	}
	end := o.beginTaskCreate(title)
	var created *domain.Issue
	var err error
	if req.OnTracker {
		owns := func(string) bool { return false }
		if canLocal {
			owns = lc.OwnsLocalTask
		}
		created, err = o.tracker.CreateIssue(ctx, o.trackerSourceID(ctx, owns), tracker.NewIssue{Title: title, Body: taskIssueBody, State: state})
	} else {
		created, err = lc.CreateLocalTask(ctx, title, "", state)
	}
	if err == nil && (created == nil || created.Identifier == "") {
		err = errors.New("orchestrator: the tracker did not return the created issue")
	}
	if err != nil {
		end()
		return TaskResult{}, fmt.Errorf("orchestrator: create task: %w", err)
	}
	local := canLocal && lc.OwnsLocalTask(created.ID)
	o.recordTask(created.Identifier, TaskSpec{Instructions: instructions, BaseBranch: strings.TrimSpace(req.BaseBranch),
		Playbook: strings.TrimSpace(req.Playbook), Local: local, CreatedAt: time.Now().UTC()})
	if p := strings.TrimSpace(req.Profile); p != "" {
		o.SetIssueProfile(created.Identifier, p)
	}
	if be := strings.TrimSpace(req.Backend); be != "" {
		o.SetIssueBackend(created.Identifier, be)
	}
	end()
	if pb := strings.TrimSpace(req.Playbook); pb != "" {
		// The dashboard's override, so the card shows it; dispatch reads the
		// spec's playbook until the event lands.
		o.SetPlaybookOverride(created.Identifier, pb)
	}
	slog.Info("orchestrator: task created", "identifier", created.Identifier, "local", local,
		"start", req.Start, "instructions_bytes", len(instructions))
	if req.Start {
		o.Refresh()
	}
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
	return TaskResult{Issue: *created, Local: local, StartRequested: req.Start}, nil
}

// resolveRunTask returns the task spec of the run startRun is starting and
// its prompt block, marking the task started. Like the playbook, the block
// is given to worker runs only: a reviewer or automation run keeps the
// prompt it had. Event loop only.
func (o *Orchestrator) resolveRunTask(entry *RunEntry) (TaskSpec, string) {
	if o == nil || entry == nil {
		return TaskSpec{}, ""
	}
	spec, ok := o.takeTaskForRun(entry.Issue.Identifier, time.Now())
	if !ok {
		return TaskSpec{}, ""
	}
	reviewerProfile := false
	if o.cfg != nil {
		o.cfgMu.RLock()
		reviewerProfile = isReviewerProfile(o.cfg, entry.ProfileName)
		o.cfgMu.RUnlock()
	}
	if !playbookApplies(entry.Kind, reviewerProfile, entry.AutomationID != "") {
		return spec, ""
	}
	return spec, spec.Block()
}

// runTask returns the task spec and prompt block a worker's run was
// dispatched with; none for a worker started without a run.
func runTask(ctx context.Context) (TaskSpec, string) {
	if h := runFrom(ctx); h != nil {
		return h.task, h.taskBlock
	}
	return TaskSpec{}, ""
}

// LocalTasksAvailable reports whether the tracker can hold local tasks.
func (o *Orchestrator) LocalTasksAvailable() bool {
	_, ok := o.tracker.(localTaskCreator)
	return ok
}
