package cors

import (
	"net/http/httptest"
	"testing"
)

// #298: a WebSocket handshake's Origin is checked by the endpoint itself:
// none (not a browser), same host, listed, or Sec-Fetch-Site same-origin
// pass; any other origin, and "null", is refused.
func TestWebSocketOriginAllowed(t *testing.T) {
	allowed := []string{"https://dash.example"}
	cases := []struct {
		name, origin, fetchSite string
		want                    bool
	}{
		{"no origin", "", "", true},
		{"same host", "http://127.0.0.1:8090", "", true},
		{"listed", "https://dash.example", "", true},
		{"listed with default port", "https://dash.example:443", "", true},
		{"dev proxy same-origin", "http://localhost:5173", "same-origin", true},
		{"other site", "https://evil.example", "", false},
		{"other site claims nothing", "https://evil.example", "cross-site", false},
		{"null", "null", "same-origin", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://127.0.0.1:8090/api/v1/sessions/x/terminal", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if got := WebSocketOriginAllowed(r, allowed); got != tc.want {
				t.Fatalf("WebSocketOriginAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}
