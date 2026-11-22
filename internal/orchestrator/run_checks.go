package orchestrator

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// run_checks.go — what a worker run's gates decided, for the per-issue
// evidence report (GET /api/v1/issues/{identifier}/evidence).
//
// The worker records each gate's outcome on its run handle (like its usage,
// #136); the event loop copies the record onto the run's CompletedRun in
// recordHistory. Check names and pass/fail only: never the evidence file's
// content, a command line or its output (the signed records keep those,
// redacted, in the attestation store).

// GateResult is one gate's outcome for a run.
type GateResult struct {
	// Required are the check names the gate asked for, in order.
	Required []string `json:"required"`
	// Failed are the names that were not proved. For the signed checks a
	// failure stops the list, so a name after it never ran and is in
	// neither list's failures.
	Failed []string `json:"failed,omitempty"`
	// Passed is the gate's decision.
	Passed    bool      `json:"passed"`
	CheckedAt time.Time `json:"checkedAt"`
	// AttestRunID names the run's records in the attestation store (signed
	// checks only): GET /api/v1/issues/{identifier}/attestations lists them
	// under this runId.
	AttestRunID string `json:"attestRunId,omitempty"`
}

// RunChecks are a run's gate outcomes: the #80 evidence gate
// (require_evidence, also the playbook's) and the signed checks (verify).
// A nil field means the gate did not run.
type RunChecks struct {
	Evidence *GateResult `json:"evidence,omitempty"`
	Signed   *GateResult `json:"signed,omitempty"`
}

// recordRunGate stores one gate's outcome on the run handle. The worker is
// the only writer and the loop reads only at exit, so a copy-on-write of the
// small record is enough. Worker only.
func recordRunGate(ctx context.Context, set func(*RunChecks)) {
	h := runFrom(ctx)
	if h == nil {
		return
	}
	next := RunChecks{}
	if prev := h.checks.Load(); prev != nil {
		next = *prev
	}
	set(&next)
	h.checks.Store(&next)
}

// recordEvidenceGate records the #80 evidence gate's verdict. The gate can
// run twice (before a daemon push, then at completion); the later one wins.
func recordEvidenceGate(ctx context.Context, required []string, v evidenceVerdict, now time.Time) {
	g := &GateResult{Required: slices.Clone(required), Failed: slices.Clone(v.FailedNames), Passed: v.ok(), CheckedAt: now}
	recordRunGate(ctx, func(c *RunChecks) { c.Evidence = g })
}

// recordSignedGate records the signed checks' verdict.
func recordSignedGate(ctx context.Context, checks []config.VerifyCheck, v verifyVerdict, attestRunID string, now time.Time) {
	g := &GateResult{Passed: v.ok(), CheckedAt: now, AttestRunID: attestRunID}
	for _, c := range checks {
		g.Required = append(g.Required, c.Name)
	}
	for _, r := range v.Records {
		if r.Kind == attest.KindCheck && !r.Passed() {
			g.Failed = append(g.Failed, r.Name)
		}
	}
	recordRunGate(ctx, func(c *RunChecks) { c.Signed = g })
}

// trackerActor names the author of a tracker reply for the feed's Actor
// ("tracker:<name>"); "" when the tracker gave no name.
func trackerActor(c domain.Comment) string {
	if name := strings.TrimSpace(c.AuthorName); name != "" {
		return "tracker:" + name
	}
	return ""
}

// runChecksOf returns what e's worker recorded, nil when nothing (or e has
// no handle: hand-built and snapshot entries). Event loop.
func runChecksOf(e *RunEntry) *RunChecks {
	if e == nil || e.run == nil {
		return nil
	}
	return e.run.checks.Load()
}
