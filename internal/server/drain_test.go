package server_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// CORE-057 — while the daemon drains for shutdown or reload, /ready is 503
// with draining:true (so a load balancer stops routing to it) and the
// admission controls answer 409 draining instead of 503 busy or 404.

func TestReadyReportsDrainingAs503(t *testing.T) {
	code, body, raw := getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.Draining = true
		return sig
	}), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, *body.Ready)
	assert.True(t, *body.LoopFresh, "a draining loop is alive, just not admitting work")
	assert.Equal(t, true, raw["draining"])
}

func TestIssueControlDrainingIs409(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ResumeIssueFn:      func(string) error { return server.ErrDraining },
		ReanalyzeIssueFn:   func(string) error { return server.ErrDraining },
		ProvideInputFn:     func(string, string) error { return server.ErrDraining },
		DispatchReviewerFn: func(string) error { return server.ErrDraining },
	}
	srv := server.New(cfg)
	for _, tc := range []struct{ path, body string }{
		{"/api/v1/issues/ENG-1/resume", ""},
		{"/api/v1/issues/ENG-1/reanalyze", ""},
		{"/api/v1/issues/ENG-1/provide-input", `{"message":"go"}`},
		{"/api/v1/issues/ENG-1/ai-review", ""},
	} {
		w := postJSON(t, srv, tc.path, tc.body)
		assert.Equal(t, http.StatusConflict, w.Code, tc.path)
		assert.Contains(t, w.Body.String(), `"draining"`, tc.path)
	}
}
