package agent

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CORE-050 — typed vendor limit signals.
//
// Ground truth (claude 2.1.283 bundle strings, codex 0.157 binary strings;
// see .plans/execution/M3-B1-limit-core/rounds.md):
//
//   - Claude stream-json `rate_limit_event`:
//     {type, rate_limit_info{status allowed|allowed_warning|rejected,
//     resetsAt int (epoch seconds; the CLI renders new Date(resetsAt*1000)),
//     rateLimitType five_hour|seven_day|seven_day_opus|seven_day_sonnet|
//     seven_day_overage_included|overage, utilization, overageStatus,
//     overageResetsAt, overageDisabledReason, isUsingOverage ...}, uuid,
//     session_id}. `rejected` with overageStatus allowed/allowed_warning means
//     requests keep running on overage credits (the CLI's own isUsingOverage
//     rule), so it is not a stop. The event is absent for API-key, Bedrock and
//     Vertex sessions. Older CLIs are decoded tolerantly: top-level fields are
//     accepted too, and resetsAt is read as seconds or milliseconds by
//     magnitude.
//   - Claude `system/api_retry`: {attempt, max_retries, retry_delay_ms,
//     error_status int|null, error rate_limit|overloaded|billing_error|...}.
//     Emitted BEFORE the CLI retries, so it is advisory and never ends a turn.
//   - Claude `result.api_error_status` (int|null): the HTTP status of the API
//     error that ended the turn.
//   - Codex exec JSONL carries only a message: a top-level
//     {"type":"error","message"} event, error items, and
//     turn.failed{error{message}}. The usage-limit text is "You've hit your
//     usage limit. ... Try again at 3:04 PM." (same day) or "... try again at
//     Sep 27th, 2026 3:04 PM." (otherwise), or "Try again later." when no
//     reset is known — so Codex detection is text based (ClassifyLimitText).

// LimitKind classifies a vendor limit signal.
type LimitKind string

const (
	// LimitKindQuota is a usage/credit limit that will not clear by retrying
	// on the same backend before its reset. It ends the turn's usefulness:
	// the orchestrator exits TerminalRateLimited on it (CORE-051).
	LimitKindQuota LimitKind = "quota"
	// LimitKindThrottle is a transient rate limit or overload. Advisory: the
	// orchestrator only uses its vendor delay when rescheduling a retry.
	LimitKindThrottle LimitKind = "throttle"
)

// Limit signal sources.
const (
	LimitSourceRateLimitEvent = "rate_limit_event"
	LimitSourceAPIRetry       = "api_retry"
	LimitSourceResult         = "result"
	LimitSourceText           = "text"
)

// EventError is the normalized type of a Codex top-level `error` event or
// an `error` item. It carries Message and, when the text is a limit, Limit.
const EventError = "error"

// maxRetryDelayMs bounds an api_retry retry_delay_ms before it becomes a
// time.Duration (one day; the breaker applies its own, tighter cap).
const maxRetryDelayMs = float64(24 * time.Hour / time.Millisecond)

// limitMessageMaxBytes bounds LimitSignal.Message (vendor text only, never
// stderr).
const limitMessageMaxBytes = 512

// LimitSignal is a typed vendor limit signal parsed from an agent stream.
type LimitSignal struct {
	Kind   LimitKind
	Source string // one of the LimitSource* constants
	// Status is the rate_limit_event status ("rejected"); empty otherwise.
	Status string
	// LimitType is the vendor window: five_hour, seven_day, ... ("" unknown).
	LimitType string
	// ResetsAt is when the limit clears; zero when unknown.
	ResetsAt time.Time
	// RetryAfter is the vendor's own retry delay (api_retry retry_delay_ms).
	RetryAfter time.Duration
	// HTTPStatus is the API status (api_retry error_status or
	// result.api_error_status); 0 when absent.
	HTTPStatus int
	// ErrorCategory is the api_retry error category (rate_limit, overloaded).
	ErrorCategory string
	// Message is the vendor's limit text (bounded), for logs and comments.
	Message string
	// ResetZoneAssumed is true when ResetsAt was read from text that names
	// no time zone, so the parser's zone was assumed (M3-close BH-M3-4).
	ResetZoneAssumed bool
}

// Terminal reports whether the signal is a quota limit: retrying on the same
// backend before ResetsAt cannot succeed.
func (s *LimitSignal) Terminal() bool {
	return s != nil && s.Kind == LimitKindQuota
}

// VendorDelay is the delay the vendor asked for, measured from now:
// RetryAfter when set, else the time until ResetsAt. Zero when unknown or
// already past.
func (s *LimitSignal) VendorDelay(now time.Time) time.Duration {
	if s == nil {
		return 0
	}
	// A quota with a known reset waits for the reset (M3-close BH-M3-2):
	// a retry delay on a quota signal can only be a leftover api_retry hint.
	if s.Terminal() && !s.ResetsAt.IsZero() {
		if d := s.ResetsAt.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	if s.RetryAfter > 0 {
		return s.RetryAfter
	}
	if !s.ResetsAt.IsZero() {
		if d := s.ResetsAt.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// mergeLimit folds a newly parsed signal into the turn's accumulated one. An
// advisory (throttle) signal never downgrades a quota signal; a later signal
// inherits fields it lacks (the reset time from a preceding rate_limit_event,
// for example, onto the result that ends the turn).
func mergeLimit(prev, next *LimitSignal) *LimitSignal {
	if next == nil {
		return prev
	}
	if prev.Terminal() && !next.Terminal() {
		return prev
	}
	c := *next
	if prev != nil {
		if c.ResetsAt.IsZero() {
			c.ResetsAt = prev.ResetsAt
			c.ResetZoneAssumed = prev.ResetZoneAssumed
		}
		if c.LimitType == "" {
			c.LimitType = prev.LimitType
		}
		if c.Status == "" {
			c.Status = prev.Status
		}
		// A quota never inherits an advisory api_retry delay (BH-M3-2): the
		// delay is seconds, the quota lasts until its reset.
		if c.RetryAfter == 0 && (!c.Terminal() || prev.Terminal()) {
			c.RetryAfter = prev.RetryAfter
		}
		if c.HTTPStatus == 0 {
			c.HTTPStatus = prev.HTTPStatus
		}
	}
	return &c
}

// rateLimitInfo is the tolerant decode target for a rate_limit_event, read
// from the nested rate_limit_info object or, for older CLIs, from the
// event's top level. Unknown keys are ignored.
type rateLimitInfo struct {
	Status                string     `json:"status"`
	ResetsAt              flexNumber `json:"resetsAt"`
	RateLimitType         string     `json:"rateLimitType"`
	OverageStatus         string     `json:"overageStatus"`
	OverageResetsAt       flexNumber `json:"overageResetsAt"`
	OverageDisabledReason string     `json:"overageDisabledReason"`
}

// flexNumber decodes a JSON number, a numeric string ("429", "8000") or
// null, and never fails: any other shape decodes as absent (M3-close V2).
// The limit fields are best-effort metadata — a CLI emitting "429" or 429.0
// must not make json.Unmarshal reject the WHOLE line, which readLines would
// then skip, losing the turn's result event.
type flexNumber struct {
	v  float64
	ok bool
}

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	*f = flexNumber{}
	var n float64
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexNumber{v: n, ok: true}
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		if n, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
			*f = flexNumber{v: n, ok: true}
		}
	}
	return nil
}

// ptr returns the value as *float64, nil when absent.
func (f flexNumber) ptr() *float64 {
	if !f.ok {
		return nil
	}
	v := f.v
	return &v
}

// int returns the value as an int, and whether it is present and integral
// within a sane range.
func (f flexNumber) int() (int, bool) {
	if !f.ok || f.v != math.Trunc(f.v) || math.Abs(f.v) > 1e9 {
		return 0, false
	}
	return int(f.v), true
}

// epochToTime reads an epoch value as seconds, or as milliseconds when its
// magnitude says so (≥ 1e12 is year 33658 in seconds but 2001 in ms).
func epochToTime(v *float64) time.Time {
	if v == nil || *v <= 0 {
		return time.Time{}
	}
	if *v >= 1e12 {
		return time.UnixMilli(int64(*v))
	}
	return time.Unix(int64(*v), 0)
}

// parseRateLimitEvent returns a quota signal for a rejected status, and nil
// for allowed, allowed_warning, a rejection covered by overage credits, or an
// undecodable payload.
func parseRateLimitEvent(line []byte, nested json.RawMessage) *LimitSignal {
	var info rateLimitInfo
	src := []byte(nested)
	if len(nested) == 0 || string(nested) == "null" {
		src = line
	}
	if err := json.Unmarshal(src, &info); err != nil {
		return nil
	}
	if info.Status != "rejected" {
		return nil
	}
	if info.OverageStatus == "allowed" || info.OverageStatus == "allowed_warning" {
		return nil // still served from overage credits
	}
	resets := epochToTime(info.ResetsAt.ptr())
	if resets.IsZero() {
		resets = epochToTime(info.OverageResetsAt.ptr())
	}
	return &LimitSignal{
		Kind:      LimitKindQuota,
		Source:    LimitSourceRateLimitEvent,
		Status:    info.Status,
		LimitType: info.RateLimitType,
		ResetsAt:  resets,
		Message:   info.OverageDisabledReason,
	}
}

// parseAPIRetry returns an advisory throttle signal for an api_retry caused
// by a rate limit or overload, nil otherwise.
func parseAPIRetry(raw rawEvent) *LimitSignal {
	category := ""
	if len(raw.Error) > 0 {
		_ = json.Unmarshal(raw.Error, &category) // non-string error: no category
	}
	status, _ := raw.ErrorStatus.int()
	switch {
	case category == "rate_limit" || category == "overloaded":
	case status == 429 || status == 529:
	default:
		return nil
	}
	var delay time.Duration
	if ms := raw.RetryDelayMs; ms.ok && ms.v > 0 {
		// Clamp before converting: 1e30 ms overflows time.Duration.
		delay = time.Duration(min(ms.v, maxRetryDelayMs)) * time.Millisecond
	}
	return &LimitSignal{
		Kind:          LimitKindThrottle,
		Source:        LimitSourceAPIRetry,
		RetryAfter:    delay,
		HTTPStatus:    status,
		ErrorCategory: category,
	}
}

// parseResultLimit classifies a Claude result event by api_error_status,
// falling back to the result text for CLIs that omit the status.
func parseResultLimit(raw rawEvent, isError bool, now time.Time) *LimitSignal {
	status, _ := raw.APIErrorStatus.int()
	var kind LimitKind
	switch status {
	case 429, 402:
		kind = LimitKindQuota
	case 529:
		kind = LimitKindThrottle
	}
	if kind == "" {
		if !isError {
			return nil
		}
		sig := ClassifyLimitText(raw.Result, now, time.Local)
		if sig != nil {
			sig.Source = LimitSourceResult
		}
		return sig
	}
	sig := &LimitSignal{
		Kind:       kind,
		Source:     LimitSourceResult,
		HTTPStatus: status,
		Message:    boundLimitMessage(raw.Result),
	}
	if t, ok, assumed := parseResetTimeZone(raw.Result, now, time.Local); ok {
		sig.ResetsAt, sig.ResetZoneAssumed = t, assumed
	}
	return sig
}

// normalizeLimitText lowercases and folds the U+2019 apostrophe upstream
// Codex uses ("You’ve") onto the ASCII one.
func normalizeLimitText(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "’", "'"))
}

// quotaLimitPhrases name a usage/credit limit that retrying cannot clear
// before its reset. Matched on normalized text.
var quotaLimitPhrases = []string{
	"you've hit your usage limit",
	"usage limit reached",
	"usage_limit_reached",
	"usagelimitexceeded",
	"usage_limit_exceeded",
	"you hit your spend cap",
	"you've hit your spend cap",
	"credit balance is too low",
	"out of credits",
	"out of extra usage",
	"insufficient_quota",
	"exceeded your current quota",
}

// quotaLimitClause matches the Claude/Codex opener followed, in the same
// clause, by limit wording: "You've hit your session limit", "... weekly
// limit", "... Opus limit", "... team's shared budget".
var quotaLimitClause = regexp.MustCompile(`you've hit your [a-z0-9' -]{0,40}\b(limit|budget|spend cap)\b`)

// throttlePhrases name a transient rate limit or overload.
var throttlePhrases = []string{
	"rate limit reached",
	"rate limit exceeded",
	"rate_limit_exceeded",
	"429 too many requests",
	"too many requests",
	"overloaded_error",
	"server is overloaded",
}

// limitKindOfText returns the limit kind a message names, or "".
func limitKindOfText(lower string) LimitKind {
	if containsAny(lower, quotaLimitPhrases) || quotaLimitClause.MatchString(lower) {
		return LimitKindQuota
	}
	if containsAny(lower, throttlePhrases) {
		return LimitKindThrottle
	}
	return ""
}

// ClassifyLimitText is the text fallback: it classifies a vendor error
// message (Codex error events, Codex turn.failed, a Claude result without
// api_error_status) as a limit, parsing a reset time when the text carries
// one. loc is the agent host's time zone for times printed without one (nil
// means time.Local). Returns nil when the message names no limit.
func ClassifyLimitText(msg string, now time.Time, loc *time.Location) *LimitSignal {
	kind := limitKindOfText(normalizeLimitText(msg))
	if kind == "" {
		return nil
	}
	sig := &LimitSignal{Kind: kind, Source: LimitSourceText, Message: boundLimitMessage(msg)}
	if t, ok, assumed := parseResetTimeZone(msg, now, loc); ok {
		sig.ResetsAt, sig.ResetZoneAssumed = t, assumed
	}
	return sig
}

func boundLimitMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= limitMessageMaxBytes {
		return s
	}
	cut := limitMessageMaxBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

var (
	// Codex: "try again at Sep 27th, 2026 3:04 PM" (chrono
	// "%b %-d<ordinal>, %Y %-I:%M %p").
	codexResetLong = regexp.MustCompile(`(?i)try again at ([a-z]{3}) (\d{1,2})(?:st|nd|rd|th)?,? (\d{4}),? (\d{1,2}):(\d{2}) ?([ap]m)`)
	// Codex same-day: "Try again at 3:04 PM" (chrono "%-I:%M %p").
	codexResetShort = regexp.MustCompile(`(?i)try again at (\d{1,2}):(\d{2}) ?([ap]m)`)
	// Claude: "resets 3pm (Europe/Berlin)", "resets 11:30am (UTC)",
	// "resets Sep 29, 5pm (UTC)", "resets Jan 3, 2027, 9am (UTC)" — the
	// CLI's en-US toLocaleString with the AM/PM lowercased and unspaced.
	claudeReset = regexp.MustCompile(`(?i)resets (?:([a-z]{3}) (\d{1,2}),? (?:(\d{4}),? )?)?(\d{1,2})(?::(\d{2}))? ?([ap]m)(?: ?\(([^)\s]+)\))?`)
)

// parseResetTimeZone is ParseResetTime that also reports whether the zone
// was assumed (loc was used because the text names no loadable zone).
func parseResetTimeZone(text string, now time.Time, loc *time.Location) (time.Time, bool, bool) {
	if loc == nil {
		loc = time.Local
	}
	if m := codexResetLong.FindStringSubmatch(text); m != nil {
		t, ok := buildResetTime(now, loc, m[1], m[2], m[3], m[4], m[5], m[6])
		return t, ok, true
	}
	if m := codexResetShort.FindStringSubmatch(text); m != nil {
		t, ok := buildResetTime(now, loc, "", "", "", m[1], m[2], m[3])
		return t, ok, true
	}
	if m := claudeReset.FindStringSubmatch(text); m != nil {
		zone, assumed := loc, true
		if m[7] != "" {
			if z, err := time.LoadLocation(m[7]); err == nil {
				zone, assumed = z, false
			}
		}
		t, ok := buildResetTime(now, zone, m[1], m[2], m[3], m[4], m[5], m[6])
		return t, ok, assumed
	}
	return time.Time{}, false, false
}

// buildResetTime assembles a reset time from its matched parts; month, day
// and year may be empty.
func buildResetTime(now time.Time, loc *time.Location, month, day, year, hour, minute, ampm string) (time.Time, bool) {
	h, err := strconv.Atoi(hour)
	if err != nil || h < 1 || h > 12 {
		return time.Time{}, false
	}
	mi := 0
	if minute != "" {
		if mi, err = strconv.Atoi(minute); err != nil || mi > 59 {
			return time.Time{}, false
		}
	}
	h %= 12
	if strings.EqualFold(ampm, "pm") {
		h += 12
	}
	local := now.In(loc)
	if month == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), h, mi, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	mon, err := time.Parse("Jan", strings.ToUpper(month[:1])+strings.ToLower(month[1:]))
	if err != nil {
		return time.Time{}, false
	}
	d, err := strconv.Atoi(day)
	if err != nil || d < 1 || d > 31 {
		return time.Time{}, false
	}
	y := local.Year()
	explicitYear := year != ""
	if explicitYear {
		if y, err = strconv.Atoi(year); err != nil {
			return time.Time{}, false
		}
	}
	t := time.Date(y, mon.Month(), d, h, mi, 0, 0, loc)
	if !explicitYear && t.Before(now.Add(-24*time.Hour)) {
		t = t.AddDate(1, 0, 0)
	}
	return t, true
}

// formatLimitReset renders a signal's reset time for logs ("" when unknown).
func formatLimitReset(s *LimitSignal) string {
	if s == nil || s.ResetsAt.IsZero() {
		return ""
	}
	return s.ResetsAt.UTC().Format(time.RFC3339)
}
