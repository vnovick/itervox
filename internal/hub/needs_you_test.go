package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// TestHubProjectsCarryMembersNeedsYou: the hub's project list hands each
// member's summary on as the member sent it, so its "Needs you" rows (the
// oldest first, with waitingSince) reach a viewer of the hub — the desktop
// app reads hub-routed projects from it — without the hub reading them.
func TestHubProjectsCarryMembersNeedsYou(t *testing.T) {
	alpha := newFakeMember(t, "alpha", time.Now().Add(-time.Hour))
	input := []server.InputRequiredRow{
		{Identifier: "ENG-2", State: "input_required", Context: "Ship it?", QueuedAt: "2026-10-22T10:30:00Z"},
		{Identifier: "ENG-1", State: "input_required", Context: "Agent wants to run: `npm publish`", GuardrailApproval: true,
			QueuedAt: "2026-10-22T09:00:00Z"},
	}
	cfg := server.Config{
		Snapshot: func() server.StateSnapshot {
			return server.StateSnapshot{ProjectName: "alpha", InputRequired: input,
				NeedsYou: server.BuildNeedsYou(input, nil, "alpha")}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    memberBootstrap,
		Tokens:      alpha.store,
		Dispatch:    alpha.disp,
		Version:     "test-alpha",
	}
	alpha.srv = server.New(cfg)
	reg := &memberList{}
	reg.add(alpha.rec)
	th := startTestHub(t, Config{Members: reg.list})
	waitProxied(t, th, "alpha")

	resp := doHub(t, th.ts.URL+"/hub/api/v1/projects", http.MethodGet, th.secrets["viewer"], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var projects ProjectsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&projects))
	require.Len(t, projects.Projects, 1)
	var sum server.Summary
	require.NoError(t, json.Unmarshal(projects.Projects[0].Summary, &sum))
	require.Len(t, sum.NeedsYou, 2)
	assert.Equal(t, "ENG-1", sum.NeedsYou[0].Identifier, "oldest first")
	assert.Equal(t, server.NeedsYouApproval, sum.NeedsYou[0].Kind)
	assert.Equal(t, "2026-10-22T09:00:00Z", sum.NeedsYou[0].WaitingSince)
	assert.Equal(t, "alpha", sum.NeedsYou[1].Project)
}
