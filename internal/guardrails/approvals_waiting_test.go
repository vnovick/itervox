package guardrails

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Waiting is the idle hold the runner consults: true while a run's request
// is unanswered and for decisionPickup after an answer its shim has not
// read yet, false once the shim read it, for another run or issue, and on a
// nil store.
func TestApprovalsWaiting(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a := NewApprovals()
	a.now = func() time.Time { return now }

	assert.False(t, a.Waiting("ENG-1", "run-1"), "no request")
	st, created := a.Request("ENG-1", "run-1", ApprovalRequest{Key: "k", Command: "vercel --prod"})
	assert.True(t, created)
	assert.True(t, a.Waiting("ENG-1", "run-1"), "unanswered")
	assert.False(t, a.Waiting("ENG-1", "run-2"), "another run of the issue")
	assert.False(t, a.Waiting("ENG-2", "run-1"), "another issue")

	now = now.Add(time.Hour) // an operator may take long; pending never expires here
	assert.True(t, a.Waiting("ENG-1", "run-1"))

	assert.True(t, a.Decide(st.ID, true, "approve"))
	assert.True(t, a.Waiting("ENG-1", "run-1"), "answered, not yet read by the shim")
	now = now.Add(decisionPickup)
	assert.False(t, a.Waiting("ENG-1", "run-1"), "a decision nobody reads stops holding")

	st2, _ := a.Request("ENG-1", "run-1", ApprovalRequest{Key: "k2", Command: "vercel --prod --force"})
	assert.True(t, a.Decide(st2.ID, false, "no"))
	assert.True(t, a.Waiting("ENG-1", "run-1"))
	a.Status("ENG-1", "run-1", st2.ID) // the shim reads its answer
	assert.False(t, a.Waiting("ENG-1", "run-1"), "read: the agent is running again")

	var none *Approvals
	assert.False(t, none.Waiting("ENG-1", "run-1"))
}
