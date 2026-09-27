package main

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M4-close D6 — every host form CORE-058 validation accepts must also bind:
// IPv4, IPv6 bare and bracketed, the IPv6 wildcard "::", and names. The
// listen address used to be built with "%s:%d", so any IPv6 literal failed
// with "too many colons in address".
func TestListenAcceptsIPv4IPv6AndNames(t *testing.T) {
	hosts := []string{"127.0.0.1", "0.0.0.0", "::1", "[::1]", "::", "[::]", "localhost"}
	if !hasIPv6Loopback() {
		t.Log("no IPv6 loopback on this host; IPv6 forms will be skipped")
	}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			if isIPv6Form(host) && !hasIPv6Loopback() {
				t.Skip("no IPv6 on this host")
			}
			ln, addr, err := listenStrict(host, 0)
			require.NoError(t, err, "listenStrict(%q, 0)", host)
			_ = ln.Close()
			_, port, err := net.SplitHostPort(addr)
			require.NoError(t, err, "addr %q is a valid host:port", addr)
			assert.NotEqual(t, "0", port)

			ln2, addr2, err := listenWithFallback(host, 0, 0)
			require.NoError(t, err, "listenWithFallback(%q, 0, 0)", host)
			_ = ln2.Close()
			_, _, err = net.SplitHostPort(addr2)
			require.NoError(t, err, "addr %q is a valid host:port", addr2)
		})
	}
	assert.Equal(t, "[::1]:8090", bindAddr("::1", 8090))
	assert.Equal(t, "[::1]:8090", bindAddr("[::1]", 8090))
	assert.Equal(t, "[::]:0", bindAddr("::", 0))
	assert.Equal(t, "127.0.0.1:8090", bindAddr("127.0.0.1", 8090))
	assert.Equal(t, "localhost:8090", bindAddr("localhost", 8090))
	assert.Equal(t, "http://[::1]:8090/", dashboardBaseURL("[::1]", 8090))
}

func isIPv6Form(h string) bool { return len(h) > 0 && (h[0] == '[' || h[0] == ':' || h == "::1") }

func hasIPv6Loopback() bool {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
