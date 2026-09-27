package linear_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// commentThreadServer is a fake Linear GraphQL endpoint for one issue whose
// thread holds n comments, served in the given order ("asc" or "desc" by
// createdAt) with Relay cursor pagination in pages of 50 — the page size the
// detail queries request. Cursors are the served index as a string.
type commentThreadServer struct {
	order       string
	comments    []map[string]any // in served order
	cursorPages atomic.Int32     // follow-up requests that carried an `after` cursor
}

func newCommentThreadServer(n int, order string) *commentThreadServer {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s := &commentThreadServer{order: order}
	for i := 1; i <= n; i++ {
		s.comments = append(s.comments, map[string]any{
			"id":        fmt.Sprintf("c%02d", i),
			"body":      fmt.Sprintf("comment %d", i),
			"createdAt": base.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z"),
			"user":      map[string]any{"id": "u1", "name": "Ada"},
		})
	}
	if order == "desc" {
		slices.Reverse(s.comments)
	}
	return s
}

func (s *commentThreadServer) page(after string) map[string]any {
	start := 0
	if after != "" {
		_, _ = fmt.Sscanf(after, "%d", &start)
	}
	end := min(start+50, len(s.comments))
	nodes := make([]any, 0, end-start)
	for _, c := range s.comments[start:end] {
		nodes = append(nodes, c)
	}
	return map[string]any{
		"nodes":    nodes,
		"pageInfo": map[string]any{"hasNextPage": end < len(s.comments), "endCursor": fmt.Sprintf("%d", end)},
	}
}

func (s *commentThreadServer) issueNode() map[string]any {
	return map[string]any{
		"id": "issue-1", "identifier": "ENG-1", "title": "Long thread", "description": "",
		"priority": float64(1), "state": map[string]any{"name": "In Progress"},
		"url": "https://linear.app/team/ENG-1", "labels": map[string]any{"nodes": []any{}},
		"inverseRelations": map[string]any{"nodes": []any{}},
		"children":         map[string]any{"nodes": []any{}},
		"comments":         s.page(""),
		"createdAt":        "2026-08-19T10:00:00.000Z", "updatedAt": "2026-09-20T10:00:00.000Z",
	}
}

func (s *commentThreadServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(raw, &req)
	w.Header().Set("Content-Type", "application/json")
	var resp map[string]any
	switch {
	case strings.Contains(req.Query, "ItervoxIssueComments"):
		after, _ := req.Variables["after"].(string)
		if after != "" {
			s.cursorPages.Add(1)
		}
		resp = map[string]any{"data": map[string]any{"issue": map[string]any{"comments": s.page(after)}}}
	case strings.Contains(req.Query, "ItervoxLinearIssueDetailsById"):
		resp = map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []any{s.issueNode()}}}}
	case strings.Contains(req.Query, "ItervoxIssueDetail"):
		resp = map[string]any{"data": map[string]any{"issue": s.issueNode()}}
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// TestLinearDetailIncludesNewestComments (CORE-124): the detail queries read
// a single 50-comment window whose direction Linear does not document. With
// the input-required question key authoritative, a question outside that
// window is never matched and a tracker reply never resumes the agent. The
// detail result must hold the whole thread (bounded) in ascending CreatedAt
// order — the domain.Comment contract — whichever order Linear serves.
func TestLinearDetailIncludesNewestComments(t *testing.T) {
	fetchers := map[string]func(*linear.Client) (*domain.Issue, error){
		"single": func(c *linear.Client) (*domain.Issue, error) {
			return c.FetchIssueDetail(context.Background(), "issue-1")
		},
		"batch": func(c *linear.Client) (*domain.Issue, error) {
			issues, err := c.FetchIssueDetailsByIDs(context.Background(), []string{"11111111-1111-4111-8111-111111111111"})
			if err != nil || len(issues) == 0 {
				return nil, err
			}
			return &issues[0], nil
		},
	}
	for _, order := range []string{"asc", "desc"} {
		for name, fetch := range fetchers {
			t.Run(order+"/"+name, func(t *testing.T) {
				fake := newCommentThreadServer(60, order)
				srv := httptest.NewServer(fake)
				defer srv.Close()
				client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})

				issue, err := fetch(client)
				require.NoError(t, err)
				require.NotNil(t, issue)

				require.Len(t, issue.Comments, 60, "the whole thread, not one 50-comment window")
				assert.Equal(t, "c60", issue.Comments[len(issue.Comments)-1].ID, "newest comment present and last")
				assert.Equal(t, "c01", issue.Comments[0].ID, "oldest first")
				assert.True(t, slices.IsSortedFunc(issue.Comments, func(a, b domain.Comment) int {
					return a.CreatedAt.Compare(*b.CreatedAt)
				}), "ascending CreatedAt order")
				assert.Positive(t, fake.cursorPages.Load(), "the remainder is read with the cursor loop")
			})
		}
	}
}
