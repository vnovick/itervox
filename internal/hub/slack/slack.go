// Package slack is the hub's Slack channel (#230): alerts, replies,
// guardrail approvals and steering through the user's own Slack app over
// Socket Mode.
//
// The hub holds the workspace's single Socket Mode connection (Socket Mode
// spreads events over every open connection, so only one process may hold
// it): Channel implements hub.InteractiveChannel, so its Run starts when a
// daemon starts serving the hub and ends when it stops. It is an HTTP
// client only — no inbound port.
//
//   - Send posts routed notifications (hub.NotificationSettings decides
//     which; this package never routes) with chat.postMessage, to the
//     project's channel (hub.slack.channels) or hub.slack.default_channel.
//     A digest is one message.
//   - An input request gets the question and a reply box; a guardrail
//     approval gets Approve and Reject buttons. Each carries a value the
//     hub signs (HMAC-SHA256 with a hub-local key) binding project, member,
//     issue, run and kind, so a forged or replayed value cannot reach
//     another project, issue or run.
//   - Every action needs an allow-listed Slack user (hub.slack.allowed_users;
//     empty: nobody, alerts only). The member must still be the one the
//     value was signed for, the request must still be pending, and a value
//     is acted on once; anything else gets an ephemeral answer.
//   - A reply or decision goes to the member's POST
//     /api/v1/issues/{identifier}/provide-input with via "slack" (a human
//     reply; never fromAgent); `/itervox steer ISSUE text` goes to POST
//     /api/v1/sessions/{runId}/message. All through hub.MemberActions: the
//     member's own API, its route table, the hub's operator token.
//   - Every action is logged (Slack user ID, project, issue, action,
//     outcome; never message text). The tokens are never logged.
package slack

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/hub"
	"github.com/vnovick/itervox/internal/metrics"
)

// Secret is a Slack token. It prints, formats and logs as "[redacted]".
type Secret string

const redacted = "[redacted]"

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// reveal is the token itself, for an Authorization header only.
func (s Secret) reveal() string { return string(s) }

// Token prefixes Slack issues.
const (
	AppTokenPrefix = "xapp-"
	BotTokenPrefix = "xoxb-"
)

// DefaultAPIBase is Slack's Web API.
const DefaultAPIBase = "https://slack.com/api/"

// Config is the Slack channel's configuration.
type Config struct {
	// AppToken (xapp-…, connections:write) opens the Socket Mode connection.
	AppToken Secret
	// BotToken (xoxb-…) posts and updates messages.
	BotToken Secret
	// AllowedUsers are the Slack user IDs that may act; empty: nobody.
	AllowedUsers []string
	// Channels maps a hub project name to its channel, private channel or
	// DM (a channel ID, or a user ID for the app's DM with that user).
	Channels map[string]string
	// DefaultChannel is used for a project without its own.
	DefaultChannel string
	// KeyPath is the hub-local signing key file (created 0600 on first use).
	KeyPath string

	// APIBase is the Web API base URL (DefaultAPIBase); tests point it at a
	// fake. A response_url must be https://hooks.slack.com/… or on the
	// APIBase host.
	APIBase string
	// HTTPClient makes the Web API calls (a 15 s timeout client when nil).
	HTTPClient *http.Client
	// Logger receives the channel's logs (slog.Default when nil).
	Logger *slog.Logger
	// Tunables; zero means the default in parentheses.
	ReconnectMin time.Duration // first reconnect delay, doubling (1s)
	ReconnectMax time.Duration // reconnect delay cap (2m)
	ReadIdle     time.Duration // reconnect after this long without a frame (10m)
	ActionTTL    time.Duration // how long a signed action stays valid (7 days)
}

// idRe is a Slack channel, group, DM or user ID.
var idRe = regexp.MustCompile(`^[CGDUW][A-Z0-9]{2,30}$`)

// ValidID reports whether s looks like a Slack channel or user ID.
func ValidID(s string) bool { return idRe.MatchString(s) }

// Validate checks the tokens' kinds and every ID. It never prints a token.
func (c Config) Validate() error {
	if !strings.HasPrefix(string(c.AppToken), AppTokenPrefix) {
		return errors.New("slack: the app token must be an app-level token (xapp-…)")
	}
	if !strings.HasPrefix(string(c.BotToken), BotTokenPrefix) {
		return errors.New("slack: the bot token must be a bot token (xoxb-…)")
	}
	for _, u := range c.AllowedUsers {
		if (!strings.HasPrefix(u, "U") && !strings.HasPrefix(u, "W")) || !ValidID(u) {
			return fmt.Errorf("slack: %q is not a Slack user ID", u)
		}
	}
	for p, ch := range c.Channels {
		if !ValidID(ch) {
			return fmt.Errorf("slack: the channel of project %q is not a Slack channel or user ID", p)
		}
	}
	if c.DefaultChannel != "" && !ValidID(c.DefaultChannel) {
		return errors.New("slack: the default channel is not a Slack channel or user ID")
	}
	if c.KeyPath == "" {
		return errors.New("slack: no signing key path")
	}
	return nil
}

// Channel is the hub's Slack channel (hub.Channel and hub.InteractiveChannel).
type Channel struct {
	cfg     Config
	key     []byte
	apiBase *url.URL
	http    *http.Client
	log     *slog.Logger
	now     func() time.Time

	members atomic.Pointer[hub.MemberActions]
	// connections counts Socket Mode connections opened (tests, status).
	connections atomic.Int64
	connected   atomic.Bool

	mu      sync.Mutex
	handled map[string]time.Time // signed values already acted on → expiry
	// sem bounds concurrent action handlers.
	sem chan struct{}
}

var (
	_ hub.Channel            = (*Channel)(nil)
	_ hub.InteractiveChannel = (*Channel)(nil)
)

// New validates cfg, loads (or creates) the signing key and returns the
// channel.
func New(cfg Config) (*Channel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if !strings.HasSuffix(cfg.APIBase, "/") {
		cfg.APIBase += "/"
	}
	base, err := url.Parse(cfg.APIBase)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("slack: the Web API base is not an http(s) URL")
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = time.Second
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 2 * time.Minute
	}
	if cfg.ReadIdle <= 0 {
		cfg.ReadIdle = 10 * time.Minute
	}
	if cfg.ActionTTL <= 0 {
		cfg.ActionTTL = 7 * 24 * time.Hour
	}
	key, err := loadKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	c := &Channel{
		cfg: cfg, key: key, apiBase: base, http: cfg.HTTPClient, log: cfg.Logger, now: time.Now,
		handled: map[string]time.Time{}, sem: make(chan struct{}, 8),
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	return c, nil
}

// Name implements hub.Channel.
func (*Channel) Name() string { return hub.ChannelSlack }

// Connected reports whether the Socket Mode connection is up.
func (c *Channel) Connected() bool { return c.connected.Load() }

// Connections is how many Socket Mode connections this channel opened.
func (c *Channel) Connections() int64 { return c.connections.Load() }

// actions returns the hub's member actions; nil before Run.
func (c *Channel) actions() hub.MemberActions {
	if p := c.members.Load(); p != nil {
		return *p
	}
	return nil
}

// allowed reports whether a Slack user may act (fail closed: an empty list
// allows nobody).
func (c *Channel) allowed(user string) bool {
	return user != "" && slices.Contains(c.cfg.AllowedUsers, user)
}

// interactive reports whether messages get buttons and reply boxes: only
// when someone may use them.
func (c *Channel) interactive() bool { return len(c.cfg.AllowedUsers) > 0 }

// channelFor is the Slack channel of a project ("" when none is set).
func (c *Channel) channelFor(project string) string {
	if ch := c.cfg.Channels[project]; ch != "" {
		return ch
	}
	return c.cfg.DefaultChannel
}

// claim marks a signed value as being acted on; false when it already is
// (or was). A claimed value is kept until it expires.
func (c *Channel) claim(value string, exp time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.handled {
		if now.After(e) {
			delete(c.handled, k)
		}
	}
	if _, ok := c.handled[value]; ok {
		return false
	}
	c.handled[value] = exp
	return true
}

// release forgets a claim whose action failed, so it can be tried again.
func (c *Channel) release(value string) {
	c.mu.Lock()
	delete(c.handled, value)
	c.mu.Unlock()
}

// recoverHubGoroutine is the hub's panic containment (CORE-008) for this
// package's goroutines, like internal/hub's: a panic is logged with its
// stack and counted, and ends only that goroutine.
func recoverHubGoroutine(name string) {
	r := recover()
	if r == nil {
		return
	}
	metrics.GoroutinePanic()
	slog.Error("hub: goroutine panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))
}
