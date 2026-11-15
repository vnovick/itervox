package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

// laneCheckingClient is a FuncClient that also implements MergeLaneChecker.
type laneCheckingClient struct {
	FuncClient
	reason string
	asked  atomic.Int32
}

func (c *laneCheckingClient) MergeLaneRefusal(_ context.Context, identifier string) string {
	c.asked.Add(1)
	if identifier != "ENG-9" {
		return "wrong identifier " + identifier
	}
	return c.reason
}

// guardrails.lanes: merge_pr asks the lane checker before any gh call; a
// reason refuses the merge with 409 merge_blocked and the reason, and gh
// is never asked to merge; an empty reason merges as before.
func TestMergePRRefusedByLaneChecker(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-9", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatalf("issue action token: %v", err)
	}
	var ghCalls atomic.Int32
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)},
		"pr checks 7 --required":       {out: []byte("all passing\n")},
		"pr merge 7 --squash":          {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit": {out: []byte(`{"mergeCommit":{"oid":"abc"}}`)},
	})
	post := func(client *laneCheckingClient) *httptest.ResponseRecorder {
		s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: client})
		s.ghRun = func(ctx context.Context, args ...string) ([]byte, error) {
			ghCalls.Add(1)
			return gh(ctx, args...)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-9/merge_pr", strings.NewReader(`{"pr":7}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}

	refusing := &laneCheckingClient{reason: "lane_human: this pull request changes pipeline files (`.github/workflows/ci.yml`)"}
	w := post(refusing)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "merge_blocked") ||
		!strings.Contains(w.Body.String(), "lane_human: this pull request changes pipeline files") {
		t.Fatalf("status = %d body = %s; want 409 merge_blocked with the lane reason", w.Code, w.Body.String())
	}
	if refusing.asked.Load() != 1 || ghCalls.Load() != 0 {
		t.Fatalf("lane checker asked %d times, gh called %d times; want 1 and 0", refusing.asked.Load(), ghCalls.Load())
	}

	w = post(&laneCheckingClient{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200 when the lanes allow the merge", w.Code, w.Body.String())
	}
}
