package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Token management and whoami (#214). Every route but whoami is admin
// (routeScopes). Secrets appear in exactly two responses, the create and
// the rotate answer, and are never logged or stored.

// TokenView is one stored token as the API shows it: no hash, no secret.
type TokenView struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Scope    string     `json:"scope"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"`
	Expired  bool       `json:"expired,omitempty"`
}

func tokenView(t apitokens.Token, now time.Time) TokenView {
	return TokenView{ID: t.ID, Name: t.Name, Scope: string(t.Scope), Created: t.Created,
		LastUsed: t.LastUsed, Expires: t.Expires, Expired: t.Expired(now)}
}

// TokenSecretResponse answers a create or rotate: the token and its secret,
// shown once.
type TokenSecretResponse struct {
	Token  TokenView `json:"token"`
	Secret string    `json:"secret"`
}

// handleWhoami answers the caller's principal: its scope, and the stored
// token's id and name when it is one.
// GET /api/v1/whoami
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, p)
}

// tokenStoreOr503 answers 503 when there is no store to manage: the daemon
// runs without one, or in server.allow_unauthenticated mode, where stored
// tokens are not consulted and creating one would only mislead.
func (s *Server) tokenStoreOr503(w http.ResponseWriter) (*apitokens.Store, bool) {
	if s.tokens == nil || s.apiToken == "" {
		writeError(w, http.StatusServiceUnavailable, "tokens_unavailable",
			"token management needs token mode: this daemon runs with server.allow_unauthenticated or without a token store")
		return nil, false
	}
	return s.tokens, true
}

// handleListTokens lists the stored tokens.
// GET /api/v1/tokens
func (s *Server) handleListTokens(w http.ResponseWriter, _ *http.Request) {
	store, ok := s.tokenStoreOr503(w)
	if !ok {
		return
	}
	tokens, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tokens_unreadable", err.Error())
		return
	}
	now := time.Now()
	views := make([]TokenView, len(tokens))
	for i, t := range tokens {
		views[i] = tokenView(t, now)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": views})
}

// handleCreateToken creates a stored token.
// POST /api/v1/tokens {"name":"ci","scope":"viewer","expires":"90d"}
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	store, ok := s.tokenStoreOr503(w)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Expires string `json:"expires"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON")
		return
	}
	scope, err := apitokens.ParseScope(body.Scope)
	if err != nil {
		writeErrorWithField(w, http.StatusBadRequest, "invalid_scope", err.Error(), "scope")
		return
	}
	now := time.Now()
	expires, err := apitokens.ParseExpiry(body.Expires, now)
	if err != nil {
		writeErrorWithField(w, http.StatusBadRequest, "invalid_expires", err.Error(), "expires")
		return
	}
	p, _ := principalFrom(r.Context())
	t, secret, err := store.Create(body.Name, scope, expires, p.actor())
	if err != nil {
		if errors.Is(err, apitokens.ErrInvalidName) {
			writeErrorWithField(w, http.StatusBadRequest, "invalid_name", err.Error(), "name")
			return
		}
		if errors.Is(err, apitokens.ErrExpiresInPast) {
			writeErrorWithField(w, http.StatusBadRequest, "invalid_expires", err.Error(), "expires")
			return
		}
		writeError(w, http.StatusInternalServerError, "token_create_failed", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, TokenSecretResponse{Token: tokenView(t, now), Secret: secret})
}

// handleRevokeToken deletes a stored token.
// DELETE /api/v1/tokens/{id}
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	store, ok := s.tokenStoreOr503(w)
	if !ok {
		return
	}
	p, _ := principalFrom(r.Context())
	t, err := store.Revoke(chi.URLParam(r, "id"), p.actor())
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": tokenView(t, time.Now())})
}

// handleRotateToken gives a stored token a new secret.
// POST /api/v1/tokens/{id}/rotate
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	store, ok := s.tokenStoreOr503(w)
	if !ok {
		return
	}
	p, _ := principalFrom(r.Context())
	t, secret, err := store.Rotate(chi.URLParam(r, "id"), p.actor())
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, TokenSecretResponse{Token: tokenView(t, time.Now()), Secret: secret})
}

func writeTokenMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, apitokens.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no token with that id")
		return
	}
	writeError(w, http.StatusInternalServerError, "token_update_failed", err.Error())
}
