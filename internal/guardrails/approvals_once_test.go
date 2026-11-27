package guardrails

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An approval allows one request once: the first read that sees it
// approved spends it (Status), a re-read is unknown, and asking for the
// same command again raises a new request that is pending, not approved.
// There is no allow-always: nothing an answer carries widens what a later
// request gets.
func TestApprovalAllowsOneRequestOnce(t *testing.T) {
	a := NewApprovals()
	req := ApprovalRequest{Key: "k1", Command: "vercel --prod"}
	st, created := a.Request("ENG-1", "run-1", req)
	require.True(t, created)
	require.True(t, a.Decide(st.ID, true, "approve"))

	assert.Equal(t, StatusApproved, a.Status("ENG-1", "run-1", st.ID).Status, "the first read sees the approval")
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run-1", st.ID).Status, "re-read after spend = not approved")
	assert.False(t, a.Decide(st.ID, true, "approve"), "a spent approval cannot be approved again")

	again, created := a.Request("ENG-1", "run-1", req)
	assert.True(t, created, "the same command asks again")
	assert.NotEqual(t, st.ID, again.ID)
	assert.Equal(t, StatusPending, again.Status)
	assert.Equal(t, StatusPending, a.Status("ENG-1", "run-1", again.ID).Status)
}

// A repeat of the approved command (Request with the same key) is how a
// shim that timed out picks the approval up: that join spends it, and the
// request id it joined is then unknown too.
func TestApprovalSpentByRequestJoin(t *testing.T) {
	a := NewApprovals()
	req := ApprovalRequest{Key: "k1", Command: "deploy"}
	st, _ := a.Request("ENG-1", "run-1", req)
	require.True(t, a.Decide(st.ID, true, "approve"))

	joined, created := a.Request("ENG-1", "run-1", req)
	assert.False(t, created)
	assert.Equal(t, StatusApproved, joined.Status)
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run-1", st.ID).Status, "spent by the join")
	next, created := a.Request("ENG-1", "run-1", req)
	assert.True(t, created)
	assert.Equal(t, StatusPending, next.Status, "a third attempt needs a new approval")
}

// An approval is bound to its issue, run and exact command: a read for
// another run or issue neither sees nor spends it, another command in the
// same run is a separate request, and a decision is final (a rejection
// cannot be turned into an approval).
func TestApprovalBoundToRunAndCommand(t *testing.T) {
	a := NewApprovals()
	st, _ := a.Request("ENG-1", "run-1", ApprovalRequest{Key: "k1", Command: "deploy"})
	require.True(t, a.Decide(st.ID, true, "approve"))
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run-2", st.ID).Status, "another run of the issue")
	assert.Equal(t, StatusUnknown, a.Status("ENG-2", "run-1", st.ID).Status, "another issue")
	other, created := a.Request("ENG-1", "run-1", ApprovalRequest{Key: "k2", Command: "deploy --force"})
	assert.True(t, created)
	assert.Equal(t, StatusPending, other.Status, "another command is not covered")
	assert.Equal(t, StatusApproved, a.Status("ENG-1", "run-1", st.ID).Status, "still unspent for its own run")
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run-1", st.ID).Status)

	rej, _ := a.Request("ENG-1", "run-1", ApprovalRequest{Key: "k3", Command: "deploy prod"})
	require.True(t, a.Decide(rej.ID, false, "no"))
	assert.False(t, a.Decide(rej.ID, true, "approve"), "a rejection is final")
	assert.Equal(t, StatusRejected, a.Status("ENG-1", "run-1", rej.ID).Status)
	a.Drop("ENG-1") // the run exits
	assert.Empty(t, a.byID, "nothing approved outlives its run")
}

// `itervox run --approve <command>` (#252) pre-approves one command for one
// run: it runs once, and a second attempt in the same run is an ordinary
// approval command again (refused in run mode, which has no approval
// channel). Another command is never covered.
func TestPreApprovalRunsOnce(t *testing.T) {
	p := Policy{Dir: t.TempDir(), PreApproved: []string{"vercel  --prod"}}
	assert.False(t, p.spendPreApproval("vercel --preview"), "only the approved command")
	assert.True(t, p.spendPreApproval("vercel --prod"), "the first run spends it")
	assert.False(t, p.spendPreApproval("vercel   --prod"), "re-read after spend = not approved")
	entries, err := os.ReadDir(filepath.Join(p.Dir, preApprovedSpentDir))
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	other := Policy{Dir: t.TempDir(), PreApproved: p.PreApproved}
	assert.True(t, other.spendPreApproval("vercel --prod"), "another run has its own approval")
	assert.False(t, Policy{PreApproved: p.PreApproved}.spendPreApproval("vercel --prod"), "no runtime dir: fail closed")
}

// End to end through the shim: the pre-approved command runs once; the
// second attempt is refused and recorded like any unapproved command.
func TestShimRunsAPreApprovedCommandOnce(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.ApprovalCommands = []string{"vercel --prod"}
		o.PreApproved = []string{"vercel --prod"}
	})
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "vercel", "echo vercel \"$@\" >> "+ran+"\n")

	out, code := r.agent("vercel --prod", fakes)
	require.Equal(t, 0, code, out)
	assert.Contains(t, out, "was approved for this run; running it once")
	out, code = r.agent("vercel --prod", fakes)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "approvals are not available here")
	data, err := os.ReadFile(ran)
	require.NoError(t, err)
	assert.Equal(t, "vercel --prod\n", string(data), "exactly once")
	assert.Equal(t, []string{"vercel --prod"}, r.rt.RefusedApprovals())
}
