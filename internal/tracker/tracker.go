package tracker

import (
	"context"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// RateLimitSnapshot holds the most recently observed API rate-limit counters.
// ComplexityLimit and ComplexityRemaining are only populated by the Linear
// adapter; GitHub uses only RequestsLimit/RequestsRemaining/Reset.
type RateLimitSnapshot struct {
	RequestsLimit       int
	RequestsRemaining   int
	Reset               *time.Time
	ComplexityLimit     int
	ComplexityRemaining int
}

// RateLimiter is an optional interface implemented by tracker adapters that
// expose API rate-limit counters. Callers should type-assert Tracker to
// RateLimiter rather than asserting the concrete adapter type (linear.Client
// or github.Client) directly, so new adapters can participate without
// requiring changes to the call site.
type RateLimiter interface {
	RateLimitSnapshot() *RateLimitSnapshot
}

// ProjectManager is an optional interface implemented by tracker adapters that
// support listing available projects and scoping fetches to a project subset at
// runtime. Callers should type-assert Tracker to ProjectManager rather than
// asserting the concrete adapter type, so new adapters can participate without
// requiring changes to the call site.
type ProjectManager interface {
	// FetchProjects returns the available projects for this tracker account.
	FetchProjects(ctx context.Context) ([]domain.Project, error)
	// GetProjectFilter returns the currently active project slugs filter.
	// An empty slice means "all projects".
	GetProjectFilter() []string
	// SetProjectFilter updates the runtime project slug filter.
	SetProjectFilter(slugs []string)
}

// Tracker is the interface all tracker adapters must implement.
type Tracker interface {
	// FetchCandidateIssues returns issues in active states for the configured project.
	FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error)

	// FetchIssuesByStates returns issues matching the given state names.
	// Empty stateNames returns empty slice without any API call.
	FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error)

	// FetchIssueStatesByIDs returns the current state snapshot for the given issue IDs.
	// Empty issueIDs returns empty slice without any API call.
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error)

	// CreateComment posts a comment on the given issue and returns the created
	// tracker comment when the adapter can provide it. Errors are logged and
	// non-fatal — callers should not abort a session on comment failure.
	CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error)

	// CreateIssue creates a follow-up issue in the same tracker/project context as
	// sourceIssueID and returns the created issue when the adapter can provide it.
	CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error)

	// UpdateIssueState transitions the issue to the named state.
	// For Linear this resolves the state name to an ID; for GitHub it manages labels.
	// Errors are logged and non-fatal.
	UpdateIssueState(ctx context.Context, issueID, stateName string) error

	// FetchIssueDetail returns a single issue with full details including comments.
	// Used before rendering the agent prompt.
	FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error)

	// FetchIssueByIdentifier returns a single issue by its human-readable identifier
	// (e.g. "ENG-42" for Linear, "#42" for GitHub). Used by the dashboard detail endpoint
	// to avoid fetching all issues just to serve one.
	FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error)

	// SetIssueBranch records the feature branch name on the tracker issue so
	// that retried workers can resume from the same branch instead of starting
	// over from the default branch. Errors are non-fatal — callers log and ignore.
	SetIssueBranch(ctx context.Context, issueID, branchName string) error
}
