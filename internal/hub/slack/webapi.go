package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
)

// Web API calls (chat.postMessage, chat.update, apps.connections.open) and
// response_url answers, over net/http. A token only ever goes into an
// Authorization header; no error carries one.

// apiResponseLimit bounds a Web API response the channel reads.
const apiResponseLimit = 1 << 20

// apiError is a Web API answer with ok: false (or an HTTP failure).
type apiError struct {
	method string
	code   string
	status int
}

func (e *apiError) Error() string {
	if e.code == "" {
		return fmt.Sprintf("slack: %s answered HTTP %d", e.method, e.status)
	}
	return fmt.Sprintf("slack: %s: %s", e.method, e.code)
}

// permanentCodes are Web API errors a retry cannot fix: a notification
// that hits one is dropped (and logged) rather than retried forever, which
// would hold every later Slack notification behind it.
var permanentCodes = []string{
	"invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired",
	"missing_scope", "channel_not_found", "not_in_channel", "is_archived", "user_not_found",
	"cannot_dm_bot", "invalid_blocks", "invalid_arguments", "msg_too_long", "no_text",
	"restricted_action", "ekm_access_denied", "team_access_not_granted",
}

// permanent reports whether err is a Web API error a retry cannot fix.
func permanent(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && slices.Contains(permanentCodes, ae.code)
}

// call POSTs body as JSON to the Web API method with token and decodes
// the answer into out (may be nil). ok: false is an *apiError.
func (c *Channel) call(ctx context.Context, method string, token Secret, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase.String()+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token.reveal())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, scrubURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, apiResponseLimit))
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &head) != nil {
		code := ""
		if resp.StatusCode == http.StatusTooManyRequests {
			code = "ratelimited"
		}
		return &apiError{method: method, code: code, status: resp.StatusCode}
	}
	if !head.OK {
		return &apiError{method: method, code: safeCode(head.Error), status: resp.StatusCode}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("slack: %s: %w", method, err)
		}
	}
	return nil
}

// safeCode keeps a Web API error code to its documented shape (lowercase
// letters and underscores), so nothing else from a response is logged.
func safeCode(code string) string {
	if len(code) > 64 {
		return "unknown_error"
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return "unknown_error"
		}
	}
	return code
}

// scrubURLError drops the URL from a transport error (it never holds a
// token, but a response_url is a capability and stays out of logs).
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// postedMessage is chat.postMessage's answer.
type postedMessage struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// postMessage posts msg to channel.
func (c *Channel) postMessage(ctx context.Context, channel string, msg message) (postedMessage, error) {
	var out postedMessage
	body := map[string]any{"channel": channel, "text": msg.Text, "unfurl_links": false, "unfurl_media": false}
	if len(msg.Blocks) > 0 {
		body["blocks"] = msg.Blocks
	}
	err := c.call(ctx, "chat.postMessage", c.cfg.BotToken, body, &out)
	return out, err
}

// updateMessage replaces the message at channel/ts.
func (c *Channel) updateMessage(ctx context.Context, channel, ts string, msg message) error {
	body := map[string]any{"channel": channel, "ts": ts, "text": msg.Text, "blocks": msg.Blocks}
	if msg.Blocks == nil {
		body["blocks"] = []block{}
	}
	return c.call(ctx, "chat.update", c.cfg.BotToken, body, nil)
}

// openConnection asks for a Socket Mode WebSocket URL with the app token.
func (c *Channel) openConnection(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.call(ctx, "apps.connections.open", c.cfg.AppToken, map[string]any{}, &out); err != nil {
		return "", err
	}
	u, err := url.Parse(out.URL)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		return "", errors.New("slack: apps.connections.open returned no WebSocket URL")
	}
	if u.Scheme == "ws" && c.apiBase.Scheme != "http" {
		return "", errors.New("slack: refusing an unencrypted Socket Mode URL")
	}
	return out.URL, nil
}

// respond answers an interaction through its response_url (an ephemeral
// message only the user sees, unless replace is set). The URL must be on
// hooks.slack.com over https, or on the Web API base's host (tests).
func (c *Channel) respond(ctx context.Context, responseURL, text string) {
	u, err := url.Parse(responseURL)
	if err != nil || !c.responseURLAllowed(u) {
		c.log.Warn("hub: slack: ignoring a response_url outside Slack")
		return
	}
	data, _ := json.Marshal(map[string]any{"response_type": "ephemeral", "replace_original": false, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("hub: slack: could not answer an interaction", "error", scrubURLError(err))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, apiResponseLimit))
	_ = resp.Body.Close()
}

func (c *Channel) responseURLAllowed(u *url.URL) bool {
	if u.Scheme == "https" && u.Host == "hooks.slack.com" {
		return true
	}
	return u.Scheme == c.apiBase.Scheme && u.Host == c.apiBase.Host
}
