package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
)

// TestWithOperationIntent pins that withOperationIntent classifies a GraphQL
// operation document by its own text, not by the HTTP method (every Linear
// call is a POST, so the method says nothing). Only a document that starts
// with "mutation" after trimming counts as a write; everything else —
// including a query with leading whitespace, and the empty string — is
// treated as a read, matching the doc comment's "worst case is the 250ms read
// grace, never starvation" design.
func TestWithOperationIntent(t *testing.T) {
	mutationDoc := `
mutation ItervoxUpdateIssueState($issueId: String!, $stateId: String!) {
  issueUpdate(id: $issueId, input: { stateId: $stateId }) {
    success
  }
}`
	queryDoc := `
query ItervoxFindCommentByKey($key: ID!) {
  comments(filter: {id: {eq: $key}}, first: 1, includeArchived: true) {
    nodes { id }
  }
}`
	queryWithLeadingWhitespace := "   \n\t query ItervoxListProjects { projects(first: 1) { nodes { id } } }"

	cases := []struct {
		name string
		doc  string
	}{
		{"query document", queryDoc},
		{"query with leading whitespace", queryWithLeadingWhitespace},
		{"empty string", ""},
	}

	// The mutation is the only document that must be classified as a write.
	mutCtx := withOperationIntent(context.Background(), mutationDoc)
	assert.True(t, tracker.HasWriteIntent(mutCtx), "a mutation document must carry write intent")
	assert.False(t, tracker.HasReadIntent(mutCtx), "a mutation document must not also carry read intent")

	mutReq, err := http.NewRequestWithContext(mutCtx, http.MethodPost, "https://example.test", nil)
	require.NoError(t, err)
	assert.True(t, tracker.HasWriteIntent(mutReq.Context()))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withOperationIntent(context.Background(), tc.doc)

			assert.False(t, tracker.HasWriteIntent(ctx), "must not carry write intent")
			assert.True(t, tracker.HasReadIntent(ctx), "must be explicitly classified as a read")

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
			require.NoError(t, err)
			assert.True(t, tracker.HasReadIntent(req.Context()),
				"a POST built with a read-classified context must still be a read")
		})
	}
}

// TestGraphQLClassifiesRealOperations exercises withOperationIntent against
// two REAL operation documents from this package — not synthetic strings —
// so the classification is pinned against what the client actually sends.
// mutationUpdateIssueState is the mutation UpdateIssueState sends (extracted
// to a package-level const in queries.go for this test, no other change).
// QueryListProjects is an existing package-level query const from
// queries.go.
func TestGraphQLClassifiesRealOperations(t *testing.T) {
	mutCtx := withOperationIntent(context.Background(), mutationUpdateIssueState)
	assert.True(t, tracker.HasWriteIntent(mutCtx), "mutationUpdateIssueState must classify as a write")

	queryCtx := withOperationIntent(context.Background(), QueryListProjects)
	assert.True(t, tracker.HasReadIntent(queryCtx), "QueryListProjects must classify as a read")
}

// intentRecord is what intentRecordingTransport captures per request.
type intentRecord struct {
	opType string // "query" or "mutation", from the document itself
	opName string
	write  bool
	read   bool
}

// intentRecordingTransport records, for every request the Linear client
// sends, the GraphQL operation (parsed from the body) alongside the intent
// the shared rate-limit gate will see on the request's context.
type intentRecordingTransport struct {
	next http.RoundTripper
	mu   sync.Mutex
	recs []intentRecord
}

func (rt *intentRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(strings.NewReader(string(raw)))
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	fields := strings.FieldsFunc(strings.TrimSpace(payload.Query), func(r rune) bool {
		return r == ' ' || r == '(' || r == '{' || r == '\n' || r == '\t'
	})
	rec := intentRecord{
		write: tracker.HasWriteIntent(req.Context()),
		read:  tracker.HasReadIntent(req.Context()),
	}
	if len(fields) > 0 {
		rec.opType = fields[0]
	}
	if len(fields) > 1 {
		rec.opName = fields[1]
	}
	rt.mu.Lock()
	rt.recs = append(rt.recs, rec)
	rt.mu.Unlock()
	return rt.next.RoundTrip(req)
}

// TestLinearMutationAdmittedBeforeReads is the spec-named end-to-end pin for
// Linear's write-first admission: every request the real client sends must
// reach the shared gate with the intent its GraphQL operation implies —
// mutations as writes (admitted first once a window lifts), queries as reads.
// Linear is GraphQL-over-POST, so without this tagging the gate would see
// every call as a write and "writes go first" would mean nothing.
//
// UpdateIssueState is driven because it sends BOTH kinds in one call: a read
// (the state-id lookup) followed by a write (the issueUpdate mutation).
// FindCommentByKey adds a standalone read-only call.
//
// The recording transport is installed on the client's own unexported
// httpClient (whitebox, same package) — no production seam was added.
func TestLinearMutationAdmittedBeforeReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(payload.Query, "ItervoxResolveStateId"):
			_, _ = w.Write([]byte(`{"data":{"issue":{"team":{"id":"t1","states":{"nodes":[{"id":"s-done","name":"Done"}]}}}}}`))
		case strings.Contains(payload.Query, "ItervoxUpdateIssueState"):
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`))
		case strings.Contains(payload.Query, "ItervoxFindCommentByKey"):
			_, _ = w.Write([]byte(`{"data":{"comments":{"nodes":[]}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{APIKey: "k", Endpoint: srv.URL})
	rt := &intentRecordingTransport{next: http.DefaultTransport}
	c.httpClient.Transport = rt

	require.NoError(t, c.UpdateIssueState(context.Background(), "i1", "Done"))
	_, _, err := c.FindCommentByKey(context.Background(), "i1", "k1")
	require.NoError(t, err)

	rt.mu.Lock()
	recs := append([]intentRecord(nil), rt.recs...)
	rt.mu.Unlock()

	require.Len(t, recs, 3, "state-id read, issueUpdate mutation, comment lookup read")
	assert.Equal(t, "ItervoxResolveStateId", recs[0].opName)
	assert.Equal(t, "ItervoxUpdateIssueState", recs[1].opName)
	assert.Equal(t, "ItervoxFindCommentByKey", recs[2].opName)

	var mutations, queries int
	for _, rec := range recs {
		switch rec.opType {
		case "mutation":
			mutations++
			assert.True(t, rec.write, "%s is a mutation and must carry write intent", rec.opName)
			assert.False(t, rec.read, "%s must not also carry read intent", rec.opName)
		case "query":
			queries++
			assert.True(t, rec.read, "%s is a query and must carry read intent", rec.opName)
			assert.False(t, rec.write, "%s must not carry write intent", rec.opName)
		default:
			t.Errorf("unrecognised operation type %q for %s", rec.opType, rec.opName)
		}
	}
	assert.Equal(t, 1, mutations)
	assert.Equal(t, 2, queries)
}
