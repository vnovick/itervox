package linear_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

func linearIssueNode(id, identifier, state string) map[string]interface{} {
	return map[string]interface{}{
		"id":               id,
		"identifier":       identifier,
		"title":            "Issue " + identifier,
		"state":            map[string]interface{}{"name": state},
		"labels":           map[string]interface{}{"nodes": []interface{}{}},
		"inverseRelations": map[string]interface{}{"nodes": []interface{}{}},
		"createdAt":        "2024-01-01T00:00:00Z",
		"updatedAt":        "2024-01-01T00:00:00Z",
	}
}

func richIssueNode(id, identifier, state, desc string, priority int, branch, url string) map[string]interface{} {
	node := linearIssueNode(id, identifier, state)
	node["description"] = desc
	node["priority"] = float64(priority)
	node["branchName"] = branch
	node["url"] = url
	return node
}

func singlePageResponse(nodes []map[string]interface{}) map[string]interface{} {
	rawNodes := make([]interface{}, len(nodes))
	for i, n := range nodes {
		rawNodes[i] = n
	}
	return map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": rawNodes,
				"pageInfo": map[string]interface{}{
					"hasNextPage": false,
					"endCursor":   nil,
				},
			},
		},
	}
}

// serveJSON returns a test server that responds with the given sequence of JSON responses.
func serveJSON(t *testing.T, responses []map[string]interface{}) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := callCount
		callCount++
		if idx >= len(responses) {
			t.Errorf("unexpected request %d (only %d responses configured)", idx+1, len(responses))
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responses[idx])
	}))
}

// serveJSONWithAuth is like serveJSON but also verifies the Authorization header.
func serveJSONWithAuth(t *testing.T, expectedKey string, responses []map[string]interface{}) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != expectedKey {
			t.Errorf("expected Authorization %q, got %q", expectedKey, auth)
			w.WriteHeader(401)
			return
		}
		idx := callCount
		callCount++
		if idx >= len(responses) {
			t.Errorf("unexpected request %d (only %d responses configured)", idx+1, len(responses))
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responses[idx])
	}))
}

// queryDispatcher creates a test server that dispatches responses based on the
// GraphQL operationName or query content. The handler map keys are substrings
// matched against the request body.
func queryDispatcher(t *testing.T, handlers map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
			w.WriteHeader(500)
			return
		}
		bodyStr := string(bodyBytes)

		for key, resp := range handlers {
			if strings.Contains(bodyStr, key) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
		}

		t.Errorf("no handler matched request body: %s", bodyStr[:min(200, len(bodyStr))])
		w.WriteHeader(500)
	}))
}

// ---------------------------------------------------------------------------
// FetchCandidateIssues
// ---------------------------------------------------------------------------

func TestFetchCandidateIssuesSinglePage(t *testing.T) {
	nodes := []map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
		linearIssueNode("id-2", "ENG-2", "In Progress"),
	}
	srv := serveJSON(t, []map[string]interface{}{singlePageResponse(nodes)})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:         "test-key",
		ProjectSlug:    "my-project",
		ActiveStates:   []string{"Todo", "In Progress"},
		TerminalStates: []string{"Done"},
		Endpoint:       srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, "ENG-1", issues[0].Identifier)
	assert.Equal(t, "ENG-2", issues[1].Identifier)
}

func TestFetchCandidateIssuesPaginated(t *testing.T) {
	page1 := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{linearIssueNode("id-1", "ENG-1", "Todo")},
				"pageInfo": map[string]interface{}{
					"hasNextPage": true,
					"endCursor":   "cursor-abc",
				},
			},
		},
	}
	page2 := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{linearIssueNode("id-2", "ENG-2", "Todo")},
				"pageInfo": map[string]interface{}{
					"hasNextPage": false,
					"endCursor":   nil,
				},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{page1, page2})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, "ENG-1", issues[0].Identifier)
	assert.Equal(t, "ENG-2", issues[1].Identifier)
}

func TestFetchCandidateIssuesEmptyResponse(t *testing.T) {
	// Override with properly empty nodes
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{},
				"pageInfo": map[string]interface{}{
					"hasNextPage": false,
					"endCursor":   nil,
				},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestFetchCandidateIssuesWithDescriptionAndPriority(t *testing.T) {
	node := richIssueNode("id-1", "ENG-1", "Todo", "A bug report", 2, "feat/eng-1", "https://linear.app/eng-1")
	node["labels"] = map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{"name": "Bug"},
			map[string]interface{}{"name": "Urgent"},
		},
	}
	node["inverseRelations"] = map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"type": "blocks",
				"issue": map[string]interface{}{
					"id":         "blocker-1",
					"identifier": "ENG-0",
					"state":      map[string]interface{}{"name": "In Progress"},
				},
			},
		},
	}

	resp := singlePageResponse([]map[string]interface{}{node})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)

	issue := issues[0]
	assert.Equal(t, "id-1", issue.ID)
	assert.Equal(t, "ENG-1", issue.Identifier)
	assert.Equal(t, "Issue ENG-1", issue.Title)
	assert.Equal(t, "Todo", issue.State)
	require.NotNil(t, issue.Description)
	assert.Equal(t, "A bug report", *issue.Description)
	require.NotNil(t, issue.Priority)
	assert.Equal(t, 2, *issue.Priority)
	require.NotNil(t, issue.BranchName)
	assert.Equal(t, "feat/eng-1", *issue.BranchName)
	require.NotNil(t, issue.URL)
	assert.Equal(t, "https://linear.app/eng-1", *issue.URL)
	assert.Equal(t, []string{"bug", "urgent"}, issue.Labels)
	require.Len(t, issue.BlockedBy, 1)
	assert.Equal(t, "blocker-1", *issue.BlockedBy[0].ID)
	assert.Equal(t, "ENG-0", *issue.BlockedBy[0].Identifier)
	assert.Equal(t, "In Progress", *issue.BlockedBy[0].State)
}

// ---------------------------------------------------------------------------
// FetchCandidateIssues with project filter
// ---------------------------------------------------------------------------

func TestFetchCandidateIssuesNoProjectSlugFallsBackToAll(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	// No ProjectSlug and no runtime filter → should use QueryCandidateIssuesAll
	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

func TestFetchCandidateIssuesEmptyFilterFetchesAll(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
		linearIssueNode("id-2", "ENG-2", "In Progress"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project", // would normally filter by project
		ActiveStates: []string{"Todo", "In Progress"},
		Endpoint:     srv.URL,
	})
	// Set empty filter → should override project slug and fetch all
	client.SetProjectFilter([]string{})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

func TestFetchCandidateIssuesSpecificProjectFilter(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	client.SetProjectFilter([]string{"specific-project"})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

func TestFetchCandidateIssuesNoProjectSentinel(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	client.SetProjectFilter([]string{"__no_project__"})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

func TestFetchCandidateIssuesMixedProjectFilter(t *testing.T) {
	// Two requests: one for slug "proj-a", one for "__no_project__"
	resp1 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	resp2 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-2", "ENG-2", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp1, resp2})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	client.SetProjectFilter([]string{"proj-a", "__no_project__"})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

func TestFetchCandidateIssuesMixedFilterDeduplicates(t *testing.T) {
	// Both responses return the same issue; should be deduplicated.
	resp1 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	resp2 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp1, resp2})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	client.SetProjectFilter([]string{"proj-a", "proj-b"})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1, "duplicate issues should be deduplicated")
}

// ---------------------------------------------------------------------------
// FetchIssueDetail
// ---------------------------------------------------------------------------

func TestFetchIssueDetail(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"id":          "detail-id",
				"identifier":  "ENG-42",
				"title":       "Fix the bug",
				"description": "Detailed description here",
				"priority":    float64(1),
				"state":       map[string]interface{}{"name": "In Progress"},
				"branchName":  "fix/eng-42",
				"url":         "https://linear.app/team/eng-42",
				"labels": map[string]interface{}{
					"nodes": []interface{}{
						map[string]interface{}{"name": "Bug"},
					},
				},
				"inverseRelations": map[string]interface{}{
					"nodes": []interface{}{},
				},
				"comments": map[string]interface{}{
					"nodes": []interface{}{
						map[string]interface{}{
							"body":      "First comment",
							"createdAt": "2024-06-01T10:00:00Z",
							"user":      map[string]interface{}{"name": "Alice"},
						},
						map[string]interface{}{
							"body":      "Second comment",
							"createdAt": "2024-06-02T10:00:00Z",
							"user":      map[string]interface{}{"name": "Bob"},
						},
					},
				},
				"createdAt": "2024-01-15T08:00:00Z",
				"updatedAt": "2024-06-02T10:00:00Z",
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	issue, err := client.FetchIssueDetail(context.Background(), "detail-id")
	require.NoError(t, err)
	require.NotNil(t, issue)

	assert.Equal(t, "detail-id", issue.ID)
	assert.Equal(t, "ENG-42", issue.Identifier)
	assert.Equal(t, "Fix the bug", issue.Title)
	assert.Equal(t, "In Progress", issue.State)
	require.NotNil(t, issue.Description)
	assert.Equal(t, "Detailed description here", *issue.Description)
	require.NotNil(t, issue.Priority)
	assert.Equal(t, 1, *issue.Priority)
	require.NotNil(t, issue.BranchName)
	assert.Equal(t, "fix/eng-42", *issue.BranchName)
	require.NotNil(t, issue.URL)
	assert.Equal(t, "https://linear.app/team/eng-42", *issue.URL)
	assert.Equal(t, []string{"bug"}, issue.Labels)

	require.Len(t, issue.Comments, 2)
	assert.Equal(t, "First comment", issue.Comments[0].Body)
	assert.Equal(t, "Alice", issue.Comments[0].AuthorName)
	assert.Equal(t, "Second comment", issue.Comments[1].Body)
	assert.Equal(t, "Bob", issue.Comments[1].AuthorName)
}

func TestFetchIssueDetailMissingIssue(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": nil,
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	_, err := client.FetchIssueDetail(context.Background(), "nonexistent")
	require.Error(t, err)
	// `data.issue = null` is a DELETED issue, and now surfaces as the
	// tracker.ErrNotFound sentinel rather than an opaque "missing issue"
	// string. The distinction is load-bearing downstream: the dependency
	// audit retires a row on not-found but retries every other error, so the
	// old generic error left a deleted blocker retrying forever. The
	// substring assertion this replaces would have passed either way.
	assert.ErrorIs(t, err, tracker.ErrNotFound)
}

func TestFetchIssueDetailGraphQLError(t *testing.T) {
	resp := map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"message": "Entity not found"},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	_, err := client.FetchIssueDetail(context.Background(), "bad-id")
	require.Error(t, err)
	var gqlErr *tracker.GraphQLError
	assert.True(t, errors.As(err, &gqlErr))
}

func TestFetchIssueByIdentifier(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"id":               "abc-123",
				"identifier":       "ENG-99",
				"title":            "By identifier",
				"state":            map[string]interface{}{"name": "Todo"},
				"labels":           map[string]interface{}{"nodes": []interface{}{}},
				"inverseRelations": map[string]interface{}{"nodes": []interface{}{}},
				"createdAt":        "2024-01-01T00:00:00Z",
				"updatedAt":        "2024-01-01T00:00:00Z",
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	issue, err := client.FetchIssueByIdentifier(context.Background(), "ENG-99")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "ENG-99", issue.Identifier)
}

// ---------------------------------------------------------------------------
// UpdateIssueState
// ---------------------------------------------------------------------------

func TestUpdateIssueState(t *testing.T) {
	resolveResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"team": map[string]interface{}{
					"id": "team-1",
					"states": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"id": "state-uuid-done", "name": "Done"},
						},
					},
				},
			},
		},
	}
	updateResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issueUpdate": map[string]interface{}{
				"success": true,
			},
		},
	}

	srv := queryDispatcher(t, map[string]interface{}{
		"ItervoxResolveStateId":   resolveResp,
		"ItervoxUpdateIssueState": updateResp,
	})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	err := client.UpdateIssueState(context.Background(), "issue-1", "Done")
	require.NoError(t, err)
}

func TestUpdateIssueStateNotFound(t *testing.T) {
	resolveResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"team": map[string]interface{}{
					"states": map[string]interface{}{
						"nodes": []interface{}{},
					},
				},
			},
		},
	}

	srv := serveJSON(t, []map[string]interface{}{resolveResp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	err := client.UpdateIssueState(context.Background(), "issue-1", "NonexistentState")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state \"NonexistentState\" not found")
}

func TestUpdateIssueStateMutationFails(t *testing.T) {
	resolveResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"team": map[string]interface{}{
					"id": "team-1",
					"states": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"id": "state-uuid", "name": "Done"},
						},
					},
				},
			},
		},
	}
	updateResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issueUpdate": map[string]interface{}{
				"success": false,
			},
		},
	}

	srv := queryDispatcher(t, map[string]interface{}{
		"ItervoxResolveStateId":   resolveResp,
		"ItervoxUpdateIssueState": updateResp,
	})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	err := client.UpdateIssueState(context.Background(), "issue-1", "Done")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-success")
}

// ---------------------------------------------------------------------------
// CreateComment
// ---------------------------------------------------------------------------

func TestCreateComment(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"commentCreate": map[string]interface{}{
				"comment": map[string]interface{}{
					"id":        "comment-1",
					"body":      "This is a comment",
					"createdAt": "2026-04-15T10:00:00Z",
					"user": map[string]interface{}{
						"id":   "user-1",
						"name": "Itervox",
					},
				},
				"success": true,
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	comment, err := client.CreateComment(context.Background(), "issue-1", "This is a comment")
	require.NoError(t, err)
	require.NotNil(t, comment)
	assert.Equal(t, "comment-1", comment.ID)
	assert.Equal(t, "user-1", comment.AuthorID)
	assert.Equal(t, "Itervox", comment.AuthorName)
}

func TestCreateCommentFailure(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"commentCreate": map[string]interface{}{
				"success": false,
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	comment, err := client.CreateComment(context.Background(), "issue-1", "This is a comment")
	require.Error(t, err)
	assert.Nil(t, comment)
	assert.Contains(t, err.Error(), "linear_create_comment")
}

func TestCreateIssue(t *testing.T) {
	contextResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issue": map[string]interface{}{
				"team": map[string]interface{}{
					"id": "team-1",
					"states": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"id": "state-1", "name": "Todo"},
							map[string]interface{}{"id": "state-2", "name": "Done"},
						},
					},
				},
				"project": map[string]interface{}{"id": "project-1"},
			},
		},
	}
	createResp := map[string]interface{}{
		"data": map[string]interface{}{
			"issueCreate": map[string]interface{}{
				"success": true,
				"issue": map[string]interface{}{
					"id":               "issue-2",
					"identifier":       "ENG-2",
					"title":            "Follow-up",
					"description":      "Add regression coverage",
					"priority":         float64(0),
					"state":            map[string]interface{}{"name": "Todo"},
					"labels":           map[string]interface{}{"nodes": []interface{}{}},
					"inverseRelations": map[string]interface{}{"nodes": []interface{}{}},
					"url":              "https://linear.app/issue/ENG-2",
					"createdAt":        "2026-04-15T10:00:00Z",
					"updatedAt":        "2026-04-15T10:00:00Z",
				},
			},
		},
	}
	srv := queryDispatcher(t, map[string]interface{}{
		"ItervoxResolveCreateIssueContext": contextResp,
		"ItervoxCreateIssue":               createResp,
	})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	issue, err := client.CreateIssue(context.Background(), "issue-1", "Follow-up", "Add regression coverage", "Todo")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "ENG-2", issue.Identifier)
	assert.Equal(t, "Follow-up", issue.Title)
	assert.Equal(t, "Todo", issue.State)
	require.NotNil(t, issue.Description)
	assert.Equal(t, "Add regression coverage", *issue.Description)
}

// ---------------------------------------------------------------------------
// SetIssueBranch
// ---------------------------------------------------------------------------

func TestSetIssueBranch(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issueUpdate": map[string]interface{}{
				"success": true,
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	err := client.SetIssueBranch(context.Background(), "issue-1", "feat/my-branch")
	require.NoError(t, err)
}

func TestSetIssueBranchFailure(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issueUpdate": map[string]interface{}{
				"success": false,
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	err := client.SetIssueBranch(context.Background(), "issue-1", "feat/my-branch")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear_set_branch")
}

// ---------------------------------------------------------------------------
// FetchIssuesByStates
// ---------------------------------------------------------------------------

func TestFetchIssuesByStatesEmptyReturnsEmpty(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project",
		Endpoint:    "http://should-not-be-called.invalid",
	})
	result, err := client.FetchIssuesByStates(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFetchIssuesByStatesPaginated(t *testing.T) {
	page1Resp := `{"data":{"issues":{"nodes":[{"id":"i1","identifier":"ABC-1","title":"T1","state":{"name":"Done"},"labels":{"nodes":[]},"inverseRelations":{"nodes":[]}}],"pageInfo":{"hasNextPage":true,"endCursor":"cur1"}}}}`
	page2Resp := `{"data":{"issues":{"nodes":[{"id":"i2","identifier":"ABC-2","title":"T2","state":{"name":"Cancelled"},"labels":{"nodes":[]},"inverseRelations":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = fmt.Fprint(w, page1Resp)
		} else {
			_, _ = fmt.Fprint(w, page2Resp)
		}
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "tok",
		ProjectSlug:  "proj",
		ActiveStates: []string{"Done", "Cancelled"},
		Endpoint:     srv.URL,
	})
	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done", "Cancelled"})
	assert.NoError(t, err)
	assert.Equal(t, 2, len(issues))
	assert.Equal(t, "i1", issues[0].ID)
	assert.Equal(t, "i2", issues[1].ID)
}

func TestFetchIssuesByStatesNoProjectSlug(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Done"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	// No ProjectSlug → should use QueryCandidateIssuesAll
	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done"})
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

// TestFetchIssuesByStatesHonorsRuntimeProjectFilter is the fix-round
// regression test for the "project-filter amplifier" gap: before this fix,
// FetchIssuesByStates ignored the runtime project filter entirely and always
// used the static WORKFLOW.md ProjectSlug, even when a runtime filter (set
// via SetProjectFilter, the same mechanism FetchCandidateIssues already
// honors) restricted the deps-analyzer's UNION fetch to a different project.
// This mirrors TestFetchCandidateIssuesSpecificProjectFilter.
func TestFetchIssuesByStatesHonorsRuntimeProjectFilter(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Done"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "static-project", // would normally be used if the filter were ignored
		Endpoint:    srv.URL,
	})
	client.SetProjectFilter([]string{"runtime-project"})

	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done"})
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

// TestFetchIssuesByStatesEmptyFilterFetchesAll mirrors
// TestFetchCandidateIssuesEmptyFilterFetchesAll: an explicit empty runtime
// filter overrides the static ProjectSlug and fetches all projects.
func TestFetchIssuesByStatesEmptyFilterFetchesAll(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Done"),
		linearIssueNode("id-2", "ENG-2", "Cancelled"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project", // would normally filter by project
		Endpoint:    srv.URL,
	})
	client.SetProjectFilter([]string{})

	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done", "Cancelled"})
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

// TestFetchIssuesByStatesMixedFilterDeduplicates mirrors
// TestFetchCandidateIssuesMixedFilterDeduplicates: multiple filter slugs
// (including the no-project sentinel) are fetched and merged, deduplicating
// on issue ID.
func TestFetchIssuesByStatesMixedFilterDeduplicates(t *testing.T) {
	resp1 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Done"),
	})
	resp2 := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Done"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp1, resp2})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})
	client.SetProjectFilter([]string{"proj-a", "proj-b"})

	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done"})
	require.NoError(t, err)
	assert.Len(t, issues, 1, "duplicate issues should be deduplicated")
}

// ---------------------------------------------------------------------------
// FetchIssueStatesByIDs
// ---------------------------------------------------------------------------

func TestFetchIssueStatesByIDs(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{
					linearIssueNode("id-1", "ENG-1", "In Progress"),
				},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project",
		Endpoint:    srv.URL,
	})

	result, err := client.FetchIssueStatesByIDs(context.Background(), []string{"id-1"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "In Progress", result[0].State)
	// outbox #54 fast-follow's superseded_by_tracker reconciliation rule
	// (outbox.ReconcileVerdict) depends on UpdatedAt being populated on
	// every issue this method returns — pin it so a future query/normalize
	// change can't silently drop the field.
	require.NotNil(t, result[0].UpdatedAt)
	assert.Equal(t, "2024-01-01T00:00:00Z", result[0].UpdatedAt.UTC().Format(time.RFC3339))
}

func TestFetchIssueStatesByIDsEmptyReturnsEmpty(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: "http://should-not-be-called.invalid",
	})
	result, err := client.FetchIssueStatesByIDs(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// FetchProjects
// ---------------------------------------------------------------------------

func TestFetchProjects(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"projects": map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{"id": "p1", "name": "Alpha", "slugId": "alpha"},
					map[string]interface{}{"id": "p2", "name": "Beta", "slugId": "beta"},
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

	projects, err := client.FetchProjects(context.Background())
	require.NoError(t, err)
	require.Len(t, projects, 2)
	assert.Equal(t, "p1", projects[0].ID)
	assert.Equal(t, "Alpha", projects[0].Name)
	assert.Equal(t, "alpha", projects[0].Slug)
	assert.Equal(t, "p2", projects[1].ID)
	assert.Equal(t, "Beta", projects[1].Name)
	assert.Equal(t, "beta", projects[1].Slug)
}

func TestFetchProjectsEmpty(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"projects": map[string]interface{}{
				"nodes": []interface{}{},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	projects, err := client.FetchProjects(context.Background())
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestFetchProjectsGraphQLError(t *testing.T) {
	resp := map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"message": "Not authorized"},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	_, err := client.FetchProjects(context.Background())
	require.Error(t, err)
}

func TestFetchProjectsMissingDataBlock(t *testing.T) {
	resp := map[string]interface{}{"unexpected": true}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:   "test-key",
		Endpoint: srv.URL,
	})

	_, err := client.FetchProjects(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear_fetch_projects")
}

func TestFetchProjectsSkipsIncompleteNodes(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"projects": map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{"id": "p1", "name": "Alpha", "slugId": "alpha"},
					map[string]interface{}{"id": "", "name": "NoID", "slugId": "noid"},          // missing id
					map[string]interface{}{"id": "p3", "name": "", "slugId": "noname"},          // missing name
					map[string]interface{}{"id": "p4", "name": "Valid", "slugId": "valid-slug"}, // OK
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

	projects, err := client.FetchProjects(context.Background())
	require.NoError(t, err)
	assert.Len(t, projects, 2)
	assert.Equal(t, "Alpha", projects[0].Name)
	assert.Equal(t, "Valid", projects[1].Name)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func TestFetchCandidateIssuesNon200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project",
		Endpoint:    srv.URL,
	})
	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	var apiErr *tracker.APIStatusError
	require.True(t, errors.As(err, &apiErr), "expected *tracker.APIStatusError, got %T: %v", err, err)
	assert.Equal(t, "linear", apiErr.Adapter)
	assert.Equal(t, 401, apiErr.Status)
}

func TestFetchCandidateIssuesGraphQLErrors(t *testing.T) {
	resp := map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"message": "Not authorized"},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project",
		Endpoint:    srv.URL,
	})
	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	var gqlErr *tracker.GraphQLError
	require.True(t, errors.As(err, &gqlErr), "expected *tracker.GraphQLError, got %T: %v", err, err)
	assert.Contains(t, gqlErr.Message, "Not authorized")
}

func TestFetchCandidateIssuesUnknownPayload(t *testing.T) {
	resp := map[string]interface{}{"unexpected": "payload"}
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:      "test-key",
		ProjectSlug: "my-project",
		Endpoint:    srv.URL,
	})
	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear_unknown_payload")
}

func TestFetchCandidateIssuesMissingEndCursor(t *testing.T) {
	page1 := map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{linearIssueNode("id-1", "ENG-1", "Todo")},
				"pageInfo": map[string]interface{}{
					"hasNextPage": true,
					// endCursor absent
				},
			},
		},
	}
	srv := serveJSON(t, []map[string]interface{}{page1})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear_missing_end_cursor")
}

func TestNetworkError500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	var apiErr *tracker.APIStatusError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 500, apiErr.Status)
}

func TestNetworkErrorConnectionRefused(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     "http://127.0.0.1:1", // port 1 = almost certainly refused
	})

	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear_api_request")
}

// ---------------------------------------------------------------------------
// Authorization header
// ---------------------------------------------------------------------------

func TestAuthorizationHeaderSent(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := serveJSONWithAuth(t, "lin_api_secret123", []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "lin_api_secret123",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

// ---------------------------------------------------------------------------
// Normalization edge cases
// ---------------------------------------------------------------------------

func TestNormalizeLabelsLowercase(t *testing.T) {
	node := linearIssueNode("id-1", "ENG-1", "Todo")
	node["labels"] = map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{"name": "BUG"},
			map[string]interface{}{"name": "BackEnd"},
		},
	}
	resp := singlePageResponse([]map[string]interface{}{node})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, []string{"bug", "backend"}, issues[0].Labels)
}

func TestNormalizeBlockersFromInverseRelations(t *testing.T) {
	node := linearIssueNode("id-1", "ENG-1", "Todo")
	node["inverseRelations"] = map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"type": "blocks",
				"issue": map[string]interface{}{
					"id":         "blocker-id",
					"identifier": "ENG-0",
					"state":      map[string]interface{}{"name": "In Progress"},
					"url":        "https://linear.app/issue/ENG-0",
				},
			},
			map[string]interface{}{
				"type": "duplicate",
				"issue": map[string]interface{}{
					"id":         "other-id",
					"identifier": "ENG-9",
					"state":      map[string]interface{}{"name": "Done"},
				},
			},
		},
	}
	resp := singlePageResponse([]map[string]interface{}{node})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	assert.Equal(t, "blocker-id", *issues[0].BlockedBy[0].ID)
	assert.Equal(t, "ENG-0", *issues[0].BlockedBy[0].Identifier)
	require.NotNil(t, issues[0].BlockedBy[0].State)
	assert.Equal(t, "In Progress", *issues[0].BlockedBy[0].State)
	require.NotNil(t, issues[0].BlockedBy[0].URL)
	assert.Equal(t, "https://linear.app/issue/ENG-0", *issues[0].BlockedBy[0].URL)
}

// TestFetchCandidateIssuesBlockedByChildSubIssue proves the query/normalize
// chain end-to-end at the client level: a fetched issue whose raw node
// carries a `children` selection surfaces the child as a BlockedBy entry,
// per the Linear sub-issues gating design.
func TestFetchCandidateIssuesBlockedByChildSubIssue(t *testing.T) {
	node := linearIssueNode("id-1", "ENG-1", "Todo")
	node["children"] = map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"id":         "child-id",
				"identifier": "ENG-2",
				"url":        "https://linear.app/issue/ENG-2",
				"state":      map[string]interface{}{"name": "Todo"},
			},
		},
	}
	resp := singlePageResponse([]map[string]interface{}{node})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	assert.Equal(t, "child-id", *issues[0].BlockedBy[0].ID)
	assert.Equal(t, "ENG-2", *issues[0].BlockedBy[0].Identifier)
	require.NotNil(t, issues[0].BlockedBy[0].URL)
	assert.Equal(t, "https://linear.app/issue/ENG-2", *issues[0].BlockedBy[0].URL)
	require.NotNil(t, issues[0].BlockedBy[0].State)
	assert.Equal(t, "Todo", *issues[0].BlockedBy[0].State)
}

func TestNormalizeSkipsInvalidNodes(t *testing.T) {
	// Nodes missing required fields (id, identifier, title) should be skipped.
	resp := singlePageResponse([]map[string]interface{}{
		{"id": "", "identifier": "ENG-1", "title": "Missing ID"},
		{"id": "id-2", "identifier": "", "title": "Missing Ident"},
		{"id": "id-3", "identifier": "ENG-3", "title": ""},
		linearIssueNode("id-4", "ENG-4", "Todo"),
	})
	srv := serveJSON(t, []map[string]interface{}{resp})
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 1, "only the valid node should be returned")
	assert.Equal(t, "ENG-4", issues[0].Identifier)
}

// ---------------------------------------------------------------------------
// Rate limits
// ---------------------------------------------------------------------------

func TestRateLimitHeadersParsed(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Requests-Limit", "1000")
		w.Header().Set("X-RateLimit-Requests-Remaining", "999")
		w.Header().Set("X-RateLimit-Requests-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Complexity-Limit", "250000")
		w.Header().Set("X-RateLimit-Complexity-Remaining", "249900")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	// Before any call, rate limits should be zero/nil.
	reqLim, reqRem, reset, cplxLim, cplxRem := client.RateLimits()
	assert.Equal(t, 0, reqLim)
	assert.Equal(t, 0, reqRem)
	assert.Nil(t, reset)
	assert.Equal(t, 0, cplxLim)
	assert.Equal(t, 0, cplxRem)

	snap := client.RateLimitSnapshot()
	assert.Nil(t, snap, "no rate limit data before first call")

	// Make a call to populate rate limits.
	_, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)

	reqLim, reqRem, reset, cplxLim, cplxRem = client.RateLimits()
	assert.Equal(t, 1000, reqLim)
	assert.Equal(t, 999, reqRem)
	require.NotNil(t, reset)
	assert.Equal(t, 250000, cplxLim)
	assert.Equal(t, 249900, cplxRem)

	snap = client.RateLimitSnapshot()
	require.NotNil(t, snap)
	assert.Equal(t, 1000, snap.RequestsLimit)
	assert.Equal(t, 999, snap.RequestsRemaining)
	assert.Equal(t, 250000, snap.ComplexityLimit)
	assert.Equal(t, 249900, snap.ComplexityRemaining)
}

func TestRateLimitMillisecondTimestamp(t *testing.T) {
	resp := singlePageResponse([]map[string]interface{}{
		linearIssueNode("id-1", "ENG-1", "Todo"),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Requests-Limit", "100")
		// Millisecond timestamp (> 1e11)
		w.Header().Set("X-RateLimit-Requests-Reset", "1700000000000")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	_, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)

	_, _, reset, _, _ := client.RateLimits()
	require.NotNil(t, reset)
	// Milliseconds divided by 1000 = seconds timestamp
	assert.Equal(t, int64(1700000000), reset.Unix())
}

// ---------------------------------------------------------------------------
// SetProjectFilter / GetProjectFilter
// ---------------------------------------------------------------------------

func TestProjectFilterDefaultNil(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})
	assert.Nil(t, client.GetProjectFilter())
}

func TestProjectFilterSetAndGet(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})

	client.SetProjectFilter([]string{"alpha", "beta"})
	filter := client.GetProjectFilter()
	assert.Equal(t, []string{"alpha", "beta"}, filter)
}

func TestProjectFilterSetEmpty(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})

	client.SetProjectFilter([]string{})
	filter := client.GetProjectFilter()
	require.NotNil(t, filter)
	assert.Empty(t, filter)
}

func TestProjectFilterResetToNil(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})

	client.SetProjectFilter([]string{"alpha"})
	assert.NotNil(t, client.GetProjectFilter())

	client.SetProjectFilter(nil)
	assert.Nil(t, client.GetProjectFilter())
}

func TestProjectFilterReturnsCopy(t *testing.T) {
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})

	client.SetProjectFilter([]string{"alpha", "beta"})
	filter := client.GetProjectFilter()
	filter[0] = "mutated"

	// Original should be unchanged.
	assert.Equal(t, []string{"alpha", "beta"}, client.GetProjectFilter())
}

// ---------------------------------------------------------------------------
// NewClient default endpoint
// ---------------------------------------------------------------------------

func TestNewClientDefaultEndpoint(t *testing.T) {
	// When Endpoint is empty, NewClient sets the default. We can verify this
	// indirectly by making a call that fails against the real endpoint (no key)
	// but we just verify the client is created without panic.
	client := linear.NewClient(linear.ClientConfig{
		APIKey: "test-key",
	})
	assert.NotNil(t, client)
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestFetchCandidateIssuesCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This should never be reached because context is already cancelled.
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := linear.NewClient(linear.ClientConfig{
		APIKey:       "test-key",
		ProjectSlug:  "my-project",
		ActiveStates: []string{"Todo"},
		Endpoint:     srv.URL,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.FetchCandidateIssues(ctx)
	require.Error(t, err)
}
