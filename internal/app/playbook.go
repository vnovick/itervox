package app

import (
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/playbook"
	"github.com/vnovick/itervox/internal/server"
)

// ApplyPlaybook sets the card's playbook fields (#218): the operator's
// override from the snapshot, and the playbook the issue's run has (a
// running issue) or would get (choose, which applies dispatch's rule: the
// override, else a label, else the keyword classifier). choose may be nil
// (playbooks off).
func ApplyPlaybook(ti *server.TrackerIssue, issue domain.Issue, snap orchestrator.State, choose func(domain.Issue, string) playbook.Choice) {
	override := snap.PlaybookOverrides[issue.Identifier]
	ti.PlaybookOverride = override
	if re, ok := snap.Running[issue.ID]; ok && re != nil && re.Kind != "reviewer" && re.Kind != "automation" {
		ti.Playbook, ti.PlaybookSource = re.Playbook, re.PlaybookSource
		return
	}
	if choose == nil {
		return
	}
	if c := choose(issue, override); c.Name() != "" {
		ti.Playbook, ti.PlaybookSource = c.Name(), c.Source
	}
}
