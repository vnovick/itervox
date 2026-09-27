package server_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

func fromAddr(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// M2-close: the limiter used to be one bucket per Server, and cmd/itervox
// builds a new Server on every WORKFLOW.md reload — so every reload (a
// settings save that touches tracker states, a hand edit) refilled it. The
// default limiter is process-scoped now.
func TestClientErrors_LimiterSurvivesServerRebuild(t *testing.T) {
	server.ResetDefaultClientErrorLimiterForTest()
	t.Cleanup(server.ResetDefaultClientErrorLimiterForTest)
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "tok"
	cfg.ReportClientError = (&clientErrorSink{}).report

	first := server.New(cfg)
	for range server.ClientErrorBurst {
		assert.Equal(t, http.StatusAccepted, postClientError(t, first, `{"kind":"error","message":"x"}`, "tok", fromAddr("198.51.100.7:5000")).Code)
	}
	reloaded := server.New(cfg) // what a WORKFLOW.md reload does
	assert.Equal(t, http.StatusTooManyRequests,
		postClientError(t, reloaded, `{"kind":"error","message":"x"}`, "tok", fromAddr("198.51.100.7:5001")).Code,
		"a reload must not refill the client's bucket")
}

// M2-close: one noisy tab (one client address) must not starve another
// client's reports. Buckets are per remote address; a global backstop still
// bounds the total.
func TestClientErrors_PerClientFairness(t *testing.T) {
	cfg := clientErrorsCfg("tok", &clientErrorSink{})
	srv := server.New(cfg)
	for range server.ClientErrorBurst + 5 {
		postClientError(t, srv, `{"kind":"error","message":"loop"}`, "tok", fromAddr("198.51.100.1:4000"))
	}
	assert.Equal(t, http.StatusTooManyRequests,
		postClientError(t, srv, `{"kind":"error","message":"loop"}`, "tok", fromAddr("198.51.100.1:4001")).Code)
	assert.Equal(t, http.StatusAccepted,
		postClientError(t, srv, `{"kind":"error","message":"other tab"}`, "tok", fromAddr("198.51.100.2:4000")).Code,
		"a second client still gets through")
}

func TestClientErrors_GlobalBackstop(t *testing.T) {
	cfg := clientErrorsCfg("tok", &clientErrorSink{})
	srv := server.New(cfg)
	accepted := 0
	for i := range 200 {
		addr := "198.51.100." + string(rune('0'+i%10)) + string(rune('0'+i/10%10)) + ":1"
		if postClientError(t, srv, `{"kind":"error","message":"x"}`, "tok", fromAddr(addr)).Code == http.StatusAccepted {
			accepted++
		}
	}
	assert.LessOrEqual(t, accepted, server.ClientErrorGlobalBurst, "many clients together are still bounded")
}
