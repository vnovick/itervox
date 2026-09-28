package server_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-010 — PUT /api/v1/settings/automations must reject a rate_limited rule
// whose switch_to_backend disagrees with the switch profile's EFFECTIVE
// command (its own command, else the client's default agent command) with a
// 400 client error, BEFORE SetAutomations persists anything. The direct and
// inherited rows must produce the same response shape.
func TestHandleSetAutomations_RejectsSwitchBackendCommandMismatch(t *testing.T) {
	rows := []struct {
		name            string
		fallbackCommand string
	}{
		{"direct mismatch", "claude --model opus"},
		{"inherited mismatch", ""}, // inherits DefaultAgentCommand() = "claude --model opus"
	}
	body := `{"automations":[{"id":"rl-switch","enabled":true,"profile":"primary","trigger":{"type":"rate_limited"},"policy":{"autoResume":true,"switchToProfile":"fallback","switchToBackend":"codex"}}]}`
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			cfg := makeTestConfig(baseSnap())
			setCalled := false
			cfg.Client = &server.FuncClient{
				ProfileDefsFn: func() map[string]server.ProfileDef {
					return map[string]server.ProfileDef{
						"primary":  {Command: "claude", Enabled: true},
						"fallback": {Command: row.fallbackCommand, Enabled: true},
					}
				},
				DefaultAgentCommandFn: func() string { return "claude --model opus" },
				// Models the cmd/itervox adapter, which also rejects the
				// mismatch before persisting — reaching it at all is the
				// pre-fix behaviour (a 500 set_automations_failed).
				SetAutomationsFn: func([]server.AutomationDef) error {
					setCalled = true
					return errors.New(`automation "rl-switch": policy.switch_to_backend "codex" does not match switch_to_profile "fallback"`)
				},
			}
			srv := server.New(cfg)
			w := putJSON(t, srv, "/api/v1/settings/automations", body)

			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.False(t, setCalled, "SetAutomations must never be invoked for a rejected rule (nothing persisted)")
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			raw := w.Body.String()
			assert.Contains(t, raw, "invalid_policy")
			assert.Contains(t, raw, "switchToBackend")
			assert.Contains(t, raw, "switch_to_backend")
		})
	}
}
