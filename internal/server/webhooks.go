package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// webhooksPathPrefix is the tracker webhook route's prefix; the Host guard
// lets POSTs under it through (see hostGuard).
const webhooksPathPrefix = "/api/v1/webhooks/"

// WebhookHandler receives one tracker's webhooks (#131): it verifies each
// delivery's signature itself.
type WebhookHandler interface {
	http.Handler
	// Tracker is the tracker kind ("linear", "github") it accepts.
	Tracker() string
}

// handleWebhook passes a delivery for the configured tracker to the webhook
// handler, and one for pr-events to the PR-event receiver (#173); any other
// path, or no receiver configured, is a 404.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// #173: PR-event deliveries have their own receiver and secret.
	if s.prEventWebhooks != nil && chi.URLParam(r, "tracker") == s.prEventWebhooks.Tracker() {
		s.prEventWebhooks.ServeHTTP(w, r)
		return
	}
	if s.webhooks == nil || chi.URLParam(r, "tracker") != s.webhooks.Tracker() {
		writeError(w, http.StatusNotFound, "not_found", "no webhook receiver for this tracker (tracker.webhooks)")
		return
	}
	s.webhooks.ServeHTTP(w, r)
}
