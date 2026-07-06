package orchestrator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The orchestrator-controlled envelope tells every
// agent how to reach a human via the dashboard's Reply & Resume textarea.
func TestOperatorReplyEnvelope_MentionsReplyAndResume(t *testing.T) {
	if operatorReplyEnvelope == "" {
		t.Fatal("operatorReplyEnvelope must be non-empty")
	}
	assert.Contains(t, operatorReplyEnvelope, "Reply & Resume Agent",
		"envelope must point agents at the dashboard textarea")
	assert.Contains(t, operatorReplyEnvelope, "input_required",
		"envelope must mention the exit status that triggers it")
}

func TestPrependEnvToCommand_PreservesBackendHintPrefix(t *testing.T) {
	command := "@@itervox-backend=codex /tmp/codex-wrapper --flag"

	got := prependEnvToCommand(command, map[string]string{
		"ITERVOX_ACTION_TOKEN": "token value",
		"PATH":                 "/tmp/bin:/usr/bin",
	})

	assert.True(t, strings.HasPrefix(got, "@@itervox-backend=codex "), "backend hint should stay at the front so runner dispatch remains stable")
	assert.Contains(t, got, "ITERVOX_ACTION_TOKEN='token value'")
	assert.Contains(t, got, "PATH='/tmp/bin:/usr/bin'")
	assert.Contains(t, got, "/tmp/codex-wrapper --flag")
}
