package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/hub"
)

// Rendering and delivery of notifications (hub.Channel.Send).

// Block Kit action IDs this channel answers.
const (
	actionReply   = "itervox_reply"
	actionApprove = "itervox_approve"
	actionReject  = "itervox_reject"
	actionOpen    = "itervox_open"
)

// Slack limits the rendering respects.
const (
	maxSectionText = 2900 // a section's text is capped at 3000
	maxBlockID     = 255
	maxDigestLines = 40
)

type block = map[string]any

// message is a Slack message: fallback text plus blocks.
type message struct {
	Text   string
	Blocks []block
}

// runContext is the part of a member's GET /api/v1/issues/{id}/context
// the channel reads.
type runContext struct {
	Run *struct {
		RunID           string `json:"runId"`
		Live            bool   `json:"live"`
		PendingInput    string `json:"pendingInput"`
		PendingApproval bool   `json:"pendingApproval"`
	} `json:"run"`
}

// fetchContext reads the member's run context of identifier; ok is false
// when the member has none (404) or could not be asked.
func fetchContext(ctx context.Context, members hub.MemberActions, ref hub.MemberRef, identifier string) (runContext, bool) {
	rc, _, err := fetchContextStatus(ctx, members, ref, identifier)
	return rc, err == nil && rc.Run != nil
}

// errNoAnswer is a run context the member could not be asked for.
var errNoAnswer = errors.New("slack: the project did not answer")

// fetchContextStatus is fetchContext that tells "the member has no such
// run context" (nil error, rc.Run nil) from "the member could not be
// asked" (errNoAnswer).
func fetchContextStatus(ctx context.Context, members hub.MemberActions, ref hub.MemberRef, identifier string) (runContext, int, error) {
	status, body, err := members.Call(ctx, ref, http.MethodGet, "/api/v1/issues/"+url.PathEscape(identifier)+"/context", nil)
	var rc runContext
	switch {
	case err != nil:
		return runContext{}, 0, errNoAnswer
	case status == http.StatusNotFound:
		return runContext{}, status, nil
	case status != http.StatusOK || json.Unmarshal(body, &rc) != nil:
		return runContext{}, status, errNoAnswer
	}
	return rc, status, nil
}

// pending reports whether the run context still waits on the request a
// signed action was issued for.
func (rc runContext) pending(kind, runID string) bool {
	if rc.Run == nil || rc.Run.PendingInput == "" || rc.Run.PendingApproval != (kind == kindApproval) {
		return false
	}
	return runID == "" || rc.Run.RunID == runID
}

// Send implements hub.Channel: one chat.postMessage per destination
// channel. A digest is one message. An error the Web API calls permanent
// (a missing channel, a revoked token) is logged and the notification
// dropped; any other error is returned so the hub retries it.
func (c *Channel) Send(ctx context.Context, n hub.Notification) error {
	groups := c.destinations(n)
	if len(groups) == 0 {
		c.log.Debug("hub: slack: no channel set for the project; notification not sent", "project", n.Project, "kind", n.Kind)
		return nil
	}
	for _, g := range groups {
		msg := c.render(ctx, g.n)
		if _, err := c.postMessage(ctx, g.channel, msg); err != nil {
			if permanent(err) {
				c.log.Warn("hub: slack: notification dropped", "project", n.Project, "kind", n.Kind, "channel", g.channel, "error", err)
				continue
			}
			return err
		}
	}
	return nil
}

type destination struct {
	channel string
	n       hub.Notification
}

// destinations splits n by Slack channel: one message per channel, a
// digest's items grouped by their project's channel.
func (c *Channel) destinations(n hub.Notification) []destination {
	if n.Kind != hub.NotificationDigest {
		ch := c.channelFor(n.Project)
		if ch == "" {
			return nil
		}
		return []destination{{channel: ch, n: n}}
	}
	var out []destination
	index := map[string]int{}
	for _, it := range n.Items {
		ch := c.channelFor(it.Project)
		if ch == "" {
			continue
		}
		i, ok := index[ch]
		if !ok {
			i = len(out)
			index[ch] = i
			d := n
			d.Items = nil
			out = append(out, destination{channel: ch, n: d})
		}
		out[i].n.Items = append(out[i].n.Items, it)
	}
	for i := range out {
		out[i].n.Title = fmt.Sprintf("%d updates", len(out[i].n.Items))
	}
	return out
}

// render builds the message of n. An input request or guardrail approval
// gets its question and, when someone may act and the request is still
// pending, a signed reply box or Approve/Reject buttons; anything else (or
// a request that cannot be confirmed) is a plain alert with a dashboard
// link.
func (c *Channel) render(ctx context.Context, n hub.Notification) message {
	if n.Kind == hub.NotificationDigest {
		return c.renderDigest(n)
	}
	msg := message{Text: n.Title}
	head := "*" + escape(n.Title) + "*"
	if n.Project != "" {
		head += "\n" + escape(n.Project)
	}
	msg.Blocks = append(msg.Blocks, section(head))
	if n.Kind == hub.KindInputRequired || n.Kind == hub.KindGuardrailApproval {
		msg.Blocks = append(msg.Blocks, c.requestBlocks(ctx, n)...)
	}
	msg.Blocks = append(msg.Blocks, c.linkBlock(n.URL))
	return msg
}

// requestBlocks are the question and controls of an input request or a
// guardrail approval; none when the member cannot confirm it is pending.
func (c *Channel) requestBlocks(ctx context.Context, n hub.Notification) []block {
	members := c.actions()
	if members == nil {
		return nil
	}
	ref, ok := members.Member(n.Project)
	if !ok {
		return nil
	}
	kind := kindReply
	if n.Kind == hub.KindGuardrailApproval {
		kind = kindApproval
	}
	rc, ok := fetchContext(ctx, members, ref, n.Identifier)
	if !ok || !rc.pending(kind, n.RunID) {
		return nil
	}
	blocks := []block{section(">" + strings.ReplaceAll(escape(truncate(rc.Run.PendingInput, maxSectionText)), "\n", "\n>"))}
	if !c.interactive() {
		return blocks
	}
	value, err := c.sign(action{
		Kind: kind, Project: ref.Project, MemberKey: ref.Key(), Identifier: n.Identifier,
		RunID: rc.Run.RunID, Expires: c.now().Add(c.cfg.ActionTTL),
	})
	if err != nil {
		return blocks
	}
	if kind == kindApproval {
		return append(blocks, block{"type": "actions", "elements": []block{
			button(actionApprove, "Approve", value, "primary"),
			button(actionReject, "Reject", value, "danger"),
		}})
	}
	if len(value) > maxBlockID {
		return blocks // too long to carry: answer in the dashboard
	}
	return append(blocks, block{
		"type": "input", "block_id": value, "dispatch_action": true, "optional": false,
		"label": plain("Reply"),
		"element": block{
			"type": "plain_text_input", "action_id": actionReply, "max_length": 3000,
			"dispatch_action_config": block{"trigger_actions_on": []string{"on_enter_pressed"}},
			"placeholder":            plain("Type the answer and press Enter"),
		},
	})
}

// renderDigest renders a digest as one message: a line per item.
func (c *Channel) renderDigest(n hub.Notification) message {
	var lines []string
	for i, it := range n.Items {
		if i == maxDigestLines {
			lines = append(lines, fmt.Sprintf("…and %d more", len(n.Items)-maxDigestLines))
			break
		}
		line := "• " + escape(it.Title)
		if it.Project != "" {
			line += " (" + escape(it.Project) + ")"
		}
		lines = append(lines, line)
	}
	link := n.URL
	if projects := digestProjects(n.Items); len(projects) == 1 {
		link = "/hub/p/" + url.PathEscape(projects[0]) + "/"
	} else if link == "" {
		link = "/hub/"
	}
	return message{
		Text: n.Title,
		Blocks: []block{
			section("*" + escape(n.Title) + "*"),
			section(truncate(strings.Join(lines, "\n"), maxSectionText)),
			c.linkBlock(link),
		},
	}
}

func digestProjects(items []hub.Notification) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		if !seen[it.Project] {
			seen[it.Project] = true
			out = append(out, it.Project)
		}
	}
	return out
}

// linkBlock is the "Open in dashboard" link.
func (c *Channel) linkBlock(link string) block {
	if link == "" {
		link = "/hub/"
	}
	if m := c.actions(); m != nil {
		link = m.AbsoluteURL(link)
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return block{"type": "actions", "elements": []block{
			{"type": "button", "action_id": actionOpen, "text": plain("Open in dashboard"), "url": link},
		}}
	}
	return block{"type": "context", "elements": []block{{"type": "mrkdwn", "text": "Dashboard: " + escape(link)}}}
}

func section(text string) block {
	return block{"type": "section", "text": block{"type": "mrkdwn", "text": text}}
}

func plain(text string) block { return block{"type": "plain_text", "text": text} }

func button(id, text, value, style string) block {
	return block{"type": "button", "action_id": id, "text": plain(text), "value": value, "style": style}
}

// escape makes text safe for mrkdwn: Slack's control characters only.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
