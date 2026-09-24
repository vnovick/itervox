package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleRetryOutboxEntry makes a pending write-ahead-outbox entry
// immediately due, bypassing its backoff (write-ahead-outbox design,
// "Surfaces"). 202 Accepted on success; 404 when no entry with that id
// exists. Unlike the deps-override handlers, there is no orchestrator event
// channel involved — the client implementation calls the Outbox handle
// directly, so a false return unambiguously means "unknown id", not "queue
// full".
func (s *Server) handleRetryOutboxEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.client.RetryOutboxEntry(id) {
		writeError(w, http.StatusNotFound, "outbox_entry_not_found", "no outbox entry with that id")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "retried": true})
}

// handleDropOutboxEntry discards a pending write-ahead-outbox entry
// (operator action — the remedy for an entry that can never auto-reconcile,
// e.g. an issue a human moved out of active states while a write was
// pending). Always answers 202 Accepted, including for an unknown id:
// Outbox.Drop is documented as idempotent (Task 1), and "the entry you
// wanted gone is gone" is true either way — there is no distinguishable
// error case to surface as 404 here.
func (s *Server) handleDropOutboxEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.client.DropOutboxEntry(id)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "dropped": true})
}
