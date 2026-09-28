package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// BH2: server.allowed_hosts entries that the Host guard can never match were
// accepted silently, so the operator's reverse proxy / tunnel / MagicDNS name
// got 403 host_not_allowed with nothing pointing at the typo. Each is now a
// config-load error naming the entry and the form that would work.
func TestServerAllowedHostsRejectsEntriesThatCanNeverMatch(t *testing.T) {
	for _, tc := range []struct {
		entry, want string
	}{
		{"http://proxy.example", `"proxy.example"`},       // normalized to "http" before
		{"https://x.example:443", `"x.example"`},          // a URL, not a name
		{"*.ts.net", "wildcard"},                          // no wildcard support
		{"proxy.example/", "path"},                        // trailing path
		{"user@proxy.example", "host name only"},          // userinfo
		{"proxy example", "not a valid host name"},        // space
		{"bad_label-.example..", "not a valid host name"}, // empty label
	} {
		t.Run(tc.entry, func(t *testing.T) {
			_, err := config.Load(workflowWithContent(t, minimal("server:\n  allowed_hosts:\n    - \""+tc.entry+"\"\n")))
			require.Error(t, err, "an allowed_hosts entry that can never match must fail config load")
			assert.Contains(t, err.Error(), "server.allowed_hosts")
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// BH2: names are normalized to exactly what the Host guard compares against:
// lower case, no trailing dot, no port, and IDNA ToASCII (punycode) for a
// Unicode name — browsers always send the xn-- form in Host, so an entry
// kept in Unicode could never match.
func TestServerAllowedHostsNormalized(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  allowed_hosts:\n"+
		"    - Itervox.Example.COM.\n"+
		"    - proxy.internal:8443\n"+
		"    - bücher.example\n"+
		"    - my_service\n"+
		"    - 192.168.1.5\n"+
		"    - \"[::1]\"\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"itervox.example.com",
		"proxy.internal",
		"xn--bcher-kva.example",
		"my_service",
		"192.168.1.5",
		"::1",
	}, cfg.Server.AllowedHosts)
}
