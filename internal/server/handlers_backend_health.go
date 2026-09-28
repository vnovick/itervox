package server

import (
	"net/http"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// BackendBreakerClearer is optionally implemented by the orchestrator
// client (M3-close V1): it queues the clearing of one agent-backend circuit
// breaker on the event loop. False means the event queue was full.
type BackendBreakerClearer interface {
	ClearBackendBreaker(backend, host string) bool
}

// validBreakerHost accepts "" (local runs) or an SSH host token: no
// whitespace or control characters, no leading '-', at most 255 bytes. It
// is used only as a map key, but is kept to the ssh_hosts shape anyway.
func validBreakerHost(host string) bool {
	if host == "" {
		return true
	}
	if len(host) > 255 || strings.HasPrefix(host, "-") {
		return false
	}
	for _, r := range host {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// handleClearBackendBreaker closes one agent-backend circuit breaker.
// POST /api/v1/backend-health/clear  {"backend":"claude","host":""}
// 202 = queued on the event loop (poll /api/v1/state to observe it); 400 on
// an unknown backend or a malformed host; 503 when the event queue is full;
// 501 when the client cannot clear breakers.
func (s *Server) handleClearBackendBreaker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend string `json:"backend"`
		Host    string `json:"host"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if !config.IsSupportedBackend(body.Backend) {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", `backend must be "claude" or "codex"`, "backend")
		return
	}
	if !validBreakerHost(body.Host) {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "host must be empty (local) or an SSH host name", "host")
		return
	}
	clearer, ok := s.client.(BackendBreakerClearer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_implemented", "this daemon cannot clear backend breakers")
		return
	}
	if !clearer.ClearBackendBreaker(body.Backend, body.Host) {
		writeError(w, http.StatusServiceUnavailable, "event_queue_full", "orchestrator event queue is full; retry")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "backend": body.Backend, "host": body.Host})
}
