package linear_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/linear"
)

func batchIssueNode(id, identifier, commentBody string) map[string]interface{} {
	return map[string]interface{}{
		"id":          id,
		"identifier":  identifier,
		"title":       "Batched " + identifier,
		"description": "",
		"priority":    float64(1),
		"state":       map[string]interface{}{"name": "In Progress"},
		"url":         "https://linear.app/team/" + identifier,
		"labels":      map[string]interface{}{"nodes": []interface{}{}},
		"inverseRelations": map[string]interface{}{
			"nodes": []interface{}{},
		},
		"children": map[string]interface{}{"nodes": []interface{}{}},
		"comments": map[string]interface{}{
			"nodes": []interface{}{
				map[string]interface{}{
					"id":        "c-" + id,
					"body":      commentBody,
					"createdAt": "2026-08-20T10:00:00.000Z",
					"user":      map[string]interface{}{"id": "u1", "name": "Ada"},
				},
			},
		},
		"createdAt": "2026-08-19T10:00:00.000Z",
		"updatedAt": "2026-08-20T10:00:00.000Z",
	}
}

// TestFetchIssueDetailsByIDsCarriesComments is the load-bearing test for issue
// #42's batching. The three callers that needed batching (tracker-reply check,
// pending-input resume, input-required replay) exist ONLY to read comments.
//
// If the batched query or its decoder dropped the comments block, every one of
// them would see an empty slice and conclude "no reply yet" — forever, and
// silently. Nothing would error; issues would simply never resume. So the
// comment content is asserted, not just the issue count.
func TestFetchIssueDetailsByIDsCarriesComments(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{
					batchIssueNode("id-1", "ENG-1", "first reply"),
					batchIssueNode("id-2", "ENG-2", "second reply"),
				},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	issues, err := client.FetchIssueDetailsByIDs(context.Background(),
		[]string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"})
	require.NoError(t, err)
	require.Len(t, issues, 2)

	byID := map[string][]string{}
	for _, iss := range issues {
		for _, c := range iss.Comments {
			byID[iss.ID] = append(byID[iss.ID], c.Body)
		}
	}
	assert.Equal(t, []string{"first reply"}, byID["id-1"],
		"batched detail must carry comments — the reply check reads nothing else")
	assert.Equal(t, []string{"second reply"}, byID["id-2"])
}

// TestFetchIssueDetailsByIDsEmptyInputCostsNoRequest pins that an empty id list
// short-circuits. Without it a quiet tick would still spend a request, which is
// the opposite of what issue #42 asked for.
func TestFetchIssueDetailsByIDsEmptyInputCostsNoRequest(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{}) // any request fails the test
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})

	issues, err := client.FetchIssueDetailsByIDs(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, issues)
}

// TestFetchIssueDetailsByIDsDropsMalformedIDs pins that one bad id does not take
// down the batch. Linear validates the whole `id: {in: [...]}` filter and fails
// the entire request if any element is malformed — observed live as a batch of
// 11 failing because 10 ids were seeded junk, including the one real UUID.
func TestFetchIssueDetailsByIDsDropsMalformedIDs(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{batchIssueNode("id-1", "ENG-1", "kept")},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})

	issues, err := client.FetchIssueDetailsByIDs(context.Background(),
		[]string{"00000000-0000-0000-0000-000000000001", "demo-id-not-a-uuid"})
	require.NoError(t, err, "a malformed id must not fail the whole batch")
	require.Len(t, issues, 1)
	assert.Equal(t, "id-1", issues[0].ID)
}

// TestFetchIssueDetailsByIDsAllMalformedCostsNoRequest pins that a batch of
// nothing-but-junk short-circuits instead of sending a query Linear will reject.
func TestFetchIssueDetailsByIDsAllMalformedCostsNoRequest(t *testing.T) {
	srv := serveJSON(t, []map[string]interface{}{}) // any request fails the test
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})

	issues, err := client.FetchIssueDetailsByIDs(context.Background(),
		[]string{"demo-id-1", "demo-id-2"})
	require.NoError(t, err)
	assert.Empty(t, issues)
}
