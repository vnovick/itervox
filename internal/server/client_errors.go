package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// CORE-048 — POST /api/v1/client-errors.
//
// The dashboard's error boundaries, global error/unhandledrejection handlers
// and snapshot schema-drift detector report here so a production UI failure
// reaches the operator's daemon-side view (log line, itervox_client_errors_total,
// the RecentFailures ring) instead of dying in the viewer's browser.
//
// The route sits in the authenticated group: bearer token in token mode, the
// CSRF guard (plus the root DNS-rebinding Host guard) in
// server.allow_unauthenticated mode. The handler never touches orchestrator
// state: it redacts, counts, logs and hands the report to a non-blocking
// sender (Config.ReportClientError → Orchestrator.RecordFailure).

// clientErrorMaxBody caps a report body, trailing bytes included — a
// dedicated cap, far below decodeJSONBody's generic 1 MiB.
const clientErrorMaxBody = 8 << 10

// Per-field bounds applied after redaction (the client truncates too; the
// server does not trust it to).
const (
	clientErrorMaxMessage = 1 << 10
	clientErrorMaxStack   = 2 << 10
	clientErrorMaxRoute   = 256
)

// ClientErrorBurst is how many reports the route accepts back-to-back;
// afterwards it refills at one report per clientErrorRefill. A reporting
// storm (a render loop throwing every frame across many tabs) therefore
// costs at most ~20 log lines a minute. The client dedupes and throttles as
// well; this is the server-side backstop.
const ClientErrorBurst = 20

const clientErrorRefill = 3 * time.Second

// ClientErrorReport is one redacted, bounded web client error.
type ClientErrorReport struct {
	// Kind is one of metrics.ClientErrorKinds ("other" for anything else).
	Kind    string
	Message string
	Route   string
	Stack   string
}

// ClientErrorGlobalBurst bounds all clients together: whatever the number of
// tabs or hosts reporting, the route accepts at most this many reports
// back-to-back and then one per second — a backstop for the log volume.
const ClientErrorGlobalBurst = 60

const clientErrorGlobalRefill = time.Second

// clientErrorMaxClients bounds the per-client table. Past it, idle (fully
// refilled) buckets are swept; if every tracked client is still active, a new
// client shares the global backstop only.
const clientErrorMaxClients = 1024

// ClientErrorLimiter rate-limits web client error reports (M2-close):
//
//   - Per client: ClientErrorBurst reports, refilling at one per
//     clientErrorRefill, keyed by the remote IP address. A dashboard tab in a
//     render loop exhausts its own bucket, not every other tab's. The key is
//     the address, not the bearer token, because every tab of a daemon
//     shares the one token; behind a reverse proxy every client has the
//     proxy's address and the per-client bucket degenerates to the former
//     single one — never worse.
//   - Global: ClientErrorGlobalBurst, one per second, over all clients.
//   - Process-scoped: DefaultClientErrorLimiter outlives the Server that
//     cmd/itervox rebuilds on every WORKFLOW.md reload, so a reload no
//     longer refills the buckets.
type ClientErrorLimiter struct {
	mu      sync.Mutex
	global  tokenBucket
	clients map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// take refills b for the time since its last use and spends one token.
func (b *tokenBucket) take(now time.Time, burst float64, refill time.Duration) bool {
	b.refill(now, burst, refill)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *tokenBucket) refill(now time.Time, burst float64, refill time.Duration) {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()/refill.Seconds())
	}
	b.last = now
}

// NewClientErrorLimiter returns an empty limiter.
func NewClientErrorLimiter() *ClientErrorLimiter {
	return &ClientErrorLimiter{clients: map[string]*tokenBucket{}}
}

// DefaultClientErrorLimiter is the process-wide limiter every Server built
// without Config.ClientErrorLimiter shares.
var DefaultClientErrorLimiter = NewClientErrorLimiter()

// clientKey is the remote IP (port dropped: every request of a tab comes
// from a new ephemeral port).
func clientKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func (l *ClientErrorLimiter) allow(remoteAddr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := clientKey(remoteAddr)
	b, ok := l.clients[key]
	if !ok {
		if len(l.clients) >= clientErrorMaxClients {
			for k, c := range l.clients {
				c.refill(now, ClientErrorBurst, clientErrorRefill)
				if c.tokens >= ClientErrorBurst {
					delete(l.clients, k)
				}
			}
		}
		if len(l.clients) < clientErrorMaxClients {
			b = &tokenBucket{}
			l.clients[key] = b
		}
	}
	if b != nil {
		// Check without spending first, so a request the global backstop
		// refuses does not also drain the client's own bucket.
		b.refill(now, ClientErrorBurst, clientErrorRefill)
		if b.tokens < 1 {
			return false
		}
	}
	if !l.global.take(now, ClientErrorGlobalBurst, clientErrorGlobalRefill) {
		return false
	}
	if b != nil {
		b.tokens--
	}
	return true
}

type clientErrorBody struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Route   string `json:"route"`
	Stack   string `json:"stack"`
}

// boundUTF8 cuts s to at most n bytes on a rune boundary.
func boundUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// handleClientError accepts one web client error report.
// POST /api/v1/client-errors → 202 {"accepted":true}; 400 malformed;
// 413 over 8 KiB (trailing bytes included); 429 rate limited; 503 when the
// orchestrator's event channel is full.
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, clientErrorMaxBody)
	var body clientErrorBody
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&body)
	if err == nil {
		// Exactly one JSON value; then drain to EOF so trailing padding
		// still counts against the cap (a single Decode stops after the
		// first value).
		var extra json.RawMessage
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("server: trailing data after the report object")
		}
		if _, cerr := io.Copy(io.Discard, r.Body); cerr != nil {
			err = cerr
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "client error report exceeds 8 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "invalid client error report: expected one JSON object")
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "message is required")
		return
	}
	kind := body.Kind
	if !slices.Contains(metrics.ClientErrorKinds, kind) {
		kind = "other"
	}
	if !s.clientErrors.allow(r.RemoteAddr, time.Now()) {
		metrics.ClientError(kind, metrics.ClientErrorOutcomeRateLimited)
		w.Header().Set("Retry-After", "3")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many client error reports; retry later")
		return
	}
	report := ClientErrorReport{
		Kind:    kind,
		Message: boundUTF8(logging.RedactString(body.Message), clientErrorMaxMessage),
		Route:   boundUTF8(logging.RedactString(body.Route), clientErrorMaxRoute),
		Stack:   boundUTF8(logging.RedactString(body.Stack), clientErrorMaxStack),
	}
	slog.Warn("web client error reported",
		"kind", report.Kind, "route", report.Route, "message", report.Message, "stack", report.Stack)
	if s.reportClientError != nil && !s.reportClientError(report) {
		metrics.ClientError(kind, metrics.ClientErrorOutcomeDropped)
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "orchestrator_busy", "orchestrator event queue is full; report dropped")
		return
	}
	metrics.ClientError(kind, metrics.ClientErrorOutcomeAccepted)
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}
