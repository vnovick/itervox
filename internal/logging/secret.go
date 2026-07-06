// Package logging provides slog handlers and helpers, including a Secret
// LogValuer for redacting sensitive values from structured log output and a
// regex scrubber for catching secrets that slipped through as plain strings.
package logging

import "log/slog"

// secretMask is the string substituted for any redacted value. Centralised so
// post-hoc analysis can grep for it across both the structured-attr path
// (Secret LogValuer) and the regex scrubber path.
const secretMask = "***"

// Secret wraps a sensitive string so that when it's emitted as an slog
// attribute its VALUE is replaced with "***" while its KEY is preserved for
// audit ("we logged that there was a token, not what it was").
//
// Use at every site where a secret could otherwise become an slog attribute
// value — bearer tokens, API keys, OAuth secrets, redirect-URL query params
// that carry them. Never pass a raw secret string as an slog attribute value
// directly; always wrap it through Secret.
//
// Example:
//
//	slog.Info("server: token authentication enabled", "token", logging.Secret(token))
//	  // → ... token=***
//
// The wrapper does NOT obscure the secret in its in-memory representation
// (Go's slog has no defence against memory inspection); its job is only to
// keep the secret out of the log sink.
type Secret string

// LogValue implements slog.LogValuer so the slog formatter calls this method
// instead of stringifying the wrapped value.
func (s Secret) LogValue() slog.Value {
	if s == "" {
		// Distinguish "key was set, value was empty" from "key was redacted".
		// Empty secret values are typically a misconfiguration the operator
		// should see; pretending they were redacted would hide the real state.
		return slog.StringValue("")
	}
	return slog.StringValue(secretMask)
}

// String implements fmt.Stringer so accidental %s / %v formatting of a Secret
// outside slog still redacts. Without this, `fmt.Sprintf("token=%s", sec)`
// would print the raw value because Sprintf doesn't know about LogValuer.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return secretMask
}
