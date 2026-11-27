//go:build unix

package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// assertWatchShowsPolicyChange checks that `itervox watch` prints the raised
// policy change. It uses the fake daemon and feed from the client command
// tests, which are Unix-only.
func assertWatchShowsPolicyChange(t *testing.T, ev orchestrator.FeedEvent, runID string) {
	t.Helper()
	feed := &fakeFeed{}
	feed.add(ev.Kind, ev.Identifier, server.FeedPayload(ev.Payload))
	d := newFakeDaemon(t, func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			writeJSONBody(rw, map[string]any{"maxConcurrentAgents": 1})
		case "/api/v1/feed":
			feed.serve(t, rw, r)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	})
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--once", "--since", "0")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "POLICY CHANGED while agents ran: .itervox/settings.jsonc (active runs: "+runID+") — possibly changed by a running agent")
}
