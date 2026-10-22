package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Hub defaults (#228).
const (
	// DefaultHubPort is hub.port when unset; when it is busy the hub takes
	// the next free port and records it in the hub lock.
	DefaultHubPort = 8089
	// DefaultHubBind is hub.bind when unset: loopback only.
	DefaultHubBind = "127.0.0.1"
)

// HubConfig is the `hub` section (#228): whether this daemon takes part in
// the machine's itervox hub, and where the hub listens when this daemon is
// the one serving it. Machine-wide in practice, so it belongs in the user
// settings layer (~/.itervox/settings.jsonc); the daemon that wins the hub
// election uses its own values. Read at startup only (a reload does not
// move the hub).
type HubConfig struct {
	// Enabled (hub.enabled, default true) lets this daemon become the hub
	// or join it as a member. False is the same as --no-hub.
	Enabled bool
	// Port (hub.port, default 8089) is the hub's port; 0 lets the OS pick.
	Port int
	// Bind (hub.bind, default 127.0.0.1) is the hub's listen address.
	// Every hub route except the health probe needs a hub token, so
	// exposing it (0.0.0.0, a Tailscale address) is safe with tokens only.
	Bind string
	// Slack is hub.slack (#230): the hub's Slack channel through the
	// user's own Slack app over Socket Mode.
	Slack HubSlackConfig
}

// HubSlackConfig is hub.slack (#230). The tokens are not settings: they
// live in ~/.itervox/hub/.env (`itervox hub slack setup`). Read at startup.
type HubSlackConfig struct {
	// Enabled (hub.slack.enabled, default false) registers the Slack
	// channel when this daemon serves the hub (it also needs both tokens).
	Enabled bool
	// AllowedUsers (hub.slack.allowed_users) are the Slack user IDs that
	// may reply, approve, reject and steer from Slack. Empty: nobody (fail
	// closed); messages are alerts only.
	AllowedUsers []string
	// Channels (hub.slack.channels) maps a hub project name to the Slack
	// channel ID (or user ID, for a DM from the app) its messages go to.
	Channels map[string]string
	// DefaultChannel (hub.slack.default_channel) serves every project
	// without its own channel. Empty: such projects are not posted.
	DefaultChannel string
}

// slackIDRe is a Slack channel, group, DM or user ID.
var slackIDRe = regexp.MustCompile(`^[CGDUW][A-Z0-9]{2,30}$`)

// parseHubConfig reads the hub section.
func parseHubConfig(m map[string]any) (HubConfig, error) {
	h := HubConfig{
		Enabled: boolField(m, "enabled", true),
		Port:    intField(m, "port", DefaultHubPort),
		Bind:    strings.TrimSpace(strField(m, "bind", DefaultHubBind)),
	}
	if h.Port < 0 || h.Port > 65535 {
		return h, fmt.Errorf("config: hub.port must be between 0 and 65535 (got %d)", h.Port)
	}
	if h.Bind != "localhost" && net.ParseIP(h.Bind) == nil {
		return h, fmt.Errorf("config: hub.bind must be an IP address or localhost (got %q)", h.Bind)
	}
	var err error
	h.Slack, err = parseHubSlackConfig(nestedMap(m, "slack"))
	return h, err
}

// parseHubSlackConfig reads hub.slack; every ID must look like a Slack ID.
func parseHubSlackConfig(m map[string]any) (HubSlackConfig, error) {
	s := HubSlackConfig{
		Enabled:        boolField(m, "enabled", false),
		AllowedUsers:   strSliceField(m, "allowed_users", nil),
		Channels:       stringMapField(m, "channels", nil),
		DefaultChannel: strings.TrimSpace(strField(m, "default_channel", "")),
	}
	for i, u := range s.AllowedUsers {
		u = strings.TrimSpace(u)
		s.AllowedUsers[i] = u
		if !slackIDRe.MatchString(u) || (u[0] != 'U' && u[0] != 'W') {
			return s, fmt.Errorf("config: hub.slack.allowed_users: %q is not a Slack user ID (U…)", u)
		}
	}
	for project, ch := range s.Channels {
		if !slackIDRe.MatchString(ch) {
			return s, fmt.Errorf("config: hub.slack.channels.%s: %q is not a Slack channel or user ID", project, ch)
		}
	}
	if s.DefaultChannel != "" && !slackIDRe.MatchString(s.DefaultChannel) {
		return s, fmt.Errorf("config: hub.slack.default_channel: %q is not a Slack channel or user ID", s.DefaultChannel)
	}
	return s, nil
}
