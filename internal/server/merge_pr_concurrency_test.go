package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

// TestMergePRConcurrentSamePRMergesOnce (CORE-161): two merge_pr requests
// for the same (identifier, PR) racing each other must run `gh pr merge`
// once. The dedup used to check and record in two separate lock sections,
// so both requests passed the check while the first merge was in flight.
func TestMergePRConcurrentSamePRMergesOnce(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-161", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: &FuncClient{}})

	var merges atomic.Int32
	firstViewing := make(chan struct{})
	release := make(chan struct{})
	var entered atomic.Bool
	canned := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required":       {out: []byte("all passing\n")},
		"pr merge 7 --squash":          {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit": {out: []byte(`{"mergeCommit":{"oid":"cafe161"}}`)},
	})
	s.ghRun = func(ctx context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if strings.HasPrefix(key, "pr view 7 --json labels") {
			// Hold the first request inside the gate until the second one
			// has had its chance at the dedup guard.
			if entered.CompareAndSwap(false, true) {
				close(firstViewing)
				<-release
			}
		}
		if key == "pr merge 7 --squash" {
			merges.Add(1)
		}
		return canned(ctx, args...)
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-161/merge_pr", strings.NewReader(`{"pr":7}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	var wg sync.WaitGroup
	var first *httptest.ResponseRecorder
	wg.Add(1)
	go func() { defer wg.Done(); first = post() }() // test driver
	<-firstViewing
	second := post()
	close(release)
	wg.Wait()

	if got := merges.Load(); got != 1 {
		t.Fatalf("gh pr merge ran %d times; want exactly 1 (first=%d %s, second=%d %s)",
			got, first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if first.Code != http.StatusOK {
		t.Errorf("first request = %d %s; want 200", first.Code, first.Body.String())
	}
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "merge_in_progress") {
		t.Errorf("second request = %d %s; want 409 merge_in_progress", second.Code, second.Body.String())
	}
	// Once committed, a retry is the idempotent already_merged answer.
	if third := post(); third.Code != http.StatusOK || !strings.Contains(third.Body.String(), `"already_merged":true`) {
		t.Errorf("third request = %d %s; want 200 already_merged", third.Code, third.Body.String())
	}
}

// TestMergePRRefusedMergeReleasesReservation (CORE-161): a refused or failed
// merge releases the reservation so a later request can try again.
func TestMergePRRefusedMergeReleasesReservation(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-161", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: &FuncClient{}})
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","state":"OPEN"}`),
		},
	})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-161/merge_pr", strings.NewReader(`{"pr":7}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "merge_blocked") {
			t.Fatalf("attempt %d = %d %s; want 409 merge_blocked (not merge_in_progress)", i, w.Code, w.Body.String())
		}
	}
}
