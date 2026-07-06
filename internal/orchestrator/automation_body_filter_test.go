package orchestrator

import (
	"regexp"
	"testing"
)

func TestCommentBodyMatchesFilter_EmptyFiltersMatchAll(t *testing.T) {
	cases := []string{"", "anything", "AI review passed — ready for merge"}
	for _, body := range cases {
		if !commentBodyMatchesFilter(body, nil, nil) {
			t.Errorf("empty filter should match body %q", body)
		}
	}
}

func TestCommentBodyMatchesFilter_BodyContainsSingleSubstring(t *testing.T) {
	if !commentBodyMatchesFilter("AI review passed — ready for merge", []string{"ready for merge"}, nil) {
		t.Error("expected match for substring")
	}
	if commentBodyMatchesFilter("Reviewer's chat about something else", []string{"ready for merge"}, nil) {
		t.Error("expected no match")
	}
}

func TestCommentBodyMatchesFilter_BodyContainsOrOfList(t *testing.T) {
	if !commentBodyMatchesFilter("approve", []string{"merge it", "approve", "lgtm"}, nil) {
		t.Error("expected match (OR-of-list)")
	}
	if commentBodyMatchesFilter("rejected", []string{"merge it", "approve", "lgtm"}, nil) {
		t.Error("expected no match")
	}
}

func TestCommentBodyMatchesFilter_CaseInsensitive(t *testing.T) {
	if !commentBodyMatchesFilter("READY FOR MERGE", []string{"ready for merge"}, nil) {
		t.Error("expected case-insensitive match")
	}
}

func TestCommentBodyMatchesFilter_BodyRegex(t *testing.T) {
	rx := regexp.MustCompile(`(?i)merge.*PR-\d+`)
	if !commentBodyMatchesFilter("ok merge PR-42 please", nil, rx) {
		t.Error("expected regex match")
	}
	if commentBodyMatchesFilter("merge it", nil, rx) {
		t.Error("expected no regex match")
	}
}

func TestCommentBodyMatchesFilter_ContainsAndRegexBothRequired(t *testing.T) {
	rx := regexp.MustCompile(`(?i)PR-\d+`)
	if !commentBodyMatchesFilter("ready for merge PR-42", []string{"ready for merge"}, rx) {
		t.Error("expected both to match")
	}
	if commentBodyMatchesFilter("ready for merge", []string{"ready for merge"}, rx) {
		t.Error("regex absent → should not match")
	}
	if commentBodyMatchesFilter("merge PR-42 now", []string{"ready for merge"}, rx) {
		t.Error("contains absent → should not match")
	}
}
