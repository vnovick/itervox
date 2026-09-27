package agent

// CORE-030 — SplitFailureText is the reader for formatFailureText's layout:
// the orchestrator's rate-limit classifier uses it to treat the CLI's own
// stderr differently from the agent-reported failure. These rows pin that
// the two stay in step, using the real formatter.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitFailureText_RoundTripsFormatFailureText(t *testing.T) {
	cases := []struct {
		name, agentFailure, stderr string
		waitErr                    error
		wantAgent, wantStderr      string
	}{
		{"both", "API Error: 429 {", "pr-1429.json", nil, "API Error: 429 {", "pr-1429.json"},
		{"stderr only", "", "disk quota exceeded", errors.New("exit status 1"), "", "disk quota exceeded"},
		{"agent only", "insufficient_quota", "", nil, "insufficient_quota", ""},
		{"exit only", "", "", errors.New("exit status 2"), "exit: exit status 2", ""},
		{"stderr containing separator", "boom", "a | b | stderr: c", nil, "boom", "a | b | stderr: c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := formatFailureText(tc.agentFailure, tc.stderr, tc.waitErr)
			a, s := SplitFailureText(ft)
			assert.Equal(t, tc.wantAgent, a)
			assert.Equal(t, tc.wantStderr, s)

			// Same split under the worker's "turn N: " exit-cause prefix.
			a, s = SplitFailureText("turn 3: " + ft)
			if tc.wantAgent == "" {
				assert.Equal(t, "turn 3: ", a)
			} else {
				assert.Equal(t, "turn 3: "+tc.wantAgent, a)
			}
			assert.Equal(t, tc.wantStderr, s)
		})
	}

	t.Run("label inside agent text is not a segment start", func(t *testing.T) {
		a, s := SplitFailureText("see mystderr: nothing")
		assert.Equal(t, "see mystderr: nothing", a)
		assert.Empty(t, s)
	})
}
