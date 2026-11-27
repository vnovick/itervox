package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// needsYouState is a snapshot with three waiting requests queued at
// different times (listed newest first) and one whose time is unknown.
func needsYouState(now time.Time) map[string]any {
	at := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	return map[string]any{
		"maxConcurrentAgents": 2,
		"inputRequired": []any{
			map[string]any{"identifier": "ENG-9", "state": "input_required", "context": "No time?"},
			map[string]any{"identifier": "ENG-3", "state": "input_required", "context": "Ship it?", "queuedAt": at(5 * time.Minute)},
			map[string]any{"identifier": "ENG-12", "state": "input_required", "context": "Which database?", "queuedAt": at(2*time.Hour + 14*time.Minute + 10*time.Second)},
			map[string]any{"identifier": "ENG-7", "state": "input_required", "guardrailApproval": true,
				"guardrailLanePaths": []any{".github/workflows/ci.yml"},
				"context":            "Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml`\n\nThese files…",
				"queuedAt":           at(40 * time.Minute)},
			map[string]any{"identifier": "ENG-5", "state": "pending_input_resume", "context": "Reply received", "queuedAt": at(5 * time.Hour)},
		},
	}
}

func needsYouDaemon(t *testing.T, state map[string]any) *fakeDaemon {
	t.Helper()
	feed := &fakeFeed{}
	return newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			writeJSONBody(w, state)
		case "/api/v1/feed":
			feed.serve(t, w, r)
		default:
			writeJSONBody(w, map[string]any{"ok": true})
		}
	})
}

// TestWatchNeedsYouOldestFirstWithWait: NEEDS YOU lists the requests
// oldest first with how long each has waited ("2h14m"), a held pipeline
// push labelled as one, and a request without a time last; an answered,
// resuming question is not listed.
func TestWatchNeedsYouOldestFirstWithWait(t *testing.T) {
	d := needsYouDaemon(t, needsYouState(time.Now()))
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--once", "--since", "0")
	require.Equal(t, 0, code, errOut)

	block := out[strings.Index(out, "NEEDS YOU"):]
	order := []string{"? ENG-12     2h14m  question: Which database?", "! ENG-7      40m", "? ENG-3      5m", "? ENG-9      question: No time?"}
	last := -1
	for _, want := range order {
		i := strings.Index(block, want)
		require.GreaterOrEqual(t, i, 0, "missing %q in:\n%s", want, block)
		assert.Greater(t, i, last, "%q is out of order:\n%s", want, block)
		last = i
	}
	assert.Contains(t, block, "pipeline change: Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml`")
	assert.NotContains(t, out, "ENG-5", "an answered question waits on nobody")
}

// TestWatchJSONNeedsYouCarriesWaitingSince: --json lists needsYou oldest
// first, each with its waitingSince (absent when the daemon gave none).
func TestWatchJSONNeedsYouCarriesWaitingSince(t *testing.T) {
	now := time.Now()
	d := needsYouDaemon(t, needsYouState(now))
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--json", "--once", "--since", "0")
	require.Equal(t, 0, code, errOut)
	var status struct {
		NeedsYou []struct {
			Identifier   string     `json:"identifier"`
			Kind         string     `json:"kind"`
			WaitingSince *time.Time `json:"waitingSince"`
		} `json:"needsYou"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &status), out)
	ids := make([]string, len(status.NeedsYou))
	for i, n := range status.NeedsYou {
		ids[i] = n.Identifier
	}
	assert.Equal(t, []string{"ENG-12", "ENG-7", "ENG-3", "ENG-9"}, ids)
	require.NotNil(t, status.NeedsYou[0].WaitingSince)
	assert.WithinDuration(t, now.Add(-(2*time.Hour + 14*time.Minute + 10*time.Second)), *status.NeedsYou[0].WaitingSince, time.Second)
	assert.Equal(t, "approval", status.NeedsYou[1].Kind, "a pipeline change is an approval")
	assert.Nil(t, status.NeedsYou[3].WaitingSince)
}
