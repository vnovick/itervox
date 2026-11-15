package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/playbook"
	"github.com/vnovick/itervox/internal/server"
)

// TestApplyPlaybookLabelsTheCard (#218): an idle issue's card shows the
// playbook dispatch would choose (with the override from the snapshot); a
// running issue's card shows the playbook its run actually has.
func TestApplyPlaybookLabelsTheCard(t *testing.T) {
	snap := orchestrator.State{
		PlaybookOverrides: map[string]string{"ENG-1": "docs"},
		Running: map[string]*orchestrator.RunEntry{
			"id2": {Playbook: "perf", PlaybookSource: playbook.SourceKeyword},
		},
	}
	var gotOverride string
	choose := func(_ domain.Issue, override string) playbook.Choice {
		gotOverride = override
		return playbook.Choice{Playbook: playbook.Playbook{Name: "docs"}, Source: playbook.SourceOverride}
	}

	var idle server.TrackerIssue
	ApplyPlaybook(&idle, domain.Issue{ID: "id1", Identifier: "ENG-1"}, snap, choose)
	assert.Equal(t, "docs", gotOverride)
	assert.Equal(t, server.TrackerIssue{Playbook: "docs", PlaybookSource: "override", PlaybookOverride: "docs"}, idle)

	var running server.TrackerIssue
	ApplyPlaybook(&running, domain.Issue{ID: "id2", Identifier: "ENG-2"}, snap, choose)
	assert.Equal(t, "perf", running.Playbook)
	assert.Equal(t, playbook.SourceKeyword, running.PlaybookSource)

	var off server.TrackerIssue
	ApplyPlaybook(&off, domain.Issue{ID: "id3", Identifier: "ENG-3"}, snap, nil)
	assert.Empty(t, off.Playbook)
}
