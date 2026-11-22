package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestRunChecksReachCompletedRun: the gate outcomes a worker records on its
// run handle reach the run's CompletedRun at exit (names and pass/fail
// only), the later evidence gate replacing the earlier one; a run without
// a handle, or whose gates never ran, records none.
func TestRunChecksReachCompletedRun(t *testing.T) {
	o := &Orchestrator{}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1"}
	h := &runHandle{id: "run-1"}
	ctx := context.WithValue(context.Background(), runHandleKey{}, h)
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	recordSignedGate(ctx, []config.VerifyCheck{{Name: "lint"}, {Name: "test"}, {Name: "build"}}, verifyVerdict{
		Failed: []string{"`test`: exit 2"},
		Records: []attest.Record{
			{Kind: attest.KindCheck, Name: "lint"},
			{Kind: attest.KindCheck, Name: "test", ExitCode: 2},
			{Kind: attest.KindSeal},
		},
	}, "20261120T100000Z_coder", at)
	recordEvidenceGate(ctx, []string{"tests"}, evidenceVerdict{Missing: []string{"x"}, FailedNames: []string{"tests"}}, at)
	recordEvidenceGate(ctx, []string{"tests", "ci"}, evidenceVerdict{}, at.Add(time.Minute))

	o.recordHistory(&RunEntry{Issue: issue, RunID: "run-1", run: h, StartedAt: at}, issue, at.Add(2*time.Minute), "input_required")
	o.recordHistory(&RunEntry{Issue: issue, RunID: "run-2", StartedAt: at}, issue, at.Add(3*time.Minute), "failed")
	o.recordHistory(&RunEntry{Issue: issue, RunID: "run-3", run: &runHandle{id: "run-3"}, StartedAt: at}, issue, at.Add(4*time.Minute), "failed")

	hist := o.RunHistory()
	require.Len(t, hist, 3)
	c := hist[0].Checks
	require.NotNil(t, c)
	assert.Equal(t, &GateResult{Required: []string{"lint", "test", "build"}, Failed: []string{"test"}, Passed: false,
		CheckedAt: at, AttestRunID: "20261120T100000Z_coder"}, c.Signed)
	assert.Equal(t, &GateResult{Required: []string{"tests", "ci"}, Passed: true, CheckedAt: at.Add(time.Minute)}, c.Evidence)
	assert.Nil(t, hist[1].Checks, "no handle: nothing recorded")
	assert.Nil(t, hist[2].Checks, "no gate ran")
}

// TestCheckEvidenceNamesFailedChecks: the verdict names each unmet check,
// "ci" included, alongside its reason.
func TestCheckEvidenceNamesFailedChecks(t *testing.T) {
	dir := t.TempDir()
	v := checkEvidence(context.Background(), dir, evidenceRelPathFor("implementer"), []string{"tests", "lint", "ci"}, "", noChecks)
	assert.False(t, v.ok())
	assert.Equal(t, []string{"tests", "lint", "ci"}, v.FailedNames)
	assert.Len(t, v.Missing, 3)
}
