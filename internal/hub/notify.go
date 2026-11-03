package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Channel delivers notifications to one place (desktop, Slack, Web Push).
// The hub wraps every registered Channel in a channel notifier — a
// Notifier named "notify-<channel>" with its own feed cursor — which asks
// the settings' Route whether an event goes to this channel, sends urgent
// kinds at once and batches the rest into a digest. A Channel therefore
// implements delivery only, never routing.
type Channel interface {
	// Name is one of ChannelNames.
	Name() string
	// Send delivers one notification (an event, a digest or a test). An
	// error retries an urgent notification (at-least-once, see Notifier)
	// and keeps a digest pending for the next interval.
	Send(ctx context.Context, n Notification) error
}

// Notification kinds that are not event kinds.
const (
	NotificationDigest = "digest"
	NotificationTest   = "test"
)

// Notification is what a Channel delivers. It is built from the event's
// kind, identifier, project and enum payload fields only: never agent
// output, prompt text or a command line.
type Notification struct {
	// ID orders a channel's own stream (the desktop SSE stream sets it).
	ID      int64  `json:"id,omitempty"`
	Channel string `json:"channel"`
	// Kind is a notification kind, NotificationDigest or NotificationTest.
	Kind       string `json:"kind"`
	Urgent     bool   `json:"urgent,omitempty"`
	Project    string `json:"project,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	// URL is the hub-relative dashboard link (/hub/p/{project}/?issue=ID).
	URL string `json:"url,omitempty"`
	// RunID is the run the event names (the feed payload's runId; an id,
	// never text), so an interactive channel (#230) can bind an answer to
	// the run that asked.
	RunID string `json:"runId,omitempty"`
	// Seq is the event's hub seq (0 for a digest or a test).
	Seq int64 `json:"seq,omitempty"`
	// Items are a digest's events.
	Items []Notification `json:"items,omitempty"`
	TS    time.Time      `json:"ts"`
}

// maxDigestPending bounds a channel's pending digest; the oldest item goes
// first when it is full.
const maxDigestPending = 200

// digestTick is how often a channel notifier checks whether its digest is due.
const digestTick = 5 * time.Second

// channelNotifier is the Notifier of one Channel.
type channelNotifier struct {
	ch       Channel
	settings *notifySettingsStore
	now      func() time.Time

	mu      sync.Mutex
	pending []Notification
	// windowStart is when the first pending item arrived: the digest goes
	// out one digest interval later.
	windowStart time.Time
}

func newChannelNotifier(ch Channel, settings *notifySettingsStore) *channelNotifier {
	return &channelNotifier{ch: ch, settings: settings, now: time.Now}
}

// channelNotifierPrefix names a channel notifier ("notify-desktop").
const channelNotifierPrefix = "notify-"

// Name implements Notifier.
func (c *channelNotifier) Name() string { return channelNotifierPrefix + c.ch.Name() }

// Enabled implements Notifier: whether any notification kind the feed kind
// maps to is routed to this channel now (the payload is checked in Notify).
func (c *channelNotifier) Enabled(project, feedKind string) bool {
	now := c.now()
	for _, k := range notificationKindsOfFeedKind(feedKind) {
		if slices.Contains(c.settings.route(project, k, now), c.ch.Name()) {
			return true
		}
	}
	return false
}

// Notify implements Notifier: an urgent kind is sent at once (an error
// retries it); any other kind joins the pending digest.
func (c *channelNotifier) Notify(ctx context.Context, ev Event) error {
	kind := NotificationKindOf(ev)
	if kind == "" || !slices.Contains(c.settings.route(ev.Project, kind, c.now()), c.ch.Name()) {
		return nil
	}
	n := eventNotification(c.ch.Name(), kind, ev)
	if n.Urgent {
		return c.ch.Send(ctx, n)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		c.windowStart = c.now()
	}
	if len(c.pending) >= maxDigestPending {
		c.pending = c.pending[1:]
	}
	c.pending = append(c.pending, n)
	return nil
}

// runDigest sends the digest whenever it is due, until ctx ends.
func (c *channelNotifier) runDigest(ctx context.Context) {
	t := time.NewTicker(digestTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.flushDue(ctx)
		}
	}
}

// flushDue sends the pending digest when one digest interval has passed
// since its first item. Items of a project snoozed meanwhile are dropped.
// A digest of one item is sent as that item. A failed send keeps the items
// for the next tick.
func (c *channelNotifier) flushDue(ctx context.Context) {
	now := c.now()
	c.mu.Lock()
	if len(c.pending) == 0 || now.Sub(c.windowStart) < c.settings.digestInterval() {
		c.mu.Unlock()
		return
	}
	items := c.pending[:0:0]
	for _, n := range c.pending {
		if !c.settings.snoozed(n.Project, now) {
			items = append(items, n)
		}
	}
	c.pending = nil
	c.mu.Unlock()
	if len(items) == 0 {
		return
	}
	n := items[0]
	if len(items) > 1 {
		n = digestNotification(c.ch.Name(), items, now)
	}
	sendCtx, cancel := context.WithTimeout(ctx, notifyTimeout)
	err := c.ch.Send(sendCtx, n)
	cancel()
	if err != nil {
		slog.Warn("hub: digest delivery failed; retrying at the next tick", "channel", c.ch.Name(), "items", len(items), "error", err)
		c.mu.Lock()
		c.pending = append(items, c.pending...)
		if extra := len(c.pending) - maxDigestPending; extra > 0 {
			c.pending = c.pending[extra:]
		}
		c.windowStart = now.Add(-c.settings.digestInterval())
		c.mu.Unlock()
	}
}

// eventNotification builds the notification of one merged-feed event.
func eventNotification(channel, kind string, ev Event) Notification {
	who := ev.Identifier
	n := Notification{
		Channel: channel, Kind: kind, Urgent: IsUrgentKind(kind), Project: ev.Project,
		Identifier: ev.Identifier, Body: ev.Project, URL: projectLink(ev.Project, ev.Identifier),
		RunID: payloadWord(ev.Payload, "runId"), Seq: ev.Seq, TS: ev.TS,
	}
	switch kind {
	case KindInputRequired:
		n.Title = who + " needs input"
	case KindGuardrailApproval:
		n.Title = who + " needs approval"
	case KindRunFailed:
		n.Title = who + " run failed"
	case KindReviewVerdict:
		n.Title = strings.TrimSpace(who + " review " + payloadWord(ev.Payload, "outcome", "verdict"))
	case KindPRMerged:
		n.Title = who + " PR merged"
	case KindLessonProposed:
		n.Title = strings.TrimSpace("Lesson proposed " + who)
	case KindDispatchPaused:
		n.Title = "Dispatch paused"
	}
	n.Title = strings.TrimSpace(n.Title)
	return n
}

// digestNotification batches items into one notification.
func digestNotification(channel string, items []Notification, now time.Time) Notification {
	projects := map[string]bool{}
	for _, it := range items {
		projects[it.Project] = true
	}
	n := Notification{Channel: channel, Kind: NotificationDigest, Items: items, TS: now,
		Title: fmt.Sprintf("%d updates", len(items))}
	if len(projects) == 1 {
		n.Project = items[0].Project
		n.Body = items[0].Project
		n.URL = projectLink(n.Project, "")
	} else {
		n.Body = fmt.Sprintf("across %d projects", len(projects))
		n.URL = "/hub/"
	}
	return n
}

// projectLink is the hub dashboard link of a project (and issue).
func projectLink(project, identifier string) string {
	if project == "" {
		return "/hub/"
	}
	link := "/hub/p/" + url.PathEscape(project) + "/"
	if identifier != "" {
		link += "?issue=" + url.QueryEscape(identifier)
	}
	return link
}

// payloadWord returns the first of keys that holds a short enum word in a
// feed payload (letters, digits, '_' and '-' only; anything else is left
// out, so free text never reaches a notification).
func payloadWord(payload []byte, keys ...string) string {
	var p map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil {
		return ""
	}
	for _, k := range keys {
		s, _ := p[k].(string)
		if s != "" && len(s) <= 40 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == "" {
			return s
		}
	}
	return ""
}
