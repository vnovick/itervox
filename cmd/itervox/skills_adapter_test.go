package main

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
)

func TestRecentlyActiveProfilesFromRuntimeState(t *testing.T) {
	t.Parallel()

	state := orchestrator.NewState(&config.Config{})
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:       domain.Issue{ID: "id1", Identifier: "ENG-1"},
		ProfileName: "running-profile",
	}
	state.InputRequiredIssues["ENG-2"] = &orchestrator.InputRequiredEntry{ProfileName: "input-profile"}
	state.PendingInputResumes["ENG-3"] = &orchestrator.PendingInputResumeEntry{ProfileName: "pending-profile"}
	state.PausedSessions["ENG-4"] = &orchestrator.PausedSessionInfo{ProfileName: "paused-profile"}

	active := recentlyActiveProfilesFromState([]orchestrator.CompletedRun{
		{ProfileName: "completed-profile"},
		{ProfileName: ""},
	}, state)

	for _, name := range []string{
		"completed-profile",
		"running-profile",
		"input-profile",
		"pending-profile",
		"paused-profile",
	} {
		if _, ok := active[name]; !ok {
			t.Fatalf("expected %q in active profile set: %#v", name, active)
		}
	}
}

func TestRecentlyActiveProfilesFromRuntimeStateReturnsNilWithoutEvidence(t *testing.T) {
	t.Parallel()

	state := orchestrator.NewState(&config.Config{})
	active := recentlyActiveProfilesFromState(nil, state)
	if active != nil {
		t.Fatalf("expected nil active profile set without runtime evidence, got %#v", active)
	}
}

func TestRecentlyActiveProfilesFromRuntimeStateReturnsNilForLegacyHistoryWithoutProfiles(t *testing.T) {
	t.Parallel()

	state := orchestrator.NewState(&config.Config{})
	active := recentlyActiveProfilesFromState([]orchestrator.CompletedRun{
		{Identifier: "ENG-1"},
	}, state)
	if active != nil {
		t.Fatalf("expected nil active profile set for legacy history without profile names, got %#v", active)
	}
}
