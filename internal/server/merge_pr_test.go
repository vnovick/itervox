package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

type fakeGHResponse struct {
	out []byte
	err error
}

func fakeGH(responses map[string]fakeGHResponse) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		r, ok := responses[key]
		if !ok {
			return nil, errors.New("fake gh: no canned response for: " + key)
		}
		return r.out, r.err
	}
}

func TestMergePRGate_InvalidStrategy(t *testing.T) {
	gate := MergePRGate{Strategy: "yolo", GH: fakeGH(nil)}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonInvalidStrategy) {
		t.Errorf("expected invalid_strategy reason; got %q", reason)
	}
}

func TestMergePRGate_BlockedLabelRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"migration"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
	})
	gate := MergePRGate{
		Strategy:    "squash",
		BlockLabels: []string{"migration"},
		GH:          gh,
	}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonBlockedLabel) {
		t.Errorf("expected blocked_label reason; got %q", reason)
	}
}

// Label matching is documented as case-insensitive (EqualFold) — a PR
// labeled "Needs-Human" must trip a "needs-human" block-list entry.
func TestMergePRGate_BlockedLabelMatchIsCaseInsensitive(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 8 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"Needs-Human"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
	})
	gate := MergePRGate{
		Strategy:    "squash",
		BlockLabels: []string{"needs-human"},
		GH:          gh,
	}
	_, reason, err := gate.Merge(context.Background(), 8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonBlockedLabel) {
		t.Errorf("expected blocked_label reason for case-variant label; got %q", reason)
	}
}

func TestMergePRGate_NotMergeableRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","state":"OPEN"}`),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonNotMergeable) {
		t.Errorf("expected not_mergeable reason; got %q", reason)
	}
}

func TestMergePRGate_RequiredChecksFailingRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required": {
			out: []byte("ci.lint failing\n"),
			err: errors.New("exit status 1"),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonChecksFailed) {
		t.Errorf("expected checks_failed reason; got %q", reason)
	}
}

func TestMergePRGate_HappyPathMergesAndReturnsCommit(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required": {
			out: []byte("all passing\n"),
		},
		"pr merge 7 --squash": {
			out: []byte("Merged pull request #7\n"),
		},
		"pr view 7 --json mergeCommit": {
			out: []byte(`{"mergeCommit":{"oid":"abc1234"}}`),
		},
		"pr view 7 --json headRefName": {
			out: []byte(`{"headRefName":"feat-happy"}`),
		},
		"api -X DELETE repos/{owner}/{repo}/git/refs/heads/feat-happy": {
			out: []byte(""),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	commit, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no refusal reason; got %q", reason)
	}
	if commit != "abc1234" {
		t.Errorf("merge commit = %q; want abc1234", commit)
	}
}

// SRV-2: local-branch deletion must never fail the merge result. The remote
// branch is deleted best-effort AFTER the merge is recorded; a failure there
// (e.g. permissions, or gh emitting a non-zero exit) must not surface as a
// merge error, must not block the returned commit SHA, and must not be
// reported as a refusal reason.
func TestMergePRGate_MergeSucceedsEvenIfBranchCleanupFails(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required":                                   {out: []byte("all passing\n")},
		"pr merge 7 --squash":                                      {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit":                             {out: []byte(`{"mergeCommit":{"oid":"abc1234"}}`)},
		"pr view 7 --json headRefName":                             {out: []byte(`{"headRefName":"feat-x"}`)},
		"api -X DELETE repos/{owner}/{repo}/git/refs/heads/feat-x": {err: errors.New("exit 1: worktree")},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	commit, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no refusal reason; got %q", reason)
	}
	if commit != "abc1234" {
		t.Errorf("merge result must not depend on branch cleanup: commit = %q; want abc1234", commit)
	}
}

// captureSlog swaps the default slog handler for one writing into the
// returned buffer, restoring the previous handler on test cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// SRV-1: real gh exits non-zero with "no required checks reported" on
// unprotected repos. Default: refuse with an actionable unarmed_gate reason.
func TestMergePRGate_UnarmedRepoRefusedByDefault(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)},
		"pr checks 7 --required":                                   {out: []byte("no required checks reported on the 'main' branch\n"), err: errors.New("exit status 1")},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonUnarmedGate) {
		t.Fatalf("expected unarmed_gate reason; got %q", reason)
	}
	if !strings.Contains(reason, "required checks") {
		t.Errorf("reason must tell the operator what to configure; got %q", reason)
	}
}

// Explicit opt-in proceeds (and the merge happens), with a loud warning.
func TestMergePRGate_UnarmedRepoMergesWithExplicitOptIn(t *testing.T) {
	logs := captureSlog(t)
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)},
		"pr checks 7 --required":                                   {out: []byte("no required checks reported on the 'main' branch\n"), err: errors.New("exit status 1")},
		"pr merge 7 --squash":                                      {out: []byte("Merged pull request #7\n")},
		"pr view 7 --json mergeCommit":                             {out: []byte(`{"mergeCommit":{"oid":"abc1234"}}`)},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh, AllowUnchecked: true}
	commit, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected merge to proceed; got refusal %q", reason)
	}
	if commit != "abc1234" {
		t.Errorf("merge commit = %q; want abc1234", commit)
	}
	if !strings.Contains(logs.String(), "unarmed") {
		t.Errorf("expected an unarmed-gate warning in logs; got: %s", logs.String())
	}
}

// A genuinely failing check is still checks_failed, not unarmed_gate.
func TestMergePRGate_FailingChecksStillRefusedAsChecksFailed(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)},
		"pr checks 7 --required":                                   {out: []byte("X ci/test failing\n"), err: errors.New("exit status 8")},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonChecksFailed) {
		t.Fatalf("expected checks_failed reason; got %q", reason)
	}
	if strings.Contains(reason, "unarmed") {
		t.Errorf("checks_failed reason must not be classified as unarmed_gate; got %q", reason)
	}
}

// runGH's ExitError-stderr wrap is production code the fakeGH harness
// bypasses entirely (fixtures simulate combined bytes directly). Exercise it
// against a real subprocess (no gh dependency) by swapping the binary name
// via a tiny wrapper that mimics exec.Cmd's ExitError.Stderr population:
// running a shell command that writes to stderr and exits non-zero proves
// Output() populates ExitError.Stderr when Stderr is left nil, which is the
// invariant runGH depends on.
func TestExitErrorStderrIsPopulatedByOutputWhenUnset(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo boom 1>&2; exit 1")
	_, err := cmd.Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError; got %T: %v", err, err)
	}
	if !strings.Contains(string(exitErr.Stderr), "boom") {
		t.Errorf("expected ExitError.Stderr to contain the subprocess's stderr; got %q", exitErr.Stderr)
	}
}

func TestMergePRGate_AlreadyMergedIsIdempotent(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"MERGED"}`),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != MergePRReasonAlreadyMerged {
		t.Errorf("expected already_merged reason; got %q", reason)
	}
}

// newMergeTestServer builds a whitebox *Server with the minimum required
// Config fields filled in, so the gaps_11 G-3 merge-policy tests can drive
// mergePRGate / handleAgentMergePR directly.
func newMergeTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.Snapshot == nil {
		cfg.Snapshot = func() StateSnapshot { return StateSnapshot{} }
	}
	if cfg.RefreshChan == nil {
		cfg.RefreshChan = make(chan struct{}, 1)
	}
	return New(cfg)
}

// gaps_11 G-3 — the configured agent.merge_strategy must be used when the
// request omits a strategy, and a request-supplied strategy must still
// override the configured default.
func TestServerMergeGate_ConfiguredStrategyUsedWhenRequestOmits(t *testing.T) {
	s := newMergeTestServer(t, Config{MergeStrategy: "rebase"})
	if got := s.mergePRGate("").Strategy; got != "rebase" {
		t.Errorf("strategy with empty request = %q; want configured rebase", got)
	}
	if got := s.mergePRGate("merge").Strategy; got != "merge" {
		t.Errorf("strategy with request override = %q; want merge", got)
	}
}

// gaps_11 G-3 — the operator's agent.merge_block_labels must be enforced by
// the gate: a custom label blocks the merge, and a default label that the
// operator removed from the list no longer blocks.
func TestServerMergeGate_ConfiguredBlockLabelsEnforced(t *testing.T) {
	s := newMergeTestServer(t, Config{MergeBlockLabels: []string{"do-not-merge"}})

	// Custom label present → refused.
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"do-not-merge"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
	})
	_, reason, err := s.mergePRGate("squash").Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonBlockedLabel) {
		t.Errorf("expected blocked_label reason for configured label; got %q", reason)
	}

	// Default label "migration" — removed by the operator's override — must
	// no longer block; the merge proceeds.
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 8 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"migration"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 8 --required": {
			out: []byte("all passing\n"),
		},
		"pr merge 8 --squash": {
			out: []byte("Merged pull request #8\n"),
		},
		"pr view 8 --json mergeCommit": {
			out: []byte(`{"mergeCommit":{"oid":"def5678"}}`),
		},
	})
	commit, reason, err := s.mergePRGate("squash").Merge(context.Background(), 8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("default label must not block once overridden; got refusal %q", reason)
	}
	if commit != "def5678" {
		t.Errorf("merge commit = %q; want def5678", commit)
	}
}

// gaps_11 G-3 — when the operator config is unset, the gate falls back to
// "squash" and DefaultMergeBlockLabels().
func TestServerMergeGate_DefaultsWhenConfigUnset(t *testing.T) {
	s := newMergeTestServer(t, Config{})
	gate := s.mergePRGate("")
	if gate.Strategy != "squash" {
		t.Errorf("default strategy = %q; want squash", gate.Strategy)
	}
	if !slices.Equal(gate.BlockLabels, DefaultMergeBlockLabels()) {
		t.Errorf("default block labels = %v; want %v", gate.BlockLabels, DefaultMergeBlockLabels())
	}
}

// gaps_11 G-3 — end-to-end through the route: the handler must consult the
// configured merge policy, not hardcoded defaults. The request omits a
// strategy, so only the configured "rebase" matches the canned gh responses;
// the PR carries "needs-human" (a DEFAULT block label) which must NOT block
// because the operator overrode the list.
func TestHandleAgentMergePR_UsesConfiguredPolicy(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-G3", "run-1", []string{config.AgentActionMergePR}, "", time.Minute)
	if err != nil {
		t.Fatalf("issue action token: %v", err)
	}
	s := newMergeTestServer(t, Config{
		MergeStrategy:    "rebase",
		MergeBlockLabels: []string{"do-not-merge"},
		ActionTokenStore: store,
		Client:           &FuncClient{},
	})
	s.ghRun = fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"needs-human"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required": {
			out: []byte("all passing\n"),
		},
		"pr merge 7 --rebase": {
			out: []byte("Merged pull request #7\n"),
		},
		"pr view 7 --json mergeCommit": {
			out: []byte(`{"mergeCommit":{"oid":"cafe123"}}`),
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-G3/merge_pr", strings.NewReader(`{"pr":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp MergePRResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK {
		t.Error("response ok = false; want true")
	}
	if resp.Strategy != "rebase" {
		t.Errorf("response strategy = %q; want configured rebase", resp.Strategy)
	}
	if resp.MergeCommit != "cafe123" {
		t.Errorf("merge commit = %q; want cafe123", resp.MergeCommit)
	}
}

func TestIsValidMergeStrategy(t *testing.T) {
	for _, s := range []string{"squash", "rebase", "merge"} {
		if !IsValidMergeStrategy(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	if IsValidMergeStrategy("yolo") {
		t.Error("expected yolo to be invalid")
	}
}
