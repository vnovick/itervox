package linear

import "testing"

// TestNormalizeIssue_TrashedFilteredOut — codex-B9. A Linear issue with
// trashed=true must be omitted from candidate poll results.
func TestNormalizeIssue_TrashedFilteredOut(t *testing.T) {
	raw := map[string]any{
		"id":         "x",
		"identifier": "ENG-1",
		"title":      "in the trash",
		"trashed":    true,
	}
	if issue := normalizeIssue(raw); issue != nil {
		t.Errorf("trashed issue should be filtered; got %+v", issue)
	}
}

func TestNormalizeIssue_TrashedFalsePassesThrough(t *testing.T) {
	raw := map[string]any{
		"id":         "x",
		"identifier": "ENG-1",
		"title":      "live",
		"trashed":    false,
	}
	if issue := normalizeIssue(raw); issue == nil {
		t.Error("non-trashed issue should not be filtered")
	}
}

func TestNormalizeIssue_TrashedMissingFieldPassesThrough(t *testing.T) {
	raw := map[string]any{
		"id":         "x",
		"identifier": "ENG-1",
		"title":      "live (legacy fixture)",
	}
	if issue := normalizeIssue(raw); issue == nil {
		t.Error("issue without trashed field should not be filtered (backward compat)")
	}
}

// QueryIssueDetail must SELECT the `trashed` field so
// a direct detail lookup of a trashed issue (e.g. a stale dashboard URL)
// is also filtered through the normalize step. Without this, the bulk poll
// drops trashed issues but a per-issue detail GET would still surface them.
func TestQueryIssueDetail_SelectsTrashed(t *testing.T) {
	if !contains(QueryIssueDetail, "trashed") {
		t.Errorf("QueryIssueDetail must select `trashed` so detail-path normalization can filter; query was:\n%s", QueryIssueDetail)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
