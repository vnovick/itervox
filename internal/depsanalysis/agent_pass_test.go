package depsanalysis

import (
	"context"
	"errors"
	"strings"
	"testing"

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
