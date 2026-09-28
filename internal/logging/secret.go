// Package logging provides slog handlers and helpers: a regex scrubber
// (RedactingHandler / RedactString) for catching secrets in log messages and
// attributes, plus exact-value redaction of registered secrets.
package logging

// secretMask is the string substituted for any redacted value. Centralised so
// post-hoc analysis can grep for it across every redaction path.
//
// CORE-110: the Secret LogValuer wrapper that used to live here was never
// used by production code (deadcode); redaction goes through
// RedactingHandler and RegisterSecret instead.
const secretMask = "***"
