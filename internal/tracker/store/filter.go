package store

import (
	"context"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// filterForward forwards tracker.FilterManager (#198) to the adapter, which
// owns the filter. Another filter means other issues, so SetFilter makes
// the store pull again before the next read, like SetScope.
type filterForward struct{ st *Store }

func (f filterForward) fm() tracker.FilterManager { return f.st.up.(tracker.FilterManager) }

// Filter implements tracker.FilterManager.
func (f filterForward) Filter() issuefilter.Filter { return f.fm().Filter() }

// FilterMode implements tracker.FilterManager.
func (f filterForward) FilterMode() bool { return f.fm().FilterMode() }

// SetFilter implements tracker.FilterManager.
func (f filterForward) SetFilter(filter issuefilter.Filter) {
	f.fm().SetFilter(filter)
	f.st.invalidate(nil)
}

// FilterConditions implements tracker.FilterManager.
func (f filterForward) FilterConditions() []string { return f.fm().FilterConditions() }

// ResolveFilter implements tracker.FilterManager.
func (f filterForward) ResolveFilter(ctx context.Context, filter issuefilter.Filter) error {
	return f.fm().ResolveFilter(ctx, filter)
}

// MatchesFilter implements tracker.FilterManager.
func (f filterForward) MatchesFilter(issue domain.Issue) (bool, bool) {
	return f.fm().MatchesFilter(issue)
}

// PreviewFilter implements tracker.FilterManager: always the tracker (the
// store holds only the current scope's issues).
func (f filterForward) PreviewFilter(ctx context.Context, filter issuefilter.Filter, limit int) (tracker.FilterPreview, error) {
	return f.fm().PreviewFilter(ctx, filter, limit)
}

// FetchTeams implements tracker.FilterManager.
func (f filterForward) FetchTeams(ctx context.Context) ([]tracker.Team, error) {
	return f.fm().FetchTeams(ctx)
}

// FetchLabels implements tracker.FilterManager.
func (f filterForward) FetchLabels(ctx context.Context) ([]string, error) {
	return f.fm().FetchLabels(ctx)
}

// FetchUsers implements tracker.FilterManager.
func (f filterForward) FetchUsers(ctx context.Context) ([]tracker.AssigneeUser, error) {
	return f.fm().FetchUsers(ctx)
}

// ScopeKey implements tracker.FilterManager.
func (f filterForward) ScopeKey() string { return f.fm().ScopeKey() }

// ScopeLabel implements tracker.FilterManager.
func (f filterForward) ScopeLabel() string { return f.fm().ScopeLabel() }

// filteredStore is the store over an adapter with a tracker filter but no
// project picker (GitHub, #198).
type filteredStore struct {
	*Store
	filterForward
}

var (
	_ tracker.FilterManager = (*scopedStore)(nil)
	_ tracker.FilterManager = (*filteredStore)(nil)
)
