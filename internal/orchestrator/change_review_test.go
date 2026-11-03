package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestIssuePRURLPicksTheLatestManagedPR (#238): an issue's PR is the
// managed PR its runs recorded most recently; other issues' PRs and an
// unknown issue give "".
func TestIssuePRURLPicksTheLatestManagedPR(t *testing.T) {
	now := time.Now()
	o := &Orchestrator{}
	o.lastSnap = State{ManagedPRs: map[string]ManagedPR{
		"https://github.com/acme/app/pull/1": {PRURL: "https://github.com/acme/app/pull/1", Identifier: "ENG-1", RecordedAt: now.Add(-time.Hour)},
		"https://github.com/acme/app/pull/2": {PRURL: "https://github.com/acme/app/pull/2", Identifier: "ENG-1", RecordedAt: now},
		"https://github.com/acme/app/pull/3": {PRURL: "https://github.com/acme/app/pull/3", Identifier: "ENG-2", RecordedAt: now.Add(time.Hour)},
	}}
	assert.Equal(t, "https://github.com/acme/app/pull/2", o.IssuePRURL("ENG-1"))
	assert.Equal(t, "https://github.com/acme/app/pull/3", o.IssuePRURL("ENG-2"))
	assert.Empty(t, o.IssuePRURL("ENG-9"))
	assert.Empty(t, o.IssuePRURL(""))
	assert.Empty(t, o.WorktreePath("ENG-1"), "no workspace manager: no path")
}
