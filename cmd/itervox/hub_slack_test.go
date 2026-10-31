//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/hub/slack"
)

const (
	cliAppToken = "xapp" + "-1-A0CLI-111-cliappsecretvalue0123456789"
	cliBotToken = "xoxb" + "-222-333-clibotsecretvalue0123456789"
)

// TestHubSlackSetupStoresTokensAndChannels (#230): `itervox hub slack
// setup` prints the manifest, stores both tokens 0600 in
// ~/.itervox/hub/.env without ever printing them, writes hub.slack.* to
// the user settings layer with a channel per member project, and the
// daemon then builds the Slack channel from them; `status` reports the
// tokens as set, never their values.
func TestHubSlackSetupStoresTokensAndChannels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userSettings := filepath.Join(home, ".itervox", "settings.jsonc")
	t.Setenv(config.UserSettingsEnv, userSettings)
	t.Setenv(slackAppTokenKey, "")
	t.Setenv(slackBotTokenKey, "")
	old := secretStdinIsTerminal
	secretStdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { secretStdinIsTerminal = old })
	regDir := filepath.Join(home, ".itervox", "daemons")
	require.NoError(t, os.MkdirAll(regDir, 0o700))
	wf := filepath.Join(t.TempDir(), "alpha", "WORKFLOW.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(wf), 0o755))
	writeHubRegistryRecord(t, regDir, wf, os.Getpid(), true)

	in := strings.NewReader(cliAppToken + "\n" + cliBotToken + "\nU0ME1, U0YOU2\nC0DEFAULT\nC0ALPHA\n")
	var out, errOut bytes.Buffer
	require.Equal(t, 0, hubSlackCmd([]string{"setup"}, in, &out, &errOut), errOut.String())
	printed := out.String() + errOut.String()
	assert.Contains(t, printed, "socket_mode_enabled: true")
	assert.Contains(t, printed, "chat:write")
	assert.NotContains(t, printed, cliAppToken)
	assert.NotContains(t, printed, cliBotToken)

	envPath := filepath.Join(home, ".itervox", "hub", ".env")
	info, err := os.Stat(envPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	app, bot := hubSlackTokens()
	assert.Equal(t, cliAppToken, app)
	assert.Equal(t, cliBotToken, bot)

	raw, err := config.ReadSettingsFile(userSettings)
	require.NoError(t, err)
	sc := raw["hub"].(map[string]any)["slack"].(map[string]any)
	assert.Equal(t, true, sc["enabled"])
	assert.Equal(t, []any{"U0ME1", "U0YOU2"}, sc["allowed_users"])
	assert.Equal(t, "C0DEFAULT", sc["default_channel"])
	assert.Equal(t, map[string]any{"alpha": "C0ALPHA"}, sc["channels"])

	cfg := &config.Config{Hub: config.HubConfig{Slack: config.HubSlackConfig{Enabled: true,
		AllowedUsers: []string{"U0ME1"}, Channels: map[string]string{"alpha": "C0ALPHA"}}}}
	chans := hubChannels(cfg, filepath.Join(home, ".itervox", "hub"))
	require.Len(t, chans, 1)
	_, ok := chans[0].(*slack.Channel)
	assert.True(t, ok)
	cfg.Hub.Slack.Enabled = false
	assert.Empty(t, hubChannels(cfg, filepath.Join(home, ".itervox", "hub")), "off unless hub.slack.enabled")

	out.Reset()
	require.Equal(t, 0, hubSlackCmd([]string{"status", "--json"}, nil, &out, &errOut))
	assert.NotContains(t, out.String(), cliAppToken)
	assert.NotContains(t, out.String(), cliBotToken)
	var st hubSlackStatusJSON
	require.NoError(t, json.Unmarshal(out.Bytes(), &st))
	assert.Equal(t, "set", st.AppToken)
	assert.Equal(t, "set", st.BotToken)
	assert.True(t, st.Enabled)
	assert.Equal(t, "C0ALPHA", st.Projects["alpha"])

	// A wrong token kind is refused and nothing is printed back.
	out.Reset()
	errOut.Reset()
	require.Equal(t, 1, hubSlackCmd([]string{"setup"}, strings.NewReader(cliBotToken+"\n"), &out, &errOut))
	assert.NotContains(t, out.String()+errOut.String(), cliBotToken)

	// --manifest-only prints the manifest alone.
	out.Reset()
	require.Equal(t, 0, hubSlackCmd([]string{"setup", "--manifest-only", "--json"}, nil, &out, &errOut))
	var m slack.AppManifest
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))
	require.NoError(t, slack.ValidateManifest(m))
}
