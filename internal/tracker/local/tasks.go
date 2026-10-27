package local

import (
	"context"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// Itervox-only tasks (#251). A project whose tracker is Linear or GitHub can
// also hold local tasks: issue files in the local tracker directory
// (.itervox/issues/) that Itervox dispatches like tracker issues but that
// never reach the tracker. WithTasks joins the two behind one
// tracker.Tracker, so the orchestrator, the dashboard and the API see one
// board and need no change: list reads return both, and every read or write
// by issue ID goes to the side that owns the ID.
//
// Ownership is decided by the ID's shape alone, never by a lookup: a task's
// ID is its identifier (TASK-1, …), which neither a Linear ID (a UUID) nor
// a GitHub ID (a number) can look like, so a read for a task file that was
// just deleted still goes to the local tracker (which reports it missing)
// and never sends a malformed ID to the tracker.

// TaskPrefix is the identifier prefix of Itervox-only tasks in a project
// whose tracker is Linear or GitHub (TASK-1, TASK-2, …).
const TaskPrefix = "TASK"

// TaskCreator is implemented by the trackers that can create an
// Itervox-only local task: the local tracker itself and the WithTasks join.
type TaskCreator interface {
	CreateLocalTask(ctx context.Context, title, body, stateName string) (*domain.Issue, error)
	// OwnsLocalTask reports whether issueID names a local task.
	OwnsLocalTask(issueID string) bool
}

var _ TaskCreator = (*Tracker)(nil)

// CreateLocalTask creates an issue file: on a local tracker every issue is
// local.
func (t *Tracker) CreateLocalTask(ctx context.Context, title, body, stateName string) (*domain.Issue, error) {
	return t.CreateIssue(ctx, "", title, body, stateName)
}

// OwnsLocalTask reports whether issueID can name an issue file.
func (t *Tracker) OwnsLocalTask(issueID string) bool { return identifierRe.MatchString(issueID) }

// Tasks joins a tracker with the local tasks of its project. Build it with
// WithTasks.
type Tasks struct {
	main  tracker.Tracker
	tasks *Tracker
}

// remoteBase is what every Linear and GitHub adapter, and the tracker store
// over either, implements; WithTasks forwards it only when main does.
type remoteBase interface {
	tracker.Tracker
	tracker.RateLimiter
	tracker.IdempotentCommenter
	tracker.FilterManager
}

// linearExtras are the optional interfaces only Linear (and the store over
// it) implements.
type linearExtras interface {
	tracker.DetailBatcher
	tracker.ProjectManager
	tracker.ScopeManager
}

// storeExtras are the tracker store's own methods its callers reach by type
// assertion: the outbox overlay, the claim check's copy and Forget, and the
// adapter behind it.
type storeExtras interface {
	Upstream() tracker.Tracker
	SetPendingStates(fn func() map[string]string)
	Forget(id string)
	TrackerCopy(id string) (domain.Issue, bool)
}

// WithTasks returns main joined with the local tasks in tasks. The result
// answers a type assertion for every optional interface main implements and
// that callers above it use (rate limits, idempotent comments, the filter
// and scope, project and batched detail reads on Linear, the store's
// overlay and claim-check methods), so wrapping a tracker changes nothing
// for its own issues. It also implements TaskCreator and
// tracker.StateListSetter. tasks should use TaskPrefix.
func WithTasks(main tracker.Tracker, tasks *Tracker) tracker.Tracker {
	t := &Tasks{main: main, tasks: tasks}
	rb, remote := main.(remoteBase)
	if !remote {
		return t
	}
	r := &remoteTasks{Tasks: t, rb: rb}
	lx, linear := main.(linearExtras)
	sx, stored := main.(storeExtras)
	switch {
	case linear && stored:
		return &linearStoredTasks{linearTasks: &linearTasks{remoteTasks: r, lx: lx}, storeForward: storeForward{t: t, sx: sx}}
	case linear:
		return &linearTasks{remoteTasks: r, lx: lx}
	case stored:
		return &storedTasks{remoteTasks: r, storeForward: storeForward{t: t, sx: sx}}
	}
	return r
}

// TasksTracker is the local tracker holding the tasks (its directory is
// watched for hand edits).
func (t *Tasks) TasksTracker() *Tracker { return t.tasks }

// Main is the joined tracker.
func (t *Tasks) Main() tracker.Tracker { return t.main }

// OwnsLocalTask reports whether issueID names a local task: an identifier
// with the tasks' prefix.
func (t *Tasks) OwnsLocalTask(issueID string) bool {
	if !identifierRe.MatchString(issueID) {
		return false
	}
	p, _ := splitIdent(issueID)
	return strings.EqualFold(p, t.tasks.prefix)
}

// CreateLocalTask writes a new task file.
func (t *Tasks) CreateLocalTask(ctx context.Context, title, body, stateName string) (*domain.Issue, error) {
	return t.tasks.CreateIssue(ctx, "", title, body, stateName)
}

// split partitions ids into the tracker's and the tasks'.
func (t *Tasks) split(ids []string) (mainIDs, taskIDs []string) {
	for _, id := range ids {
		if t.OwnsLocalTask(id) {
			taskIDs = append(taskIDs, id)
		} else {
			mainIDs = append(mainIDs, id)
		}
	}
	return mainIDs, taskIDs
}

// join reads both sides; either failing fails the read, since a caller
// treats an issue missing from a list as gone.
func join(a []domain.Issue, aErr error, b func() ([]domain.Issue, error)) ([]domain.Issue, error) {
	if aErr != nil {
		return nil, aErr
	}
	more, err := b()
	if err != nil {
		return nil, err
	}
	if len(more) == 0 {
		return a, nil
	}
	return append(slices.Clone(a), more...), nil
}

// FetchCandidateIssues returns the tracker's candidates, then the tasks in
// an active state.
func (t *Tasks) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	issues, err := t.main.FetchCandidateIssues(ctx)
	return join(issues, err, func() ([]domain.Issue, error) { return t.tasks.FetchCandidateIssues(ctx) })
}

// FetchIssuesByStates returns both sides' issues in stateNames.
func (t *Tasks) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	issues, err := t.main.FetchIssuesByStates(ctx, stateNames)
	return join(issues, err, func() ([]domain.Issue, error) { return t.tasks.FetchIssuesByStates(ctx, stateNames) })
}

// FetchIssueStatesByIDs reads each ID from the side that owns it.
func (t *Tasks) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	mainIDs, taskIDs := t.split(issueIDs)
	var issues []domain.Issue
	var err error
	if len(mainIDs) > 0 || len(taskIDs) == 0 {
		issues, err = t.main.FetchIssueStatesByIDs(ctx, mainIDs)
	}
	return join(issues, err, func() ([]domain.Issue, error) {
		if len(taskIDs) == 0 {
			return nil, nil
		}
		return t.tasks.FetchIssueStatesByIDs(ctx, taskIDs)
	})
}

// side is the tracker that owns issueID.
func (t *Tasks) side(issueID string) tracker.Tracker {
	if t.OwnsLocalTask(issueID) {
		return t.tasks
	}
	return t.main
}

// CreateComment comments on the side that owns issueID.
func (t *Tasks) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	return t.side(issueID).CreateComment(ctx, issueID, body)
}

// CreateIssue creates a follow-up issue next to sourceIssueID: a task's
// follow-up is another task (the tracker could not resolve a task as its
// context), anything else goes to the tracker.
func (t *Tasks) CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error) {
	if t.OwnsLocalTask(sourceIssueID) {
		return t.tasks.CreateIssue(ctx, "", title, body, stateName)
	}
	return t.main.CreateIssue(ctx, sourceIssueID, title, body, stateName)
}

// UpdateIssueState moves the issue on the side that owns it.
func (t *Tasks) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	return t.side(issueID).UpdateIssueState(ctx, issueID, stateName)
}

// FetchIssueDetail reads the issue from the side that owns it.
func (t *Tasks) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	return t.side(issueID).FetchIssueDetail(ctx, issueID)
}

// FetchIssueByIdentifier reads the issue from the side that owns it (a
// task's ID is its identifier).
func (t *Tasks) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	return t.side(identifier).FetchIssueByIdentifier(ctx, identifier)
}

// SetIssueBranch records the branch on the side that owns the issue.
func (t *Tasks) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	return t.side(issueID).SetIssueBranch(ctx, issueID, branchName)
}

// SetStateLists applies the dashboard's state lists to the tasks, and to
// the tracker when it takes them.
func (t *Tasks) SetStateLists(active, terminal []string) {
	t.tasks.SetStateLists(active, terminal)
	if s, ok := t.main.(tracker.StateListSetter); ok {
		s.SetStateLists(active, terminal)
	}
}
