package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/agent"
)

func TestDetectInputRequiredFallback_ChoicePrompt(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Implementation complete. What would you like to do?

1. Merge back to main locally
2. Push and create a Pull Request
3. Keep the branch as-is (I'll handle it later)
4. Discard this work

Which option?`)

	assert.True(t, decision.NeedsInput)
	assert.Contains(t, decision.Question, "Which option?")
	assert.Contains(t, decision.Question, "1. Merge back to main locally")
	assert.Contains(t, decision.Reason, "asks the human to choose the next action")
}

func TestDetectInputRequiredFallback_ConfirmationPrompt(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`I found no commits on this branch.

Type "discard" to confirm.`)

	assert.True(t, decision.NeedsInput)
	assert.Equal(t, `Type "discard" to confirm.`, decision.Question)
	assert.Contains(t, decision.Reason, "asks for explicit confirmation or approval")
}

func TestDetectInputRequiredFallback_BlockingStatement(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`I need your decision before proceeding with the deploy step.`)

	assert.True(t, decision.NeedsInput)
	assert.Equal(t, "I need your decision before proceeding with the deploy step.", decision.Question)
	assert.Contains(t, decision.Reason, "states that the agent is waiting before it can continue")
}

func TestDetectInputRequiredFallback_NonBlockingQuestion(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Implemented the requested change and ran tests. Can I help with anything else?`)

	assert.False(t, decision.NeedsInput)
	assert.Empty(t, decision.Question)
}

func TestDetectInputRequiredFallback_DoesNotMatchApprovedContinuing(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Approved, continuing with the existing branch.`)

	assert.False(t, decision.NeedsInput)
	assert.Empty(t, decision.Question)
}

func TestDetectInputRequiredFallback_Empty(t *testing.T) {
	decision := agent.DetectInputRequiredFallback("")
	assert.False(t, decision.NeedsInput)
}
