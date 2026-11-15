package local

import (
	"context"
	"slices"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// The WithTasks variants (#251). Each implements exactly the optional
// interfaces its tracker does, so a type assertion on the join answers as
// it would on the tracker. Reads and writes by issue ID are routed; the
// tracker-wide methods (filter, scope, projects, rate limits) are the
// tracker's.

// remoteTasks joins a Linear or GitHub adapter (or the store over one).
type remoteTasks struct {
	*Tasks
	rb remoteBase
}

var (
	_ remoteBase              = (*remoteTasks)(nil)
	_ tracker.StateListSetter = (*remoteTasks)(nil)
	_ TaskCreator             = (*remoteTasks)(nil)
)

// RateLimitSnapshot implements tracker.RateLimiter: the tracker's.
func (r *remoteTasks) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	return r.rb.RateLimitSnapshot()
}

// CreateCommentWithKey implements tracker.IdempotentCommenter on the side
// that owns issueID.
func (r *remoteTasks) CreateCommentWithKey(ctx context.Context, issueID, key, body string) (*domain.Comment, error) {
	if r.OwnsLocalTask(issueID) {
		return r.tasks.CreateCommentWithKey(ctx, issueID, key, body)
	}
	return r.rb.CreateCommentWithKey(ctx, issueID, key, body)
}

// FindCommentByKey implements tracker.IdempotentCommenter on the side that
// owns issueID.
func (r *remoteTasks) FindCommentByKey(ctx context.Context, issueID, key string) (*domain.Comment, bool, error) {
	if r.OwnsLocalTask(issueID) {
		return r.tasks.FindCommentByKey(ctx, issueID, key)
	}
	return r.rb.FindCommentByKey(ctx, issueID, key)
}

// MatchesFilter implements tracker.ScopeMatcher: a task is always in scope
// (the tracker's filter does not apply to it).
func (r *remoteTasks) MatchesFilter(issue domain.Issue) (bool, bool) {
	if r.OwnsLocalTask(issue.ID) {
		return true, true
	}
	return r.rb.MatchesFilter(issue)
}

// Filter implements tracker.FilterManager.
func (r *remoteTasks) Filter() issuefilter.Filter { return r.rb.Filter() }

// FilterMode implements tracker.FilterManager.
func (r *remoteTasks) FilterMode() bool { return r.rb.FilterMode() }

// SetFilter implements tracker.FilterManager.
func (r *remoteTasks) SetFilter(f issuefilter.Filter) { r.rb.SetFilter(f) }

// FilterConditions implements tracker.FilterManager.
func (r *remoteTasks) FilterConditions() []string { return r.rb.FilterConditions() }

// ResolveFilter implements tracker.FilterManager.
func (r *remoteTasks) ResolveFilter(ctx context.Context, f issuefilter.Filter) error {
	return r.rb.ResolveFilter(ctx, f)
}

// PreviewFilter implements tracker.FilterManager.
func (r *remoteTasks) PreviewFilter(ctx context.Context, f issuefilter.Filter, limit int) (tracker.FilterPreview, error) {
	return r.rb.PreviewFilter(ctx, f, limit)
}

// FetchTeams implements tracker.FilterManager.
func (r *remoteTasks) FetchTeams(ctx context.Context) ([]tracker.Team, error) {
	return r.rb.FetchTeams(ctx)
}

// FetchLabels implements tracker.FilterManager and tracker.ScopeManager.
func (r *remoteTasks) FetchLabels(ctx context.Context) ([]string, error) {
	return r.rb.FetchLabels(ctx)
}

// FetchUsers implements tracker.FilterManager and tracker.ScopeManager.
func (r *remoteTasks) FetchUsers(ctx context.Context) ([]tracker.AssigneeUser, error) {
	return r.rb.FetchUsers(ctx)
}

// ScopeKey implements tracker.FilterManager and tracker.ScopeManager.
func (r *remoteTasks) ScopeKey() string { return r.rb.ScopeKey() }

// ScopeLabel implements tracker.FilterManager and tracker.ScopeManager.
func (r *remoteTasks) ScopeLabel() string { return r.rb.ScopeLabel() }

// linearTasks joins a Linear adapter (or the store over one): batched
// detail reads, projects and the assignee scope.
type linearTasks struct {
	*remoteTasks
	lx linearExtras
}

var _ linearExtras = (*linearTasks)(nil)

// FetchIssueDetailsByIDs implements tracker.DetailBatcher: one batch to the
// tracker for its IDs, the tasks read from their files.
func (l *linearTasks) FetchIssueDetailsByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	mainIDs, taskIDs := l.split(issueIDs)
	var issues []domain.Issue
	if len(mainIDs) > 0 {
		var err error
		if issues, err = l.lx.FetchIssueDetailsByIDs(ctx, mainIDs); err != nil {
			return nil, err
		}
	}
	if len(taskIDs) == 0 {
		return issues, nil
	}
	more, err := l.tasks.FetchIssueDetailsByIDs(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	return append(slices.Clone(issues), more...), nil
}

// FetchProjects implements tracker.ProjectManager.
func (l *linearTasks) FetchProjects(ctx context.Context) ([]domain.Project, error) {
	return l.lx.FetchProjects(ctx)
}

// GetProjectFilter implements tracker.ProjectManager.
func (l *linearTasks) GetProjectFilter() []string { return l.lx.GetProjectFilter() }

// SetProjectFilter implements tracker.ProjectManager.
func (l *linearTasks) SetProjectFilter(slugs []string) { l.lx.SetProjectFilter(slugs) }

// Scope implements tracker.ScopeManager.
func (l *linearTasks) Scope() tracker.Scope { return l.lx.Scope() }

// SetScope implements tracker.ScopeManager.
func (l *linearTasks) SetScope(sc tracker.Scope) { l.lx.SetScope(sc) }

// ResolveUser implements tracker.ScopeManager.
func (l *linearTasks) ResolveUser(ctx context.Context, spec string) (tracker.AssigneeUser, error) {
	return l.lx.ResolveUser(ctx, spec)
}

// ResolveAssignee implements tracker.ScopeManager.
func (l *linearTasks) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	return l.lx.ResolveAssignee(ctx)
}

// ResolvedAssignee implements tracker.ScopeManager.
func (l *linearTasks) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	return l.lx.ResolvedAssignee()
}

// storeForward forwards the tracker store's own methods; the per-issue ones
// are routed (a task is never in the store).
type storeForward struct {
	t  *Tasks
	sx storeExtras
}

// Upstream is the adapter behind the store.
func (s storeForward) Upstream() tracker.Tracker { return s.sx.Upstream() }

// SetPendingStates lays the outbox's queued state moves over the store's
// list reads.
func (s storeForward) SetPendingStates(fn func() map[string]string) { s.sx.SetPendingStates(fn) }

// Forget drops an issue from the store; a task is not in it.
func (s storeForward) Forget(id string) {
	if !s.t.OwnsLocalTask(id) {
		s.sx.Forget(id)
	}
}

// TrackerCopy is the store's copy of an issue; a task has none.
func (s storeForward) TrackerCopy(id string) (domain.Issue, bool) {
	if s.t.OwnsLocalTask(id) {
		return domain.Issue{}, false
	}
	return s.sx.TrackerCopy(id)
}

// storedTasks joins the store over a GitHub adapter.
type storedTasks struct {
	*remoteTasks
	storeForward
}

// linearStoredTasks joins the store over a Linear adapter.
type linearStoredTasks struct {
	*linearTasks
	storeForward
}

var (
	_ storeExtras  = (*storedTasks)(nil)
	_ storeExtras  = (*linearStoredTasks)(nil)
	_ linearExtras = (*linearStoredTasks)(nil)
	_ remoteBase   = (*linearStoredTasks)(nil)
)
