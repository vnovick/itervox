package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	// AllowUnchecked controls the SRV-1 unarmed-gate refusal: when gh reports
	// "no required checks reported" on the target branch, false (default)
	// refuses the merge with MergePRReasonUnarmedGate; true logs a loud
	// warning and proceeds. Wired from cfg.Agent.AllowUncheckedMerge.
	AllowUnchecked bool
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
	// MergePRReasonUnarmedGate is returned when gh reports zero required
	// checks configured on the target branch (SRV-1 / spec F3: "a gate that
	// can never fail is not a gate"). Refused by default; operators opt out
	// via agent.allow_unchecked_merge: true.
	MergePRReasonUnarmedGate = "unarmed_gate"
)

// runGH shells out to the gh CLI and returns stdout. On a non-zero exit,
// exec.Cmd.Output() populates (*exec.ExitError).Stderr (since we never set
// cmd.Stderr ourselves) — we append that to the returned bytes so callers
// see gh's actual diagnostic (e.g. "no required checks reported on the
// 'main' branch") instead of an empty stdout. We deliberately do NOT switch
// to CombinedOutput(): several callers (`pr view --json ...`) parse stdout
// as JSON on the success path, and gh is known to emit non-JSON warnings to
// stderr even on exit 0 (e.g. auth-token nudges) — merging that into stdout
// would break json.Unmarshal on otherwise-successful calls. Appending
// stderr only on the error path is safe because none of those JSON calls
// are ever read on a non-nil error.
func runGH(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			out = append(out, exitErr.Stderr...)
		}
	}
	return out, err
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

	gate := s.mergePRGate(req.Strategy)
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
				Strategy:    gate.Strategy,
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, MergePRResponse{
		OK:          true,
		MergeCommit: mergeCommit,
		Strategy:    gate.Strategy,
	})
}

// mergePRGate builds the per-request MergePRGate from the operator's
// startup-fixed merge policy (gaps_11 G-3 — `agent.merge_strategy` /
// `agent.merge_block_labels` were parsed but never read). Resolution order:
//
//   - Strategy: request body (if non-empty) overrides the configured
//     `agent.merge_strategy`; both empty falls back to "squash". An invalid
//     request strategy is intentionally NOT replaced here — the gate refuses
//     it with MergePRReasonInvalidStrategy so the caller gets a clear error
//     instead of a silently substituted merge mode.
//   - BlockLabels: the configured `agent.merge_block_labels` replaces the
//     defaults entirely; unset/empty falls back to DefaultMergeBlockLabels().
//
// Both fields are read-only after startup (not in the cfgMu allowlist), so no
// locking is required.
func (s *Server) mergePRGate(requestStrategy string) MergePRGate {
	strategy := strings.TrimSpace(requestStrategy)
	if strategy == "" {
		strategy = strings.TrimSpace(s.mergeStrategy)
	}
	if strategy == "" {
		strategy = "squash"
	}
	blockLabels := s.mergeBlockLabels
	if len(blockLabels) == 0 {
		blockLabels = DefaultMergeBlockLabels()
	}
	gh := s.ghRun
	if gh == nil {
		gh = runGH
	}
	return MergePRGate{
		BlockLabels:    blockLabels,
		Strategy:       strategy,
		AllowUnchecked: s.allowUncheckedMerge,
		GH:             gh,
	}
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
		detail := truncateForReason(string(checksOut))
		// gh exits non-zero BOTH for failing checks and for "no required
		// checks reported" on the target branch (an unprotected repo with no
		// branch-protection required checks configured). The latter is an
		// unarmed gate (spec F3: "a gate that can never fail is not a gate")
		// — refuse by default rather than merging with zero CI coverage.
		if strings.Contains(strings.ToLower(detail), "no required checks") {
			if g.AllowUnchecked {
				slog.Warn("merge_pr: merging with ZERO required checks (allow_unchecked_merge: true) — the CI gate is unarmed",
					"pr", pr)
			} else {
				return "", MergePRReasonUnarmedGate + ": repository has no required checks — configure branch protection, or set agent.allow_unchecked_merge: true to merge anyway", nil
			}
		} else {
			return "", MergePRReasonChecksFailed + ":" + detail, nil
		}
	}

	// 3. Merge. Deliberately no --delete-branch: gh's local-branch delete runs
	// AFTER the remote merge already landed, and fails with a non-zero exit
	// whenever the branch is checked out in an itervox worktree (SRV-2) —
	// that failure would otherwise surface as a spurious merge error even
	// though the PR merged successfully. Remote cleanup is handled
	// separately, best-effort, below.
	strategyFlag := "--" + g.Strategy
	mergeOut, err := g.GH(ctx, "pr", "merge", fmt.Sprint(pr), strategyFlag)
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

	// Best-effort remote branch cleanup — replaces --delete-branch, whose
	// LOCAL delete fails (non-zero exit AFTER a successful merge) whenever
	// the branch is checked out in an itervox worktree (SRV-2). Local
	// branches are cleaned by workspace auto-clear. Any failure here is
	// logged and swallowed — it must never turn an already-successful merge
	// into a reported failure.
	if headOut, herr := g.GH(ctx, "pr", "view", fmt.Sprint(pr), "--json", "headRefName"); herr == nil {
		var head struct {
			HeadRefName string `json:"headRefName"`
		}
		if json.Unmarshal(headOut, &head) == nil && head.HeadRefName != "" {
			if _, derr := g.GH(ctx, "api", "-X", "DELETE", "repos/{owner}/{repo}/git/refs/heads/"+head.HeadRefName); derr != nil {
				slog.Warn("merge_pr: remote branch cleanup failed (non-fatal)", "pr", pr, "branch", head.HeadRefName, "error", derr)
			}
		}
	}

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
