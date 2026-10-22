package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/hub"
)

// Interactions: button clicks and reply boxes (block_actions) and the
// /itervox slash command. Every path checks, in order: the Slack user is
// allow-listed; the signed value verifies (or, for a slash command, the
// target resolves to a current member); the member is still the one it
// was signed for; the request is still pending; the value was not acted on
// already. Each refusal answers the user ephemerally and is logged.

// maxReplyBytes bounds a reply relayed to a member.
const maxReplyBytes = 10 * 1024

// Audit outcomes.
const (
	outcomeOK         = "ok"
	outcomeNotAllowed = "not_allowed"
	outcomeBadValue   = "bad_value"
	outcomeGone       = "member_gone"
	outcomeHandled    = "already_handled"
	outcomeRefused    = "member_refused"
	outcomeFailed     = "failed"
	outcomeInvalid    = "invalid"
)

// Ephemeral answers.
const (
	msgNotAllowed = "You are not allowed to act on Itervox from Slack. Ask the hub's owner to add your Slack user ID to hub.slack.allowed_users."
	msgBadValue   = "This action is not valid: it expired or was not issued by this Itervox hub. Open the dashboard instead."
	msgGone       = "That project is no longer on this Itervox hub."
	msgHandled    = "Already handled: this request is no longer waiting for an answer."
	msgNotReady   = "The Itervox hub is not ready yet; try again in a moment."
)

// audit logs one Slack action: who, where, what and how it ended. Never
// the message text.
func (c *Channel) audit(user, project, identifier, act, outcome string) {
	c.log.Info("hub: slack action", "slack_user", user, "project", project, "issue", identifier,
		"action", act, "outcome", outcome, "via", "slack")
}

// interactivePayload is the part of an interactive envelope's payload the
// channel reads.
type interactivePayload struct {
	Type string `json:"type"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Container struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	ResponseURL string `json:"response_url"`
	Actions     []struct {
		ActionID string `json:"action_id"`
		BlockID  string `json:"block_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// handleInteractive handles a block_actions payload.
func (c *Channel) handleInteractive(ctx context.Context, raw json.RawMessage) {
	var p interactivePayload
	if json.Unmarshal(raw, &p) != nil || p.Type != "block_actions" {
		return
	}
	for _, a := range p.Actions {
		var name, value, text string
		switch a.ActionID {
		case actionApprove:
			name, value, text = "approve", a.Value, "approve"
		case actionReject:
			name, value, text = "reject", a.Value, "reject (rejected in Slack)"
		case actionReply:
			name, value, text = "reply", a.BlockID, strings.TrimSpace(a.Value)
		default:
			continue // the dashboard link, or something not ours
		}
		c.act(ctx, p, name, value, text)
		return
	}
}

// act carries out one reply or decision.
func (c *Channel) act(ctx context.Context, p interactivePayload, name, value, text string) {
	user := p.User.ID
	if !c.allowed(user) {
		c.audit(user, "", "", name, outcomeNotAllowed)
		c.respond(ctx, p.ResponseURL, msgNotAllowed)
		return
	}
	a, verr := c.verify(value)
	wantKind := kindApproval
	if name == "reply" {
		wantKind = kindReply
	}
	if verr != nil || a.Kind != wantKind {
		c.audit(user, "", "", name, outcomeBadValue)
		c.respond(ctx, p.ResponseURL, msgBadValue)
		return
	}
	if name == "reply" && (text == "" || len(text) > maxReplyBytes) {
		c.audit(user, a.Project, a.Identifier, name, outcomeInvalid)
		c.respond(ctx, p.ResponseURL, "The reply must be between 1 character and 10 KiB.")
		return
	}
	members := c.actions()
	if members == nil {
		c.respond(ctx, p.ResponseURL, msgNotReady)
		return
	}
	ref, ok := members.Member(a.Project)
	if !ok || ref.Key() != a.MemberKey {
		c.audit(user, a.Project, a.Identifier, name, outcomeGone)
		c.respond(ctx, p.ResponseURL, msgGone)
		return
	}
	if !c.claim(value, a.Expires) {
		c.audit(user, a.Project, a.Identifier, name, outcomeHandled)
		c.respond(ctx, p.ResponseURL, msgHandled)
		return
	}
	rc, _, err := fetchContextStatus(ctx, members, ref, a.Identifier)
	if err != nil {
		c.release(value) // a daemon that did not answer may answer the next click
		c.audit(user, a.Project, a.Identifier, name, outcomeFailed)
		c.respond(ctx, p.ResponseURL, refusedText(0))
		return
	}
	if !rc.pending(a.Kind, a.RunID) {
		c.audit(user, a.Project, a.Identifier, name, outcomeHandled)
		c.respond(ctx, p.ResponseURL, msgHandled)
		return // the claim stays: the request is gone for good
	}
	// A human reply: via "slack", never fromAgent.
	status, _, err := members.Call(ctx, ref, http.MethodPost,
		"/api/v1/issues/"+url.PathEscape(a.Identifier)+"/provide-input",
		provideInputBody{Message: text, Via: "slack"})
	if err != nil || status != http.StatusAccepted {
		c.release(value)
		outcome := outcomeFailed
		if err == nil {
			outcome = outcomeRefused
		}
		c.log.Warn("hub: slack: the project did not take the answer", "project", a.Project, "issue", a.Identifier, "status", status, "error", err)
		c.audit(user, a.Project, a.Identifier, name, outcome)
		c.respond(ctx, p.ResponseURL, refusedText(status))
		return
	}
	c.audit(user, a.Project, a.Identifier, name, outcomeOK)
	c.markDone(ctx, p, a, name, user)
}

// provideInputBody is the member's provide-input body. It has no
// fromAgent field at all: a Slack answer is a person's.
type provideInputBody struct {
	Message string `json:"message"`
	Via     string `json:"via"`
}

// refusedText explains a member's refusal.
func refusedText(status int) string {
	switch status {
	case http.StatusConflict:
		return "The project answers questions in its tracker only (agent.inline_input); reply there."
	case http.StatusNotFound:
		return msgHandled
	case 0:
		return "The project's daemon did not answer; try again or open the dashboard."
	}
	return "The project refused the answer (HTTP " + http.StatusText(status) + "); open the dashboard."
}

// markDone replaces the original message's controls with who answered.
func (c *Channel) markDone(ctx context.Context, p interactivePayload, a action, name, user string) {
	channel := p.Container.ChannelID
	if channel == "" {
		channel = p.Channel.ID
	}
	if channel == "" || p.Container.MessageTS == "" {
		return
	}
	verb := map[string]string{"approve": "Approved", "reject": "Rejected", "reply": "Answered"}[name]
	text := escape(a.Identifier) + " (" + escape(a.Project) + "): " + verb + " by <@" + user + "> in Slack"
	link := "/hub/p/" + url.PathEscape(a.Project) + "/?issue=" + url.QueryEscape(a.Identifier)
	msg := message{Text: text, Blocks: []block{section(text), c.linkBlock(link)}}
	if err := c.updateMessage(ctx, channel, p.Container.MessageTS, msg); err != nil {
		c.log.Debug("hub: slack: could not update the answered message", "error", err)
	}
}

// slashPayload is a slash_commands envelope's payload.
type slashPayload struct {
	Command     string `json:"command"`
	Text        string `json:"text"`
	UserID      string `json:"user_id"`
	ChannelID   string `json:"channel_id"`
	ResponseURL string `json:"response_url"`
}

const steerUsage = "Usage: /itervox steer [project/]ISSUE text — sends text to the issue's running agent."

// handleSlashCommand handles `/itervox steer [project/]ISSUE text`.
func (c *Channel) handleSlashCommand(ctx context.Context, raw json.RawMessage) {
	var p slashPayload
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	if !c.allowed(p.UserID) {
		c.audit(p.UserID, "", "", "steer", outcomeNotAllowed)
		c.respond(ctx, p.ResponseURL, msgNotAllowed)
		return
	}
	fields := strings.Fields(p.Text)
	if len(fields) < 3 || fields[0] != "steer" {
		c.respond(ctx, p.ResponseURL, steerUsage)
		return
	}
	target := fields[1]
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Text), "steer")), target))
	if text == "" || len(text) > maxReplyBytes {
		c.respond(ctx, p.ResponseURL, steerUsage)
		return
	}
	members := c.actions()
	if members == nil {
		c.respond(ctx, p.ResponseURL, msgNotReady)
		return
	}
	ref, identifier, runID, problem := c.resolveSteer(ctx, members, target, p.ChannelID)
	if problem != "" {
		c.audit(p.UserID, ref.Project, identifier, "steer", outcomeInvalid)
		c.respond(ctx, p.ResponseURL, problem)
		return
	}
	status, _, err := members.Call(ctx, ref, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(runID)+"/message",
		map[string]string{"message": text})
	switch {
	case err == nil && status == http.StatusAccepted:
		c.audit(p.UserID, ref.Project, identifier, "steer", outcomeOK)
		c.respond(ctx, p.ResponseURL, "Sent to "+identifier+" ("+ref.Project+"): it reaches the agent at its next turn.")
	case err == nil && status == http.StatusConflict:
		c.audit(p.UserID, ref.Project, identifier, "steer", outcomeRefused)
		c.respond(ctx, p.ResponseURL, identifier+" is waiting for an answer: reply to its question instead.")
	default:
		outcome := outcomeFailed
		if err == nil {
			outcome = outcomeRefused
		}
		c.audit(p.UserID, ref.Project, identifier, "steer", outcome)
		c.respond(ctx, p.ResponseURL, "The project did not take the steer; is "+identifier+" still running?")
	}
}

// resolveSteer finds the member and live run of target ("ISSUE" or
// "project/ISSUE"). Without a project it uses the project whose channel
// the command came from, else the one member with a live run of ISSUE.
func (c *Channel) resolveSteer(ctx context.Context, members hub.MemberActions, target, channelID string) (hub.MemberRef, string, string, string) {
	identifier := target
	var candidates []hub.MemberRef
	if project, id, ok := strings.Cut(target, "/"); ok {
		if ref, ok := members.Member(project); ok {
			candidates, identifier = []hub.MemberRef{ref}, id
		}
	}
	if candidates == nil {
		for _, ref := range members.Projects() {
			if channelID != "" && c.cfg.Channels[ref.Project] == channelID {
				candidates = append(candidates, ref)
			}
		}
		if len(candidates) != 1 {
			candidates = members.Projects()
		}
	}
	var found []hub.MemberRef
	var runID string
	for _, ref := range candidates {
		rc, ok := fetchContext(ctx, members, ref, identifier)
		if ok && rc.Run.Live && rc.Run.RunID != "" {
			found, runID = append(found, ref), rc.Run.RunID
		}
	}
	switch len(found) {
	case 0:
		return hub.MemberRef{}, identifier, "", "No running agent for " + identifier + " on this hub."
	case 1:
		return found[0], identifier, runID, ""
	}
	return hub.MemberRef{}, identifier, "", identifier + " runs in several projects; name one: /itervox steer PROJECT/" + identifier + " text"
}
