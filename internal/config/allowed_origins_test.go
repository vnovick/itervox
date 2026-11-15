package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// #231: server.allowed_origins is parsed, normalized to a browser's Origin
// serialization (lower case, default port dropped, duplicates folded) and
// absent by default; an entry that can never match fails the load.
func TestServerAllowedOriginsParse(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Server.AllowedOrigins, "default: same-origin only")

	cfg, err = config.Load(workflowWithContent(t, minimal("server:\n  allowed_origins:\n"+
		"    - https://Dash.Example.COM\n"+
		"    - https://dash.example.com:443/\n"+
		"    - http://localhost:5173\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{"https://dash.example.com", "http://localhost:5173"}, cfg.Server.AllowedOrigins)

	for _, bad := range []string{"dash.example.com", "https://*.example.com", "https://dash.example.com/app", "ftp://dash.example.com", "https://user@dash.example.com"} {
		_, err := config.Load(workflowWithContent(t, minimal("server:\n  allowed_origins:\n    - \""+bad+"\"\n")))
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "server.allowed_origins")
	}
}
