package hub

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
)

// desktopRingCap bounds the desktop notifications a reconnecting app can
// catch up on.
const desktopRingCap = 100

// desktopChannel is the built-in desktop channel: it publishes routed
// notifications on GET /hub/api/v1/events as `event: notification`
// frames, which the Mac app shows as native notifications. The app does
// not route: every frame it receives is one the settings sent to desktop.
type desktopChannel struct {
	mu      sync.Mutex
	ring    []Notification
	head    int64
	changed chan struct{} // closed and replaced on publish
}

func newDesktopChannel() *desktopChannel { return &desktopChannel{changed: make(chan struct{})} }

// Name implements Channel.
func (*desktopChannel) Name() string { return ChannelDesktop }

// Send implements Channel: the notification gets the next stream id and
// wakes every connected stream. It never fails (no app connected means the
// notification waits in the ring for the next connect with notify_since).
func (d *desktopChannel) Send(_ context.Context, n Notification) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.head++
	n.ID = d.head
	n.Channel = ChannelDesktop
	d.ring = append(d.ring, n)
	if extra := len(d.ring) - desktopRingCap; extra > 0 {
		d.ring = append([]Notification(nil), d.ring[extra:]...)
	}
	close(d.changed)
	d.changed = make(chan struct{})
	return nil
}

// after returns the notifications after id and a channel closed on the
// next Send. An id ahead of the head (the hub restarted and its ids began
// again) reads from the start of the ring.
func (d *desktopChannel) after(id int64) ([]Notification, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id > d.head {
		id = 0
	}
	start := sort.Search(len(d.ring), func(i int) bool { return d.ring[i].ID > id })
	return append([]Notification(nil), d.ring[start:]...), d.changed
}

// headID is the newest stream id.
func (d *desktopChannel) headID() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.head
}

// setupChannels loads the notification settings and registers the
// channels: desktop and push (built in) and cfg.Channels, each with its
// notifier.
func (h *Hub) setupChannels(cfg Config) {
	h.notify = loadNotifySettings(filepath.Join(cfg.StateDir, NotificationsFileName))
	h.desktop = newDesktopChannel()
	h.channels = map[string]Channel{ChannelDesktop: h.desktop, ChannelPush: h.push}
	for _, ch := range cfg.Channels {
		if ch == nil || !slices.Contains(ChannelNames, ch.Name()) || h.channels[ch.Name()] != nil {
			slog.Warn("hub: notification channel skipped (unknown or duplicate name)")
			continue
		}
		h.channels[ch.Name()] = ch
	}
	for _, name := range ChannelNames {
		if ch, ok := h.channels[name]; ok {
			h.chanNotifiers = append(h.chanNotifiers, newChannelNotifier(ch, h.notify))
		}
	}
}

// notifySince is where a desktop stream starts: the notify_since query
// parameter, else the Last-Event-ID header (an SSE client's reconnect),
// else the current head (nothing old is replayed).
func (h *Hub) notifySince(r *http.Request) int64 {
	for _, v := range []string{r.URL.Query().Get("notify_since"), r.Header.Get("Last-Event-ID")} {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return h.desktop.headID()
}
