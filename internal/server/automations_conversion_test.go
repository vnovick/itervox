package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAutomationConfigsFromDefsPreservesPolicyFields(t *testing.T) {
	t.Parallel()

	cfgs := automationConfigsFromDefs([]AutomationDef{{
		ID:      "rate-limit-switch",
		Enabled: true,
		Profile: "codex",
		Trigger: AutomationTriggerDef{Type: "rate_limited"},
		Policy: AutomationPolicyDef{
			AutoResume:      true,
			SwitchToProfile: "reviewer",
			SwitchToBackend: "codex",
			CooldownMinutes: 45,
		},
	}})

	if len(cfgs) != 1 {
		t.Fatalf("expected one automation config, got %d", len(cfgs))
	}
	if got := cfgs[0].Policy.SwitchToProfile; got != "reviewer" {
		t.Fatalf("SwitchToProfile not preserved: %q", got)
	}
	if got := cfgs[0].Policy.SwitchToBackend; got != "codex" {
		t.Fatalf("SwitchToBackend not preserved: %q", got)
	}
	if got := cfgs[0].Policy.CooldownMinutes; got != 45 {
		t.Fatalf("CooldownMinutes not preserved: %d", got)
	}
	if !cfgs[0].Policy.AutoResume {
		t.Fatalf("AutoResume not preserved")
	}
}

func TestWriteAutomationValidationErrorMapsRateLimitPolicyFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		err   string
		field string
	}{
		{
			name:  "auto switch",
			err:   `automation "rl": rate_limited automations require policy.auto_switch: true or policy.auto_resume: true`,
			field: "autoResume",
		},
		{
			name:  "switch profile",
			err:   `automation "rl": rate_limited automations require policy.switch_to_profile`,
			field: "switchToProfile",
		},
		{
			name:  "unknown switch profile",
			err:   `automation "rl" references unknown switch_to_profile "fallback"`,
			field: "switchToProfile",
		},
		{
			name:  "switch backend",
			err:   `automation "rl": policy.switch_to_backend must be empty, "claude", or "codex"`,
			field: "switchToBackend",
		},
		{
			name:  "cooldown",
			err:   `automation "rl": policy.cooldown_minutes must be >= 0`,
			field: "cooldownMinutes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeAutomationValidationError(rec, errors.New(tc.err))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
			var body struct {
				Error struct {
					Code  string `json:"code"`
					Field string `json:"field"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Error.Code != "invalid_policy" {
				t.Fatalf("expected invalid_policy, got %q", body.Error.Code)
			}
			if body.Error.Field != tc.field {
				t.Fatalf("expected field %q, got %q", tc.field, body.Error.Field)
			}
		})
	}
}
