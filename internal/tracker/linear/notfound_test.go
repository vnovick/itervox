package linear_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// TestFetchIssueDetailEntityNotFoundGraphQLErrorIsNotFound covers the shape
// Linear actually returns for an issue ID that does not exist.
//
// A deleted-but-known issue yields `data.issue = null`, but an ID Linear has
// never seen — a stale audit row, a seeded/demo identifier, an issue moved to
// another workspace — comes back as a GraphQL ERROR instead:
//
//	Entity not found: Issue  (code INPUT_ERROR, statusCode 400)
//
// That was wrapped as a generic GraphQLError, so errors.Is(err,
// tracker.ErrNotFound) was false and the dependency-audit refresh routed the
// row to FailedKeys (retain and retry) instead of MissingKeys (delete). The
// row could never succeed, so it burned one Linear request per refresh cycle
// forever — the exact budget drain issue #42 is about, on rows that will
// never resolve.
func TestFetchIssueDetailEntityNotFoundGraphQLErrorIsNotFound(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{
		{"errors": []interface{}{
			map[string]interface{}{
				"message": "Entity not found: Issue",
				"path":    []interface{}{"issue"},
				"extensions": map[string]interface{}{
					"code": "INPUT_ERROR", "type": "invalid input", "statusCode": 400,
					"userPresentableMessage": "Could not find referenced Issue.",
				},
			},
		}},
	})
	defer srv.Close()

	c := linear.NewClient(linear.ClientConfig{Endpoint: srv.URL, APIKey: "k"})
	_, err := c.FetchIssueDetail(context.Background(), "demo-id-5")

	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("errors.Is(err, tracker.ErrNotFound) = false for %v — "+
			"the dependency audit will retry this row every cycle forever", err)
	}
}

// TestGraphQLErrorsOtherThanNotFoundStayTransient keeps the mapping narrow.
// A rate-limit or auth failure IS transient and must stay retryable —
// classifying it as not-found would delete a live audit row on a blip.
func TestGraphQLErrorsOtherThanNotFoundStayTransient(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{
		{"errors": []interface{}{
			map[string]interface{}{"message": "Rate limit exceeded"},
		}},
	})
	defer srv.Close()

	c := linear.NewClient(linear.ClientConfig{Endpoint: srv.URL, APIKey: "k"})
	_, err := c.FetchIssueDetail(context.Background(), "id-1")

	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("a transient GraphQL error must not be treated as not-found: %v", err)
	}
}

// TestFetchIssueStatesByIDsSkipsMalformedIDs counts REQUESTS, which is the
// only thing that proves the filter runs.
//
// The previous version of this test declared a `sawRequest` flag, never set
// it, and discarded it — its stub returned an empty node list, so "a request
// was issued" and "no request was issued" satisfied it identically. Deleting
// the entire filter wiring from FetchIssueStatesByIDs left the package green.
//
// Live failure this guards: Linear's batched `issues(filter: {id: {in: […]}})`
// validates the whole list and rejects the request if any element is not a
// UUID or TEAM-123 identifier, so one stale row took down every healthy row
// beside it — observed as a batch of 11 failing because 10 ids were malformed.
func TestFetchIssueStatesByIDsSkipsMalformedIDs(t *testing.T) {
	var requests int32
	var lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{
			"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
		}}})
	}))
	defer srv.Close()
	c := linear.NewClient(linear.ClientConfig{Endpoint: srv.URL, APIKey: "k"})

	t.Run("all invalid issues no request at all", func(t *testing.T) {
		atomic.StoreInt32(&requests, 0)
		issues, err := c.FetchIssueStatesByIDs(context.Background(), []string{"demo-id-1", "demo-id-2"})
		if err != nil {
			t.Fatalf("malformed ids must be skipped, not error: %v", err)
		}
		if len(issues) != 0 {
			t.Fatalf("expected no issues, got %d", len(issues))
		}
		if n := atomic.LoadInt32(&requests); n != 0 {
			t.Fatalf("issued %d request(s) for an all-malformed batch; a request Linear is "+
				"guaranteed to reject must not be sent at all", n)
		}
	})

	t.Run("mixed list queries only the valid ids", func(t *testing.T) {
		atomic.StoreInt32(&requests, 0)
		_, err := c.FetchIssueStatesByIDs(context.Background(), []string{
			"demo-id-1", "4e65c1a1-42ad-44dc-938f-5591ea50f744", "ENG-7", "demo-id-2",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := atomic.LoadInt32(&requests); n != 1 {
			t.Fatalf("expected exactly 1 request, got %d", n)
		}
		body, _ := lastBody.Load().(string)
		for _, want := range []string{"4e65c1a1-42ad-44dc-938f-5591ea50f744", "ENG-7"} {
			if !strings.Contains(body, want) {
				t.Fatalf("valid id %q was dropped from the query — filtering must not lose healthy rows: %s", want, body)
			}
		}
		for _, bad := range []string{"demo-id-1", "demo-id-2"} {
			if strings.Contains(body, bad) {
				t.Fatalf("malformed id %q reached the query; it would reject the whole batch: %s", bad, body)
			}
		}
	})
}
