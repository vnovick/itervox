package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Event is one event of the merged feed: a member's #213 feed event plus
// the project it came from and its place in the hub's own sequence.
type Event struct {
	// Seq is the hub sequence number: it orders the merged feed and only
	// grows, across hub restarts and failovers too (it is persisted).
	Seq int64 `json:"seq"`
	// Project is the member's project name (the /hub/p/{project} segment).
	Project string `json:"project"`
	// Workflow is the member's WORKFLOW.md path, the stable member key.
	Workflow string `json:"workflow"`
	// MemberSeq is the event's seq in the member's own feed.
	MemberSeq  int64     `json:"memberSeq"`
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	Identifier string    `json:"identifier,omitempty"`
	// Payload is the member's redacted feed payload, passed through as is.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// FeedPage is the response of GET /hub/api/v1/feed, the same shape as a
// member's GET /api/v1/feed.
type FeedPage struct {
	Events    []Event `json:"events"`
	NextSeq   int64   `json:"nextSeq"`
	HeadSeq   int64   `json:"headSeq"`
	OldestSeq int64   `json:"oldestSeq"`
	Truncated bool    `json:"truncated"`
	HasMore   bool    `json:"hasMore"`
}

// memberFeedEvent / memberFeedPage decode a member's GET /api/v1/feed.
type memberFeedEvent struct {
	Seq        int64           `json:"seq"`
	TS         time.Time       `json:"ts"`
	Kind       string          `json:"kind"`
	Identifier string          `json:"identifier"`
	Payload    json.RawMessage `json:"payload"`
}

type memberFeedPage struct {
	Events    []memberFeedEvent `json:"events"`
	NextSeq   int64             `json:"nextSeq"`
	HeadSeq   int64             `json:"headSeq"`
	Truncated bool              `json:"truncated"`
	HasMore   bool              `json:"hasMore"`
}

// Merged feed bounds.
const (
	feedCap          = 5000
	feedMaxLimit     = 1000
	feedDefaultLimit = 100
	// feedPagesPerPoll bounds one member's catch-up in one poll round.
	feedPagesPerPoll = 20
)

// feed is the merged feed ring (in memory; cursors make it resumable).
type feed struct {
	mu      sync.Mutex
	ring    []Event
	head    int64
	changed chan struct{} // closed and replaced on append
}

func newFeed(head int64) *feed { return &feed{head: head, changed: make(chan struct{})} }

// append gives evs the next hub seqs and wakes every waiter.
func (f *feed) append(evs []Event) {
	if len(evs) == 0 {
		return
	}
	f.mu.Lock()
	for i := range evs {
		f.head++
		evs[i].Seq = f.head
	}
	f.ring = append(f.ring, evs...)
	if n := len(f.ring) - feedCap; n > 0 {
		f.ring = append([]Event(nil), f.ring[n:]...)
	}
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

// headSeq is the newest hub seq.
func (f *feed) headSeq() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head
}

// after returns the events after pos and a channel closed on the next
// append; skipped counts events after pos that already left the ring.
func (f *feed) after(pos int64) (evs []Event, skipped int64, wait <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := sort.Search(len(f.ring), func(i int) bool { return f.ring[i].Seq > pos })
	if len(f.ring) > 0 && f.ring[0].Seq > pos+1 {
		skipped = f.ring[0].Seq - pos - 1
	}
	return append([]Event(nil), f.ring[start:]...), skipped, f.changed
}

// page is the cursor read of GET /hub/api/v1/feed (same rules as a member's).
func (f *feed) page(since int64, limit int) FeedPage {
	if limit <= 0 {
		limit = feedDefaultLimit
	}
	limit = min(limit, feedMaxLimit)
	since = max(since, 0)
	f.mu.Lock()
	defer f.mu.Unlock()
	p := FeedPage{Events: []Event{}, HeadSeq: f.head}
	if len(f.ring) > 0 {
		p.OldestSeq = f.ring[0].Seq
	}
	switch {
	case since > f.head:
		p.Truncated = true
	case len(f.ring) == 0:
		p.Truncated = since < f.head
	default:
		p.Truncated = since < p.OldestSeq-1
	}
	start := sort.Search(len(f.ring), func(i int) bool { return f.ring[i].Seq > since })
	end := min(start+limit, len(f.ring))
	p.Events = append(p.Events, f.ring[start:end]...)
	p.HasMore = end < len(f.ring)
	p.NextSeq = min(since, f.head)
	if n := len(p.Events); n > 0 {
		p.NextSeq = p.Events[n-1].Seq
	}
	return p
}

// pollFeeds reads every member's feed each FeedPoll until ctx ends.
func (h *Hub) pollFeeds(ctx context.Context) {
	t := time.NewTicker(h.cfg.FeedPoll)
	defer t.Stop()
	caughtUp := map[string]bool{}
	for {
		for _, m := range h.members.list() {
			if h.pollMember(ctx, m, !caughtUp[m.rec.Workflow]) {
				caughtUp[m.rec.Workflow] = true
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pollMember appends a member's new feed events to the merged feed and
// moves its cursor. On the first poll of a run it reads from the lowest of
// the member's feed cursor and its notifier cursors, so an event a notifier
// had not delivered before a restart is delivered after it. A member the
// hub has never seen starts at the events of its current process: older
// history is not news. It reports whether the poll succeeded.
func (h *Hub) pollMember(ctx context.Context, m memberState, firstOfRun bool) bool {
	wf := m.rec.Workflow
	cursor, known := h.cursors.member(wf)
	since := cursor
	if known && firstOfRun {
		since = h.cursors.lowestNotifier(wf, cursor)
	}
	if !known {
		since = 0
	}
	notBefore := m.rec.startedAt()
	fresh := !known
	for range feedPagesPerPoll {
		rep, err := h.client.get(ctx, m.rec, apitokens.ScopeViewer, "/api/v1/feed?limit="+strconv.Itoa(feedMaxLimit)+"&since="+strconv.FormatInt(since, 10), nil)
		if err != nil || rep.status != http.StatusOK {
			if err == nil {
				slog.Debug("hub: member feed read failed", "project", m.name, "status", rep.status)
			}
			return false
		}
		var page memberFeedPage
		if err := json.Unmarshal(rep.body, &page); err != nil {
			slog.Debug("hub: member feed does not parse", "project", m.name, "error", err)
			return false
		}
		if page.HeadSeq < since {
			// The member's feed restarted below our cursor (its feed.jsonl was
			// removed): follow it, notifier cursors included.
			slog.Info("hub: member feed restarted; following it", "project", m.name, "cursor", since, "head", page.HeadSeq)
			h.cursors.resetMember(wf, page.NextSeq)
			return true
		}
		evs := make([]Event, 0, len(page.Events))
		base := since
		for _, e := range page.Events {
			if fresh && e.TS.Before(notBefore) {
				base = e.Seq // history from before this member process
				continue
			}
			evs = append(evs, Event{Project: m.name, Workflow: wf, MemberSeq: e.Seq, TS: e.TS, Kind: e.Kind, Identifier: e.Identifier, Payload: e.Payload})
		}
		if !known {
			h.cursors.initMember(wf, base, h.notifierNames())
			known = true
		}
		h.feed.append(evs)
		h.cursors.setMember(wf, page.NextSeq, h.feed.headSeq())
		since = page.NextSeq
		if !page.HasMore {
			break
		}
	}
	return true
}
