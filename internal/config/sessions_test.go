package config_test

import (
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
		{"absent uses defaults", "", config.SessionsConfig{External: true, ExternalWindowHours: 24, ExternalScope: "project"}},
		{"all set", "sessions:\n  external: false\n  external_window_hours: 6\n  external_scope: all\n  tmux_control: true\n  external_roots:\n    - /srv/other\n",
			config.SessionsConfig{External: false, ExternalWindowHours: 6, ExternalScope: "all", ExternalRoots: []string{"/srv/other"}, TmuxControl: true}},
		{"unknown scope is private", "sessions:\n  external_scope: everything\n  external_window_hours: 0\n",
			config.SessionsConfig{External: true, ExternalWindowHours: 24, ExternalScope: "project"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Sessions)
		})
	}
}
