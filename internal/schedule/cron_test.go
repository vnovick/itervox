package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustTime parses an RFC3339 timestamp or fails the test. Each test case is
// self-contained so this is just a tiny shorthand.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	out, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return out
}

func TestParse_ValidExpressions(t *testing.T) {
	cases := []string{
		"* * * * *",
		"0 9 * * 1-5",
		"*/5 * * * *",
		"0 */2 * * *",
		"15,30,45 * * * *",
		"0 9-17 * * *",
		"0 0 1,15 * *",
	}
	for _, expr := range cases {
		_, err := Parse(expr)
		assert.NoError(t, err, "expected %q to parse", expr)
	}
}

func TestParse_Invalid(t *testing.T) {
	cases := map[string]string{
		"too few fields":          "* * * *",
		"quartz wildcard seconds": "* * * * * *",
		"minute out of range":     "60 * * * *",
		"hour out of range":       "* 24 * * *",
		"month zero":              "* * * 0 *",
		"day zero":                "* * 0 * *",
		"weekday seven":           "* * * * 7",
		"descending range":        "5-3 * * * *",
		"step zero":               "*/0 * * * *",
		"eight fields":            "0 0 0 * * * * *",
		"quartz year non-wild":    "0 0 9 * * ? 2030",
	}
	for name, expr := range cases {
		_, err := Parse(expr)
		assert.Error(t, err, "expected %s (%q) to error", name, expr)
	}
}

// TestParse_AcceptsQuartz pins the back-compat path: web cron pickers
// (react-cron-generator with default `isUnix=false`, react-js-cron in Quartz
// mode, etc.) emit 6-field expressions. Existing WORKFLOW.md files saved
// with those tools must keep loading after the strict 5-field migration.
func TestParse_AcceptsQuartz(t *testing.T) {
	cases := map[string]string{
		"6-field daily 9am":          "0 0 9 * * ?",
		"6-field every minute":       "0 0/1 * * ? *",
		"7-field with wildcard year": "0 0 9 * * ? *",
		"6-field question in dom":    "0 30 8 ? * 1-5",
		"5-field still works":        "0 9 * * 1-5",
	}
	for name, expr := range cases {
		_, err := Parse(expr)
		assert.NoError(t, err, "%s (%q) should parse", name, expr)
	}
}

// TestParse_QuartzMatchesUnixSemantics verifies that the Quartz translation
// produces the same firing pattern as the equivalent Unix expression — a
// regression test ensuring the seconds/year stripping doesn't accidentally
// shift other fields.
func TestParse_QuartzMatchesUnixSemantics(t *testing.T) {
	unix, err := Parse("0 9 * * 1-5")
	require.NoError(t, err)
	quartz, err := Parse("0 0 9 ? * 1-5")
	require.NoError(t, err)

	// Sample one matching weekday and one non-matching weekend day.
	monday9am := mustTime(t, "2026-04-13T09:00:00Z")
	saturday9am := mustTime(t, "2026-04-18T09:00:00Z")

	assert.True(t, unix.Matches(monday9am))
	assert.True(t, quartz.Matches(monday9am), "Quartz form must fire on the same day Unix form does")
	assert.False(t, unix.Matches(saturday9am))
	assert.False(t, quartz.Matches(saturday9am))
}

func TestMatches_MinuteAndHourAndMonthAllAND(t *testing.T) {
	// "At 09:00 on weekdays in any month" — every non-listed field must fail.
	expr, err := Parse("0 9 * * 1-5")
	require.NoError(t, err)

	// Monday 9:00 UTC 2026-04-13 matches.
	assert.True(t, expr.Matches(mustTime(t, "2026-04-13T09:00:00Z")))
	// Minute mismatch.
	assert.False(t, expr.Matches(mustTime(t, "2026-04-13T09:05:00Z")))
	// Hour mismatch.
	assert.False(t, expr.Matches(mustTime(t, "2026-04-13T10:00:00Z")))
	// Saturday (weekday 6).
	assert.False(t, expr.Matches(mustTime(t, "2026-04-18T09:00:00Z")))
}

// TestMatches_DayOfMonth_DayOfWeek_OR exercises the documented OR semantics:
// when both fields are constrained, the expression matches if EITHER day
// matches. When one is `*` and the other is constrained, only the constrained
// one is required.
func TestMatches_DayOfMonth_DayOfWeek_OR(t *testing.T) {
	// "At 00:00 on the 1st OR on Mondays" — both fields constrained.
	expr, err := Parse("0 0 1 * 1")
	require.NoError(t, err)

	// 2026-04-01 is a Wednesday — matches via day-of-month=1.
	assert.True(t, expr.Matches(mustTime(t, "2026-04-01T00:00:00Z")))
	// 2026-04-06 is a Monday (weekday 1) — matches via day-of-week.
	assert.True(t, expr.Matches(mustTime(t, "2026-04-06T00:00:00Z")))
	// 2026-04-07 is a Tuesday, not the 1st — no match.
	assert.False(t, expr.Matches(mustTime(t, "2026-04-07T00:00:00Z")))
}

func TestMatches_DayOfMonthAlone(t *testing.T) {
	// Day-of-week is `*`, so only day-of-month must match.
	expr, err := Parse("0 0 15 * *")
	require.NoError(t, err)

	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T00:00:00Z")))
	assert.False(t, expr.Matches(mustTime(t, "2026-04-14T00:00:00Z")))
}

func TestMatches_DayOfWeekAlone(t *testing.T) {
	// Day-of-month is `*`, so only day-of-week must match.
	expr, err := Parse("0 0 * * 1") // Mondays at midnight
	require.NoError(t, err)

	assert.True(t, expr.Matches(mustTime(t, "2026-04-13T00:00:00Z")))  // Monday
	assert.False(t, expr.Matches(mustTime(t, "2026-04-14T00:00:00Z"))) // Tuesday
}

func TestMatches_StepExpressions(t *testing.T) {
	// Every 15 minutes on the quarter-hour.
	expr, err := Parse("*/15 * * * *")
	require.NoError(t, err)

	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:00:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:15:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:30:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:45:00Z")))
	assert.False(t, expr.Matches(mustTime(t, "2026-04-15T09:07:00Z")))
}

func TestMatches_RangeWithStep(t *testing.T) {
	// Every 10 minutes between :10 and :30 (inclusive).
	expr, err := Parse("10-30/10 * * * *")
	require.NoError(t, err)

	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:10:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:20:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:30:00Z")))
	assert.False(t, expr.Matches(mustTime(t, "2026-04-15T09:40:00Z")))
	assert.False(t, expr.Matches(mustTime(t, "2026-04-15T09:00:00Z")))
}

func TestMatches_ListExpression(t *testing.T) {
	// Minutes 0, 15, and 45 only.
	expr, err := Parse("0,15,45 * * * *")
	require.NoError(t, err)

	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:00:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:15:00Z")))
	assert.True(t, expr.Matches(mustTime(t, "2026-04-15T09:45:00Z")))
	assert.False(t, expr.Matches(mustTime(t, "2026-04-15T09:30:00Z")))
}

func TestMatches_ZeroValue_MatchesNothing(t *testing.T) {
	// Zero-value Expression has no populated fieldSpecs — by default every
	// field has any=false and empty values, so matchesField returns false
	// and Matches short-circuits. This test pins the zero-value behavior so
	// a future refactor doesn't silently switch to "zero matches everything".
	var expr Expression
	assert.False(t, expr.Matches(mustTime(t, "2026-04-15T09:00:00Z")))
}
