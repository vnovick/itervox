package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLiveSteeringAppliesOnlyToLocalPromptedClaudeTurns: an SSH worker's
// stdin carries the remote script and a bare resume reads no prompt, so
// both keep the classic path; so does a run whose steering is not live.
func TestLiveSteeringAppliesOnlyToLocalPromptedClaudeTurns(t *testing.T) {
	sid := "sess-1"
	live := WithSteering(context.Background(), NewSteering(nil, true, nil))
	notLive := WithSteering(context.Background(), NewSteering(nil, false, nil))

	assert.NotNil(t, liveSteeringFor(live, "", nil, "prompt"), "local first turn")
	assert.NotNil(t, liveSteeringFor(live, "", &sid, "reply"), "local resume with a prompt")
	assert.Nil(t, liveSteeringFor(live, "worker-1", nil, "prompt"), "SSH worker")
	assert.Nil(t, liveSteeringFor(live, "", &sid, ""), "bare resume")
	assert.Nil(t, liveSteeringFor(notLive, "", nil, "prompt"), "live steering off")
	assert.Nil(t, liveSteeringFor(context.Background(), "", nil, "prompt"), "no steering")

	assert.Contains(t, buildShellCmdLive("claude", nil, "p", localPromptRedirect, PermissionBypass, true),
		" --input-format stream-json -p < /dev/fd/3")
	assert.NotContains(t, buildShellCmd("claude", nil, "p", localPromptRedirect, PermissionBypass), "--input-format")
}

func TestStreamUserMessageIsOneJSONLine(t *testing.T) {
	b, err := streamUserMessage("line one\nline \"two\"")
	assert.NoError(t, err)
	assert.Equal(t, `{"message":{"content":"line one\nline \"two\"","role":"user"},"type":"user"}`+"\n", string(b))
}
