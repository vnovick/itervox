package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

// prMergedRecordingClient is a FuncClient that also implements
// PRMergedEmitter and records what the merge handler hands it.
type prMergedRecordingClient struct {
	FuncClient
	mu                                         sync.Mutex
	calls                                      int
	identifier, prURL, mergedSHA, base, headRf string
	prNumber                                   int
	err                                        error
}

func (c *prMergedRecordingClient) EmitPRMerged(_ context.Context, identifier, prURL string, prNumber int, mergedSHA, baseRef, headRef string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.identifier, c.prURL, c.prNumber, c.mergedSHA, c.base, c.headRf = identifier, prURL, prNumber, mergedSHA, baseRef, headRef
	return c.err
}

// TestMergePREmitsPRURL (CORE-108): the merge_pr handler hands pr_merged
// automations the PR's url, base branch and head branch from gh, and an
// emitter error still answers 200 with the dedup key recorded.
func TestMergePREmitsPRURL(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-108", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatalf("issue action token: %v", err)
	}
	client := &prMergedRecordingClient{err: errors.New("emit failed")}
	s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: client})
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN",` +
				`"url":"https://github.com/acme/app/pull/7","baseRefName":"main","headRefName":"itervox/ENG-108"}`),
		},
		"pr checks 7 --required": {out: []byte("all passing\n")},
		"pr merge 7 --squash":    {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit": {
			out: []byte(`{"mergeCommit":{"oid":"cafe108"}}`),
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-108/merge_pr", strings.NewReader(`{"pr":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 even though EmitPRMerged failed (body: %s)", w.Code, w.Body.String())
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls != 1 {
		t.Fatalf("EmitPRMerged calls = %d; want 1", client.calls)
	}
	if client.identifier != "ENG-108" || client.prNumber != 7 || client.mergedSHA != "cafe108" {
		t.Errorf("emit identity = (%q, %d, %q)", client.identifier, client.prNumber, client.mergedSHA)
	}
	if client.prURL != "https://github.com/acme/app/pull/7" {
		t.Errorf("prURL = %q; want the gh url", client.prURL)
	}
	if client.base != "main" {
		t.Errorf("baseRef = %q; want main", client.base)
	}
	if client.headRf != "itervox/ENG-108" {
		t.Errorf("headRef = %q; want itervox/ENG-108", client.headRf)
	}
	defaultMergePRDedup.mu.Lock()
	commit, recorded := defaultMergePRDedup.merged["ENG-108:7"]
	defaultMergePRDedup.mu.Unlock()
	if !recorded || commit != "cafe108" {
		t.Errorf("dedup key ENG-108:7 = (%q, %v); want recorded cafe108", commit, recorded)
	}
}

// TestMergePREmitFailureIsLogged (M6-close BH-M6-2): a pr_merged emit
// failure after a successful merge still answers 200, but is logged at ERROR
// with the identifier, PR number and error — it used to vanish.
func TestMergePREmitFailureIsLogged(t *testing.T) {
	logs := captureSlog(t)
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-208", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client := &prMergedRecordingClient{err: errors.New("fetch issue: tracker 502")}
	s := newMergeTestServer(t, Config{ActionTokenStore: store, Client: client})
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 8 --json labels,mergeable,mergeStateStatus,state,url,baseRefName,headRefName": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 8 --required":       {out: []byte("all passing\n")},
		"pr merge 8 --squash":          {out: []byte("Merged pull request #8\n")},
		"pr view 8 --json mergeCommit": {out: []byte(`{"mergeCommit":{"oid":"beef208"}}`)},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-208/merge_pr", strings.NewReader(`{"pr":8}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (the merge itself succeeded)", w.Code)
	}
	out := logs.String()
	for _, want := range []string{"level=ERROR", "identifier=ENG-208", "pr=8", "tracker 502"} {
		if !strings.Contains(out, want) {
			t.Errorf("emit failure log missing %q:\n%s", want, out)
		}
	}
}
