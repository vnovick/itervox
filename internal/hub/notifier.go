package hub

import (
	"context"
	"log/slog"
	"time"
)

// Notifier receives the merged feed (#228); the seam that notification
// channels (#229 desktop, #230 Slack, #231 Web Push) plug into.
//
// Delivery is at least once: the hub calls Notify for each event Enabled
// accepts, in hub order, and retries a failed call with backoff (doubling
// from Config.NotifierBackoff to a minute) until it succeeds. A
// per-notifier, per-member cursor persisted in hub_feed_cursors.json moves
// only after a success, so an event in flight when the hub stops is
// delivered again by the next hub. A Notifier must therefore tolerate a
// repeat (Event.Workflow plus Event.MemberSeq identifies an event).
//
// Notify runs on the notifier's own goroutine, so a slow notifier delays
// only itself. Name must be unique and stable: it keys the cursor.
type Notifier interface {
	Name() string
	// Enabled reports whether the notifier wants events of kind from
	// project. A false answer skips the event for good.
	Enabled(project, kind string) bool
	// Notify delivers one event. An error is retried.
	Notify(ctx context.Context, ev Event) error
}

// notifyTimeout bounds one Notify call.
const notifyTimeout = 30 * time.Second

// maxNotifierBackoff caps the retry delay of a failing notifier.
const maxNotifierBackoff = time.Minute

// LogNotifier logs every merged event at debug level: the v1 notifier, for
// tests and debugging (`itervox --verbose` shows them).
type LogNotifier struct {
	Logger *slog.Logger // nil: slog.Default()
}

// Name implements Notifier.
func (LogNotifier) Name() string { return "log" }

// Enabled implements Notifier: every event.
func (LogNotifier) Enabled(string, string) bool { return true }

// Notify implements Notifier.
func (n LogNotifier) Notify(ctx context.Context, ev Event) error {
	l := n.Logger
	if l == nil {
		l = slog.Default()
	}
	l.DebugContext(ctx, "hub: event", "seq", ev.Seq, "project", ev.Project, "kind", ev.Kind,
		"identifier", ev.Identifier, "member_seq", ev.MemberSeq)
	return nil
}

// notifierNames lists the configured notifiers' names.
func (h *Hub) notifierNames() []string {
	out := make([]string, 0, len(h.notifiers))
	for _, n := range h.notifiers {
		out = append(out, n.Name())
	}
	return out
}

// runNotifier delivers the merged feed to n until ctx ends.
func (h *Hub) runNotifier(ctx context.Context, n Notifier) {
	name := n.Name()
	var pos int64
	for {
		evs, skipped, wait := h.feed.after(pos)
		if skipped > 0 && pos > 0 {
			slog.Warn("hub: a notifier fell behind the merged feed; events were skipped", "notifier", name, "skipped", skipped)
		}
		for _, ev := range evs {
			if done, ok := h.cursors.notifier(name, ev.Workflow); !ok || ev.MemberSeq > done {
				if n.Enabled(ev.Project, ev.Kind) && !h.deliver(ctx, n, ev) {
					return // ctx ended mid-retry; the cursor did not move
				}
				h.cursors.setNotifier(name, ev.Workflow, ev.MemberSeq)
			}
			pos = ev.Seq
		}
		select {
		case <-ctx.Done():
			return
		case <-wait:
		}
	}
}

// deliver calls Notify until it succeeds; false when ctx ended first.
func (h *Hub) deliver(ctx context.Context, n Notifier, ev Event) bool {
	backoff := h.cfg.NotifierBackoff
	for {
		callCtx, cancel := context.WithTimeout(ctx, notifyTimeout)
		err := n.Notify(callCtx, ev)
		cancel()
		if err == nil {
			return true
		}
		slog.Warn("hub: notifier failed; retrying", "notifier", n.Name(), "seq", ev.Seq, "project", ev.Project,
			"kind", ev.Kind, "retry_in", backoff.String(), "error", err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxNotifierBackoff)
	}
}
