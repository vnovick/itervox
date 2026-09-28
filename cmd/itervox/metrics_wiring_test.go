package main

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-045 — the collector wiring in cmd/itervox.

func metricsTestCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "linear"
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxTurns = 3
	cfg.Polling.IntervalMs = 20
	return cfg
}

// orchestratorCfgMu reaches the unexported cfgMu of a real orchestrator so
// the test can hold it — there is deliberately no production accessor.
func orchestratorCfgMu(t *testing.T, o *orchestrator.Orchestrator) *sync.RWMutex {
	t.Helper()
	f := reflect.ValueOf(o).Elem().FieldByName("cfgMu")
	require.True(t, f.IsValid(), "orchestrator.cfgMu renamed; update this test")
	return (*sync.RWMutex)(unsafe.Pointer(f.UnsafeAddr()))
}

// TestMetricsCollectorDoesNotTakeCfgMu: a scrape must never queue behind a
// settings save. With cfgMu write-locked (as a PUT /settings holds it), the
// collector's view function must still return.
func TestMetricsCollectorDoesNotTakeCfgMu(t *testing.T) {
	cfg := metricsTestCfg()
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	view := metricsViewFunc(orch, ob, cfg.Tracker.Kind)

	mu := orchestratorCfgMu(t, orch)
	mu.Lock()
	defer mu.Unlock()

	done := make(chan metrics.View, 1)
	go func() { done <- view() }()
	select {
	case v := <-done:
		assert.Equal(t, "linear", v.TrackerAdapter)
	case <-time.After(3 * time.Second):
		t.Fatal("metrics view blocked on cfgMu")
	}
}

// inputRequiredRunner asks for human input on its only turn, so the
// worker exits exactly once (TerminalInputRequired) and is not re-dispatched.
type inputRequiredRunner struct{}

func (inputRequiredRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	return agent.TurnResult{
		InputRequired: true, ResultText: "Which database should I use?",
		InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}, nil
}

// scrapeValue renders the wired exposition and returns one series' value.
func scrapeValue(t *testing.T, view func() metrics.View, series string) float64 {
	t.Helper()
	var b strings.Builder
	require.NoError(t, metrics.WriteText(&b, view()))
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(series) + ` (\S+)$`)
	m := re.FindStringSubmatch(b.String())
	require.NotNil(t, m, "series %s missing from:\n%s", series, b.String())
	v, err := strconv.ParseFloat(m[1], 64)
	require.NoError(t, err)
	return v
}

// TestMetricsCountersIncrement: a synthetic worker exit and a dropped event
// move worker_exits_total and events_dropped_total by exactly 1 in the
// scraped output.
func TestMetricsCountersIncrement(t *testing.T) {
	cfg := metricsTestCfg()
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{ID: "id-m1", Identifier: "MET-1", Title: "T", State: "Todo"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)

	// Dropped event: an orchestrator whose loop is not running has a full
	// channel after its buffer fills; the next non-blocking send drops.
	idle := orchestrator.New(cfg, mt, nil, nil)
	idleView := metricsViewFunc(idle, ob, cfg.Tracker.Kind)
	sent := 0
	for ; sent < 1024 && idle.SetDepsOverride(fmt.Sprintf("MET-%d", sent), true); sent++ {
	}
	require.Less(t, sent, 1024, "the event channel never filled")
	droppedBefore := scrapeValue(t, idleView, "itervox_events_dropped_total")
	require.False(t, idle.SetDepsOverride("MET-overflow", true))
	assert.Equal(t, droppedBefore+1, scrapeValue(t, idleView, "itervox_events_dropped_total"))

	// Worker exit: one input-required exit on a running orchestrator.
	preinitMetrics(cfg.Tracker.Kind)
	orch := orchestrator.New(cfg, mt, inputRequiredRunner{}, nil)
	view := metricsViewFunc(orch, ob, cfg.Tracker.Kind)
	series := `itervox_worker_exits_total{reason="input_required"}`
	exitsBefore := scrapeValue(t, view, series)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	defer func() { cancel(); <-done }()

	require.Eventually(t, func() bool {
		_, ok := orch.Snapshot().InputRequiredIssues["MET-1"]
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the worker never exited input-required")
	assert.Equal(t, exitsBefore+1, scrapeValue(t, view, series))
	assert.Equal(t, 1.0, scrapeValue(t, view, "itervox_input_required"))
}
