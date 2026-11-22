package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

var evT0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// evidenceState is a real orchestrator.State for ENG-7 (and noise for
// ENG-8): a live reviewer waiting on a held push, reviewer verdicts, a
// push lane, a managed PR, a retry, and feed events for approvals,
// refusals and a merge.
func evidenceState() orchestrator.State {
	s := orchestrator.NewState(&config.Config{})
	attempt := 1
	s.Running["issue-7"] = &orchestrator.RunEntry{
		Issue: domain.Issue{ID: "issue-7", Identifier: "ENG-7", Title: "Fix the parser"},
		RunID: "run-3", Kind: "reviewer", ProfileName: "reviewer", Backend: "codex",
		StartedAt: evT0.Add(2 * time.Hour), TurnCount: 2, RetryAttempt: &attempt,
		PromptDigest: strings.Repeat("c", 64), RuleIDs: []string{"no-force"}, RulesHash: "rh3",
		Steers: []orchestrator.RunSteer{{Text: "check the tests", At: evT0.Add(2*time.Hour + time.Minute), Delivery: orchestrator.SteeringLive}},
	}
	s.Running["issue-8"] = &orchestrator.RunEntry{Issue: domain.Issue{ID: "issue-8", Identifier: "ENG-8"}, RunID: "run-x"}
	s.RetryAttempts["issue-7"] = &orchestrator.RetryEntry{IssueID: "issue-7", Identifier: "ENG-7", Attempt: 2, DueAt: evT0.Add(3 * time.Hour)}
	s.ReviewVerdicts["ENG-7"] = []orchestrator.ReviewVerdict{{
		Profile: "reviewer", Verdict: "approve", Backend: "codex", RecordedAt: evT0.Add(90 * time.Minute),
		Reasons: []string{"secret finding text"}, Comments: []orchestrator.ReviewComment{{}, {}},
	}}
	s.ReviewOutcomes["ENG-7"] = orchestrator.ReviewOutcome{Approvals: 1, Quorum: "any_block"}
	s.PushLanes["ENG-7"] = orchestrator.PushLaneRecord{Lane: guardrails.LaneHuman, Paths: []string{".github/workflows/ci.yml"},
		PathsTotal: 3, WriterBackends: []string{"claude"}, RunID: "run-1", UpdatedAt: evT0.Add(30 * time.Minute)}
	s.ManagedPRs["https://github.com/o/r/pull/9"] = orchestrator.ManagedPR{PRURL: "https://github.com/o/r/pull/9",
		Identifier: "ENG-7", Branch: "eng-7", RecordedAt: evT0.Add(time.Hour)}
	s.ManagedPRs["https://github.com/o/r/pull/10"] = orchestrator.ManagedPR{PRURL: "https://github.com/o/r/pull/10", Identifier: "ENG-8"}
	s.InputRequiredIssues["ENG-7"] = &orchestrator.InputRequiredEntry{IssueID: "issue-7", Identifier: "ENG-7",
		Context: "approve `git push --force`?", QueuedAt: evT0.Add(2*time.Hour + 5*time.Minute),
		GuardrailApprovalID: "ap-2", GuardrailLanePaths: []string{"deploy/prod.yml"}, GuardrailLanePathsTotal: 4}
	feed := func(seq int64, at time.Duration, kind, id string, p orchestrator.FeedPayload) orchestrator.FeedEvent {
		return orchestrator.FeedEvent{Seq: seq, TS: evT0.Add(at), Kind: kind, Identifier: id, Payload: p}
	}
	s.Feed = []orchestrator.FeedEvent{
		feed(1, 0, orchestrator.FeedRunStarted, "ENG-7", orchestrator.FeedPayload{RunID: "run-1"}),
		feed(2, 10*time.Minute, orchestrator.FeedInputAnswered, "ENG-7", orchestrator.FeedPayload{Via: "tracker", Actor: "tracker:Dana"}),
		feed(3, 20*time.Minute, orchestrator.FeedGuardrailBlocked, "ENG-7", orchestrator.FeedPayload{Kind: guardrails.KindForcePush}),
		feed(4, 25*time.Minute, orchestrator.FeedGuardrailApproved, "ENG-7", orchestrator.FeedPayload{Kind: guardrails.KindLaneHuman,
			Via: "dashboard", Actor: "dashboard:tok_op", RunID: "run-1"}),
		feed(5, 26*time.Minute, orchestrator.FeedGuardrailBlocked, "ENG-7", orchestrator.FeedPayload{Kind: guardrails.KindApprovalRejected,
			Via: "slack", Actor: "dashboard:tok_hub", RunID: "run-1"}),
		feed(6, 27*time.Minute, orchestrator.FeedGuardrailApproved, "ENG-8", orchestrator.FeedPayload{Via: "dashboard"}),
		feed(7, 80*time.Minute, orchestrator.FeedPRMerged, "ENG-7", orchestrator.FeedPayload{PRURL: "https://github.com/o/r/pull/9"}),
	}
	return s
}

func evidenceHistory() []orchestrator.CompletedRun {
	return []orchestrator.CompletedRun{
		{Identifier: "ENG-7", Title: "Fix the parser", RunID: "run-2", Kind: "worker", ProfileName: "coder", Backend: "claude",
			Status: "succeeded", StartedAt: evT0.Add(40 * time.Minute), FinishedAt: evT0.Add(70 * time.Minute), ElapsedMs: 1_800_000,
			PRURL: "https://github.com/o/r/pull/9", BranchName: "eng-7", Playbook: "bugfix", MemoryIDs: []string{"m1"},
			PromptDigest: strings.Repeat("b", 64), PromptParts: orchestrator.PromptParts{Soul: "s1", Template: "t1"},
			Checks: &orchestrator.RunChecks{
				Evidence: &orchestrator.GateResult{Required: []string{"tests", "ci"}, Passed: true, CheckedAt: evT0.Add(69 * time.Minute)},
				Signed:   &orchestrator.GateResult{Required: []string{"lint"}, Passed: true, AttestRunID: "20261120T094000Z_coder"},
			}},
		{Identifier: "ENG-8", RunID: "run-y", Status: "failed"},
		{Identifier: "ENG-7", RunID: "run-1", Kind: "", ProfileName: "coder", Status: "input_required",
			StartedAt: evT0, FinishedAt: evT0.Add(30 * time.Minute),
			Checks: &orchestrator.RunChecks{Evidence: &orchestrator.GateResult{Required: []string{"tests"}, Failed: []string{"tests"}}}},
	}
}

// TestBuildIssueEvidenceFromRealState assembles ENG-7's report from a real
// State, history and signed records: only ENG-7's data, runs oldest first
// with the live run last, approvals from the feed then the pending one,
// refusals, the push lane without paths, reviews without their text, and
// the PR marked merged.
func TestBuildIssueEvidenceFromRealState(t *testing.T) {
	signed := server.AttestationsResponse{Runs: []server.AttestationRunView{{
		RunID: "20261120T094000Z_coder", Profile: "coder",
		Records: []server.AttestationRecordView{
			{Kind: "check", Name: "lint", Command: "make lint", OutputTail: "agent output", ExitCode: 0, Verified: true, DurationMs: 1200},
			{Kind: "check", Name: "test", ExitCode: 2, Verified: true},
			{Kind: "seal", Verified: true},
		},
	}}}
	rep := buildIssueEvidence(evidenceInputs{
		identifier: "ENG-7", state: evidenceState(), history: evidenceHistory(), signed: signed,
		profiles: map[string]config.AgentProfile{"coder": {Command: "claude --model claude-sonnet-4-5 --verbose"},
			"reviewer": {Command: "codex --model=gpt-5"}},
		now: evT0.Add(2*time.Hour + 10*time.Minute),
	})

	assert.Equal(t, "Fix the parser", rep.Title)
	require.Len(t, rep.Runs, 3)
	assert.Equal(t, []string{"run-1", "run-2", "run-3"}, []string{rep.Runs[0].RunID, rep.Runs[1].RunID, rep.Runs[2].RunID})
	assert.Equal(t, "worker", rep.Runs[0].Kind, "an empty kind is a worker")
	assert.Equal(t, []string{"tests"}, rep.Runs[0].Checks.Evidence.Failed)
	assert.False(t, rep.Runs[0].Checks.Evidence.Passed)
	r2 := rep.Runs[1]
	assert.Equal(t, "claude-sonnet-4-5", r2.ProfileModel)
	assert.Equal(t, "succeeded", r2.Status)
	assert.Equal(t, strings.Repeat("b", 64), r2.PromptDigest)
	require.NotNil(t, r2.PromptParts)
	assert.Equal(t, "s1", r2.PromptParts.Soul)
	assert.Equal(t, "bugfix", r2.Playbook)
	assert.Equal(t, []string{"m1"}, r2.MemoryIDs)
	assert.Equal(t, "20261120T094000Z_coder", r2.Checks.Signed.AttestRunID)
	live := rep.Runs[2]
	assert.True(t, live.Live)
	assert.Equal(t, "running", live.Status)
	assert.Equal(t, "gpt-5", live.ProfileModel)
	assert.Equal(t, 1, live.RetryAttempt)
	assert.Equal(t, int64(10*time.Minute/time.Millisecond), live.ElapsedMs)
	require.Len(t, live.Steers, 1)
	assert.Equal(t, "check the tests", live.Steers[0].Text, "unredacted here: the handler redacts per scope")
	assert.Equal(t, 2, rep.Limits.HistoryRuns)

	require.NotNil(t, rep.Retry)
	assert.Equal(t, 2, rep.Retry.Attempt)

	require.Len(t, rep.Reviews.Verdicts, 1)
	assert.Equal(t, server.EvidenceVerdict{Profile: "reviewer", Verdict: "approve", Backend: "codex",
		RecordedAt: evT0.Add(90 * time.Minute), Reasons: 1, Comments: 2}, rep.Reviews.Verdicts[0])
	require.NotNil(t, rep.Reviews.Outcome)
	assert.False(t, rep.Reviews.Outcome.Blocked)

	require.Len(t, rep.SignedChecks, 1)
	sr := rep.SignedChecks[0]
	assert.True(t, sr.Sealed)
	assert.True(t, sr.Verified)
	require.Len(t, sr.Checks, 2)
	assert.True(t, sr.Checks[0].Passed)
	assert.False(t, sr.Checks[1].Passed)
	assert.Equal(t, 2, sr.Checks[1].ExitCode)

	assert.Equal(t, []server.EvidenceGuardrailBlock{{At: evT0.Add(20 * time.Minute), Kind: guardrails.KindForcePush}}, rep.GuardrailBlocks)
	require.NotNil(t, rep.PushLane)
	assert.Equal(t, server.EvidencePushLane{Lane: "human", PathsTotal: 3, PathsKept: 1, Truncated: true,
		WriterBackends: []string{"claude"}, RunID: "run-1", UpdatedAt: evT0.Add(30 * time.Minute)}, *rep.PushLane)

	require.Len(t, rep.Approvals, 4)
	assert.Equal(t, server.EvidenceApproval{At: evT0.Add(10 * time.Minute), Kind: "input", Decision: "answered",
		Via: "tracker", Actor: "tracker:Dana"}, rep.Approvals[0])
	assert.Equal(t, server.EvidenceApproval{At: evT0.Add(25 * time.Minute), Kind: "guardrail", Decision: "approved",
		Via: "dashboard", Actor: "dashboard:tok_op", RunID: "run-1", Push: true}, rep.Approvals[1])
	assert.Equal(t, "rejected", rep.Approvals[2].Decision)
	assert.Equal(t, "slack", rep.Approvals[2].Via)
	assert.False(t, rep.Approvals[2].Push)
	assert.Equal(t, server.EvidenceApproval{At: evT0.Add(2*time.Hour + 5*time.Minute), Kind: "guardrail", Decision: "pending",
		RunID: "run-3", Push: true, PathsTotal: 4}, rep.Approvals[3])

	require.Len(t, rep.PullRequests, 1)
	pr := rep.PullRequests[0]
	assert.Equal(t, "https://github.com/o/r/pull/9", pr.URL)
	assert.Equal(t, "merged", pr.State)
	assert.Equal(t, "eng-7", pr.Branch)
	require.NotNil(t, pr.MergedAt)
	require.NotNil(t, rep.Limits.FeedSince)
	assert.Equal(t, evT0, *rep.Limits.FeedSince)

	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	for _, never := range []string{"secret finding text", "make lint", "agent output", "git push --force",
		".github/workflows", "deploy/prod.yml", "ENG-8", "run-x", "run-y", "pull/10"} {
		assert.NotContains(t, string(raw), never)
	}
}

// TestBuildIssueEvidenceEmptyIssue: an issue the daemon knows nothing about
// gets an empty report with arrays, not nulls.
func TestBuildIssueEvidenceEmptyIssue(t *testing.T) {
	rep := buildIssueEvidence(evidenceInputs{identifier: "ENG-404", state: orchestrator.NewState(&config.Config{}), now: evT0})
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	for _, key := range []string{`"runs":[]`, `"approvals":[]`, `"signedChecks":[]`, `"pullRequests":[]`, `"verdicts":[]`} {
		assert.Contains(t, string(raw), key)
	}
	assert.Nil(t, rep.Limits.FeedSince)
}
