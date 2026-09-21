package tracker

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/domain"
)

// MemoryTracker is an in-memory Tracker implementation for tests and reconciliation.
type MemoryTracker struct {
	mu             sync.RWMutex
	issues         []domain.Issue
	activeStates   []string
	terminalStates []string
	injectedError  error
	nextCommentID  int
	nextIssueID    int

	// detailBatchCalls counts FetchIssueDetailsByIDs invocations so tests can
	// assert N issues cost one call, not N.
	detailBatchCalls int

	// detailCalls counts per-issue FetchIssueDetail invocations. Batching is
	// only proven when this reaches ZERO for a batched path — a batch that
	// happens alongside N per-issue calls has saved nothing.
	detailCalls int
}

// NewMemoryTracker constructs a MemoryTracker with the given issues and state config.
func NewMemoryTracker(issues []domain.Issue, activeStates, terminalStates []string) *MemoryTracker {
	cp := make([]domain.Issue, len(issues))
	copy(cp, issues)
	return &MemoryTracker{
		issues:         cp,
		activeStates:   activeStates,
		terminalStates: terminalStates,
		nextIssueID:    maxIssueSuffix(cp),
	}
}

// InjectError causes all subsequent calls to return the given error.
// Pass nil to clear.
func (m *MemoryTracker) InjectError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectedError = err
}

// SetIssueState updates the state of an issue by ID.
func (m *MemoryTracker) SetIssueState(id, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.issues {
		if m.issues[i].ID == id {
			m.issues[i].State = state
			return
		}
	}
}

// FetchCandidateIssues returns issues whose state (case-insensitive) is in activeStates.
func (m *MemoryTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.injectedError != nil {
		return nil, m.injectedError
	}
	var result []domain.Issue
	for _, issue := range m.issues {
		if m.isActive(issue.State) {
			result = append(result, issue)
		}
	}
	return result, nil
}

// FetchIssuesByStates returns issues whose state matches any of the given state names (case-insensitive).
func (m *MemoryTracker) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.injectedError != nil {
		return nil, m.injectedError
	}
	wantSet := make(map[string]bool, len(stateNames))
	for _, s := range stateNames {
		wantSet[strings.ToLower(s)] = true
	}
	var result []domain.Issue
	for _, issue := range m.issues {
		if wantSet[strings.ToLower(issue.State)] {
			result = append(result, issue)
		}
	}
	return result, nil
}

// FetchIssueDetailsByIDs returns issues matching the given IDs with full
// detail. MemoryTracker stores whole domain.Issue values, so comments are
// already present — it satisfies tracker.DetailBatcher so orchestrator tests
// can exercise the batched path rather than only the per-issue fallback.
//
// It also counts calls via detailBatchCalls so a test can assert that N issues
// cost ONE call, which is the entire point of the batching and the only thing
// that distinguishes it from a loop.
func (m *MemoryTracker) FetchIssueDetailsByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}
	m.mu.Lock()
	m.detailBatchCalls++
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.injectedError != nil {
		return nil, m.injectedError
	}
	idSet := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		idSet[id] = true
	}
	var result []domain.Issue
	for _, issue := range m.issues {
		if idSet[issue.ID] {
			result = append(result, issue)
		}
	}
	return result, nil
}

// DetailCalls reports how many times per-issue FetchIssueDetail was invoked.
func (m *MemoryTracker) DetailCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.detailCalls
}

// DetailBatchCalls reports how many times FetchIssueDetailsByIDs was invoked.
func (m *MemoryTracker) DetailBatchCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.detailBatchCalls
}

// FetchIssueStatesByIDs returns issues matching the given IDs.
func (m *MemoryTracker) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.injectedError != nil {
		return nil, m.injectedError
	}
	idSet := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		idSet[id] = true
	}
	var result []domain.Issue
	for _, issue := range m.issues {
		if idSet[issue.ID] {
			result = append(result, issue)
		}
	}
	return result, nil
}

// CreateComment fabricates a tracker comment for tests and persists it on the
// in-memory issue so local/demo comment-driven flows round-trip through
// FetchIssueDetail.
func (m *MemoryTracker) CreateComment(_ context.Context, issueID, body string) (*domain.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextCommentID++
	comment := &domain.Comment{
		ID:         "memory-comment-" + strconv.Itoa(m.nextCommentID),
		Body:       body,
		AuthorID:   "memory-tracker",
		AuthorName: "Itervox",
	}
	for i := range m.issues {
		if m.issues[i].ID != issueID {
			continue
		}
		m.issues[i].Comments = append(m.issues[i].Comments, *comment)
		break
	}
	return comment, nil
}

// CreateCommentWithKey implements IdempotentCommenter. The key is stored as
// a body marker, exactly as the GitHub adapter does, so tests exercise the
// same shape the real fallback path uses.
func (m *MemoryTracker) CreateCommentWithKey(ctx context.Context, issueID, key, body string) (*domain.Comment, error) {
	return m.CreateComment(ctx, issueID, MarkCommentKey(body, key))
}

// FindCommentByKey implements IdempotentCommenter.
func (m *MemoryTracker) FindCommentByKey(_ context.Context, issueID, key string) (*domain.Comment, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.issues {
		if m.issues[i].ID != issueID {
			continue
		}
		for j := range m.issues[i].Comments {
			if CommentHasKey(m.issues[i].Comments[j].Body, key) {
				found := m.issues[i].Comments[j]
				return &found, true, nil
			}
		}
	}
	return nil, false, nil
}

// CreateIssue creates a new in-memory issue for tests and local/demo flows.
func (m *MemoryTracker) CreateIssue(_ context.Context, _ string, title, body, stateName string) (*domain.Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextIssueID++
	id := "id-" + strconv.Itoa(m.nextIssueID)
	identifier := "ENG-" + strconv.Itoa(m.nextIssueID)
	var description *string
	if strings.TrimSpace(body) != "" {
		desc := body
		description = &desc
	}
	issue := domain.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       title,
		State:       stateName,
		Description: description,
	}
	m.issues = append(m.issues, issue)
	cp := issue
	return &cp, nil
}

// UpdateIssueState updates the in-memory state for testing.
func (m *MemoryTracker) UpdateIssueState(_ context.Context, issueID, stateName string) error {
	m.SetIssueState(issueID, stateName)
	return nil
}

// SetIssueBranch updates the BranchName field on the in-memory issue.
func (m *MemoryTracker) SetIssueBranch(_ context.Context, issueID, branchName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.issues {
		if m.issues[i].ID == issueID {
			b := branchName
			m.issues[i].BranchName = &b
			return nil
		}
	}
	return nil // issue not found — non-fatal
}

// FetchIssueDetail returns the issue from storage if it exists, else an error.
func (m *MemoryTracker) FetchIssueDetail(_ context.Context, issueID string) (*domain.Issue, error) {
	m.mu.Lock()
	m.detailCalls++
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, issue := range m.issues {
		if issue.ID == issueID {
			cp := deepCopyIssue(issue)
			return &cp, nil
		}
	}
	return nil, &NotFoundError{Adapter: "memory", Identifier: issueID}
}

// FetchIssueByIdentifier returns the issue matching the human-readable identifier.
func (m *MemoryTracker) FetchIssueByIdentifier(_ context.Context, identifier string) (*domain.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, issue := range m.issues {
		if issue.Identifier == identifier {
			cp := deepCopyIssue(issue)
			return &cp, nil
		}
	}
	return nil, &NotFoundError{Adapter: "memory", Identifier: identifier}
}

// deepCopyIssue clones an issue including its slice/map fields so callers
// cannot mutate the in-memory store by mutating the returned value.
//
// v0.2.0 audit P3-2 — the previous `cp := issue; return &cp` produced a
// shallow copy: the struct itself was new but Labels, BlockedBy, and
// Comments shared the same backing array. Test mutations could corrupt the
// store and create false-positive bug confirmations. Linear/GitHub adapters
// build issues from JSON so they are already safe; only the memory adapter
// needed this defence.
func deepCopyIssue(issue domain.Issue) domain.Issue {
	if len(issue.Labels) > 0 {
		issue.Labels = append([]string(nil), issue.Labels...)
	}
	if len(issue.BlockedBy) > 0 {
		issue.BlockedBy = append([]domain.BlockerRef(nil), issue.BlockedBy...)
	}
	if len(issue.Comments) > 0 {
		issue.Comments = append([]domain.Comment(nil), issue.Comments...)
	}
	return issue
}

func (m *MemoryTracker) isActive(state string) bool {
	lower := strings.ToLower(state)
	for _, s := range m.activeStates {
		if strings.ToLower(s) == lower {
			return true
		}
	}
	return false
}

func maxIssueSuffix(issues []domain.Issue) int {
	maxSuffix := 0
	for _, issue := range issues {
		maxSuffix = max(maxSuffix, issueNumericSuffix(issue.ID, "id-"))
		maxSuffix = max(maxSuffix, issueNumericSuffix(issue.Identifier, "ENG-"))
	}
	return maxSuffix
}

func issueNumericSuffix(value, prefix string) int {
	if !strings.HasPrefix(value, prefix) {
		return 0
	}
	suffix, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	if err != nil || suffix < 0 {
		return 0
	}
	return suffix
}

// Compile-time proof MemoryTracker satisfies DetailBatcher. Without this a
// signature drift would silently demote every test to the per-issue fallback,
// and the batching tests would still pass while asserting nothing.
var _ DetailBatcher = (*MemoryTracker)(nil)
