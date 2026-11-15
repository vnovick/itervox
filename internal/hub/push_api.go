package hub

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// The Web Push routes (#231). Subscribing and managing push devices is
// operator scope (a phone paired with the hub holds a device-scoped
// operator token, pairing.go); the VAPID public key is viewer.

// PushSubscribeRequest is the body of POST /hub/api/v1/push/subscribe:
// a device name and the browser's PushSubscription.toJSON().
type PushSubscribeRequest struct {
	Name         string           `json:"name"`
	Subscription PushSubscription `json:"subscription"`
}

// PushSubscription is a browser's push subscription (PushSubscription.toJSON).
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// maxPushSubscribeBody bounds the subscribe body.
const maxPushSubscribeBody = 8 << 10

// handlePushPublicKey serves GET /hub/api/v1/push/vapid-public-key: the
// application server key to subscribe with (generated on first use).
func (h *Hub) handlePushPublicKey(w http.ResponseWriter, _ *http.Request) {
	key, err := h.vapid.publicKey()
	if err != nil {
		slog.Warn("hub: push: no VAPID key", "error", err)
		writeError(w, http.StatusInternalServerError, "push_unavailable", "the hub could not load or create its push key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": key})
}

// handlePushSubscribe serves POST /hub/api/v1/push/subscribe (operator).
// The device is bound to the caller's hub token: revoking that token
// removes the device's pushes too.
func (h *Hub) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req PushSubscribeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPushSubscribeBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be {\"name\": ..., \"subscription\": PushSubscription.toJSON()}")
		return
	}
	name, err := deviceName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", err.Error())
		return
	}
	d, err := h.validSubscription(req.Subscription)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_subscription", err.Error())
		return
	}
	if d.ID, err = newPushDeviceID(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create a device id")
		return
	}
	d.Name, d.TokenID, d.Created = name, p.TokenID, time.Now().UTC()
	saved, err := h.push.store.upsert(d)
	switch {
	case errors.Is(err, errTooManyPushDevices):
		writeError(w, http.StatusConflict, "too_many_devices", "this hub already has the most push devices it keeps; revoke one first")
		return
	case err != nil:
		slog.Warn("hub: push: could not save a device", "error", err)
		writeError(w, http.StatusInternalServerError, "save_failed", "could not save the push device")
		return
	}
	slog.Info("hub: push: device subscribed", "device", saved.ID, "name", saved.Name, "service", saved.row().Service, "token_id", p.TokenID)
	writeJSON(w, http.StatusCreated, map[string]any{"device": saved.row()})
}

// validSubscription checks a browser subscription: an https endpoint on a
// known push service and a valid P-256 key and 16-byte auth secret.
func (h *Hub) validSubscription(s PushSubscription) (pushDevice, error) {
	if s.Endpoint == "" || len(s.Endpoint) > maxPushEndpointLen {
		return pushDevice{}, errors.New("the endpoint is missing or too long")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.User != nil || u.Host == "" || !h.push.allowed(u) {
		return pushDevice{}, errors.New("the endpoint is not an https URL of a known push service")
	}
	pub, err := decodeB64URL(s.Keys.P256dh)
	if err != nil || len(pub) != pushP256PointLen {
		return pushDevice{}, errors.New("keys.p256dh is not a P-256 public key")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return pushDevice{}, errors.New("keys.p256dh is not a P-256 public key")
	}
	auth, err := decodeB64URL(s.Keys.Auth)
	if err != nil || len(auth) != pushAuthSecretLen {
		return pushDevice{}, errors.New("keys.auth is not a 16-byte secret")
	}
	return pushDevice{Endpoint: s.Endpoint, P256dh: s.Keys.P256dh, Auth: s.Keys.Auth}, nil
}

// deviceName validates a device name: 1-48 printable characters.
func deviceName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxPushDeviceName {
		return "", errors.New("the device name must be 1-48 characters")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", errors.New("the device name must be printable")
		}
	}
	return name, nil
}

// handlePushDevices serves GET /hub/api/v1/push/devices (operator).
func (h *Hub) handlePushDevices(w http.ResponseWriter, _ *http.Request) {
	rows := []PushDeviceRow{}
	for _, d := range h.push.store.list() {
		rows = append(rows, d.row())
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows})
}

// handlePushRevoke serves DELETE /hub/api/v1/push/devices/{id} (operator).
func (h *Hub) handlePushRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	n, err := h.push.store.remove(func(d pushDevice) bool { return d.ID == id })
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, "save_failed", "could not save the push device list")
	case n == 0:
		writeError(w, http.StatusNotFound, "not_found", "no push device with that id")
	default:
		p, _ := principalFrom(r.Context())
		slog.Info("hub: push: device revoked", "device", id, "token_id", p.TokenID)
		w.WriteHeader(http.StatusNoContent)
	}
}
