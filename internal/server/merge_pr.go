package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/vnovick/itervox/internal/config"
)

// MergePRRequest is the JSON body accepted by
// POST /agent-actions/{identifier}/merge_pr. PR is the GitHub PR number (or
// URL fragment). Strategy may override the configured default; empty falls
// back to cfg.Agent.MergeStrategy.
type MergePRRequest struct {
	PR       int    `json:"pr"`
	Strategy string `json:"strategy,omitempty"`
}

// MergePRResponse carries the merge SHA the gh CLI reported on success.
type MergePRResponse struct {
	OK            bool   `json:"ok"`
	MergeCommit   string `json:"merge_commit"`
	Strategy      string `json:"strategy"`
	AlreadyMerged bool   `json:"already_merged,omitempty"`
}

// mergePRDedup is a process-level dedup ledger keyed on
// "<identifier>:<pr-number>" so a re-dispatch + an external nudge can't
// double-merge the same PR. Mirrors the existing PROpenedDispatched pattern
// but lives outside orchestrator.State because merge_pr is server-side.
type mergePRDedup struct {
	mu     sync.Mutex
	merged map[string]string // key -> merge commit
}

var defaultMergePRDedup = &mergePRDedup{merged: map[string]string{}}

// MergePRGate runs the precondition checks for a single merge attempt. It is
// extracted from handleAgentMergePR so unit tests can exercise the policy
// without needing an http.ResponseWriter or a tracker client.
type MergePRGate struct {
	BlockLabels []string
	Strategy    string
	// gh is the gh-CLI invoker. Tests inject a fake; production uses runGH.
	GH func(ctx context.Context, args ...string) ([]byte, error)
}

// Reason values returned by MergePRGate.Check.
const (
	MergePRReasonAlreadyMerged   = "already_merged"
	MergePRReasonChecksFailed    = "checks_failed"
	MergePRReasonNotMergeable    = "not_mergeable"
	MergePRReasonBlockedLabel    = "blocked_label"
	MergePRReasonInvalidStrategy = "invalid_strategy"
)

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
}

func (s *Server) handleAgentMergePR(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validateAgentActionRequest(w, r, config.AgentActionMergePR); !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var req MergePRRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if req.PR <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "pr is required")
		return
	}

	dedupKey := fmt.Sprintf("%s:%d", identifier, req.PR)
	defaultMergePRDedup.mu.Lock()
	if existing, alreadyMerged := defaultMergePRDedup.merged[dedupKey]; alreadyMerged {
		defaultMergePRDedup.mu.Unlock()
		writeJSON(w, http.StatusOK, MergePRResponse{
			OK:            true,
			MergeCommit:   existing,
			AlreadyMerged: true,
		})
		return
	}
	defaultMergePRDedup.mu.Unlock()

	strategy := strings.TrimSpace(req.Strategy)
	if strategy == "" {
		strategy = "squash"
	}
	gate := MergePRGate{
		BlockLabels: DefaultMergeBlockLabels(),
		Strategy:    strategy,
		GH:          runGH,
	}
	mergeCommit, reason, err := gate.Merge(r.Context(), req.PR)
	if err != nil {
		writeError(w, http.StatusBadGateway, "merge_failed", err.Error())
		return
	}
	if reason != "" {
		writeError(w, http.StatusConflict, "merge_blocked", reason)
		return
	}

	defaultMergePRDedup.mu.Lock()
	defaultMergePRDedup.merged[dedupKey] = mergeCommit
	defaultMergePRDedup.mu.Unlock()

	// P1 — fire pr_merged automations on the daemon-side. Optional capability
	// resolved via type assertion so non-orchestrator backends can no-op.
	if emitter, ok := s.client.(PRMergedEmitter); ok {
		if err := emitter.EmitPRMerged(r.Context(), identifier, "", req.PR, mergeCommit, ""); err != nil {
			// Log and continue — the merge itself succeeded; emitting the
			// follow-up automation should never make the action fail.
			writeJSON(w, http.StatusOK, MergePRResponse{
				OK:          true,
				MergeCommit: mergeCommit,
				Strategy:    strategy,
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, MergePRResponse{
		OK:          true,
		MergeCommit: mergeCommit,
		Strategy:    strategy,
	})
}

// Merge runs the gh-CLI gates and the actual merge. Returns (commit, "", nil)
// on success; (_, reason, nil) on a precondition refusal; (_, _, err) only on
// gh CLI invocation failures the operator should look at directly.
func (g MergePRGate) Merge(ctx context.Context, pr int) (string, string, error) {
	if g.GH == nil {
		return "", "", fmt.Errorf("merge_pr: GH invoker not configured")
	}
	switch g.Strategy {
	case "squash", "rebase", "merge":
	default:
		return "", MergePRReasonInvalidStrategy + ":" + g.Strategy, nil
	}

	// 1. PR view: labels + mergeable + mergeStateStatus + state
	out, err := g.GH(ctx, "pr", "view", fmt.Sprint(pr), "--json", "labels,mergeable,mergeStateStatus,state")
	if err != nil {
		return "", "", fmt.Errorf("gh pr view: %w", err)
	}
	var view struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
		State            string `json:"state"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		return "", "", fmt.Errorf("parse gh pr view: %w", err)
	}
	if strings.EqualFold(view.State, "MERGED") {
		return "", MergePRReasonAlreadyMerged, nil
	}
	if strings.ToUpper(view.Mergeable) != "MERGEABLE" {
		return "", MergePRReasonNotMergeable + ":" + view.Mergeable, nil
	}
	if strings.ToUpper(view.MergeStateStatus) != "CLEAN" {
		return "", MergePRReasonNotMergeable + ":" + view.MergeStateStatus, nil
	}
	for _, l := range view.Labels {
		for _, blocked := range g.BlockLabels {
			if strings.EqualFold(l.Name, blocked) {
				return "", MergePRReasonBlockedLabel + ":" + l.Name, nil
			}
		}
	}

	// 2. Required checks must all pass.
	checksOut, err := g.GH(ctx, "pr", "checks", fmt.Sprint(pr), "--required")
	if err != nil {
		// gh exits non-zero when any required check is failing/pending.
		return "", MergePRReasonChecksFailed + ":" + truncateForReason(string(checksOut)), nil
	}

	// 3. Merge.
	strategyFlag := "--" + g.Strategy
	mergeOut, err := g.GH(ctx, "pr", "merge", fmt.Sprint(pr), strategyFlag, "--delete-branch")
	if err != nil {
		return "", "", fmt.Errorf("gh pr merge: %w: %s", err, string(mergeOut))
	}
	// gh's stdout includes a "Merged pull request #N (...)" line; the merge
	// SHA isn't in that line on every version. Read it explicitly.
	shaOut, _ := g.GH(ctx, "pr", "view", fmt.Sprint(pr), "--json", "mergeCommit")
	var shaResp struct {
		MergeCommit struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	_ = json.Unmarshal(shaOut, &shaResp)
	return shaResp.MergeCommit.OID, "", nil
}

func truncateForReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 240 {
		return s
	}
	return s[:237] + "…"
}

// validatedMergeStrategies is exposed for config validation tests.
var validatedMergeStrategies = []string{"squash", "rebase", "merge"}

// DefaultMergeBlockLabels is the default block-list applied by merge_pr when
// the operator does not override agent.merge_block_labels. Kept in sync with
// the default in internal/config/config.go.
func DefaultMergeBlockLabels() []string {
	return []string{"needs-human", "migration", "auth", "feature-flag", "breaking"}
}

// IsValidMergeStrategy reports whether s is one of the three accepted strategies.
func IsValidMergeStrategy(s string) bool {
	return slices.Contains(validatedMergeStrategies, strings.TrimSpace(s))
}
