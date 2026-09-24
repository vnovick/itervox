package orchestrator

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestReviewerChainCfgIsRaceFreeAgainstSetReviewerCfg pins that the review
// path reads cfg.Agent.ReviewerProfile under cfgMu.
//
// That field is on the cfgMu allowlist and has a live runtime writer:
// SetReviewerCfg, reachable from the PUT /settings/reviewer handler
// goroutine. ReviewerProfileChain reads it whenever the plural
// reviewer_profiles is unset — the default single-reviewer shape — so the
// three call sites that passed o.cfg directly were unsynchronized
// string-header reads racing that writer. One of them is on a WORKER
// goroutine (runWorker's prompt assembly), one on the event loop, one in the
// review-chain advance.
//
// Run under -race this fails if any of them regresses to touching o.cfg
// without the lock.
func TestReviewerChainCfgIsRaceFreeAgainstSetReviewerCfg(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ReviewerProfile = "reviewer-a"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer-a": {Command: "claude"},
		"reviewer-b": {Command: "claude"},
	}
	o := &Orchestrator{cfg: cfg}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // the HTTP settings handler
		defer wg.Done()
		for i := range 300 {
			name := "reviewer-a"
			if i%2 == 1 {
				name = "reviewer-b"
			}
			_ = o.SetReviewerCfg(name, true)
		}
	}()
	go func() { // the event loop / chain advance read
		defer wg.Done()
		for range 300 {
			_ = o.reviewerChainCfg()
		}
	}()
	go func() { // the worker goroutine's prompt assembly read
		defer wg.Done()
		for range 300 {
			_ = o.reviewVerdictRelPathCfg("ENG-1", "reviewer-a")
		}
	}()
	wg.Wait()

	profile, autoReview := o.ReviewerCfg()
	require.Contains(t, []string{"reviewer-a", "reviewer-b"}, profile)
	require.True(t, autoReview)
}
