package guardrails

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #252: with no daemon endpoint (`itervox run`), an approval command is
// refused and recorded in the run's local refusal log, which
// RefusedApprovals reads back once per command.
func TestRefusedApprovalsReadsTheLocalLogWithoutADaemon(t *testing.T) {
	r := newRig(t, nil)
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "vercel", "echo \"$@\" >> "+ran+"\n")

	for range 2 {
		out, code := r.agent("vercel --prod", fakes)
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "approvals are not available here")
	}
	assert.NoFileExists(t, ran, "fail closed: vercel never ran")
	assert.Equal(t, []string{"vercel --prod"}, r.rt.RefusedApprovals())
	assert.Nil(t, (*Runtime)(nil).RefusedApprovals())
}

// #252: a command the operator approved for the run (`itervox run
// --approve`) runs without asking; other approval commands are still
// refused.
func TestShimRunsAPreApprovedCommandOnly(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.ApprovalCommands = []string{"vercel --prod", "deploy"}
		o.PreApproved = []string{"vercel  --prod"}
	})
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "vercel", "echo vercel \"$@\" >> "+ran+"\n")
	fakeProgram(t, fakes, "deploy", "echo deploy \"$@\" >> "+ran+"\n")

	out, code := r.agent("vercel --prod", fakes)
	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, "was approved for this run")
	out, code = r.agent("deploy staging", fakes)
	assert.Equal(t, 1, code, out)

	data, err := os.ReadFile(ran)
	require.NoError(t, err)
	assert.Equal(t, "vercel --prod\n", string(data))
	assert.Equal(t, []string{"deploy staging"}, r.rt.RefusedApprovals())
}
