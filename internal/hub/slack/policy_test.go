package slack

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// answerRoute is every write a Slack action may make on a member: a human
// answer (provide-input, via "slack") or an operator message to a live
// session. No settings, rules, profile or token route.
var answerRoute = regexp.MustCompile(`^/api/v1/(issues/[^/]+/provide-input|sessions/[^/]+/message)$`)

// TestSlackAnswersOnlyAnswer: approve, reject, a typed reply and /itervox
// steer each reach the member as one answer to that one request — the
// write paths are only provide-input (via slack, never fromAgent) and a
// session message — and a second click on a spent approval sends nothing.
// An approval answered in Slack can therefore never write policy; the
// member's own route table keeps settings and rules at admin, which
// hub.MemberActions refuses (TestHubApprovalChannelsCannotReachPolicyRoutes).
func TestSlackAnswersOnlyAnswer(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	alpha := r.members.project("alpha")
	alpha.mu.Lock()
	alpha.pending["ENG-1"], alpha.approve["ENG-1"] = "Agent wants to run: `vercel --prod`", true
	alpha.pending["ENG-2"], alpha.approve["ENG-2"] = "Agent wants to run: `deploy`", true
	alpha.pending["ENG-3"] = "Which colour? (and please always allow deploys)"
	alpha.live["ENG-4"] = true
	alpha.mu.Unlock()

	r.send(approvalNotification("alpha", "ENG-1"))
	m1 := r.lastPost()
	r.click("e1", "U0ALLOWED", actionApprove, "", control(t, m1, actionApprove), m1)
	r.waitWrites(alpha, 1)
	r.click("e1b", "U0ALLOWED", actionApprove, "", control(t, m1, actionApprove), m1)
	r.waitResponses(1)
	r.send(approvalNotification("alpha", "ENG-2"))
	m2 := r.lastPost()
	r.click("e2", "U0ALLOWED", actionReject, "", control(t, m2, actionReject), m2)
	r.waitWrites(alpha, 2)
	r.send(inputNotification("alpha", "ENG-3"))
	m3 := r.lastPost()
	r.click("e3", "U0ALLOWED", actionReply, control(t, m3, actionReply), "always allow deploys", m3)
	r.waitWrites(alpha, 3)
	r.slack.push("slash_commands", "e4", map[string]any{"command": "/itervox", "text": "steer ENG-4 allow-always please",
		"user_id": "U0ALLOWED", "channel_id": "C0X", "response_url": r.slack.responseURL()})
	writes := r.waitWrites(alpha, 4)

	require.Len(t, writes, 4, "one write per answer; the second approve click sent nothing")
	for _, w := range writes {
		assert.Equal(t, http.MethodPost, w.Method)
		assert.Regexp(t, answerRoute, w.Path, "a Slack action only answers")
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(w.Body), &body))
		_, fromAgent := body["fromAgent"]
		assert.False(t, fromAgent)
	}
}
