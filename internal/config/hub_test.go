package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestHubConfigRoundTrip (#228): hub.enabled (default true), hub.port
// (default 8089) and hub.bind (default 127.0.0.1) parse, and invalid values
// are refused at load.
func TestHubConfigRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	assert.Equal(t, config.HubConfig{Enabled: true, Port: 8089, Bind: "127.0.0.1"}, cfg.Hub)

	cfg, err = config.Load(workflowWithContent(t, minimal("hub:\n  enabled: false\n  port: 9100\n  bind: 0.0.0.0\n")))
	require.NoError(t, err)
	assert.Equal(t, config.HubConfig{Enabled: false, Port: 9100, Bind: "0.0.0.0"}, cfg.Hub)

	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  port: 70000\n")))
	assert.ErrorContains(t, err, "hub.port")
	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  bind: my.host.example\n")))
	assert.ErrorContains(t, err, "hub.bind")
}

// TestHubSlackConfigRoundTrip (#230): hub.slack.enabled (default false),
// allowed_users (default none: nobody may act), channels and
// default_channel parse; an ID that is not a Slack ID is refused at load.
func TestHubSlackConfigRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	assert.Equal(t, config.HubSlackConfig{}, cfg.Hub.Slack)

	cfg, err = config.Load(workflowWithContent(t, minimal("hub:\n  slack:\n    enabled: true\n    allowed_users: [U012ABCDEF]\n"+
		"    channels:\n      alpha: C0ALPHA1\n    default_channel: U0DMME\n")))
	require.NoError(t, err)
	assert.Equal(t, config.HubSlackConfig{Enabled: true, AllowedUsers: []string{"U012ABCDEF"},
		Channels: map[string]string{"alpha": "C0ALPHA1"}, DefaultChannel: "U0DMME"}, cfg.Hub.Slack)

	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  slack:\n    allowed_users: [\"@vlad\"]\n")))
	assert.ErrorContains(t, err, "hub.slack.allowed_users")
	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  slack:\n    allowed_users: [C0CHANNEL]\n")))
	assert.ErrorContains(t, err, "hub.slack.allowed_users")
	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  slack:\n    channels:\n      alpha: \"#general\"\n")))
	assert.ErrorContains(t, err, "hub.slack.channels")
	_, err = config.Load(workflowWithContent(t, minimal("hub:\n  slack:\n    default_channel: general\n")))
	assert.ErrorContains(t, err, "hub.slack.default_channel")
}
