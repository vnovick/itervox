package depsanalysis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// fakeRunner replays a fixed TurnResult and remembers the prompt it received
// so tests can assert the embedded JSON contract.
type fakeRunner struct {
	result    agent.TurnResult
	err       error
	gotPrompt string
}

func (r *fakeRunner) RunTurn(
	_ context.Context,
	_ agent.Logger,
	_ func(agent.TurnResult),
	_ *string,
	prompt, _, _, _, _ string,
	_, _ int,
) (agent.TurnResult, error) {
	r.gotPrompt = prompt
	return r.result, r.err
}

func TestRunAgentPass_ParsesPlainJSONResult(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{
		ResultText: `{"edges":[{"source":"ENG-1","target":"ENG-2","evidence":"body"}]}`,
	}}
	edges, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
		Issues:      []AnalyzerIssue{{Identifier: "ENG-2", Title: "blocked"}},
	})
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "ENG-1", edges[0].Source)
	assert.Equal(t, "ENG-2", edges[0].Target)
	assert.Equal(t, "body", edges[0].Evidence)
	assert.False(t, edges[0].InferredAt.IsZero(), "InferredAt is stamped by RunAgentPass")
}

func TestRunAgentPass_ExtractsJSONFromSurroundingProse(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{
		ResultText: "Here's the result:\n{\"edges\":[{\"source\":\"A\",\"target\":\"B\",\"evidence\":\"e\"}]}\nThanks!",
	}}
	edges, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "A", edges[0].Source)
}

func TestRunAgentPass_RejectsMalformedJSON(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{ResultText: "no json here"}}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAnalyzerOutputInvalid)
}

func TestRunAgentPass_PropagatesAgentFailure(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{Failed: true, FailureText: "rate limited"}}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAnalyzerFailed)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestRunAgentPass_PromptCarriesIssuesAndTrackerEdges(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{ResultText: `{"edges":[]}`}}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner: runner,
		Profile: config.AgentProfile{
			Command:      "claude",
			Soul:         "## My SOUL\n",
			Instructions: "## My INSTRUCTIONS\n",
		},
		ProfileName:  "deps-analyzer",
		Issues:       []AnalyzerIssue{{Identifier: "ENG-1", Title: "Foo"}},
		TrackerEdges: []TrackerEdge{{Source: "ENG-1", Target: "ENG-2"}},
	})
	require.NoError(t, err)
	assert.Contains(t, runner.gotPrompt, "## My SOUL")
	assert.Contains(t, runner.gotPrompt, "## My INSTRUCTIONS")
	assert.Contains(t, runner.gotPrompt, "ENG-1")
	assert.Contains(t, runner.gotPrompt, "Existing Tracker Edges")
	assert.Contains(t, runner.gotPrompt, `"source": "ENG-1"`)
}

func TestRunAgentPass_RejectsMissingProfileCommand(t *testing.T) {
	runner := &fakeRunner{}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: ""},
		ProfileName: "deps-analyzer",
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "no command"))
}

func TestRunAgentPass_PropagatesRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("boom")}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAnalyzerFailed)
}

func TestAgentPassParsesConfidence(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{
		ResultText: `{"edges":[{"source":"ENG-1","target":"ENG-2","evidence":"body","confidence":0.9}]}`,
	}}
	edges, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.InDelta(t, 0.9, edges[0].Confidence, 0.0001)
}

func TestAgentPassMissingConfidenceDefaultsZero(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{
		ResultText: `{"edges":[{"source":"ENG-1","target":"ENG-2","evidence":"missing"},{"source":"ENG-3","target":"ENG-4","evidence":"invalid","confidence":"high"}]}`,
	}}
	edges, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.NoError(t, err, "an invalid confidence field must never fail the pass")
	require.Len(t, edges, 2)
	assert.Equal(t, 0.0, edges[0].Confidence, "missing confidence defaults to 0")
	assert.Equal(t, 0.0, edges[1].Confidence, "invalid (non-numeric) confidence defaults to 0")
}

// #46-2 — an issue's Description had no size limit of its own: chunking
// bounds the prompt by issue COUNT, not bytes, so a handful of issues with
// very long descriptions could still blow the prompt up. This test proves
// buildAnalyzerPrompt (via RunAgentPass) caps each issue's rendered
// Description at analyzerDescriptionByteCap and marks the cut with the
// "…[truncated]" suffix, while a short description passes through verbatim.
func TestRunAgentPass_CapsLongDescriptionInPrompt(t *testing.T) {
	long := strings.Repeat("x", analyzerDescriptionByteCap+500)
	runner := &fakeRunner{result: agent.TurnResult{ResultText: `{"edges":[]}`}}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
		Issues: []AnalyzerIssue{
			{Identifier: "ENG-1", Title: "long one", Description: long},
			{Identifier: "ENG-2", Title: "short one", Description: "a short description"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, runner.gotPrompt, "…[truncated]",
		"a description over the byte cap must be truncated with the marker")
	assert.NotContains(t, runner.gotPrompt, long,
		"the untruncated long description must not appear verbatim in the prompt")
	assert.Contains(t, runner.gotPrompt, "a short description",
		"a description under the cap must pass through unmodified")
}

// The cap must apply at render time only — the AnalyzerIssue values passed
// in must come back untouched, since MergeIncremental fingerprints the
// original (uncapped) Description for incremental-mode change detection.
func TestRunAgentPass_DoesNotMutateCallerIssueSlice(t *testing.T) {
	long := strings.Repeat("y", analyzerDescriptionByteCap+500)
	issues := []AnalyzerIssue{{Identifier: "ENG-1", Title: "t", Description: long}}
	runner := &fakeRunner{result: agent.TurnResult{ResultText: `{"edges":[]}`}}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
		Issues:      issues,
	})
	require.NoError(t, err)
	assert.Equal(t, long, issues[0].Description, "the caller's issue slice must not be mutated by prompt rendering")
}

// Fix-round item 3 — the brief asked for a boundary case where the cut
// point lands mid-rune: a 3-byte UTF-8 rune ('€', U+20AC = 0xE2 0x82 0xAC)
// straddling byte offset analyzerDescriptionByteCap. truncateDescriptionForPrompt
// must back the cut up to before the rune (never splitting it), so the
// output is valid UTF-8 and does not contain a truncated/garbled rune tail.
func TestTruncateDescriptionForPrompt_MultiByteRuneAtBoundary(t *testing.T) {
	prefix := strings.Repeat("a", analyzerDescriptionByteCap-2) // bytes [0, 4094)
	euroSign := "€"                                             // 0xE2 0x82 0xAC — 3 bytes
	require.Equal(t, 3, len(euroSign))
	s := prefix + euroSign + strings.Repeat("b", 500)
	require.Greater(t, len(s), analyzerDescriptionByteCap, "input must actually exceed the cap to exercise truncation")

	// Sanity-check the fixture: the rune's bytes must genuinely straddle the
	// cap boundary (index analyzerDescriptionByteCap sits mid-rune), or this
	// test would not exercise the boundary case at all.
	require.Equal(t, byte(0xE2), s[analyzerDescriptionByteCap-2], "rune must start 2 bytes before the cap")
	require.Equal(t, byte(0xAC), s[analyzerDescriptionByteCap], "rune's last byte must land exactly at the cap index")

	got := truncateDescriptionForPrompt(s)

	assert.True(t, utf8.ValidString(got), "truncated output must always be valid UTF-8")
	require.True(t, strings.HasSuffix(got, truncatedSuffix))
	body := strings.TrimSuffix(got, truncatedSuffix)
	assert.Equal(t, prefix, body, "the cut must land before the straddling rune entirely, not mid-rune")
	assert.LessOrEqual(t, len(body), analyzerDescriptionByteCap)
}

func TestAgentPassClampsConfidence(t *testing.T) {
	runner := &fakeRunner{result: agent.TurnResult{
		ResultText: `{"edges":[{"source":"ENG-1","target":"ENG-2","evidence":"over","confidence":1.5},{"source":"ENG-3","target":"ENG-4","evidence":"under","confidence":-0.4}]}`,
	}}
	edges, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner:      runner,
		Profile:     config.AgentProfile{Command: "claude"},
		ProfileName: "deps-analyzer",
	})
	require.NoError(t, err)
	require.Len(t, edges, 2)
	assert.Equal(t, 1.0, edges[0].Confidence, "confidence above 1 clamps to 1")
	assert.Equal(t, 0.0, edges[1].Confidence, "confidence below 0 clamps to 0")
}
