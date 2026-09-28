package logging

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
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
// that logs a secret inside a larger string.
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
	// OpenAI keys (CORE-104), ordered after sk-ant- so the Anthropic mask
	// applies first: project / service-account / admin keys
	// ("sk-proj-…", "sk-svcacct-…", "sk-admin-…") and the legacy bare
	// "sk-" + 40+ alphanumerics. A short "sk-abc123" is left alone.
	{regexp.MustCompile(`sk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}`), secretMask},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{40,}`), secretMask},
	// Linear API keys: "lin_api_..." (legacy) and "lin_oauth_..." (OAuth).
	{regexp.MustCompile(`lin_(?:api|oauth)_[A-Za-z0-9]{32,}`), secretMask},
	// GitHub tokens: personal (ghp_), OAuth (gho_), user-to-server (ghu_),
	// server-to-server / Actions (ghs_), refresh (ghr_), and fine-grained.
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`), secretMask},
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
	// CORE-167 — shapes agent stderr carries that no vendor prefix catches.
	//
	// An environment assignment whose UPPER_CASE name says it holds a secret
	// (`export AWS_SECRET_ACCESS_KEY=…`, `GITLAB_TOKEN: …`,
	// `"OPENAI_API_KEY": "…"`): the name is kept so the line still says
	// what leaked, the value is masked. Case-sensitive on purpose: log
	// attributes such as input_tokens=123 must not match. An unquoted value
	// needs 6+ characters, so flags such as ITERVOX_PRINT_TOKEN=1 or
	// SOME_TOKEN=true in help text stay readable.
	{regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|PRIVATE_KEY|ACCESS_KEY|CREDENTIALS?)[A-Z0-9_]*"?\s*[=:]\s*)(?:"[^"\n]*"|'[^'\n]*'|[^\s"',;}]{6,})`), "${1}" + secretMask},
	// The same assignment with a lower- or mixed-case name (M2-close):
	// `password=…`, `passwd: …`, `secret=…`, `api_key=…`, `"apiKey":"…"`,
	// `x-api-key: …`, `aws_secret_access_key = …`, `client_secret=…`,
	// `access_token=…`. A bare `token=` is left to the dashboard-token rule
	// above (its 32-hex floor is deliberate). The name must END with the keyword, so
	// `input_tokens=123` or `secretary: …` never match, and an unquoted
	// value needs 6+ characters (`password: true` stays).
	{regexp.MustCompile(`(?i)(\b(?:[a-z0-9_.-]*?(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?key|private[_-]?key|(?:auth|access|refresh|id|session)[_-]?token))["']?\s*[=:]\s*)(?:"[^"\n]*"|'[^'\n]*'|[^\s"',;}]{6,})`), "${1}" + secretMask},
	// HTTP Basic credentials in an Authorization header (any case).
	{regexp.MustCompile(`(?i)(authorization["']?\s*[=:]\s*["']?basic\s+)[A-Za-z0-9+/=_-]+`), "${1}" + secretMask},
	// URL userinfo password: scheme://user:password@host, including an
	// empty user (`https://:TOKEN@host`, M2-close).
	{regexp.MustCompile(`(://[^/\s:@]*:)[^/\s@]+@`), "${1}" + secretMask + "@"},
	// GitLab personal/project access tokens (shorter than the entropy floor).
	{regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`), secretMask},
	// PEM private key blocks.
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), secretMask},
	// JSON Web Tokens (three base64url segments, header starting "eyJ").
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), secretMask},
	// Slack tokens (bot/user/… and app-level xapp-).
	{regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{10,}`), secretMask},
	{regexp.MustCompile(`xapp-[A-Za-z0-9-]{10,}`), secretMask},
	// AWS access key ids.
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), secretMask},
}

// highEntropyCandidate finds runs that could be an unprefixed secret: 32+
// characters of the base64 / base64url alphabet with optional padding.
// redactHighEntropy then keeps only the ones that look random.
var highEntropyCandidate = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)

// minSecretEntropyBits is the Shannon entropy (bits per character) a
// candidate must reach to be masked. 40 random base64 characters average
// ~4.9; camelCase identifiers and English-ish slugs sit well below 4.
const minSecretEntropyBits = 4.0

// redactHighEntropy masks long random-looking tokens that carry no known
// prefix (CORE-167): an agent's stderr can echo a session cookie, a signing
// key or a vendor token nobody wrote a pattern for. A candidate is masked
// only when it mixes upper case, lower case and digits AND its character
// entropy is at least minSecretEntropyBits, which deliberately leaves alone:
//   - hex digests such as git SHAs (no upper case) and UUIDs (hyphen-split
//     into short runs) — they are what makes a failure debuggable;
//   - absolute paths (a run starting with '/'), identifiers and relative
//     paths without digits, and all-caps constants;
//   - anything under 32 characters.
//
// The trade-off: a random secret of fewer than 32 characters, or one that
// happens to lack a digit (~0.1% of 40-char base64 strings), is not caught
// here; the prefix and KEY=value patterns above are the first line.
func redactHighEntropy(s string) string {
	return highEntropyCandidate.ReplaceAllStringFunc(s, func(tok string) string {
		if looksRandom(tok) {
			return secretMask
		}
		// A path or URL whose one segment is a token (`/hooks/<base64>`):
		// mask just that segment. Segments are judged on their own so a
		// wordy path cannot shield a random segment inside it (M2-close;
		// the former leading-'/' exemption let every such token through).
		if !strings.Contains(tok, "/") {
			return tok
		}
		parts := strings.Split(tok, "/")
		changed := false
		for i, p := range parts {
			if len(p) >= minSegmentSecretLen && looksRandom(p) {
				parts[i] = secretMask
				changed = true
			}
		}
		if !changed {
			return tok
		}
		return strings.Join(parts, "/")
	})
}

// minSegmentSecretLen is the shortest path segment redactHighEntropy judges
// on its own.
const minSegmentSecretLen = 24

// basicCredential finds `Basic <base64>` outside an Authorization header;
// redactBasicCredentials masks it only when the value decodes to
// "user:password", so prose such as "Basic information" is untouched.
var basicCredential = regexp.MustCompile(`\b([Bb]asic\s+)([A-Za-z0-9+/]{6,}={0,2})`)

func redactBasicCredentials(s string) string {
	return basicCredential.ReplaceAllStringFunc(s, func(m string) string {
		sub := basicCredential.FindStringSubmatch(m)
		dec, err := base64.StdEncoding.DecodeString(sub[2])
		if err != nil {
			dec, err = base64.RawStdEncoding.DecodeString(sub[2])
		}
		if err != nil || !bytes.ContainsRune(dec, ':') {
			return m
		}
		return sub[1] + secretMask
	})
}

// diagnosticIDPrefixes mark runs that are random by construction but are
// what an operator quotes to a vendor or a build log (M2-close): API request
// ids (`req_…`, `msg_…`) and Subresource Integrity hashes (`sha512-…`).
var diagnosticIDPrefixes = []string{"req_", "msg_", "sha1-", "sha256-", "sha384-", "sha512-"}

// wordyLetterShare is the share of a run's letters that sit in runs of four
// or more consecutive lower-case letters. Identifiers, slugs and paths are
// made of words (well above 0.4); random base64 almost never has four lower
// case letters in a row (a random 40-char token scores ~0.05).
func wordyLetterShare(tok string) float64 {
	letters, wordy, run := 0, 0, 0
	flush := func() {
		if run >= 4 {
			wordy += run
		}
		run = 0
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= 'a' && c <= 'z':
			letters++
			run++
		case c >= 'A' && c <= 'Z':
			letters++
			flush()
		default:
			flush()
		}
	}
	flush()
	if letters == 0 {
		return 0
	}
	return float64(wordy) / float64(letters)
}

// maxWordyLetterShare is the wordyLetterShare at or above which a run is
// treated as words, not a secret.
const maxWordyLetterShare = 0.4

func looksRandom(tok string) bool {
	for _, p := range diagnosticIDPrefixes {
		if strings.HasPrefix(tok, p) {
			return false
		}
	}
	if wordyLetterShare(tok) >= maxWordyLetterShare {
		return false
	}
	var upper, lower, digit bool
	var counts [256]int
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		counts[c]++
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	// Upper AND lower case are required (hex digests, UUIDs and all-caps
	// constants have one case); a digit is not (M2-close: base64 with no
	// digit leaked). Words are excluded by wordyLetterShare above.
	_ = digit
	if !upper || !lower {
		return false
	}
	n := float64(len(tok))
	entropy := 0.0
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy >= minSecretEntropyBits
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
	s = redactBasicCredentials(s)
	s = redactHighEntropy(s)

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

// RedactString applies the same secret scrubbing the RedactingHandler applies
// to log records (every secretValuePattern plus every RegisterSecret value)
// to s. It exists for text that leaves the process by a path other than a
// log line, such as an input-required question posted as a tracker comment
// (CORE-164): no tracker-comment path runs through the log handler.
func RedactString(s string) string {
	return redactString(s)
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
// text handler that writes to the rotating file sink). It covers msg
// strings, attribute values, stderr blobs, panic dumps and third-party
// library output; RegisterSecret adds exact-value redaction.
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
	case slog.KindAny:
		// An error or fmt.Stringer attribute (`"error", err`) is KindAny,
		// and the inner handler renders it through Error()/String() — so
		// it must be redacted in that form, or every error attribute (the
		// worker's "turn failed" cause carries agent stderr) bypasses the
		// redactor (CORE-167). Replaced only when redaction changes it.
		var text string
		switch x := v.Any().(type) {
		case error:
			text = x.Error()
		case fmt.Stringer:
			text = x.String()
		default:
			return v
		}
		if red := redactString(text); red != text {
			return slog.StringValue(red)
		}
		return v
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
