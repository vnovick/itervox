package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/skills"
)

// errFence is what cmd/itervox's beginSettingsSave returns when a reload has
// superseded the save's generation: a wrapped server.ErrSettingsReloading.
var errFence = fmt.Errorf("settings: save from superseded generation 1 (current 2): %w", server.ErrSettingsReloading)

// fencedClient answers every settings method the reload fence guards with
// the fence error.
func fencedClient() *server.FuncClient {
	return &server.FuncClient{
		SetWorkersFn:                      func(int) error { return errFence },
		BumpWorkersFn:                     func(int) (int, error) { return 0, errFence },
		SetMaxRetriesFn:                   func(int) error { return errFence },
		SetFailedStateFn:                  func(string) error { return errFence },
		SetMaxSwitchesPerIssuePerWindowFn: func(int) error { return errFence },
		SetSwitchWindowHoursFn:            func(int) error { return errFence },
		SetReviewerConfigFn:               func(string, bool) error { return errFence },
		UpsertProfileFn:                   func(string, server.ProfileDef, string) error { return errFence },
		DeleteProfileFn:                   func(string) error { return errFence },
		SetAutomationsFn:                  func([]server.AutomationDef) error { return errFence },
		SetAutoClearWorkspaceFn:           func(bool) error { return errFence },
		SetDepsAnalysisModeFn:             func(string) error { return errFence },
		UpdateTrackerStatesFn:             func([]string, []string, string) error { return errFence },
		AddSSHHostFn:                      func(string, string) error { return errFence },
		RemoveSSHHostFn:                   func(string) error { return errFence },
		SetDispatchStrategyFn:             func(string) error { return errFence },
		SetInlineInputFn:                  func(bool) error { return errFence },
	}
}

// fencedRefreshClient adds the optional server.ModelRefresher capability,
// fenced like the rest (CORE-160).
type fencedRefreshClient struct{ *server.FuncClient }

func (fencedRefreshClient) RefreshAvailableModels(context.Context, string) (map[string][]server.ModelOption, error) {
	return nil, errFence
}

// fencedSkillsClient: ApplyFix's edit-yaml action saves through the adapter's
// UpsertProfile, so it reaches the fence too (M1-close E1).
type fencedSkillsClient struct{}

func (fencedSkillsClient) Inventory() *skills.Inventory                       { return nil }
func (fencedSkillsClient) RefreshInventory(context.Context) error             { return nil }
func (fencedSkillsClient) Issues() []skills.InventoryIssue                    { return nil }
func (fencedSkillsClient) ApplyFix(context.Context, string, skills.Fix) error { return errFence }
func (fencedSkillsClient) Analytics() *skills.AnalyticsSnapshot               { return nil }
func (fencedSkillsClient) AnalyticsRecommendations() []skills.Recommendation  { return nil }

// fencedProjectManager: the Linear project-filter save is fenced by
// cmd/itervox's saveProjectFilter (M4-close D4/D5).
type fencedProjectManager struct{}

func (fencedProjectManager) FetchProjects(context.Context) ([]server.Project, error) { return nil, nil }
func (fencedProjectManager) SetProjectFilter([]string) error                         { return errFence }
func (fencedProjectManager) GetProjectFilter() []string                              { return nil }

// M1-close C2/D2/E1: every settings route whose adapter method the reload fence
// guards must answer a refused save with a retryable 503 settings_reloading
// and Retry-After: 1 — never a 400/409/500 that tells the client the value
// itself was wrong. One row per fenced adapter method (workers covers both
// SetWorkers and BumpWorkers).
func TestSettingsSaveRefusedByReloadFenceIs503(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/settings/workers", `{"workers":4}`},
		{http.MethodPost, "/api/v1/settings/workers", `{"delta":1}`},
		{http.MethodPut, "/api/v1/settings/agent/max-retries", `{"maxRetries":3}`},
		{http.MethodPut, "/api/v1/settings/tracker/failed-state", `{"failedState":"Backlog"}`},
		{http.MethodPut, "/api/v1/settings/agent/max-switches-per-issue-per-window", `{"maxSwitchesPerIssuePerWindow":2}`},
		{http.MethodPut, "/api/v1/settings/agent/switch-window-hours", `{"switchWindowHours":6}`},
		{http.MethodPut, "/api/v1/settings/reviewer", `{"profile":"","auto_review":false}`},
		{http.MethodPut, "/api/v1/settings/profiles/x", `{"command":"claude"}`},
		{http.MethodDelete, "/api/v1/settings/profiles/x", ``},
		{http.MethodPut, "/api/v1/settings/automations", `{"automations":[]}`},
		{http.MethodPost, "/api/v1/settings/workspace/auto-clear", `{"enabled":true}`},
		{http.MethodPost, "/api/v1/settings/deps-analysis-mode", `{"mode":"manual"}`},
		{http.MethodPut, "/api/v1/settings/tracker/states", `{"activeStates":["Todo"],"terminalStates":["Done"],"completionState":"Done"}`},
		{http.MethodPost, "/api/v1/settings/ssh-hosts", `{"host":"build-1"}`},
		{http.MethodDelete, "/api/v1/settings/ssh-hosts/build-1", ``},
		{http.MethodPut, "/api/v1/settings/dispatch-strategy", `{"strategy":"least-loaded"}`},
		{http.MethodPost, "/api/v1/settings/inline-input", `{"enabled":true}`},
		{http.MethodPost, "/api/v1/skills/fix", `{"issueID":"i1","fix":{"Action":"edit-yaml","Target":"agent.profiles.x.enabled"}}`},
		// CORE-160: the model refresh is a fenced self-write now.
		{http.MethodPost, "/api/v1/settings/models/refresh", `{"backend":"claude"}`},
		// M4-close D4/D5: the project-filter save is fenced (saveProjectFilter).
		{http.MethodPut, "/api/v1/projects/filter", `{"slugs":["alpha"]}`},
		{http.MethodPut, "/api/v1/projects/filter", `{}`},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.body, func(t *testing.T) {
			cfg := makeTestConfig(baseSnap())
			cfg.Client = fencedRefreshClient{fencedClient()}
			cfg.SkillsClient = fencedSkillsClient{}
			cfg.ProjectManager = fencedProjectManager{}
			srv := server.New(cfg)
			var req *http.Request
			if tc.body == "" {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "1", w.Header().Get("Retry-After"))
			assert.Contains(t, w.Body.String(), "settings_reloading")
		})
	}
}
