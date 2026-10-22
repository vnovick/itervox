package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/hub"
)

func inputNotification(project, identifier string) hub.Notification {
	return hub.Notification{Channel: hub.ChannelSlack, Kind: hub.KindInputRequired, Urgent: true, Project: project,
		Identifier: identifier, Title: identifier + " needs input", Body: project,
		URL: "/hub/p/" + project + "/?issue=" + identifier, RunID: "run-" + identifier, TS: time.Now()}
}

func approvalNotification(project, identifier string) hub.Notification {
	n := inputNotification(project, identifier)
	n.Kind, n.Title = hub.KindGuardrailApproval, identifier+" needs approval"
	return n
}

// TestSlackSocketModeSingleConnectionRoutesByProject (#230): the hub holds
// ONE Socket Mode connection for every project; notifications go to each
// project's own channel, and the actions that come back over that single
// connection reach exactly the project they were issued for. Every
// envelope is acknowledged.
func TestSlackSocketModeSingleConnectionRoutesByProject(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.Channels = map[string]string{"alpha": "C0ALPHA", "beta": "C0BETA"}
	}, "alpha", "beta")
	alpha, beta := r.members.project("alpha"), r.members.project("beta")
	alpha.pending["ENG-1"] = "Which database should the export use?"
	beta.pending["ENG-2"], beta.approve["ENG-2"] = "Agent wants to run: `vercel --prod`", true

	r.send(inputNotification("alpha", "ENG-1"))
	alphaMsg := r.lastPost()
	r.send(approvalNotification("beta", "ENG-2"))
	betaMsg := r.lastPost()
	assert.Equal(t, "C0ALPHA", alphaMsg.Channel)
	assert.Equal(t, "C0BETA", betaMsg.Channel)
	assert.Contains(t, fmt.Sprint(alphaMsg.Blocks), "Which database should the export use?")

	r.click("env-1", "U0ALLOWED", actionApprove, "", control(t, betaMsg, actionApprove), betaMsg)
	r.click("env-2", "U0ALLOWED", actionReply, control(t, alphaMsg, actionReply), "Postgres", alphaMsg)
	betaWrites := r.waitWrites(beta, 1)
	alphaWrites := r.waitWrites(alpha, 1)
	assert.Equal(t, "/api/v1/issues/ENG-2/provide-input", betaWrites[0].Path)
	assert.Equal(t, "/api/v1/issues/ENG-1/provide-input", alphaWrites[0].Path)
	assert.JSONEq(t, `{"message":"approve","via":"slack"}`, betaWrites[0].Body)
	assert.JSONEq(t, `{"message":"Postgres","via":"slack"}`, alphaWrites[0].Body)
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, beta.writes(), 1, "no cross-project leakage")
	assert.Len(t, alpha.writes(), 1, "no cross-project leakage")

	require.Eventually(t, func() bool { _, _, _, acks := r.slack.snapshot(); return len(acks) == 2 }, 5*time.Second, 10*time.Millisecond)
	_, _, _, acks := r.slack.snapshot()
	assert.ElementsMatch(t, []string{"env-1", "env-2"}, acks, "every envelope is acknowledged")
	assert.EqualValues(t, 1, r.slack.opens.Load(), "one apps.connections.open")
	assert.EqualValues(t, 1, r.slack.connections.Load(), "one Socket Mode connection for every project")
	assert.EqualValues(t, 1, r.ch.Connections())
}

// TestSlackApproveCallsMemberApproval (#230): Approve calls the member's
// approval path (provide-input with "approve", via slack) once; a second
// click gets an ephemeral "already handled" and calls nothing; the
// message is updated to show who approved; Reject sends a non-approval.
func TestSlackApproveCallsMemberApproval(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	alpha := r.members.project("alpha")
	alpha.pending["ENG-2"], alpha.approve["ENG-2"] = "Agent wants to run: `vercel --prod`", true
	r.send(approvalNotification("alpha", "ENG-2"))
	msg := r.lastPost()
	assert.Equal(t, "C0OPS", msg.Channel, "the default channel serves a project without its own")
	value := control(t, msg, actionApprove)
	assert.Equal(t, value, control(t, msg, actionReject))

	r.click("e1", "U0ALLOWED", actionApprove, "", value, msg)
	writes := r.waitWrites(alpha, 1)
	assert.Equal(t, http.MethodPost, writes[0].Method)
	assert.Equal(t, "/api/v1/issues/ENG-2/provide-input", writes[0].Path)
	assert.JSONEq(t, `{"message":"approve","via":"slack"}`, writes[0].Body)
	require.Eventually(t, func() bool { _, ups, _, _ := r.slack.snapshot(); return len(ups) == 1 }, 5*time.Second, 10*time.Millisecond)
	_, ups, _, _ := r.slack.snapshot()
	assert.Equal(t, msg.Channel, ups[0].Channel)
	assert.Equal(t, msg.TS, ups[0].TS)
	assert.Contains(t, ups[0].Text, "Approved by <@U0ALLOWED>")
	assert.NotContains(t, fmt.Sprint(ups[0].Blocks), actionApprove, "the buttons are gone")

	r.click("e2", "U0ALLOWED", actionApprove, "", value, msg)
	resp := r.waitResponses(1)
	assert.Equal(t, msgHandled, resp[0])
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, alpha.writes(), 1, "a second click acts on nothing")

	// A new request, rejected.
	alpha.mu.Lock()
	alpha.pending["ENG-3"], alpha.approve["ENG-3"] = "Agent wants to run: `git push --force`", true
	alpha.mu.Unlock()
	r.send(approvalNotification("alpha", "ENG-3"))
	msg3 := r.lastPost()
	r.click("e3", "U0ALLOWED", actionReject, "", control(t, msg3, actionReject), msg3)
	writes = r.waitWrites(alpha, 2)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(writes[1].Body), &body))
	assert.Equal(t, "/api/v1/issues/ENG-3/provide-input", writes[1].Path)
	assert.Equal(t, "slack", body["via"])
	assert.False(t, strings.HasPrefix(body["message"].(string), "approve"), "a rejection is never an approval word")
}

// TestSlackReplyIsHumanNotAgent (#230): a reply typed in Slack goes to the
// member's human provide-input route with via "slack" — never fromAgent,
// never the agent-actions route.
func TestSlackReplyIsHumanNotAgent(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Channels = map[string]string{"alpha": "C0ALPHA"} }, "alpha")
	alpha := r.members.project("alpha")
	alpha.pending["ENG-1"] = "Which colour?"
	r.send(inputNotification("alpha", "ENG-1"))
	msg := r.lastPost()
	r.click("e1", "U0ALLOWED", actionReply, control(t, msg, actionReply), "  Blue, please  ", msg)
	writes := r.waitWrites(alpha, 1)
	require.Len(t, writes, 1)
	assert.Equal(t, "/api/v1/issues/ENG-1/provide-input", writes[0].Path)
	assert.NotContains(t, writes[0].Path, "agent-actions")
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(writes[0].Body), &body))
	assert.Equal(t, map[string]any{"message": "Blue, please", "via": "slack"}, body)
	_, hasFromAgent := body["fromAgent"]
	assert.False(t, hasFromAgent, "a Slack reply never claims to come from an agent")

	// The question was answered: a repeat delivery is an alert only.
	r.send(inputNotification("alpha", "ENG-1"))
	assert.NotContains(t, fmt.Sprint(r.lastPost().Blocks), actionReply)

	// A reply box's value cannot approve a guardrail: the kind is signed.
	alpha.mu.Lock()
	alpha.pending["ENG-5"] = "Which port?"
	alpha.mu.Unlock()
	r.send(inputNotification("alpha", "ENG-5"))
	msg5 := r.lastPost()
	r.click("e2", "U0ALLOWED", actionApprove, "", control(t, msg5, actionReply), msg5)
	assert.Equal(t, msgBadValue, r.waitResponses(1)[0])
	assert.Len(t, alpha.writes(), 1)
}

// TestSlackRejectsUsersOutsideAllowList (#230): a user outside
// hub.slack.allowed_users gets an ephemeral "not allowed" and nothing
// reaches a member; with an empty allow-list nobody may act and messages
// carry no controls (alerts only).
func TestSlackRejectsUsersOutsideAllowList(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	alpha := r.members.project("alpha")
	alpha.pending["ENG-2"], alpha.approve["ENG-2"] = "Agent wants to run: `vercel --prod`", true
	r.send(approvalNotification("alpha", "ENG-2"))
	msg := r.lastPost()
	r.click("e1", "U0STRANGER", actionApprove, "", control(t, msg, actionApprove), msg)
	r.slack.push("slash_commands", "e2", map[string]any{"command": "/itervox", "text": "steer ENG-2 stop", "user_id": "U0STRANGER",
		"channel_id": "C0OPS", "response_url": r.slack.responseURL()})
	resp := r.waitResponses(2)
	assert.Equal(t, []string{msgNotAllowed, msgNotAllowed}, resp)
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, alpha.writes(), "nothing reaches the member")
	assert.Contains(t, r.logs.String(), "slack_user=U0STRANGER")
	assert.Contains(t, r.logs.String(), "outcome=not_allowed")

	// Empty allow-list: fail closed, alerts only.
	r2 := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS"; c.AllowedUsers = nil }, "alpha")
	a2 := r2.members.project("alpha")
	a2.pending["ENG-2"], a2.approve["ENG-2"] = "Agent wants to run: `vercel --prod`", true
	r2.send(approvalNotification("alpha", "ENG-2"))
	plainMsg := r2.lastPost()
	assert.NotContains(t, fmt.Sprint(plainMsg.Blocks), actionApprove, "no buttons when nobody may act")
	// Even a correctly signed value from a real message is refused.
	r.click("e3", "", actionApprove, "", control(t, msg, actionApprove), msg)
	r.waitResponses(3)
	assert.Empty(t, alpha.writes())
}

// TestSlackForgedValueCannotTargetAnotherProject (#230): a value whose
// payload was changed (another project or issue) fails its signature; a
// value signed for a member that has since been replaced is refused.
func TestSlackForgedValueCannotTargetAnotherProject(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha", "beta")
	alpha, beta := r.members.project("alpha"), r.members.project("beta")
	alpha.pending["ENG-2"], alpha.approve["ENG-2"] = "approve?", true
	beta.pending["ENG-2"], beta.approve["ENG-2"] = "approve?", true
	r.send(approvalNotification("alpha", "ENG-2"))
	msg := r.lastPost()
	value := control(t, msg, actionApprove)

	forged, err := r.ch.sign(action{Kind: kindApproval, Project: "beta", MemberKey: beta.ref.Key(), Identifier: "ENG-2",
		RunID: "run-ENG-2", Expires: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	_, sig, _ := strings.Cut(strings.TrimPrefix(value, valuePrefix), ".")
	payload, _, _ := strings.Cut(strings.TrimPrefix(forged, valuePrefix), ".")
	r.click("e1", "U0ALLOWED", actionApprove, "", valuePrefix+payload+"."+sig, msg) // beta's payload, alpha's signature
	assert.Equal(t, msgBadValue, r.waitResponses(1)[0])

	r.members.mu.Lock()
	r.members.projects["alpha"].ref.Workflow = "/elsewhere/WORKFLOW.md" // the name now names another member
	r.members.mu.Unlock()
	r.click("e2", "U0ALLOWED", actionApprove, "", value, msg)
	assert.Equal(t, msgGone, r.waitResponses(2)[1])
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, alpha.writes())
	assert.Empty(t, beta.writes())
}

// TestSlackSteerSlashCommand (#230): `/itervox steer ISSUE text` reaches
// the live run's session through the member's steering route; a name
// running in two projects must be qualified.
func TestSlackSteerSlashCommand(t *testing.T) {
	r := newRig(t, nil, "alpha", "beta")
	alpha, beta := r.members.project("alpha"), r.members.project("beta")
	alpha.live["ENG-12"] = true
	r.slack.push("slash_commands", "e1", map[string]any{"command": "/itervox", "text": "steer ENG-12 use the v2 API",
		"user_id": "U0ALLOWED", "channel_id": "C0X", "response_url": r.slack.responseURL()})
	writes := r.waitWrites(alpha, 1)
	assert.Equal(t, "/api/v1/sessions/run-ENG-12/message", writes[0].Path)
	assert.JSONEq(t, `{"message":"use the v2 API"}`, writes[0].Body)
	assert.Contains(t, r.waitResponses(1)[0], "Sent to ENG-12 (alpha)")

	beta.mu.Lock()
	beta.live["ENG-12"] = true
	beta.mu.Unlock()
	r.slack.push("slash_commands", "e2", map[string]any{"command": "/itervox", "text": "steer ENG-12 again",
		"user_id": "U0ALLOWED", "response_url": r.slack.responseURL()})
	assert.Contains(t, r.waitResponses(2)[1], "several projects")
	r.slack.push("slash_commands", "e3", map[string]any{"command": "/itervox", "text": "steer beta/ENG-12 only beta",
		"user_id": "U0ALLOWED", "response_url": r.slack.responseURL()})
	bw := r.waitWrites(beta, 1)
	assert.JSONEq(t, `{"message":"only beta"}`, bw[0].Body)
	assert.Len(t, alpha.writes(), 1)
}

// TestSlackDigestIsOneMessage (#229/#230): a digest is rendered as one
// Slack message listing its items.
func TestSlackDigestIsOneMessage(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	items := []hub.Notification{
		{Kind: hub.KindPRMerged, Project: "alpha", Identifier: "ENG-1", Title: "ENG-1 PR merged"},
		{Kind: hub.KindReviewVerdict, Project: "alpha", Identifier: "ENG-2", Title: "ENG-2 review approved"},
		{Kind: hub.KindLessonProposed, Project: "alpha", Identifier: "ENG-3", Title: "Lesson proposed ENG-3"},
	}
	r.send(hub.Notification{Channel: hub.ChannelSlack, Kind: hub.NotificationDigest, Title: "3 updates", Items: items, URL: "/hub/p/alpha/"})
	posts, _, _, _ := r.slack.snapshot()
	require.Len(t, posts, 1, "one message")
	text := fmt.Sprint(posts[0].Blocks)
	for _, it := range items {
		assert.Contains(t, text, it.Title)
	}
	assert.Contains(t, text, "http://127.0.0.1:8089/hub/p/alpha/")
}

// TestSlackReconnectsAfterDisconnect (#230): a disconnect message ends the
// connection and the client opens a new one; a failing
// apps.connections.open is retried with backoff.
func TestSlackReconnectsAfterDisconnect(t *testing.T) {
	r := newRig(t, nil, "alpha")
	r.slack.failOpens.Store(2)
	r.slack.push("disconnect", "", map[string]any{})
	require.Eventually(t, func() bool { return r.slack.connections.Load() == 2 && r.ch.Connected() }, 5*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 4, r.slack.opens.Load(), "two failed opens were retried")
}

// TestSlackTokensNeverLogged (#230): neither token appears in any log line
// (connection failures, Web API failures, actions included) nor in a
// formatted Config.
func TestSlackTokensNeverLogged(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	r.slack.failOpens.Store(1)
	r.slack.push("disconnect", "", map[string]any{"reason": "refresh_requested"})
	require.Eventually(t, func() bool { return r.slack.connections.Load() == 2 && r.ch.Connected() }, 5*time.Second, 10*time.Millisecond)
	r.slack.failPosts.Store(1)
	ctx := t.Context()
	assert.Error(t, r.ch.Send(ctx, inputNotification("alpha", "ENG-1")), "a 500 is retried by the hub")
	alpha := r.members.project("alpha")
	alpha.pending["ENG-1"] = "q?"
	r.send(inputNotification("alpha", "ENG-1"))
	msg := r.lastPost()
	r.click("e1", "U0ALLOWED", actionReply, control(t, msg, actionReply), "secret reply text", msg)
	r.waitWrites(alpha, 1)
	r.stop()

	logs := r.logs.String()
	require.NotEmpty(t, logs)
	for _, tok := range []string{testAppToken, testBotToken, "xapp-", "xoxb-"} {
		assert.NotContains(t, logs, tok)
	}
	assert.NotContains(t, logs, "secret reply text", "the audit line never carries message text")
	assert.Contains(t, logs, "action=reply")

	cfg := r.ch.cfg
	for _, s := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), fmt.Sprint(cfg.AppToken), cfg.BotToken.String()} {
		assert.NotContains(t, s, testAppToken)
		assert.NotContains(t, s, testBotToken)
	}
	_, err := New(Config{AppToken: testBotToken, BotToken: testAppToken, KeyPath: cfg.KeyPath})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "xoxb-1234", "a validation error never echoes a token")
}

// TestSlackManifestIsValid (#230): the printed manifest (YAML and JSON)
// decodes strictly into the manifest shape and meets Slack's rules: Socket
// Mode and interactivity on, bot scopes chat:write, commands and im:write,
// the /itervox command.
func TestSlackManifestIsValid(t *testing.T) {
	require.NoError(t, ValidateManifest(Manifest()))

	var fromYAML AppManifest
	dec := yaml.NewDecoder(strings.NewReader(ManifestYAML()))
	dec.KnownFields(true)
	require.NoError(t, dec.Decode(&fromYAML))
	require.NoError(t, ValidateManifest(fromYAML))
	assert.Equal(t, Manifest(), fromYAML)

	var fromJSON AppManifest
	jd := json.NewDecoder(strings.NewReader(ManifestJSON()))
	jd.DisallowUnknownFields()
	require.NoError(t, jd.Decode(&fromJSON))
	assert.Equal(t, Manifest(), fromJSON)

	var raw map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(ManifestYAML()), &raw))
	settings := raw["settings"].(map[string]any)
	assert.Equal(t, true, settings["socket_mode_enabled"])
	assert.Equal(t, true, settings["interactivity"].(map[string]any)["is_enabled"])
	_, hasURL := settings["interactivity"].(map[string]any)["request_url"]
	assert.False(t, hasURL, "Socket Mode: no request URL")
	assert.ElementsMatch(t, []any{"chat:write", "commands", "im:write"}, raw["oauth_config"].(map[string]any)["scopes"].(map[string]any)["bot"])

	bad := Manifest()
	bad.Settings.SocketModeEnabled = false
	bad.OAuthConfig.Scopes.Bot = append(bad.OAuthConfig.Scopes.Bot, "channels:history")
	assert.Error(t, ValidateManifest(bad))
}

// TestSlackGuideShowsTheExactManifest (#230): the Slack guide prints the
// manifest `itervox hub slack setup` prints, byte for byte.
func TestSlackGuideShowsTheExactManifest(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "..", "site", "src", "content", "docs", "guides", "slack.mdx"))
	require.NoError(t, err)
	assert.Contains(t, string(guide), "```yaml\n"+ManifestYAML()+"```", "update the manifest in guides/slack.mdx")
}

// TestSlackUnreachableMemberCanBeRetried (#230): when the member cannot be
// asked whether the request is still pending, nothing is sent and the
// action is not used up: a later click goes through.
func TestSlackUnreachableMemberCanBeRetried(t *testing.T) {
	r := newRig(t, func(c *Config) { c.DefaultChannel = "C0OPS" }, "alpha")
	alpha := r.members.project("alpha")
	alpha.pending["ENG-2"], alpha.approve["ENG-2"] = "Agent wants to run: `vercel --prod`", true
	r.send(approvalNotification("alpha", "ENG-2"))
	msg := r.lastPost()
	value := control(t, msg, actionApprove)
	alpha.down.Store(true)
	r.click("e1", "U0ALLOWED", actionApprove, "", value, msg)
	assert.Equal(t, refusedText(0), r.waitResponses(1)[0])
	assert.Empty(t, alpha.writes())
	alpha.down.Store(false)
	r.click("e2", "U0ALLOWED", actionApprove, "", value, msg)
	writes := r.waitWrites(alpha, 1)
	assert.JSONEq(t, `{"message":"approve","via":"slack"}`, writes[0].Body)
}
