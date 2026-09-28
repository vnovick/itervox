package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// CORE-043 — /api/v1/ready readiness probe.

type readyBody struct {
	Ready                   *bool      `json:"ready"`
	LoopFresh               *bool      `json:"loop_fresh"`
	LastPollOK              *bool      `json:"last_poll_ok"`
	ConfigInvalid           *bool      `json:"config_invalid"`
	Degraded                *bool      `json:"degraded"`
	TrackerRateLimitedUntil *time.Time `json:"tracker_rate_limited_until"`
}

// healthySignals is a daemon whose loop went idle a second ago after a good
// poll, polling every 30 s.
func healthySignals(now time.Time) server.ReadinessSignals {
	return server.ReadinessSignals{
		StartedAt:            now.Add(-10 * time.Minute),
		LoopStarted:          true,
		LastLoopIdle:         now.Add(-time.Second),
		PollInterval:         30 * time.Second,
		LastPollOK:           true,
		PollFailureThreshold: 3,
	}
}

func getReady(t *testing.T, cfg server.Config, host string) (int, readyBody, map[string]any) {
	t.Helper()
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	if host != "" {
		req.Host = host
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var body readyBody
	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	return w.Code, body, raw
}

func readyCfg(sig func() server.ReadinessSignals) server.Config {
	cfg := makeTestConfig(baseSnap())
	cfg.Readiness = sig
	return cfg
}

func TestReadyHealthyReturns200WithDocumentedKeys(t *testing.T) {
	code, body, raw := getReady(t, readyCfg(func() server.ReadinessSignals {
		return healthySignals(time.Now())
	}), "")
	assert.Equal(t, http.StatusOK, code)
	for _, k := range []string{"ready", "loop_fresh", "last_poll_ok", "config_invalid", "degraded", "tracker_rate_limited_until", "draining"} {
		assert.Contains(t, raw, k, "documented key %q", k)
	}
	assert.Len(t, raw, 7, "booleans and the rate-limit instant only: %v", raw)
	assert.True(t, *body.Ready)
	assert.True(t, *body.LoopFresh)
	assert.True(t, *body.LastPollOK)
	assert.False(t, *body.ConfigInvalid)
	assert.False(t, *body.Degraded)
	assert.Nil(t, raw["tracker_rate_limited_until"], "null when the gate is closed")
}

func TestReadyReturns503WhenLoopStale(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		now := time.Now()
		sig := healthySignals(now)
		// Idle, not inside a tick, and no iteration for 4x the interval.
		sig.LastLoopIdle = now.Add(-4 * sig.PollInterval)
		return sig
	}), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, *body.Ready)
	assert.False(t, *body.LoopFresh)
}

func TestReadyStaysFreshDuringLongOnTick(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		now := time.Now()
		sig := healthySignals(now)
		// The last completed iteration is far older than 3x the interval,
		// but the loop is inside a tick (a slow tracker call) well within
		// the tick budget: that is not a wedge.
		sig.LastLoopIdle = now.Add(-5 * time.Minute)
		sig.TickStarted = now.Add(-4 * time.Minute)
		return sig
	}), "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, *body.Ready)
	assert.True(t, *body.LoopFresh)
}

func TestReadyStaleWhenTickExceedsBudget(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		now := time.Now()
		sig := healthySignals(now)
		sig.LastLoopIdle = now.Add(-server.ReadyTickBudget - 2*time.Minute)
		sig.TickStarted = now.Add(-server.ReadyTickBudget - time.Minute)
		return sig
	}), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, *body.LoopFresh)
}

// Three consecutive *tracker.RateLimitedError polls: the orchestrator does
// not count them as failures (TestPollRateLimitedDoesNotEscalate pins that
// and TestReadinessRateLimitedPollDoesNotCount pins the published signal),
// so the probe stays ready and reports degraded with the gate's reset.
func TestReadyRateLimitedPollIsDegradedNotStale(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.LastPollOK = false
		sig.PollRateLimited = true
		sig.ConsecutivePollFailures = 0
		sig.RateLimitedUntil = &reset
		return sig
	}), "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, *body.Ready)
	assert.True(t, *body.Degraded)
	assert.False(t, *body.LastPollOK)
	require.NotNil(t, body.TrackerRateLimitedUntil)
	assert.True(t, body.TrackerRateLimitedUntil.Equal(reset))
}

func TestReadyReturns503AfterConsecutivePollFailures(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.LastPollOK = false
		sig.ConsecutivePollFailures = 3
		return sig
	}), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, *body.Ready)
	assert.True(t, *body.LoopFresh)

	code, body, _ = getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.LastPollOK = false
		sig.ConsecutivePollFailures = 2
		return sig
	}), "")
	assert.Equal(t, http.StatusOK, code, "below the threshold: degraded, still ready")
	assert.True(t, *body.Degraded)
}

// An invalid WORKFLOW.md edit leaves the daemon running on its last valid
// config; failing readiness would pull the dashboard (where the operator
// fixes the file) out of the load balancer. Reported, degraded, still ready.
func TestReadyConfigInvalidIsDegradedNotUnready(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.ConfigInvalid = true
		return sig
	}), "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, *body.ConfigInvalid)
	assert.True(t, *body.Degraded)
}

func TestReadyStartupGrace(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		now := time.Now()
		return server.ReadinessSignals{StartedAt: now.Add(-time.Second), PollInterval: 30 * time.Second, PollFailureThreshold: 3}
	}), "")
	assert.Equal(t, http.StatusOK, code, "within one poll interval of start the loop may not have run yet")
	assert.True(t, *body.Ready)

	code, _, _ = getReady(t, readyCfg(func() server.ReadinessSignals {
		now := time.Now()
		return server.ReadinessSignals{StartedAt: now.Add(-2 * time.Minute), PollInterval: 30 * time.Second, PollFailureThreshold: 3}
	}), "")
	assert.Equal(t, http.StatusServiceUnavailable, code, "a loop that never started past the grace is not ready")
}

func TestReadyWithoutReadinessSourceIs503(t *testing.T) {
	code, body, _ := getReady(t, makeTestConfig(baseSnap()), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, *body.Ready)
}

// /ready is unauthenticated (like /health) in token mode, and exempt from the
// unauthenticated-mode Host guard: probes address it by whatever name the
// platform uses, and it answers booleans and one timestamp only.
func TestReadyIsUnauthenticatedAndHostGuardExempt(t *testing.T) {
	sig := func() server.ReadinessSignals { return healthySignals(time.Now()) }

	tokenCfg := readyCfg(sig)
	tokenCfg.APIToken = "secret-token-value"
	code, _, _ := getReady(t, tokenCfg, "")
	assert.Equal(t, http.StatusOK, code, "no bearer token needed")

	openCfg := readyCfg(sig)
	openCfg.AllowedHosts = nil
	code, _, _ = getReady(t, openCfg, "itervox.default.svc.cluster.local:8090")
	assert.Equal(t, http.StatusOK, code, "unlisted Host name passes the guard for GET /ready")

	// Only GET is exempt: any other method on the path still meets the guard.
	srv := server.New(openCfg)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ready", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// The handler must not build the full snapshot: GeneratedAt is stamped at
// request time and says nothing about loop liveness.
func TestReadyDoesNotBuildSnapshot(t *testing.T) {
	cfg := readyCfg(func() server.ReadinessSignals { return healthySignals(time.Now()) })
	cfg.Snapshot = func() server.StateSnapshot {
		t.Fatal("/ready must not call Snapshot")
		return server.StateSnapshot{}
	}
	code, _, _ := getReady(t, cfg, "")
	assert.Equal(t, http.StatusOK, code)
}

// BH-M2-5: the startup grace takes the same 30 s floor as the idle
// threshold, so a short polling.interval_ms cannot flap a daemon that is
// still loading its ledgers to 503 before its first tick.
func TestReadyStartupGraceHasMinimumFloor(t *testing.T) {
	now := time.Now()
	sig := server.ReadinessSignals{StartedAt: now.Add(-10 * time.Second), PollInterval: time.Second, PollFailureThreshold: 3}
	assert.True(t, server.EvaluateReadiness(sig, now).LoopFresh, "10 s after start with a 1 s interval is inside the 30 s floor")
	sig.StartedAt = now.Add(-31 * time.Second)
	assert.False(t, server.EvaluateReadiness(sig, now).LoopFresh, "past the floor, a loop that never started is stale")
}

func TestReadyReadSheddingIsDegradedNotUnready(t *testing.T) {
	code, body, _ := getReady(t, readyCfg(func() server.ReadinessSignals {
		sig := healthySignals(time.Now())
		sig.LastPollOK = false
		sig.PollShedding = true
		return sig
	}), "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, *body.Degraded)
	assert.False(t, *body.LastPollOK)
}
