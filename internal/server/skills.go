package server

import (
	"context"
	"net/http"

	"github.com/vnovick/itervox/internal/skills"
)

// SkillsClient exposes skills-inventory operations to the HTTP server.
// Defined separately from `OrchestratorClient` so the existing interface
// doesn't grow. Wired by the orchestrator adapter (T-88).
type SkillsClient interface {
	// Inventory returns the most recent cached inventory. May return nil if
	// the daemon hasn't completed its first scan yet.
	Inventory() *skills.Inventory
	// RefreshInventory forces a re-scan and swaps in the new inventory on
	// success. A scanner error may still store a partial inventory with
	// warning metadata when best-effort data is available.
	RefreshInventory(ctx context.Context) error
	// Issues returns the static-analysis issues against the most recent
	// inventory. May be empty if no rules fire or if no scan has run yet.
	Issues() []skills.InventoryIssue
	// ApplyFix runs the given Fix descriptor (T-95/T-96). Returns an error
	// if the action is unknown or fails. The caller has already gated
	// destructive Fixes behind a confirm prompt.
	ApplyFix(ctx context.Context, issueID string, fix skills.Fix) error
	// Analytics returns the latest AnalyticsSnapshot built from the cached
	// inventory + runtime evidence (T-102). May be nil before inventory exists;
	// snapshots without logs report HasRuntimeEvidence=false.
	Analytics() *skills.AnalyticsSnapshot
	// AnalyticsRecommendations returns the recommend-analytics output
	// (T-101) keyed off the same blend.
	AnalyticsRecommendations() []skills.Recommendation
}

// noopSkillsClient is the fallback when the orchestrator hasn't wired a
// concrete implementation yet (e.g. unit tests of the server in isolation).
type noopSkillsClient struct{}

func (noopSkillsClient) Inventory() *skills.Inventory                       { return nil }
func (noopSkillsClient) RefreshInventory(context.Context) error             { return errNotConfigured }
func (noopSkillsClient) Issues() []skills.InventoryIssue                    { return nil }
func (noopSkillsClient) ApplyFix(context.Context, string, skills.Fix) error { return errNotConfigured }
func (noopSkillsClient) Analytics() *skills.AnalyticsSnapshot               { return nil }
func (noopSkillsClient) AnalyticsRecommendations() []skills.Recommendation  { return nil }

// handleSkillsInventory returns the cached inventory as JSON. 503 when no
// scan has completed yet.
func (s *Server) handleSkillsInventory(w http.ResponseWriter, _ *http.Request) {
	inv := s.skills.Inventory()
	if inv == nil {
		writeError(w, http.StatusServiceUnavailable, "inventory_unavailable",
			"skills inventory has not been scanned yet")
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// handleSkillsScan triggers a re-scan and returns the freshly populated
// inventory. If a scanner fails after producing best-effort data, return the
// partial inventory so the caller can show useful results plus a warning.
func (s *Server) handleSkillsScan(w http.ResponseWriter, r *http.Request) {
	before := s.skills.Inventory()
	if err := s.skills.RefreshInventory(r.Context()); err != nil {
		if inv := s.skills.Inventory(); inv != nil && inv != before && inv.Partial {
			writeJSON(w, http.StatusOK, inv)
			return
		}
		writeError(w, http.StatusInternalServerError, "scan_failed", err.Error())
		return
	}
	inv := s.skills.Inventory()
	if inv == nil {
		writeError(w, http.StatusServiceUnavailable, "inventory_unavailable",
			"skills inventory unavailable after refresh")
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// handleSkillsIssues returns just the analyzer's issue list — used by the
// dashboard's RecommendationsPanel for a lightweight surface.
func (s *Server) handleSkillsIssues(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.skills.Issues())
}

// fixRequest is the JSON body for POST /api/v1/skills/fix.
type fixRequest struct {
	IssueID string     `json:"issueID"`
	Fix     skills.Fix `json:"fix"`
}

// handleSkillsAnalytics returns the analytics snapshot. 503 only when no
// snapshot can be built yet.
func (s *Server) handleSkillsAnalytics(w http.ResponseWriter, _ *http.Request) {
	snap := s.skills.Analytics()
	if snap == nil {
		writeError(w, http.StatusServiceUnavailable, "analytics_unavailable",
			"analytics snapshot not yet computed")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleSkillsAnalyticsRecommendations returns the analytics-side
// recommendations (T-101). May be empty.
func (s *Server) handleSkillsAnalyticsRecommendations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.skills.AnalyticsRecommendations())
}

// handleSkillsFix runs a one-click Fix descriptor (T-95/T-96). The frontend
// gates destructive actions behind a confirm prompt; this handler trusts the
// caller's intent but the underlying ApplyFix implementation MAY refuse
// actions it doesn't know how to perform safely.
func (s *Server) handleSkillsFix(w http.ResponseWriter, r *http.Request) {
	var body fixRequest
	if err := decodeJSONBody(w, r, &body); err != nil {
		// decodeJSONBody already wrote the error.
		return
	}
	if body.IssueID == "" || body.Fix.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid_fix",
			"issueID and fix.action are required")
		return
	}
	if err := s.skills.ApplyFix(r.Context(), body.IssueID, body.Fix); err != nil {
		writeError(w, http.StatusInternalServerError, "fix_failed", err.Error())
		return
	}
	// Re-scan synchronously so the next /issues call reflects the change.
	if err := s.skills.RefreshInventory(r.Context()); err != nil {
		// Non-fatal — the fix landed; refresh failure surfaces on next read.
		// Continue to 200.
		_ = err
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
