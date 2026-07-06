// Package schedule implements a minimal 5-field cron parser and matcher for
// itervox automation triggers. It supports `*`, integer lists (e.g. `1,15,30`),
// step ranges (`*/5`, `0-30/10`), and inclusive ranges (`9-17`) over the
// standard minute/hour/day-of-month/month/day-of-week fields.
//
// Special forms (@yearly, @reboot, seconds, timezones-in-expression) are
// deliberately NOT supported — callers supply the time in their target zone.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type fieldSpec struct {
	any    bool
	values map[int]struct{}
}

// Expression is a parsed, ready-to-match 5-field cron rule. The zero value
// matches nothing — always obtain one via Parse.
type Expression struct {
	minute fieldSpec
	hour   fieldSpec
	day    fieldSpec
	month  fieldSpec
	week   fieldSpec
}

// Parse returns a compiled Expression for a standard 5-field cron string:
// `minute hour day-of-month month day-of-week`. Fields accept `*`,
// comma lists, `-`-ranges, and `/`-steps. Returns a wrapped error when any
// field is out of range or syntactically invalid.
//
// As a compatibility courtesy, this also accepts the 6-field Quartz form
// (`seconds minute hour dom month dow`) and the 7-field Quartz form
// (`… year`) emitted by some web cron pickers — the seconds field must be
// `0` (minute granularity is the smallest unit itervox schedules at) and
// the year field, when present, must be `*`. Quartz's `?` placeholder in
// either day field is normalised to `*`.
func Parse(expr string) (Expression, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	switch len(fields) {
	case 5:
		// canonical Unix form
	case 6, 7:
		// Quartz `seconds` must be exactly `0`. We deliberately do NOT accept
		// `*` here because it would mean "every second" — itervox dispatches
		// at minute granularity, so that intent is almost always a mistake
		// from a misconfigured picker. Existing 5-field test "* * * * * *"
		// stays invalid for the same reason.
		seconds := fields[0]
		if seconds != "0" {
			return Expression{}, fmt.Errorf("quartz seconds field must be 0 (minute is the smallest schedulable unit), got %q", seconds)
		}
		if len(fields) == 7 && fields[6] != "*" {
			return Expression{}, fmt.Errorf("quartz year field must be * (year-pinned schedules are not supported), got %q", fields[6])
		}
		fields = fields[1:6] // drop seconds + optional year
	default:
		return Expression{}, fmt.Errorf("expected 5 cron fields (or 6/7 Quartz fields), got %d", len(fields))
	}
	// Quartz uses `?` as "no specific value" — strictly speaking only valid in
	// day-of-month and day-of-week, but some web pickers
	// (react-cron-generator's "every minute" template, for one) emit it in
	// the month position too. We normalise every `?` to `*` so the rest of
	// the parser stays Unix-cron only and the user gets the schedule they
	// almost certainly intended.
	for i, f := range fields {
		if f == "?" {
			fields[i] = "*"
		}
	}
	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return Expression{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return Expression{}, fmt.Errorf("hour: %w", err)
	}
	day, err := parseField(fields[2], 1, 31)
	if err != nil {
		return Expression{}, fmt.Errorf("day-of-month: %w", err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return Expression{}, fmt.Errorf("month: %w", err)
	}
	week, err := parseField(fields[4], 0, 6)
	if err != nil {
		return Expression{}, fmt.Errorf("day-of-week: %w", err)
	}
	return Expression{
		minute: minute,
		hour:   hour,
		day:    day,
		month:  month,
		week:   week,
	}, nil
}

// Matches reports whether t satisfies this Expression. Day-of-month and
// day-of-week use standard cron OR semantics: when both fields are
// constrained (neither is `*`), the expression matches if EITHER day matches
// — not both. When only one is constrained, only that one is required. When
// both are `*`, both are ignored. Minute, hour, and month fields always AND
// together.
func (e Expression) Matches(t time.Time) bool {
	if !matchesField(e.minute, t.Minute()) || !matchesField(e.hour, t.Hour()) || !matchesField(e.month, int(t.Month())) {
		return false
	}
	dayMatch := matchesField(e.day, t.Day())
	weekMatch := matchesField(e.week, int(t.Weekday()))
	switch {
	case e.day.any && e.week.any:
		return true
	case e.day.any:
		return weekMatch
	case e.week.any:
		return dayMatch
	default:
		return dayMatch || weekMatch
	}
}

func matchesField(spec fieldSpec, value int) bool {
	if spec.any {
		return true
	}
	_, ok := spec.values[value]
	return ok
}

func parseField(expr string, min, max int) (fieldSpec, error) {
	if expr == "*" {
		return fieldSpec{any: true}, nil
	}
	values := make(map[int]struct{})
	parts := strings.Split(expr, ",")
	for _, part := range parts {
		if err := addFieldPart(values, strings.TrimSpace(part), min, max); err != nil {
			return fieldSpec{}, err
		}
	}
	if len(values) == 0 {
		return fieldSpec{}, fmt.Errorf("no values")
	}
	return fieldSpec{values: values}, nil
}

func addFieldPart(values map[int]struct{}, expr string, min, max int) error {
	if expr == "" {
		return fmt.Errorf("empty field part")
	}
	step := 1
	base := expr
	if strings.Contains(expr, "/") {
		pieces := strings.Split(expr, "/")
		if len(pieces) != 2 {
			return fmt.Errorf("invalid step expression %q", expr)
		}
		base = pieces[0]
		n, err := strconv.Atoi(pieces[1])
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid step value %q", pieces[1])
		}
		step = n
	}

	start, end, err := parseRange(base, min, max)
	if err != nil {
		return err
	}
	for value := start; value <= end; value += step {
		values[value] = struct{}{}
	}
	return nil
}

func parseRange(expr string, min, max int) (int, int, error) {
	switch {
	case expr == "" || expr == "*":
		return min, max, nil
	case strings.Contains(expr, "-"):
		pieces := strings.Split(expr, "-")
		if len(pieces) != 2 {
			return 0, 0, fmt.Errorf("invalid range %q", expr)
		}
		start, err := parseBound(pieces[0], min, max)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseBound(pieces[1], min, max)
		if err != nil {
			return 0, 0, err
		}
		if end < start {
			return 0, 0, fmt.Errorf("descending range %q", expr)
		}
		return start, end, nil
	default:
		value, err := parseBound(expr, min, max)
		if err != nil {
			return 0, 0, err
		}
		return value, value, nil
	}
}

func parseBound(expr string, min, max int) (int, error) {
	value, err := strconv.Atoi(expr)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", expr)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("value %d out of range [%d,%d]", value, min, max)
	}
	return value, nil
}
