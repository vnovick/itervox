package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// #178 step 3: the sessions: block configures discovery of hand-started
// sessions. Absent, discovery is on, 24h, project-scoped, and tmux control
// is off; an unknown scope falls back to the private project scope.
func TestSessionsConfigRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  config.SessionsConfig
	}{
		{"absent uses defaults", "", config.SessionsConfig{External: true, ExternalWindowHours: 24, ExternalScope: "project", SteeringPhrases: config.DefaultSteeringPhrases}},
		{"all set", "sessions:\n  external: false\n  external_window_hours: 6\n  external_scope: all\n  tmux_control: true\n  external_roots:\n    - /srv/other\n",
			config.SessionsConfig{External: false, ExternalWindowHours: 6, ExternalScope: "all", ExternalRoots: []string{"/srv/other"}, TmuxControl: true, SteeringPhrases: config.DefaultSteeringPhrases}},
		{"unknown scope is private", "sessions:\n  external_scope: everything\n  external_window_hours: 0\n",
			config.SessionsConfig{External: true, ExternalWindowHours: 24, ExternalScope: "project", SteeringPhrases: config.DefaultSteeringPhrases}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Sessions)
		})
	}
}

// TestSteeringPhrasesConfig (#218): sessions.steering_phrases is absent →
// the documented defaults; a list of {label, message} maps or bare strings
// (a default's label picks that default) replaces them; an empty list shows
// no chips; malformed or oversized entries are dropped.
func TestSteeringPhrasesConfig(t *testing.T) {
	load := func(extra string) []config.SteeringPhrase {
		t.Helper()
		cfg, err := config.Load(workflowWithContent(t, minimal(extra)))
		require.NoError(t, err)
		return cfg.Sessions.SteeringPhrases
	}
	assert.Equal(t, config.DefaultSteeringPhrases, load(""))
	assert.Len(t, config.DefaultSteeringPhrases, 6)
	assert.Empty(t, load("sessions:\n  steering_phrases: []\n"))
	got := load("sessions:\n  steering_phrases:\n" +
		"    - label: Ship it\n      message: Open the pull request now.\n" +
		"    - Reproduce first\n" +
		"    - label: \"\"\n      message: no label\n" +
		"    - label: " + strings.Repeat("x", 41) + "\n      message: too long a label\n")
	require.Len(t, got, 2)
	assert.Equal(t, config.SteeringPhrase{Label: "Ship it", Message: "Open the pull request now."}, got[0])
	assert.Equal(t, config.DefaultSteeringPhrases[3], got[1], "a bare default label picks that default's message")
}
