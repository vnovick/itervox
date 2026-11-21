package server

import (
	"context"
	"errors"
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

// prLaneCheckingClient also implements MergePRLaneChecker: it refuses every
// pull request but number 7 (a stack split's lower, code-only PR).
type prLaneCheckingClient struct {
	laneCheckingClient
	prs []int
}

func (c *prLaneCheckingClient) MergeLaneRefusalForPR(_ context.Context, identifier string, pr int,
	_ func(context.Context) (string, error)) (string, string) {
	c.prs = append(c.prs, pr)
	if identifier != "ENG-9" || pr != 7 {
		return "lane_human: not the code pull request", ""
	}
	return "", ""
}

// guardrails.lanes.split: merge_pr passes the pull request's number to a
// client that judges lanes per pull request, and prefers it over the
// per-issue check.
func TestMergePRAsksLanesPerPullRequest(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-9", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatalf("issue action token: %v", err)
	}
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)},
		"pr checks 7 --required":       {out: []byte("all passing\n")},
		"pr merge 7 --squash":          {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit": {out: []byte(`{"mergeCommit":{"oid":"abc"}}`)},
	})
	client := &prLaneCheckingClient{laneCheckingClient: laneCheckingClient{reason: "lane_human: the issue's lane"}}
	post := func(pr string) *httptest.ResponseRecorder {
		s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: client})
		s.ghRun = gh
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-9/merge_pr", strings.NewReader(`{"pr":`+pr+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	if w := post("8"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not the code pull request") {
		t.Fatalf("pr 8: status = %d body = %s; want 409 with the per-PR reason", w.Code, w.Body.String())
	}
	if w := post("7"); w.Code != http.StatusOK {
		t.Fatalf("pr 7: status = %d body = %s; want 200", w.Code, w.Body.String())
	}
	if client.asked.Load() != 0 {
		t.Fatalf("the per-issue check was asked %d times; want 0", client.asked.Load())
	}
	if len(client.prs) != 2 || client.prs[0] != 8 || client.prs[1] != 7 {
		t.Fatalf("per-PR checks = %v; want [8 7]", client.prs)
	}
}

// headCheckingClient judges pull request 7 by a split's own lane only at
// commit split, reading the head through the callback merge_pr passes.
type headCheckingClient struct {
	FuncClient
	split string
	heads []string
}

func (c *headCheckingClient) MergeLaneRefusalForPR(ctx context.Context, _ string, _ int,
	headSHA func(context.Context) (string, error)) (string, string) {
	head, err := headSHA(ctx)
	if err != nil {
		return "lane_human: " + err.Error(), ""
	}
	c.heads = append(c.heads, head)
	if head != c.split {
		return "lane_human: the head moved", ""
	}
	return "", head
}

// guardrails.lanes.split: merge_pr reads the pull request's head for the
// lane check (gh pr view --json headRefOid) and, when the decision rested
// on that commit, merges only that commit: the gate refuses a head that
// moved before the merge, and gh is told --match-head-commit for one that
// moves during it.
func TestMergePRLowerPRMergesOnlyAtJudgedHead(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-9", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatalf("issue action token: %v", err)
	}
	const split = "1111111111111111111111111111111111111111"
	post := func(client *headCheckingClient, gh func(ctx context.Context, args ...string) ([]byte, error)) *httptest.ResponseRecorder {
		s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: client})
		s.ghRun = gh
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-9/merge_pr", strings.NewReader(`{"pr":7}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	view := func(head string) fakeGHResponse {
		return fakeGHResponse{out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN","headRefOid":"` + head + `"}`)}
	}
	var merged atomic.Int32
	responses := func(laneHead, gateHead string) map[string]fakeGHResponse {
		return map[string]fakeGHResponse{
			"pr view 7 --json headRefOid": {out: []byte(`{"headRefOid":"` + laneHead + `"}`)},
			"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName,headRefOid": view(gateHead),
			"pr checks 7 --required":       {out: []byte("all passing\n")},
			"pr view 7 --json mergeCommit": {out: []byte(`{"mergeCommit":{"oid":"abc"}}`)},
		}
	}
	gh := func(r map[string]fakeGHResponse) func(ctx context.Context, args ...string) ([]byte, error) {
		inner := fakeGH(r)
		return func(ctx context.Context, args ...string) ([]byte, error) {
			key := strings.Join(args, " ")
			if strings.HasPrefix(key, "pr merge 7") {
				if key != "pr merge 7 --squash --match-head-commit "+split {
					return nil, errors.New("fake gh: merge without the judged head: " + key)
				}
				merged.Add(1)
				return []byte("Merged pull request #7\n"), nil
			}
			return inner(ctx, args...)
		}
	}

	client := &headCheckingClient{split: split}
	if w := post(client, gh(responses(split, split))); w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200 at the split's commit", w.Code, w.Body.String())
	}
	if merged.Load() != 1 || len(client.heads) != 1 || client.heads[0] != split {
		t.Fatalf("merged %d times, heads read %v; want one merge at %s", merged.Load(), client.heads, split)
	}

	moved := "2222222222222222222222222222222222222222"
	if w := post(&headCheckingClient{split: split}, gh(responses(moved, moved))); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "the head moved") {
		t.Fatalf("status = %d body = %s; want 409 when the lane check saw a moved head", w.Code, w.Body.String())
	}
	if w := post(&headCheckingClient{split: split}, gh(responses(split, moved))); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), MergePRReasonHeadMoved) {
		t.Fatalf("status = %d body = %s; want 409 head_moved when the head moved after the lane check", w.Code, w.Body.String())
	}
	if merged.Load() != 1 {
		t.Fatalf("merged %d times; a moved head must never merge", merged.Load())
	}
}
