package logging

import (
	"context"
	"log/slog"
	"regexp"
)

// secretValuePatterns are regex patterns for plain-string secrets that may
// appear in log records. These catch values that escape the structured-attr
// path — e.g. a stderr dump from an agent subprocess that contains its env,
// a panic stack trace, or a pre-existing slog.*("foo bar="+token, ...) call
// that hasn't been migrated to the Secret LogValuer yet.
//
// Each pattern is RE2-compatible (Go regexp). The redactor replaces every
// match with the `secretMask` constant from secret.go.
//
// Patterns are intentionally conservative — false-redacting is much better
// than false-leaking. If a new secret format appears in production logs, add
// a pattern here.
var secretValuePatterns = []*regexp.Regexp{
	// Anthropic API keys: "sk-ant-..." (alphanumeric body of variable length).
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{32,}`),
	// Linear API keys: "lin_api_..." (legacy) and "lin_oauth_..." (OAuth).
	regexp.MustCompile(`lin_(?:api|oauth)_[A-Za-z0-9]{32,}`),
	// GitHub personal-access tokens (classic + fine-grained).
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{82,}`),
	// Authorization: Bearer <token> (any token shape).
	regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+[^\s"',]+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`),
}

// redactString applies every secretValuePattern to s, replacing every match
// with `secretMask`. Returns the original string when no pattern matches —
// callers can use the returned string == s comparison as a fast-path check.
func redactString(s string) string {
	for _, re := range secretValuePatterns {
		s = re.ReplaceAllString(s, secretMask)
	}
	return s
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
