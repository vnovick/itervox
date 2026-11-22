package main

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// evidence_build.go — the per-issue evidence report (GET
// /api/v1/issues/{identifier}/evidence): what ran, what was checked, who
// approved. Assembled on the HTTP goroutine from Snapshot() (a copy of the
// event-loop state), the run history, the event feed and the attestation
// store; it reads nothing the daemon does not already keep and never
// touches the event loop. The server redacts it for the caller's scope.

// evidenceAdapter implements server.EvidenceClient.
type evidenceAdapter struct {
	orch *orchestrator.Orchestrator
}

func (a evidenceAdapter) IssueEvidence(identifier string) (server.EvidenceReport, error) {
	signed, err := attestationsAdapter(a).Attestations(identifier)
	if err != nil {
		return server.EvidenceReport{}, err
	}
	return buildIssueEvidence(evidenceInputs{
		identifier: identifier,
		state:      a.orch.Snapshot(),
		history:    a.orch.RunHistory(),
		signed:     signed,
		profiles:   a.orch.ProfilesCfg(),
		now:        time.Now(),
	}), nil
}

// ProvideInputAs implements server.InputActorProvider: the reply's actor
// reaches the feed (and the evidence report's approvals).
func (a *orchestratorAdapter) ProvideInputAs(identifier, message, via, actor string) error {
	return mapIssueControlErr(a.orch.ProvideInputAs(identifier, message, via, actor))
}

// DismissInputAs implements server.InputActorProvider.
func (a *orchestratorAdapter) DismissInputAs(identifier, actor string) error {
	return mapIssueControlErr(a.orch.DismissInputAs(identifier, actor))
}

// evidenceInputs are the sources of one report.
type evidenceInputs struct {
	identifier string
	state      orchestrator.State
	history    []orchestrator.CompletedRun
	signed     server.AttestationsResponse
	profiles   map[string]config.AgentProfile
	now        time.Time
}

// buildIssueEvidence assembles the unredacted report for in.identifier.
func buildIssueEvidence(in evidenceInputs) server.EvidenceReport {
	id := in.identifier
	s := in.state
	rep := server.EvidenceReport{
		Identifier:  id,
		GeneratedAt: in.now,
		Runs:        []server.EvidenceRun{},
		Approvals:   []server.EvidenceApproval{},
		Limits: server.EvidenceLimits{
			FeedMaxAgeHours:    int(orchestrator.FeedMaxAge / time.Hour),
			ReviewsSessionOnly: true,
		},
	}
	for _, r := range in.history {
		if r.Identifier != id {
			continue
		}
		rep.Runs = append(rep.Runs, evidenceRunFromHistory(r, in.profiles))
		rep.Limits.HistoryRuns++
		rep.Title = cmp.Or(r.Title, rep.Title)
	}
	slices.SortStableFunc(rep.Runs, func(a, b server.EvidenceRun) int { return a.StartedAt.Compare(b.StartedAt) })
	var live *orchestrator.RunEntry
	for _, r := range s.Running {
		if r != nil && r.Issue.Identifier == id {
			live = r
			rep.Runs = append(rep.Runs, evidenceRunFromLive(r, in.profiles, in.now))
			rep.Title = cmp.Or(r.Issue.Title, rep.Title)
		}
	}
	for _, r := range s.RetryAttempts {
		if r != nil && r.Identifier == id {
			rep.Retry = &server.EvidenceRetry{Attempt: r.Attempt, DueAt: r.DueAt}
		}
	}
	rep.Reviews = evidenceReviews(s, id)
	rep.SignedChecks = evidenceSignedRuns(in.signed)
	if lane, ok := s.PushLanes[id]; ok {
		rep.PushLane = &server.EvidencePushLane{
			Lane: lane.Lane, PathsTotal: lane.PathsTotal, PathsKept: len(lane.Paths),
			Truncated:      lane.PathsTotal > len(lane.Paths),
			WriterBackends: slices.Clone(lane.WriterBackends), RunID: lane.RunID, UpdatedAt: lane.UpdatedAt,
		}
	}
	prs := newEvidencePRs()
	for _, r := range rep.Runs {
		prs.add(r.PRURL, r.BranchName, nil)
	}
	for _, pr := range s.ManagedPRs {
		if pr.Identifier == id {
			at := pr.RecordedAt
			prs.add(pr.PRURL, pr.Branch, &at)
		}
	}
	if len(s.Feed) > 0 {
		oldest := s.Feed[0].TS
		rep.Limits.FeedSince = &oldest
	}
	for _, ev := range s.Feed {
		if ev.Identifier != id {
			continue
		}
		p := ev.Payload
		switch ev.Kind {
		case orchestrator.FeedInputAnswered:
			decision := server.EvidenceDecisionAnswered
			if p.Outcome == "dismissed" {
				decision = server.EvidenceDecisionDismissed
			}
			rep.Approvals = append(rep.Approvals, server.EvidenceApproval{At: ev.TS, Kind: server.EvidenceApprovalInput,
				Decision: decision, Via: p.Via, RunID: p.RunID, Actor: p.Actor})
		case orchestrator.FeedGuardrailApproved:
			rep.Approvals = append(rep.Approvals, server.EvidenceApproval{At: ev.TS, Kind: server.EvidenceApprovalGuardrail,
				Decision: server.EvidenceDecisionApproved, Via: p.Via, RunID: p.RunID, Actor: p.Actor,
				Push: p.Kind == guardrails.KindLaneHuman})
		case orchestrator.FeedGuardrailBlocked:
			if p.Kind == guardrails.KindApprovalRejected || p.Kind == guardrails.KindLaneRejected {
				rep.Approvals = append(rep.Approvals, server.EvidenceApproval{At: ev.TS, Kind: server.EvidenceApprovalGuardrail,
					Decision: server.EvidenceDecisionRejected, Via: p.Via, RunID: p.RunID, Actor: p.Actor,
					Push: p.Kind == guardrails.KindLaneRejected})
				continue
			}
			rep.GuardrailBlocks = append(rep.GuardrailBlocks, server.EvidenceGuardrailBlock{At: ev.TS, Kind: p.Kind})
		case orchestrator.FeedPROpened:
			prs.add(p.PRURL, "", nil)
		case orchestrator.FeedPRMerged:
			prs.merged(p.PRURL, ev.TS)
		}
	}
	if wait, ok := s.InputRequiredIssues[id]; ok && wait != nil {
		a := server.EvidenceApproval{At: wait.QueuedAt, Kind: server.EvidenceApprovalInput, Decision: server.EvidenceDecisionPending}
		if wait.GuardrailApprovalID != "" {
			a.Kind = server.EvidenceApprovalGuardrail
			a.Push = wait.GuardrailLanePaths != nil
			a.PathsTotal = wait.GuardrailLanePathsTotal
			if live != nil {
				a.RunID = live.RunID
			}
		}
		rep.Approvals = append(rep.Approvals, a)
	}
	rep.PullRequests = prs.list()
	return rep
}

func evidenceRunFromHistory(r orchestrator.CompletedRun, profiles map[string]config.AgentProfile) server.EvidenceRun {
	finished := r.FinishedAt
	return server.EvidenceRun{
		RunID: r.RunID, Kind: cmp.Or(r.Kind, "worker"), Profile: r.ProfileName, Backend: r.Backend,
		ProfileModel: profileModel(profiles, r.ProfileName), WorkerHost: r.WorkerHost,
		Status: r.Status, StartedAt: r.StartedAt, FinishedAt: &finished, ElapsedMs: r.ElapsedMs, TurnCount: r.TurnCount,
		AutomationID: r.AutomationID, TriggerType: r.TriggerType,
		PromptDigest: r.PromptDigest, PromptParts: historyPromptParts(r.PromptParts),
		RulesHash: r.RulesHash, RuleIDs: slices.Clone(r.RuleIDs), MemoryIDs: slices.Clone(r.MemoryIDs), Playbook: r.Playbook,
		Steers: evidenceSteers(r.Steers), SteersDropped: r.SteersDropped,
		BranchName: r.BranchName, PRURL: r.PRURL, ReviewVerdict: r.ReviewVerdict,
		Checks: evidenceRunChecks(r.Checks),
	}
}

func evidenceRunFromLive(r *orchestrator.RunEntry, profiles map[string]config.AgentProfile, now time.Time) server.EvidenceRun {
	run := server.EvidenceRun{
		RunID: r.RunID, Kind: cmp.Or(r.Kind, "worker"), Profile: r.ProfileName, Backend: r.Backend,
		ProfileModel: profileModel(profiles, r.ProfileName), WorkerHost: r.WorkerHost,
		Status: "running", Live: true, StartedAt: r.StartedAt, ElapsedMs: now.Sub(r.StartedAt).Milliseconds(), TurnCount: r.TurnCount,
		AutomationID: r.AutomationID, TriggerType: r.TriggerType,
		PromptDigest: r.PromptDigest, PromptParts: historyPromptParts(r.PromptParts),
		RulesHash: r.RulesHash, RuleIDs: slices.Clone(r.RuleIDs), MemoryIDs: slices.Clone(r.MemoryIDs), Playbook: r.Playbook,
		Steers: evidenceSteers(r.Steers), SteersDropped: r.SteersDropped,
		BranchName: r.BranchName, PRURL: r.PRURL,
	}
	if r.RetryAttempt != nil {
		run.RetryAttempt = *r.RetryAttempt
	}
	return run
}

func evidenceSteers(steers []orchestrator.RunSteer) []server.EvidenceSteer {
	if len(steers) == 0 {
		return nil
	}
	out := make([]server.EvidenceSteer, len(steers))
	for i, st := range steers {
		out[i] = server.EvidenceSteer{At: st.At, Delivery: st.Delivery, Text: st.Text}
	}
	return out
}

func evidenceRunChecks(c *orchestrator.RunChecks) *server.EvidenceRunChecks {
	if c == nil || (c.Evidence == nil && c.Signed == nil) {
		return nil
	}
	gate := func(g *orchestrator.GateResult) *server.EvidenceGate {
		if g == nil {
			return nil
		}
		return &server.EvidenceGate{Required: slices.Clone(g.Required), Failed: slices.Clone(g.Failed),
			Passed: g.Passed, CheckedAt: g.CheckedAt, AttestRunID: g.AttestRunID}
	}
	return &server.EvidenceRunChecks{Evidence: gate(c.Evidence), Signed: gate(c.Signed)}
}

// evidenceReviews are id's reviewer verdicts (counts of reasons and
// comments, never their text) and the quorum outcome.
func evidenceReviews(s orchestrator.State, id string) server.EvidenceReviews {
	out := server.EvidenceReviews{Verdicts: []server.EvidenceVerdict{}}
	for _, v := range s.ReviewVerdicts[id] {
		out.Verdicts = append(out.Verdicts, server.EvidenceVerdict{
			Profile: v.Profile, Verdict: orchestrator.NormalizeReviewVerdict(v.Verdict), Backend: v.Backend,
			RecordedAt: v.RecordedAt, Reasons: len(v.Reasons), Comments: len(v.Comments),
		})
	}
	if o, ok := s.ReviewOutcomes[id]; ok {
		out.Outcome = &server.EvidenceOutcome{Blocked: o.Blocked, Approvals: o.Approvals, Blocks: o.Blocks, Quorum: o.Quorum}
	}
	return out
}

// evidenceSignedRuns summarizes the signed check records: names, exit
// codes and verification, never a command or an output tail.
func evidenceSignedRuns(resp server.AttestationsResponse) []server.EvidenceSignedRun {
	out := []server.EvidenceSignedRun{}
	for _, run := range resp.Runs {
		sr := server.EvidenceSignedRun{AttestRunID: run.RunID, Profile: run.Profile, Checks: []server.EvidenceSignedCheck{},
			Verified: len(run.Records) > 0}
		for _, r := range run.Records {
			sr.Verified = sr.Verified && r.Verified
			switch r.Kind {
			case attest.KindSeal:
				sr.Sealed = true
			case attest.KindCheck:
				sr.Checks = append(sr.Checks, server.EvidenceSignedCheck{
					Name: r.Name, Passed: r.ExitCode == 0 && !r.TimedOut && r.Error == "",
					ExitCode: r.ExitCode, TimedOut: r.TimedOut, Verified: r.Verified,
					StartedAt: r.StartedAt, DurationMs: r.DurationMs,
				})
			}
		}
		out = append(out, sr)
	}
	return out
}

// profileModel is the model the profile's command names (--model X or
// --model=X); "" when it names none.
func profileModel(profiles map[string]config.AgentProfile, name string) string {
	p, ok := profiles[name]
	if !ok {
		return ""
	}
	fields := strings.Fields(p.Command)
	for i, f := range fields {
		if v, ok := strings.CutPrefix(f, "--model="); ok {
			return v
		}
		if f == "--model" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// evidencePRs collects the issue's PRs in first-seen order.
type evidencePRs struct {
	order []string
	byURL map[string]*server.EvidencePullRequest
}

func newEvidencePRs() *evidencePRs {
	return &evidencePRs{byURL: map[string]*server.EvidencePullRequest{}}
}

func (p *evidencePRs) add(url, branch string, recordedAt *time.Time) *server.EvidencePullRequest {
	if url == "" {
		return nil
	}
	pr, ok := p.byURL[url]
	if !ok {
		pr = &server.EvidencePullRequest{URL: url, State: "recorded"}
		p.byURL[url] = pr
		p.order = append(p.order, url)
	}
	pr.Branch = cmp.Or(pr.Branch, branch)
	if recordedAt != nil {
		pr.RecordedAt = recordedAt
	}
	return pr
}

func (p *evidencePRs) merged(url string, at time.Time) {
	if pr := p.add(url, "", nil); pr != nil {
		pr.State, pr.MergedAt = "merged", &at
	}
}

func (p *evidencePRs) list() []server.EvidencePullRequest {
	out := make([]server.EvidencePullRequest, 0, len(p.order))
	for _, u := range p.order {
		out = append(out, *p.byURL[u])
	}
	return out
}
