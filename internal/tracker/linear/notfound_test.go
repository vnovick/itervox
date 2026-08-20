package linear_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// TestFetchIssueDetailDeletedIssueIsNotFound pins that a deleted or
// inaccessible Linear issue surfaces as tracker.ErrNotFound.
//
// Linear's GraphQL returns `data.issue = null` for an issue that no longer
// exists. That fell into a generic fmt.Errorf, so errors.Is(err,
// tracker.ErrNotFound) could NEVER match on this backend — the sentinel was
// only ever produced by the GitHub adapter and the in-memory test tracker,
// despite NotFoundError's own doc naming "linear" as an adapter.
//
// The consequence is in the dependency-audit refresh
// (internal/orchestrator/dependency_refresh.go): a not-found result routes to
// MissingKeys, which retires the audit row, while anything else routes to
// FailedKeys and is retried. A deleted blocker therefore stayed a
// "transient failure" forever — burning a tracker request every refresh
// interval and never releasing the issues it blocked.
func TestFetchIssueDetailDeletedIssueIsNotFound(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{
		{"data": map[string]interface{}{"issue": nil}},
	})
	defer srv.Close()

	c := linear.NewClient(linear.ClientConfig{Endpoint: srv.URL, APIKey: "k"})
	issue, err := c.FetchIssueDetail(context.Background(), "deleted-id")

	if issue != nil {
		t.Fatalf("issue = %v, want nil", issue)
	}
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("errors.Is(err, tracker.ErrNotFound) = false for err %v — "+
			"the dependency audit will retry a deleted issue forever", err)
	}
	var nfe *tracker.NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("errors.As(&NotFoundError) = false for err %v", err)
	}
	if nfe.Adapter != "linear" {
		t.Errorf("adapter = %q, want %q", nfe.Adapter, "linear")
	}
	if nfe.Identifier != "deleted-id" {
		t.Errorf("identifier = %q, want %q", nfe.Identifier, "deleted-id")
	}
}

// TestFetchIssueDetailMalformedResponseIsNotNotFound keeps the fix narrow: a
// response with no `issue` key at all is a protocol error, not evidence the
// issue is gone. Classifying it as not-found would retire a live audit row.
func TestFetchIssueDetailMalformedResponseIsNotNotFound(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{
		{"data": map[string]interface{}{"somethingElse": 1}},
	})
	defer srv.Close()

	c := linear.NewClient(linear.ClientConfig{Endpoint: srv.URL, APIKey: "k"})
	_, err := c.FetchIssueDetail(context.Background(), "id-1")
	if err == nil {
		t.Fatal("expected an error for a malformed response")
	}
	if errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("a malformed response must not be classified not-found: %v", err)
	}
}
