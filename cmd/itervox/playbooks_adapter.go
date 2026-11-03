package main

import (
	"context"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/playbook"
	"github.com/vnovick/itervox/internal/server"
)

// playbooksAdapter backs the #218 endpoints with the orchestrator: the
// playbooks on disk, the per-issue override (an event to the loop), the
// decision logs in an issue's worktree and the verify-skill issue. Reads
// files or calls the tracker on the HTTP goroutine, never on the event loop.
type playbooksAdapter struct {
	orch *orchestrator.Orchestrator
}

func (a playbooksAdapter) Playbooks() server.PlaybooksResponse {
	resp := server.PlaybooksResponse{Enabled: a.orch.PlaybooksEnabled()}
	for _, p := range a.orch.Playbooks() {
		resp.Playbooks = append(resp.Playbooks, server.PlaybookRow{
			Name: p.Name, Description: p.Description,
			Keywords:        append([]string{}, p.Keywords...),
			RequireEvidence: append([]string{}, p.RequireEvidence...),
		})
	}
	return resp
}

func (a playbooksAdapter) SetIssuePlaybook(identifier, name string) error {
	name = strings.TrimSpace(name)
	if name != "" && name != playbook.None &&
		!slices.ContainsFunc(a.orch.Playbooks(), func(p playbook.Playbook) bool { return p.Name == name }) {
		return server.ErrUnknownPlaybook
	}
	if !a.orch.SetPlaybookOverride(identifier, name) {
		return server.ErrBusy
	}
	return nil
}

func (a playbooksAdapter) Decisions(identifier string) []server.DecisionRunView {
	runs := a.orch.Decisions(identifier)
	out := make([]server.DecisionRunView, 0, len(runs))
	for _, r := range runs {
		v := server.DecisionRunView{Path: r.Path, RunTimestamp: r.RunTimestamp, Role: r.Role,
			Truncated: r.Truncated, Skipped: r.Skipped, Decisions: make([]server.DecisionRowView, 0, len(r.Decisions))}
		for _, d := range r.Decisions {
			v.Decisions = append(v.Decisions, server.DecisionRowView{Time: d.Time, At: d.At, Decision: d.Decision, Why: d.Why, Alternative: d.Alternative})
		}
		out = append(out, v)
	}
	return out
}

func (a playbooksAdapter) CreateVerifySkillIssue(ctx context.Context) (server.VerifySkillIssueResult, error) {
	created, err := a.orch.CreateVerifySkillIssue(ctx)
	if err != nil {
		return server.VerifySkillIssueResult{}, err
	}
	var out server.VerifySkillIssueResult
	if created != nil {
		out.Identifier = created.Identifier
		if created.URL != nil {
			out.URL = *created.URL
		}
	}
	return out, nil
}
