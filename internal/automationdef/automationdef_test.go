package automationdef_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/workflow"
)

func TestWorkflowAutomationEntriesAreCanonicalDefinitions(t *testing.T) {
	t.Parallel()

	require.True(t,
		reflect.TypeOf(workflow.AutomationEntry{}) == reflect.TypeOf(automationdef.Definition{}),
		"workflow automation entries must alias the canonical automation definition",
	)
}
