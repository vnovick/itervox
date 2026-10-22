package hub

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

// NotificationKindInfo describes one kind in GET /hub/api/v1/notifications.
type NotificationKindInfo struct {
	Name   string `json:"name"`
	Urgent bool   `json:"urgent"`
}

// NotificationChannelInfo describes one channel: Available is false until
// the channel is set up (a registered Channel; desktop always is).
type NotificationChannelInfo struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// NotificationsResponse is the body of GET and PUT /hub/api/v1/notifications.
type NotificationsResponse struct {
	Settings NotificationSettings      `json:"settings"`
	Kinds    []NotificationKindInfo    `json:"kinds"`
	Channels []NotificationChannelInfo `json:"channels"`
	// Projects are the current members' names, for the settings matrix.
	Projects []string `json:"projects"`
}

// NotificationTestRequest is the body of POST /hub/api/v1/notifications/test.
type NotificationTestRequest struct {
	Channel string `json:"channel"`
}

func (h *Hub) notificationsResponse(s NotificationSettings) NotificationsResponse {
	resp := NotificationsResponse{Settings: s, Projects: []string{}}
	for _, k := range NotificationKinds {
		resp.Kinds = append(resp.Kinds, NotificationKindInfo{Name: k, Urgent: IsUrgentKind(k)})
	}
	for _, ch := range ChannelNames {
		_, ok := h.channels[ch]
		resp.Channels = append(resp.Channels, NotificationChannelInfo{Name: ch, Available: ok})
	}
	for _, m := range h.members.list() {
		resp.Projects = append(resp.Projects, m.name)
	}
	slices.Sort(resp.Projects)
	return resp
}

// handleGetNotifications serves GET /hub/api/v1/notifications (admin).
func (h *Hub) handleGetNotifications(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.notificationsResponse(h.notify.get()))
}

// handlePutNotifications serves PUT /hub/api/v1/notifications (admin): the
// whole settings document, validated, saved 0600 and applied at once.
func (h *Hub) handlePutNotifications(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxNotificationsFileBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read the body")
		return
	}
	parsed, err := ParseNotificationSettings(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	saved, err := h.notify.put(parsed)
	if err != nil {
		slog.Warn("hub: could not save the notification settings", "error", err)
		writeError(w, http.StatusInternalServerError, "save_failed", "could not save the notification settings")
		return
	}
	writeJSON(w, http.StatusOK, h.notificationsResponse(saved))
}

// handleTestNotification serves POST /hub/api/v1/notifications/test
// (operator): one test notification through the named channel, whatever
// the routes and snoozes say.
func (h *Hub) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var req NotificationTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be {\"channel\": \"push|slack|desktop\"}")
		return
	}
	if !slices.Contains(ChannelNames, req.Channel) {
		writeError(w, http.StatusBadRequest, "unknown_channel", "channel must be one of push, slack, desktop")
		return
	}
	ch, ok := h.channels[req.Channel]
	if !ok {
		writeError(w, http.StatusConflict, "channel_unavailable", "the "+req.Channel+" channel is not set up on this hub")
		return
	}
	n := Notification{
		Channel: req.Channel, Kind: NotificationTest, Title: "Itervox test notification",
		Body: "Sent from the hub's notification settings", URL: "/hub/", TS: time.Now().UTC(),
	}
	if err := ch.Send(r.Context(), n); err != nil {
		slog.Warn("hub: test notification failed", "channel", req.Channel, "error", err)
		writeError(w, http.StatusBadGateway, "send_failed", "the "+req.Channel+" channel could not send the test notification")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "channel": req.Channel})
}

// annotateChannels sets each event's notification kind and the channels
// the settings route it to now, so a client (the Mac app) needs no
// routing of its own.
func (h *Hub) annotateChannels(evs []Event) {
	now := time.Now()
	for i := range evs {
		k := NotificationKindOf(evs[i])
		if k == "" {
			continue
		}
		evs[i].NotifyKind = k
		evs[i].Channels = h.notify.route(evs[i].Project, k, now)
	}
}
