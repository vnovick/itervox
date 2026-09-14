package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

// secretPattern pairs a regex with its replacement string. Most patterns
// replace the entire match with secretMask; the token query-param pattern
// captures a prefix group and needs a `$1`-style replacement to preserve it
// (see secretValuePatterns below), so ReplaceAllString needs a per-pattern
// replacement rather than a single constant.
type secretPattern struct {
	re          *regexp.Regexp
	replacement string
}

// secretValuePatterns are regex patterns for plain-string secrets that may
// appear in log records. These catch values that escape the structured-attr
// path — e.g. a stderr dump from an agent subprocess that contains its env,
// a panic stack trace, or a pre-existing slog.*("foo bar="+token, ...) call
// that hasn't been migrated to the Secret LogValuer yet.
//
// Each pattern is RE2-compatible (Go regexp). The redactor replaces every
// match with `secretMask` (or, for patterns with a capture group, with the
// captured group followed by `secretMask`).
//
// Patterns are intentionally conservative — false-redacting is much better
// than false-leaking. If a new secret format appears in production logs, add
// a pattern here.
var secretValuePatterns = []secretPattern{
	// Anthropic API keys: "sk-ant-..." (alphanumeric body of variable length).
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{32,}`), secretMask},
	// Linear API keys: "lin_api_..." (legacy) and "lin_oauth_..." (OAuth).
	{regexp.MustCompile(`lin_(?:api|oauth)_[A-Za-z0-9]{32,}`), secretMask},
	// GitHub personal-access tokens (classic + fine-grained).
	{regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`), secretMask},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{82,}`), secretMask},
	// Authorization: Bearer <token> (any token shape).
	{regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+[^\s"',]+`), secretMask},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`), secretMask},
	// Dashboard URL token query param: "?token=<hex>" / "&token=<hex>". The
	// startup log line prints "http://host/?token=<hex>" verbatim (see
	// main.go), so any log line, doctor output, or copy/paste that echoes
	// that URL carries the bearer token in the clear. The `[?&]token=`
	// prefix is captured and kept so the redacted line still reads as a
	// URL with a token param — only the hex value itself is masked.
	{regexp.MustCompile(`([?&]token=)[0-9a-f]{32,}`), "${1}" + secretMask},
}

// redactString applies every secretValuePattern to s, replacing every match
// with `secretMask` (preserving captured prefixes where the pattern has
// one), then scrubs any exact values registered via RegisterSecret. Returns
// the original string when nothing matches — callers can use the returned
// string == s comparison as a fast-path check.
func redactString(s string) string {
	for _, p := range secretValuePatterns {
		s = p.re.ReplaceAllString(s, p.replacement)
	}

	registeredSecretsMu.RLock()
	secrets := registeredSecrets
	registeredSecretsMu.RUnlock()
	for _, v := range secrets {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, secretMask)
		}
	}
	return s
}

// minRegisteredSecretLen is the shortest value RegisterSecret will accept.
// Values shorter than this are far more likely to be an accidental short
// string (a typo'd env var, a test fixture) than a real secret, and
// registering them would risk mass-redacting ordinary log text that happens
// to contain the same short substring.
const minRegisteredSecretLen = 8

// registeredSecretsMu guards registeredSecrets. RegisterSecret is called
// rarely (a handful of times at startup); redactString is called on every
// log record from potentially many goroutines (worker subprocesses, HTTP
// handlers, the orchestrator event loop), so the hot path takes RLock and
// copies the slice header only — no per-record allocation or contention
// against other readers.
var (
	registeredSecretsMu sync.RWMutex
	registeredSecrets   []string
)

// RegisterSecret adds value to the set of exact-match strings that
// redactString scrubs from every subsequent log line (message and
// attributes), in addition to the pattern-based matches in
// secretValuePatterns above.
//
// Rationale (wave-1 review): agent subprocesses (claude/codex) inherit the
// full process environment, including ITERVOX_API_TOKEN — a bare hex string
// that doesn't match any Anthropic/Linear/GitHub/Bearer pattern above. If a
// subprocess dumps its environment (debug output, a crash, `env` invoked by
// the agent itself) that output is slogged verbatim and the pattern-based
// redactor would miss it entirely. Headless mode additionally fans
// post-startup logs to journald, widening the blast radius of any leak.
// Registering the live token's exact value closes that gap independent of
// its shape.
//
// value is never logged by this function — only its length is inspectable
// (via the no-op-on-short guard below), never its content.
//
// No-ops on empty or short (<8 char) values: an empty value signals nothing
// was configured, and a short value is likely not a real secret — treating
// it as one would risk redacting unrelated log text that happens to share
// the substring.
func RegisterSecret(value string) {
	if len(value) < minRegisteredSecretLen {
		return
	}
	registeredSecretsMu.Lock()
	defer registeredSecretsMu.Unlock()
	for _, v := range registeredSecrets {
		if v == value {
			return
		}
	}
	// Append-only: never mutate an existing element or truncate the slice,
	// so a reader that copied the old slice header under RLock before this
	// Lock can safely range over it after we release — the elements it
	// already saw never change.
	registeredSecrets = append(registeredSecrets, value)
}

// RedactingHandler wraps another slog.Handler and runs every string-typed
// attribute value through redactString before forwarding the record. Use it
// as the OUTERMOST layer of the log pipeline (typically wrapping a JSON or
// text handler that writes to the rotating file sink). The Secret LogValuer
// covers attribute values that you control; this handler covers everything
// else — including msg strings, stderr blobs, panic dumps, and third-party
// library output.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps inner with secret redaction. inner MUST not be nil.
func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// Apply redaction to the message itself.
	r.Message = redactString(r.Message)

	// Walk every attribute and rebuild any string-valued ones whose redacted
	// form differs. We avoid mutating in place — slog.Record exposes attrs
	// only via AddAttrs, so we collect, redact, and rebuild.
	type kv struct {
		k string
		v slog.Value
	}
	collected := make([]kv, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		collected = append(collected, kv{a.Key, redactValue(a.Value)})
		return true
	})

	// Build a fresh Record so we can replace the attrs cleanly.
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	for _, c := range collected {
		nr.AddAttrs(slog.Attr{Key: c.k, Value: c.v})
	}
	return h.inner.Handle(ctx, nr)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = slog.Attr{Key: a.Key, Value: redactValue(a.Value)}
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(redacted)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

// redactValue handles any slog.Value, recursing into groups so nested attrs
// are scrubbed too. Non-string leaf values are returned unchanged.
func redactValue(v slog.Value) slog.Value {
	v = v.Resolve() // unwraps LogValuer (e.g. Secret) before string match
	switch v.Kind() {
	case slog.KindString:
		return slog.StringValue(redactString(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			out[i] = slog.Attr{Key: a.Key, Value: redactValue(a.Value)}
		}
		return slog.GroupValue(out...)
	default:
		return v
	}
}
