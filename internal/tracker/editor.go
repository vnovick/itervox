package tracker

import (
	"context"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
)

// IssueDraft is a new issue for IssueEditor.CreateScopedIssue (#118).
//
// Priority uses the adapter's own read convention, the value its normalized
// issues carry in domain.Issue.Priority (Linear: 0 none, 1 urgent … 4 low;
// GitHub: the N of a pN label). Nil leaves it unset. BlockedBy holds the
// tracker IDs (not identifiers) of the issues that block the new one.
type IssueDraft struct {
	// Key is a client-generated UUIDv4. Adapters whose API accepts a
	// client-supplied issue id (Linear) send it, so a retried create either
	// creates the one issue or collides with it, and FindIssueByKey can tell
	// which happened. Adapters without that (GitHub) ignore it.
	Key         string
	Title       string
	Description string
	State       string
	Labels      []string
	Priority    *int
	BlockedBy   []string
}

// IssueFieldsPatch is a partial edit for IssueEditor.UpdateIssueFields
// (#118). A nil field is left unchanged; a set one replaces the whole value
// (Labels and BlockedBy are the complete new lists, not additions).
// Priority and BlockedBy follow IssueDraft's conventions; a Priority of -1
// clears it.
type IssueFieldsPatch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Labels      *[]string `json:"labels,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	BlockedBy   *[]string `json:"blocked_by,omitempty"`
}

// Empty reports whether the patch changes nothing.
func (p IssueFieldsPatch) Empty() bool {
	return p.Title == nil && p.Description == nil && p.Labels == nil && p.Priority == nil && p.BlockedBy == nil
}

// IssueEditor is an optional interface implemented by remote adapters that
// can create an issue in their configured scope (the Linear project, the
// GitHub repository) without a source issue, and edit an issue's title,
// description, labels, priority and blockers. The local issue sync (#118)
// needs it; callers type-assert Tracker to IssueEditor.
//
// Labels in a patch are the issue's own labels: an adapter that keeps state
// or priority in labels (GitHub) keeps those labels and replaces the rest.
type IssueEditor interface {
	CreateScopedIssue(ctx context.Context, draft IssueDraft) (*domain.Issue, error)
	UpdateIssueFields(ctx context.Context, issueID string, patch IssueFieldsPatch) error
	// FindIssueByKey looks up an issue created with draft.Key. It returns
	// (nil, false, nil) only when the issue definitely does not exist, and
	// an error when the answer is unknown. An adapter that cannot look a
	// key up returns (nil, false, nil) and documents the duplicate risk.
	FindIssueByKey(ctx context.Context, key string) (*domain.Issue, bool, error)
}

// syncBlockersMarker ends the body line an adapter that keeps blockers in
// the issue body (GitHub: "Blocked by #3, #4") writes for
// IssueFieldsPatch.BlockedBy. The line is the adapter's to rewrite; the rest
// of the body is the description.
const syncBlockersMarker = "<!-- itervox:blocked-by -->"

// SyncBlockersLine renders the managed blockers line for refs (like "#3"),
// or "" when there are none.
func SyncBlockersLine(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return "Blocked by " + strings.Join(refs, ", ") + " " + syncBlockersMarker
}

// SplitSyncBlockersLine separates a body into the description and the
// managed blockers line written by SyncBlockersLine ("" when absent).
func SplitSyncBlockersLine(body string) (desc, line string) {
	lines := strings.Split(body, "\n")
	kept := lines[:0:0]
	for _, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), syncBlockersMarker) {
			line = strings.TrimSpace(l)
			continue
		}
		kept = append(kept, l)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), line
}

// JoinSyncBlockersLine is SplitSyncBlockersLine's inverse: the description,
// then the managed line after a blank line.
func JoinSyncBlockersLine(desc, line string) string {
	desc = strings.TrimSpace(desc)
	switch {
	case line == "":
		return desc
	case desc == "":
		return line
	}
	return desc + "\n\n" + line
}
