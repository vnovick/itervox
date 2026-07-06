package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/skills"
)

// fakeSkillsClient returns canned values for the inventory + issues routes.
type fakeSkillsClient struct {
	inv          *skills.Inventory
	issues       []skills.InventoryIssue
	refreshErr   error
	refreshInv   *skills.Inventory
	refreshCalls int
	fixErr       error
	fixCalls     int
	lastFix      skills.Fix
	analytics    *skills.AnalyticsSnapshot
	recs         []skills.Recommendation
}

func (c *fakeSkillsClient) Inventory() *skills.Inventory { return c.inv }
func (c *fakeSkillsClient) Issues() []skills.InventoryIssue {
	return c.issues
}
func (c *fakeSkillsClient) RefreshInventory(ctx context.Context) error {
	c.refreshCalls++
	if c.refreshInv != nil {
		c.inv = c.refreshInv
	}
	return c.refreshErr
}
func (c *fakeSkillsClient) ApplyFix(_ context.Context, _ string, fix skills.Fix) error {
	c.fixCalls++
	c.lastFix = fix
	return c.fixErr
}
func (c *fakeSkillsClient) Analytics() *skills.AnalyticsSnapshot              { return c.analytics }
func (c *fakeSkillsClient) AnalyticsRecommendations() []skills.Recommendation { return c.recs }

func newSkillsTestServer(t *testing.T, fake *fakeSkillsClient) *Server {
	t.Helper()
	cfg := Config{
		Snapshot:     func() StateSnapshot { return StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		SkillsClient: fake,
	}
	return New(cfg)
}

func TestSkills_InventoryUnavailableUntilFirstScan(t *testing.T) {
	t.Parallel()
	s := newSkillsTestServer(t, &fakeSkillsClient{})
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/skills/inventory", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestSkills_InventoryReturnsCachedJSON(t *testing.T) {
	t.Parallel()
	fake := &fakeSkillsClient{
		inv: &skills.Inventory{
			Skills: []skills.Skill{{Name: "alpha", Provider: "claude"}},
			Stale:  true,
		},
	}
	s := newSkillsTestServer(t, fake)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/skills/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"alpha\"") {
		t.Errorf("expected body to contain alpha skill name, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"Stale\":true") {
		t.Errorf("expected body to expose stale status, got %s", rec.Body.String())
	}
}

func TestSkills_ScanRefreshesAndReturnsInventory(t *testing.T) {
	t.Parallel()
	fake := &fakeSkillsClient{
		inv: &skills.Inventory{Skills: []skills.Skill{{Name: "x"}}},
	}
	s := newSkillsTestServer(t, fake)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/skills/scan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if fake.refreshCalls != 1 {
		t.Errorf("expected 1 refresh call, got %d", fake.refreshCalls)
	}
}

func TestSkills_ScanReturns500OnRefreshError(t *testing.T) {
	t.Parallel()
	fake := &fakeSkillsClient{refreshErr: errors.New("scan blew up")}
	s := newSkillsTestServer(t, fake)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/skills/scan", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestSkills_ScanReturnsPartialInventoryOnRefreshError(t *testing.T) {
	t.Parallel()
	fake := &fakeSkillsClient{
		refreshErr: errors.New("codex scanner failed"),
		refreshInv: &skills.Inventory{
			Skills:    []skills.Skill{{Name: "alpha", Provider: "claude"}},
			Partial:   true,
			ScanError: "codex scanner failed",
		},
	}
	s := newSkillsTestServer(t, fake)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/skills/scan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for partial inventory, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out skills.Inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Partial || out.ScanError != "codex scanner failed" || len(out.Skills) != 1 {
		t.Fatalf("unexpected partial inventory body: %+v", out)
	}
}

func TestSkills_IssuesReturnsArray(t *testing.T) {
	t.Parallel()
	fake := &fakeSkillsClient{
		issues: []skills.InventoryIssue{{ID: "DUPLICATE_MCP", Severity: "warn"}},
	}
	s := newSkillsTestServer(t, fake)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/skills/issues", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []skills.InventoryIssue
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].ID != "DUPLICATE_MCP" {
		t.Errorf("unexpected issues body: %+v", out)
	}
}

func TestSkills_NilClientFallsBackToNoop(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		// SkillsClient intentionally nil — should fall back to noop.
	}
	s := New(cfg)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/skills/inventory", nil))
	// Inventory() returns nil from noop, so handler returns 503.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
