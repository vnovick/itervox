package server

import "testing"

// testAllowedHosts allowlists the Host httptest.NewRequest uses
// ("example.com") for in-package tests that run without an API token, so the
// CORE-162 Host guard does not refuse them. host_guard_test.go covers the
// guard itself with no allowlist.
var testAllowedHosts = []string{"example.com"}

func TestHostAllowedRule(t *testing.T) {
	names := map[string]struct{}{"localhost": {}, "proxy.example": {}}
	cases := []struct {
		host string
		want bool
	}{
		{"", true},
		{"localhost", true},
		{"localhost:8090", true},
		{"LocalHost.:1", true},
		{"127.0.0.1:8090", true},
		{"[::1]:8090", true},
		{"[::1]", true},
		{"::1", true},
		{"192.168.0.10:8090", true},
		{"[fe80::1%25en0]:8090", true}, // a zoned IPv6 literal is still no DNS name
		{"proxy.example:443", true},
		{"PROXY.EXAMPLE", true},
		{"evil.test:8090", false},
		{"localhost.evil.test", false},
		{"127.0.0.1.evil.test", false},
		{"2130706433", false},
		{"0x7f000001", false},
		{"[evil.test]:8090", false},
	}
	for _, c := range cases {
		if got := hostAllowed(c.host, names); got != c.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}
