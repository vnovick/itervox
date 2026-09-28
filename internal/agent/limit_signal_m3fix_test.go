package agent

// M3-close fix round — limit-signal robustness (BH-M3-2, BH-M3-4, V2).

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BH-M3-2: an api_retry delay (seconds) must not ride onto the quota signal
// that ends the turn, or the retry fires in seconds instead of at the reset.
func TestMergeLimit_QuotaDoesNotInheritAPIRetryDelay(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	throttle := &LimitSignal{Kind: LimitKindThrottle, Source: LimitSourceAPIRetry, RetryAfter: 8 * time.Second, HTTPStatus: 429}
	quota := &LimitSignal{Kind: LimitKindQuota, Source: LimitSourceRateLimitEvent, ResetsAt: reset}

	got := mergeLimit(throttle, quota)
	require.True(t, got.Terminal())
	assert.Zero(t, got.RetryAfter, "a quota never inherits the api_retry delay")
	assert.Equal(t, 3*time.Hour, got.VendorDelay(now), "the reset decides, not the 8s retry delay")

	// Also through the stream: api_retry, then rate_limit_event, then result.
	r := TurnResult{}
	for _, line := range []string{
		`{"type":"system","subtype":"api_retry","retry_delay_ms":8000,"error_status":429,"error":"rate_limit"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":` + strconv.FormatInt(reset.Unix(), 10) + `,"rateLimitType":"five_hour"}}`,
		`{"type":"result","is_error":true,"api_error_status":429,"result":"limit"}`,
	} {
		ev, err := ParseLine([]byte(line))
		require.NoError(t, err)
		r = ApplyEvent(r, ev)
	}
	require.True(t, r.LastLimit.Terminal())
	assert.Zero(t, r.LastLimit.RetryAfter)
	assert.Equal(t, 3*time.Hour, r.LastLimit.VendorDelay(now))
}

// V2: a non-int limit field must never fail the whole line (the result event
// would then be dropped silently by readLines).
func TestParseLine_TolerantLimitFields(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		wantType   string
		wantQuota  bool
	}{
		{"string api_error_status", `{"type":"result","is_error":true,"api_error_status":"429","result":"x"}`, "result", true},
		{"float api_error_status", `{"type":"result","is_error":true,"api_error_status":429.0,"result":"x"}`, "result", true},
		{"garbage api_error_status", `{"type":"result","is_error":false,"api_error_status":{"a":1},"result":"done"}`, "result", false},
		{"string error_status on init", `{"type":"system","subtype":"init","session_id":"s","error_status":"n/a"}`, "system", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseLine([]byte(tc.line))
			require.NoError(t, err, "a bad limit field must not fail the line")
			assert.Equal(t, tc.wantType, ev.Type)
			assert.Equal(t, tc.wantQuota, ev.Limit.Terminal())
		})
	}

	ev, err := ParseLine([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":"1790000000"}}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Limit, "a string resetsAt must not lose the rejection")
	assert.Equal(t, int64(1790000000), ev.Limit.ResetsAt.Unix())

	ev, err = ParseLine([]byte(`{"type":"system","subtype":"api_retry","retry_delay_ms":"8000","error":"rate_limit"}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Limit)
	assert.Equal(t, 8*time.Second, ev.Limit.RetryAfter)

	ev, err = ParseLine([]byte(`{"type":"system","subtype":"api_retry","retry_delay_ms":1e30,"error":"rate_limit"}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Limit)
	assert.GreaterOrEqual(t, ev.Limit.RetryAfter, time.Duration(0), "no overflow to a negative delay")
}

// V2 end to end: the turn's result survives a string api_error_status.
func TestReadLines_ResultSurvivesStringAPIErrorStatus(t *testing.T) {
	r := TurnResult{}
	ev, err := ParseLine([]byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":"429","result":"You've hit your limit"}`))
	require.NoError(t, err)
	r = ApplyEvent(r, ev)
	assert.True(t, r.Failed, "the result event was applied")
	assert.True(t, r.LastLimit.Terminal())
}

// BH-M3-4: a reset read from text with no zone is marked as zone-assumed
// (the daemon's zone was used); an explicit Claude zone is not.
func TestParseResetTime_MarksAssumedZone(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	sig := ClassifyLimitText("You've hit your usage limit. Try again at 3:04 PM.", now, time.UTC)
	require.NotNil(t, sig)
	assert.True(t, sig.ResetZoneAssumed, "codex prints host-local time with no zone")

	sig = ClassifyLimitText("You've hit your session limit · resets 3pm (Europe/Berlin)", now, time.UTC)
	require.NotNil(t, sig)
	assert.False(t, sig.ResetZoneAssumed, "an explicit IANA zone is authoritative")
}
