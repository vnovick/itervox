package metrics

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parsedFamily is one metric family from a strict parse of the Prometheus
// text exposition format (version 0.0.4).
type parsedFamily struct {
	typ     string
	help    string
	samples map[string]float64 // label set text ("" or `{a="b"}`) → value
}

var (
	metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	sampleRe     = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})? (\S+)$`)
	labelPairRe  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\\n]|\\["\\n])*"$`)
)

// parseExposition is deliberately strict: every sample must follow a
// `# HELP` and `# TYPE` for its family, families must be contiguous and
// declared once, series unique, label pairs well formed, values parseable,
// and the text must end with a newline. It fails the test on any deviation.
func parseExposition(t *testing.T, text string) map[string]*parsedFamily {
	t.Helper()
	require.True(t, strings.HasSuffix(text, "\n"), "exposition must end with a newline")
	families := map[string]*parsedFamily{}
	var current string
	closed := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		where := fmt.Sprintf("line %d %q", lineNo, line)
		require.NotEmpty(t, line, "blank line: %s", where)
		if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
			parts := strings.SplitN(line, " ", 4)
			require.Len(t, parts, 4, where)
			name := parts[2]
			require.Regexp(t, metricNameRe, name, where)
			if name != current {
				require.False(t, closed[name], "family %s declared twice / not contiguous (%s)", name, where)
				if current != "" {
					closed[current] = true
				}
				current = name
			}
			f := families[name]
			if f == nil {
				f = &parsedFamily{samples: map[string]float64{}}
				families[name] = f
			}
			if parts[1] == "HELP" {
				require.Empty(t, f.help, "duplicate HELP: %s", where)
				require.Empty(t, f.samples, "HELP after samples: %s", where)
				f.help = parts[3]
			} else {
				require.Empty(t, f.typ, "duplicate TYPE: %s", where)
				require.Empty(t, f.samples, "TYPE after samples: %s", where)
				require.Contains(t, []string{"counter", "gauge"}, parts[3], where)
				f.typ = parts[3]
			}
			continue
		}
		require.False(t, strings.HasPrefix(line, "#"), "unexpected comment: %s", where)
		m := sampleRe.FindStringSubmatch(line)
		require.NotNil(t, m, "malformed sample: %s", where)
		name, labels, value := m[1], m[2], m[3]
		require.Equal(t, current, name, "sample outside its family block: %s", where)
		f := families[name]
		require.NotEmpty(t, f.typ, "sample before TYPE: %s", where)
		require.NotEmpty(t, f.help, "sample before HELP: %s", where)
		if labels != "" {
			inner := labels[1 : len(labels)-1]
			for _, pair := range splitLabelPairs(inner) {
				require.Regexp(t, labelPairRe, pair, where)
			}
		}
		v, err := strconv.ParseFloat(value, 64)
		require.NoError(t, err, where)
		require.False(t, math.IsNaN(v), where)
		_, dup := f.samples[labels]
		require.False(t, dup, "duplicate series: %s", where)
		f.samples[labels] = v
		if f.typ == "counter" {
			require.True(t, strings.HasSuffix(name, "_total"), "counter %s must end in _total", name)
			require.GreaterOrEqual(t, v, 0.0, where)
		}
	}
	require.NoError(t, sc.Err())
	for name, f := range families {
		require.NotEmpty(t, f.typ, "family %s has no TYPE", name)
		require.True(t, strings.HasPrefix(name, "itervox_"), "family %s must carry the itervox_ namespace", name)
		if f.typ == "gauge" {
			require.False(t, strings.HasSuffix(name, "_total"), "gauge %s must not end in _total", name)
		}
	}
	return families
}

// splitLabelPairs splits `a="x",b="y,z"` on commas outside quotes.
func splitLabelPairs(s string) []string {
	var out []string
	var b strings.Builder
	inQuote, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func sampleView() View {
	reset := time.Unix(1_790_000_000, 0)
	return View{
		TrackerAdapter:          "linear",
		WorkersRunning:          2,
		MaxConcurrentAgents:     3,
		RetryQueueLength:        1,
		AutomationQueueLength:   4,
		InputRequired:           1,
		PersistWriteErrors:      7,
		TransportFailures:       2,
		DispatchTicksObserved:   10,
		DispatchTicksSlotBound:  3,
		DispatchTicksDepBound:   1,
		DispatchEligibleWaiting: 5,
		DispatchBlockedByDep:    2,
		OutboxEntries:           3,
		OutboxDegraded:          1,
		OutboxRateLimited:       1,
		TrackerRateLimitedUntil: reset,
		TrackerPollFailures:     2,
		LastTrackerErrorAt:      time.Unix(1_789_999_000, 0),
		LastTrackerErrorKind:    `rate"limited\n`,
		LoopLastIdle:            time.Unix(1_789_999_990, 0),
	}
}

func TestExpositionIsStrictlyValid(t *testing.T) {
	resetForTest()
	PreinitWorkerExitReasons("succeeded", "failed")
	PreinitTrackerAdapter("linear")
	WorkerExit("failed")
	TrackerRequest("linear", TrackerOutcomeRateLimited)
	GoroutinePanic()
	EventDropped()

	var b strings.Builder
	require.NoError(t, WriteText(&b, sampleView()))
	fams := parseExposition(t, b.String())

	for _, name := range []string{
		"itervox_workers_running", "itervox_worker_exits_total", "itervox_tracker_requests_total",
		"itervox_goroutine_panics_total", "itervox_events_dropped_total", "itervox_tracker_rate_limited_until_seconds",
		"itervox_persist_write_errors_total", "itervox_outbox_entries", "itervox_dispatch_ticks_total",
	} {
		require.Contains(t, fams, name)
		require.NotEmpty(t, fams[name].samples, "family %s must have a sample", name)
	}
	assert.Equal(t, 1.0, fams["itervox_worker_exits_total"].samples[`{reason="failed"}`])
	assert.Equal(t, 0.0, fams["itervox_worker_exits_total"].samples[`{reason="succeeded"}`], "preinit series are exported at 0")
	assert.Equal(t, 1.0, fams["itervox_tracker_requests_total"].samples[`{adapter="linear",outcome="rate_limited"}`])
	assert.Equal(t, 0.0, fams["itervox_tracker_requests_total"].samples[`{adapter="linear",outcome="ok"}`])
	assert.Equal(t, 1.0, fams["itervox_goroutine_panics_total"].samples[""])
	assert.Equal(t, 1.0, fams["itervox_events_dropped_total"].samples[""])
	assert.Equal(t, 1_790_000_000.0, fams["itervox_tracker_rate_limited_until_seconds"].samples[`{adapter="linear"}`])
	assert.Equal(t, 7.0, fams["itervox_persist_write_errors_total"].samples[""])
	assert.Equal(t, 3.0, fams["itervox_dispatch_ticks_total"].samples[`{bound="slot"}`])
	assert.Equal(t, 1.0, fams["itervox_outbox_entries"].samples[`{state="degraded"}`])
	assert.Contains(t, fams["itervox_tracker_last_error_timestamp_seconds"].samples, `{kind="rate\"limited\\n"}`,
		"label values are escaped")
}

func TestRateLimitGaugeIsZeroWhenGateClosed(t *testing.T) {
	resetForTest()
	v := sampleView()
	v.TrackerRateLimitedUntil = time.Time{}
	v.LastTrackerErrorAt = time.Time{}
	var b strings.Builder
	require.NoError(t, WriteText(&b, v))
	fams := parseExposition(t, b.String())
	assert.Equal(t, 0.0, fams["itervox_tracker_rate_limited_until_seconds"].samples[`{adapter="linear"}`])
	assert.Empty(t, fams["itervox_tracker_last_error_timestamp_seconds"].samples, "no error recorded → no series")
}

func TestHandlerServesTextFormat(t *testing.T) {
	resetForTest()
	h := Handler(func() View { return sampleView() })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", w.Header().Get("Content-Type"))
	parseExposition(t, w.Body.String())

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestCountersAreConcurrencySafe(t *testing.T) {
	resetForTest()
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 1000 {
				WorkerExit("failed")
				TrackerRequest("github", TrackerOutcomeOK)
				EventDropped()
			}
		}()
	}
	for range 8 {
		<-done
	}
	var b strings.Builder
	require.NoError(t, WriteText(&b, View{}))
	fams := parseExposition(t, b.String())
	assert.Equal(t, 8000.0, fams["itervox_worker_exits_total"].samples[`{reason="failed"}`])
	assert.Equal(t, 8000.0, fams["itervox_tracker_requests_total"].samples[`{adapter="github",outcome="ok"}`])
	assert.Equal(t, 8000.0, fams["itervox_events_dropped_total"].samples[""])
}
