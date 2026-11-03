package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Phone pairing (#231). An admin on the hub page asks for a pairing code
// (POST /hub/api/v1/pairing) and shows it as a QR code linking to
// /hub/pair#code=...; the phone exchanges the code (POST
// /hub/api/v1/pairing/exchange, the one unauthenticated write) for its own
// operator token in the hub token store, named "device: <name>". A code is
// 160 random bits, works once and expires after PairingCodeTTL; the hub
// keeps only its hash, in memory. GET /hub/api/v1/devices lists the paired
// devices and DELETE /hub/api/v1/devices/{id} revokes one: its token stops
// working at once and its push subscriptions go with it.

// PairingCodeTTL is how long a pairing code can be exchanged.
const PairingCodeTTL = 10 * time.Minute

// DeviceTokenPrefix names the hub tokens pairing creates.
const DeviceTokenPrefix = "device: "

// Pairing limits.
const (
	maxPendingPairings = 20
	// maxPairingFailures is how many wrong codes the exchange takes per
	// pairingFailureWindow before it answers 429 until the window ends.
	maxPairingFailures   = 10
	pairingFailureWindow = time.Minute
	maxPairingBody       = 4 << 10
)

// pairingCodes holds the pending codes' hashes and their expiry.
type pairingCodes struct {
	now func() time.Time

	mu       sync.Mutex
	pending  map[string]time.Time
	failures []time.Time
}

func newPairingCodes() *pairingCodes {
	return &pairingCodes{now: time.Now, pending: map[string]time.Time{}}
}

func pairingHash(code string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}

var errTooManyPairings = errors.New("hub: too many pairing codes are pending; wait for one to expire")

// issue creates a code valid for PairingCodeTTL.
func (p *pairingCodes) issue() (code string, expires time.Time, err error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	code = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.pruneLocked(now)
	if len(p.pending) >= maxPendingPairings {
		return "", time.Time{}, errTooManyPairings
	}
	expires = now.Add(PairingCodeTTL)
	p.pending[pairingHash(code)] = expires
	return code, expires, nil
}

func (p *pairingCodes) pruneLocked(now time.Time) {
	for h, exp := range p.pending {
		if !now.Before(exp) {
			delete(p.pending, h)
		}
	}
}

// throttled reports whether the exchange is refusing for now (too many
// wrong codes in the window).
func (p *pairingCodes) throttled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := p.now().Add(-pairingFailureWindow)
	p.failures = slices.DeleteFunc(p.failures, func(t time.Time) bool { return t.Before(cutoff) })
	return len(p.failures) >= maxPairingFailures
}

// redeem consumes code: true once for a pending, unexpired code.
func (p *pairingCodes) redeem(code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	h := pairingHash(code)
	exp, ok := p.pending[h]
	delete(p.pending, h)
	if !ok || !now.Before(exp) {
		p.failures = append(p.failures, now)
		return false
	}
	return true
}

// PairingResponse is the body of POST /hub/api/v1/pairing.
type PairingResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Path is the hub-relative pairing link (the code in the fragment, so it
	// never reaches a server log or a Referer).
	Path string `json:"path"`
}

// PairingExchangeRequest is the body of POST /hub/api/v1/pairing/exchange.
type PairingExchangeRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// DeviceRow is one paired device in GET /hub/api/v1/devices.
type DeviceRow struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	// PushDevices counts the push subscriptions made with its token.
	PushDevices int `json:"pushDevices"`
}

// handleCreatePairing serves POST /hub/api/v1/pairing (admin).
func (h *Hub) handleCreatePairing(w http.ResponseWriter, r *http.Request) {
	code, expires, err := h.pairing.issue()
	if errors.Is(err, errTooManyPairings) {
		writeError(w, http.StatusTooManyRequests, "too_many_pairings", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create a pairing code")
		return
	}
	p, _ := principalFrom(r.Context())
	slog.Info("hub: pairing code issued", "token_id", p.TokenID, "expires", expires.UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, PairingResponse{Code: code, ExpiresAt: expires.UTC(), Path: "/hub/pair#code=" + code})
}

// handlePairingExchange serves POST /hub/api/v1/pairing/exchange (public):
// a valid code becomes the device's operator token, returned once.
func (h *Hub) handlePairingExchange(w http.ResponseWriter, r *http.Request) {
	if h.pairing.throttled() {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many wrong pairing codes; try again in a minute")
		return
	}
	var req PairingExchangeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPairingBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be {\"code\": ..., \"name\": ...}")
		return
	}
	name, err := deviceName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", err.Error())
		return
	}
	if req.Code == "" || !h.pairing.redeem(req.Code) {
		writeError(w, http.StatusBadRequest, "invalid_code", "the pairing code is wrong, used or expired; show a new QR code on the hub page")
		return
	}
	tok, secret, err := h.tokens.Create(DeviceTokenPrefix+name, apitokens.ScopeOperator, nil, "hub pairing")
	if err != nil {
		slog.Warn("hub: pairing: could not create the device token", "error", err)
		writeError(w, http.StatusInternalServerError, "token_failed", "could not create the device token")
		return
	}
	slog.Info("hub: device paired", "token_id", tok.ID, "name", name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":  secret,
		"device": DeviceRow{ID: tok.ID, Name: name, Created: tok.Created},
	})
}

// handleDevices serves GET /hub/api/v1/devices (admin).
func (h *Hub) handleDevices(w http.ResponseWriter, _ *http.Request) {
	tokens, err := h.tokens.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_store", "the hub token store cannot be read")
		return
	}
	pushes := map[string]int{}
	for _, d := range h.push.store.list() {
		pushes[d.TokenID]++
	}
	rows := []DeviceRow{}
	for _, t := range tokens {
		name, ok := strings.CutPrefix(t.Name, DeviceTokenPrefix)
		if !ok {
			continue
		}
		rows = append(rows, DeviceRow{ID: t.ID, Name: name, Created: t.Created, LastUsed: t.LastUsed, PushDevices: pushes[t.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows})
}

// handleRevokeDevice serves DELETE /hub/api/v1/devices/{id} (admin): the
// device's token is revoked and its push subscriptions removed. Only a
// paired device's token can be revoked here.
func (h *Hub) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tokens, err := h.tokens.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_store", "the hub token store cannot be read")
		return
	}
	if !slices.ContainsFunc(tokens, func(t apitokens.Token) bool { return t.ID == id && strings.HasPrefix(t.Name, DeviceTokenPrefix) }) {
		writeError(w, http.StatusNotFound, "not_found", "no paired device with that id")
		return
	}
	p, _ := principalFrom(r.Context())
	if _, err := h.tokens.Revoke(id, "hub device revoke by "+p.TokenID); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke_failed", "could not revoke the device token")
		return
	}
	removed, _ := h.push.store.remove(func(d pushDevice) bool { return d.TokenID == id })
	slog.Info("hub: device revoked", "token_id", id, "push_devices_removed", removed)
	w.WriteHeader(http.StatusNoContent)
}
