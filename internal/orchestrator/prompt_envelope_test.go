package orchestrator

import (
	"strings"
	"testing"
)

// TestPromptEnvelopeMentionsReplyAndResume — codex-B3 named test.
// The orchestrator-controlled operatorReplyEnvelope MUST be appended after
// the rendered profile prompt before the worker hands the assembled prompt
// to the agent runner. This test exercises the assembly shape that
// runWorker performs at internal/orchestrator/worker.go:398.
func TestPromptEnvelopeMentionsReplyAndResume(t *testing.T) {
	renderedPrompt := "You are working on an issue.\nDo the thing."
	assembled := renderedPrompt + "\n\n" + operatorReplyEnvelope

	if !strings.Contains(assembled, "Operator Reply Channel") {
		t.Error("assembled prompt missing 'Operator Reply Channel' header")
	}
	if !strings.Contains(assembled, "Reply & Resume Agent") {
		t.Error("assembled prompt missing 'Reply & Resume Agent' guidance")
	}
	if !strings.Contains(assembled, "input_required") {
		t.Error("assembled prompt missing 'input_required' reference")
	}
	if !strings.HasPrefix(assembled, renderedPrompt) {
		t.Error("envelope must follow the profile prompt, not precede it")
	}
}
