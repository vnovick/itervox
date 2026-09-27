package agent

// CORE-050 — typed vendor limit signals.
//
// Ground truth (recorded in .plans/execution/M3-B1-limit-core/rounds.md):
//   - claude 2.1.283 bundle: rate_limit_event = {type, rate_limit_info{status
//     allowed|allowed_warning|rejected, resetsAt int (epoch SECONDS — the CLI
//     renders it with new Date(e*1000)), rateLimitType five_hour|seven_day|
//     seven_day_opus|seven_day_sonnet|seven_day_overage_included|overage,
//     utilization, overageStatus, overageResetsAt, overageDisabledReason ...},
//     uuid, session_id}; system/api_retry = {attempt, max_retries,
//     retry_delay_ms, error_status int|null, error rate_limit|overloaded|
//     billing_error|...}; result carries api_error_status int|null.
//   - codex 0.157: exec JSONL has a top-level {"type":"error","message"}
//     event and turn.failed{error{message}}; the usage-limit text is
//     "You've hit your usage limit. ... Try again at 3:04 PM." (same day) or
//     "... try again at Sep 27th, 2026 3:04 PM." (chrono "%-I:%M %p" and
//     "%b %-d<ordinal>, %Y %-I:%M %p").

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLine_RateLimitEventRejected(t *testing.T) {
	const resetsSec = int64(1790000000)
	want := time.Unix(resetsSec, 0)

	tests := []struct {
		name      string
		line      string
		wantNil   bool
		wantType  string
		wantReset time.Time
	}{
		{
			name:      "nested rate_limit_info, epoch seconds (2.1.283 shape)",
			line:      `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000,"rateLimitType":"five_hour","utilization":1,"isUsingOverage":false},"uuid":"u-1","session_id":"sess-rl"}`,
			wantType:  "five_hour",
			wantReset: want,
		},
		{
			name:      "resetsAt in epoch milliseconds is accepted by magnitude",
			line:      `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000000,"rateLimitType":"seven_day"},"session_id":"sess-rl"}`,
			wantType:  "seven_day",
			wantReset: want,
		},
		{
			name:      "older CLI: top-level fields, unknown keys ignored",
			line:      `{"type":"rate_limit_event","status":"rejected","resetsAt":1790000000,"rateLimitType":"seven_day_opus","someFutureKey":{"x":1},"session_id":"sess-rl"}`,
			wantType:  "seven_day_opus",
			wantReset: want,
		},
		{
			name:      "overage rejected with out_of_credits is a quota signal",
			line:      `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"out_of_credits"},"session_id":"sess-rl"}`,
			wantType:  "five_hour",
			wantReset: want,
		},
		{
			name:    "rejected but running on overage credits is not a limit",
			line:    `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000,"rateLimitType":"five_hour","overageStatus":"allowed","isUsingOverage":true},"session_id":"sess-rl"}`,
			wantNil: true,
		},
		{
			name:    "allowed is informational only",
			line:    `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790000000,"rateLimitType":"five_hour"},"session_id":"sess-rl"}`,
			wantNil: true,
		},
		{
			name:    "allowed_warning is informational only",
			line:    `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1790000000,"rateLimitType":"seven_day","utilization":0.8},"session_id":"sess-rl"}`,
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseLine([]byte(tc.line))
			require.NoError(t, err)
			assert.Equal(t, "rate_limit_event", ev.Type)
			if tc.wantNil {
				assert.Nil(t, ev.Limit, "no limit signal expected")
				return
			}
			require.NotNil(t, ev.Limit, "rejected rate_limit_event must carry a LimitSignal")
			assert.Equal(t, LimitKindQuota, ev.Limit.Kind)
			assert.True(t, ev.Limit.Terminal(), "a rejected status ends the turn")
			assert.Equal(t, "rejected", ev.Limit.Status)
			assert.Equal(t, tc.wantType, ev.Limit.LimitType)
			assert.True(t, tc.wantReset.Equal(ev.Limit.ResetsAt), "ResetsAt = %v, want %v", ev.Limit.ResetsAt, tc.wantReset)
			assert.Equal(t, LimitSourceRateLimitEvent, ev.Limit.Source)
		})
	}
}

func TestParseLine_ApiRetryRateLimit(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":4500,"error_status":429,"error":"rate_limit","uuid":"u","session_id":"sess-retry"}`))
	require.NoError(t, err)
	assert.Equal(t, EventSystem, ev.Type)
	assert.Equal(t, "api_retry", ev.Subtype)
	require.NotNil(t, ev.Limit)
	assert.Equal(t, LimitKindThrottle, ev.Limit.Kind)
	assert.False(t, ev.Limit.Terminal(), "api_retry is advisory: the CLI is retrying")
	assert.Equal(t, 4500*time.Millisecond, ev.Limit.RetryAfter)
	assert.Equal(t, 429, ev.Limit.HTTPStatus)
	assert.Equal(t, "rate_limit", ev.Limit.ErrorCategory)
	assert.Equal(t, LimitSourceAPIRetry, ev.Limit.Source)

	// overloaded (529) is a throttle too.
	ev, err = ParseLine([]byte(`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":1000,"error_status":529,"error":"overloaded","session_id":"s"}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Limit)
	assert.Equal(t, LimitKindThrottle, ev.Limit.Kind)
	assert.Equal(t, time.Second, ev.Limit.RetryAfter)

	// A retry for an unrelated cause (server_error, null status) is not a limit.
	ev, err = ParseLine([]byte(`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":1000,"error_status":null,"error":"server_error","session_id":"s"}`))
	require.NoError(t, err)
	assert.Nil(t, ev.Limit)

	// The init event keeps its session and carries no limit.
	ev, err = ParseLine([]byte(`{"type":"system","subtype":"init","session_id":"sess-init"}`))
	require.NoError(t, err)
	assert.Equal(t, "init", ev.Subtype)
	assert.Equal(t, "sess-init", ev.SessionID)
	assert.Nil(t, ev.Limit)
}

func TestParseLine_ApiRetryDoesNotFailTurn(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":2000,"error_status":429,"error":"rate_limit","session_id":"sess-1"}`,
		`{"type":"assistant","session_id":"sess-1","message":{"content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-1","result":"All done","api_error_status":null}`,
	}
	var r TurnResult
	for _, l := range lines {
		ev, err := ParseLine([]byte(l))
		require.NoError(t, err)
		r = ApplyEvent(r, ev)
	}
	assert.False(t, r.Failed, "an advisory api_retry followed by success must not fail the turn")
	require.NotNil(t, r.LastLimit, "the advisory signal is still reported")
	assert.Equal(t, LimitKindThrottle, r.LastLimit.Kind)
	assert.Equal(t, 2*time.Second, r.LastLimit.RetryAfter)
}

// TestParseLine_ResultApiErrorStatus pins result.api_error_status: a 429
// result after a rejected rate_limit_event is a quota signal that keeps the
// event's reset time; a result with no limit status adds no signal.
func TestParseLine_ResultApiErrorStatus(t *testing.T) {
	ev, err := ParseLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","result":"You've hit your session limit · resets 3pm (UTC)","api_error_status":429}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Limit)
	assert.Equal(t, LimitKindQuota, ev.Limit.Kind)
	assert.Equal(t, 429, ev.Limit.HTTPStatus)
	assert.Equal(t, LimitSourceResult, ev.Limit.Source)

	ev, err = ParseLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","result":"boom","api_error_status":500}`))
	require.NoError(t, err)
	assert.Nil(t, ev.Limit)

	var r TurnResult
	for _, l := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000,"rateLimitType":"seven_day"},"session_id":"s"}`,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","result":"limit","api_error_status":429}`,
	} {
		ev, err := ParseLine([]byte(l))
		require.NoError(t, err)
		r = ApplyEvent(r, ev)
	}
	require.NotNil(t, r.LastLimit)
	assert.True(t, r.LastLimit.Terminal())
	assert.Equal(t, "seven_day", r.LastLimit.LimitType, "merged from the rate_limit_event")
	assert.True(t, time.Unix(1790000000, 0).Equal(r.LastLimit.ResetsAt))
	assert.True(t, r.Failed)
}

func TestParseCodexLine_TopLevelErrorUsageLimit(t *testing.T) {
	msg := "You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 27th, 2099 3:04 PM."
	ev, err := ParseCodexLine([]byte(`{"type":"error","message":"` + msg + `"}`))
	require.NoError(t, err, "the top-level error event must no longer be skipped")
	assert.Equal(t, EventError, ev.Type)
	assert.Equal(t, msg, ev.Message)
	require.NotNil(t, ev.Limit)
	assert.Equal(t, LimitKindQuota, ev.Limit.Kind)
	assert.True(t, ev.Limit.Terminal())
	assert.Equal(t, LimitSourceText, ev.Limit.Source)
	wantReset := time.Date(2099, time.September, 27, 15, 4, 0, 0, time.Local)
	assert.True(t, wantReset.Equal(ev.Limit.ResetsAt), "ResetsAt = %v, want %v", ev.Limit.ResetsAt, wantReset)

	// turn.failed carrying the same message classifies too.
	ev, err = ParseCodexLine([]byte(`{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again later."}}`))
	require.NoError(t, err)
	assert.True(t, ev.IsError)
	require.NotNil(t, ev.Limit, "curly apostrophe spelling must classify")
	assert.True(t, ev.Limit.ResetsAt.IsZero(), "\"try again later\" carries no reset time")

	// An error item (item.completed, type error) is parsed, not dropped.
	ev, err = ParseCodexLine([]byte(`{"type":"item.completed","item":{"id":"i1","type":"error","message":"stream error: rate limit reached, retrying"}}`))
	require.NoError(t, err)
	assert.Equal(t, EventError, ev.Type)

	// An unrelated top-level error parses with no limit and does not fail.
	ev, err = ParseCodexLine([]byte(`{"type":"error","message":"stream disconnected before completion: connection reset"}`))
	require.NoError(t, err)
	assert.Nil(t, ev.Limit)

	// The error event keeps LastLimit on the accumulated result.
	var r TurnResult
	for _, l := range []string{
		`{"type":"thread.started","thread_id":"th-1"}`,
		`{"type":"error","message":"` + msg + `"}`,
		`{"type":"turn.failed","error":{"message":"` + msg + `"}}`,
	} {
		ev, err := ParseCodexLine([]byte(l))
		require.NoError(t, err)
		r = ApplyEvent(r, ev)
	}
	assert.True(t, r.Failed)
	require.NotNil(t, r.LastLimit)
	assert.True(t, r.LastLimit.Terminal())
	assert.False(t, r.InputRequired, "a usage limit is never an input request")
}

func TestParseResetTime_ClaudeAndCodexFormats(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	utc := time.UTC
	// now: 2026-09-26 10:00 UTC
	now := time.Date(2026, time.September, 26, 10, 0, 0, 0, utc)

	tests := []struct {
		name string
		text string
		want time.Time
		ok   bool
	}{
		{"codex same-day", "You've hit your usage limit. Try again at 3:04 PM.", time.Date(2026, 9, 26, 15, 4, 0, 0, utc), true},
		{"codex same-day earlier than now rolls to tomorrow", "You've hit your usage limit. Try again at 9:15 AM.", time.Date(2026, 9, 27, 9, 15, 0, 0, utc), true},
		{"codex long form with ordinal", "... or try again at Sep 27th, 2026 3:04 PM.", time.Date(2026, 9, 27, 15, 4, 0, 0, utc), true},
		{"codex long form 1st", "try again at Oct 1st, 2026 12:30 AM", time.Date(2026, 10, 1, 0, 30, 0, 0, utc), true},
		{"claude hour only with tz", "You've hit your session limit · resets 3pm (Europe/Berlin)", time.Date(2026, 9, 26, 15, 0, 0, 0, berlin), true},
		{"claude hour:minute", "You've hit your session limit · resets 11:30am (UTC)", time.Date(2026, 9, 26, 11, 30, 0, 0, utc), true},
		{"claude month day", "You've hit your weekly limit · resets Sep 29, 5pm (UTC)", time.Date(2026, 9, 29, 17, 0, 0, 0, utc), true},
		{"claude month day year", "You've hit your weekly limit · resets Jan 3, 2027, 9am (UTC)", time.Date(2027, 1, 3, 9, 0, 0, 0, utc), true},
		{"claude unknown tz falls back to loc", "resets 3pm (Mars/Olympus)", time.Date(2026, 9, 26, 15, 0, 0, 0, utc), true},
		{"no time", "You've hit your usage limit. Try again later.", time.Time{}, false},
		{"unrelated", "network timeout; try again at your convenience", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseResetTime(tc.text, now, utc)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.True(t, tc.want.Equal(got), "got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReadLines_KeepsLastLimitOnReadError: TurnResult keeps LastLimit even
// when the runner returns an error (idle read timeout here).
func TestReadLines_KeepsLastLimitOnReadError(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() {
		_, _ = pw.Write([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790000000,"rateLimitType":"five_hour"},"session_id":"s"}` + "\n"))
	}()
	res, err := readLines(t.Context(), nopLogger{}, nil, pr, 100, "claude", ParseLine)
	require.Error(t, err)
	require.NotNil(t, res.LastLimit)
	assert.True(t, res.LastLimit.Terminal())
}

// TestIsInputRequiredMsg_LimitTextNeverParks: a limit message wins over any
// pending-answer phrase (CORE-166 override list).
func TestIsInputRequiredMsg_LimitTextNeverParks(t *testing.T) {
	assert.False(t, isInputRequiredMsg("You've hit your usage limit; waiting for your input after it resets. Try again at 3:04 PM."))
	assert.True(t, isInputRequiredMsg("Human turn required"), "no-regression guard")
}
