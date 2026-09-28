package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// CORE-137: toolDescription's default branch ranged over a Go map, so an
// unknown tool with several string fields got a different log description on
// different calls. It must be a pure function of its input.
func TestToolDescriptionDefaultBranchIsDeterministic(t *testing.T) {
	input := json.RawMessage(`{"zeta":"last","alpha":"first","mid":"middle","beta":"second","gamma":"third","delta":"fourth"}`)
	want := toolDescription("mcp__custom__tool", input)
	for i := range 50 {
		assert.Equal(t, want, toolDescription("mcp__custom__tool", input), "call %d differed", i)
	}
	assert.Equal(t, "first", want, "the default branch picks the first non-empty string field by sorted key")

	// Empty and non-string fields are skipped in the same sorted order.
	assert.Equal(t, "value", toolDescription("x", json.RawMessage(`{"a":"","b":7,"c":"value","d":"other"}`)))
}
