package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// Sync status (#117): when the board last heard from the tracker and what is
// still waiting to be written back. Everything here is read through the
// producers' own accessors (the store's and outbox's mutex-guarded reads, the
// orchestrator's readiness atomics) at snapshot time; nothing touches
// orchestrator.State.

// syncLagWarnIntervals and syncLagWarnFloor set when the sync lag is stale:
// three missed pull intervals, and never less than five minutes, so a short
// interval or one slow pull does not raise a warning. Derived rather than
// configured: it follows tracker.store.sync_interval_ms (or the webhook
// safety net, or polling.interval_ms without a store) on its own.
const (
	syncLagWarnIntervals = 3
	syncLagWarnFloor     = 5 * time.Minute
)

// syncLagWarnAfter is the stale threshold for a pull interval.
func syncLagWarnAfter(interval time.Duration) time.Duration {
	return max(syncLagWarnIntervals*interval, syncLagWarnFloor)
}

// storeSyncStatus is what the tracker store (#113) reports; the store and its
// Linear wrapper both satisfy it.
type storeSyncStatus interface {
	SyncStatus() store.SyncStatus
}

// syncStatusSource assembles server.SyncStatusRow from the live producers.
type syncStatusSource struct {
	store        storeSyncStatus // nil without a tracker store
	localSync    localSyncStatus // nil without tracker.sync (#118)
	outbox       *outbox.Outbox  // nil-safe
	pollOKAt     func() time.Time
	pollInterval time.Duration
	pushInterval time.Duration
}

func newSyncStatusSource(cfg *config.Config, tr tracker.Tracker, ob *outbox.Outbox, orch *orchestrator.Orchestrator) *syncStatusSource {
	src := &syncStatusSource{
		outbox:       ob,
		pollInterval: time.Duration(cfg.Polling.IntervalMs) * time.Millisecond,
	}
	if st, ok := tr.(storeSyncStatus); ok {
		src.store = st
	}
	if ls, ok := tr.(localSyncStatus); ok {
		src.localSync = ls
	}
	if orch != nil {
		src.pollOKAt = func() time.Time { return orch.Readiness().LastPollOKAt }
	}
	if cfg.Tracker.Outbox {
		src.pushInterval = time.Duration(cfg.Tracker.PushIntervalMs) * time.Millisecond
	}
	return src
}

// row returns the sync status at now.
func (s *syncStatusSource) row(now time.Time) *server.SyncStatusRow {
	var (
		row      = &server.SyncStatusRow{Mode: server.SyncModeDirect}
		lastPull time.Time
		interval = s.pollInterval
	)
	var syncOutbox *outbox.Outbox
	if s.localSync != nil {
		// #118: the local files synced with Linear or GitHub. The pull
		// side is the sync's last good cycle; the push side adds the
		// sync's queued remote writes to the orchestrator's.
		st, ob := s.localSync.LocalSyncStatus()
		row.Mode = server.SyncModeLocalSync
		lastPull, interval, syncOutbox = st.SyncedAt, st.Interval, ob
		row.CursorAt = nilIfZero(st.Cursor)
		row.ConsecutiveFailures = st.Failures
		if st.LastErr != nil {
			class := tracker.ClassifyError(st.LastErr)
			row.LastError = &server.SyncErrorRow{At: st.LastErrAt, Kind: class.Kind, Message: logging.RedactString(class.Summary)}
		}
	} else if s.store != nil {
		st := s.store.SyncStatus()
		row.Mode = server.SyncModeStore
		lastPull, interval = st.SyncedAt, st.Interval
		row.CursorAt = nilIfZero(st.Cursor)
		row.ConsecutiveFailures = st.Failures
		if st.LastErr != nil {
			class := tracker.ClassifyError(st.LastErr)
			row.LastError = &server.SyncErrorRow{At: st.LastErrAt, Kind: class.Kind, Message: logging.RedactString(class.Summary)}
		}
	} else if s.pollOKAt != nil {
		lastPull = s.pollOKAt()
	}
	warnAfter := syncLagWarnAfter(interval)
	row.SyncIntervalMs = interval.Milliseconds()
	row.LagWarnAfterMs = warnAfter.Milliseconds()
	if !lastPull.IsZero() {
		lag := max(now.Sub(lastPull), 0)
		lagMs := lag.Milliseconds()
		row.LastPullAt, row.LagMs = &lastPull, &lagMs
		row.Stale = lag > warnAfter
	} else {
		row.Stale = row.LastError != nil
	}

	row.PushIntervalMs = s.pushInterval.Milliseconds()
	if s.outbox != nil || syncOutbox != nil {
		st := addOutboxStats(outboxStats(s.outbox, now), outboxStats(syncOutbox, now))
		row.PendingWrites, row.PendingUrgent, row.PendingBatched = st.Pending, st.Urgent, st.Batched
		row.PendingRetrying, row.PendingDegraded, row.PendingRateLimited = st.Retrying, st.Degraded, st.RateLimited
		row.OldestPendingAt, row.NextPushAt = nilIfZero(st.OldestEnqueuedAt), nilIfZero(st.NextPushAt)
	}
	return row
}

// outboxStats is ob's queue summary; nil reads as empty.
func outboxStats(ob *outbox.Outbox, now time.Time) outbox.Stats {
	if ob == nil {
		return outbox.Stats{}
	}
	return ob.Stats(now)
}

// addOutboxStats sums two queue summaries: the counts add up, the oldest
// write and the next push are the earlier of the two.
func addOutboxStats(a, b outbox.Stats) outbox.Stats {
	earlier := func(x, y time.Time) time.Time {
		if x.IsZero() || (!y.IsZero() && y.Before(x)) {
			return y
		}
		return x
	}
	return outbox.Stats{
		Pending: a.Pending + b.Pending, Urgent: a.Urgent + b.Urgent, Batched: a.Batched + b.Batched,
		Retrying: a.Retrying + b.Retrying, Degraded: a.Degraded + b.Degraded, RateLimited: a.RateLimited + b.RateLimited,
		OldestEnqueuedAt: earlier(a.OldestEnqueuedAt, b.OldestEnqueuedAt),
		NextPushAt:       earlier(a.NextPushAt, b.NextPushAt),
	}
}

// fingerprint changes whenever the HEARTBEAT's sync section would say
// something new, except the lag itself (which changes every instant): the
// heartbeat writer rewrites the file when it changes, so `itervox doctor`
// reads a recent last-pull time even on an otherwise idle daemon.
func (s *syncStatusSource) fingerprint() string {
	r := s.row(time.Now())
	var errKey string
	if r.LastError != nil {
		errKey = r.LastError.Kind + "@" + r.LastError.At.String()
	}
	return fmt.Sprintf("%s|%v|%s|%d|%d|%d|%d|%s",
		timeKey(r.LastPullAt), r.Stale, errKey, r.PendingWrites, r.PendingRetrying, r.PendingDegraded, r.PendingUrgent, timeKey(r.NextPushAt))
}

func timeKey(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// HEARTBEAT.md "## Sync" section (#117). writeHeartbeatSync and
// parseHeartbeatSync are the two halves of one format: `itervox doctor`
// reads the daemon's sync status from the heartbeat, because it needs no API
// token. Times are absolute, so doctor computes the lag at its own now.
const (
	hbSyncHeader    = "## Sync"
	hbSyncMode      = "- Mode: "
	hbSyncLastPull  = "- Last pull: "
	hbSyncLag       = "- Lag: "
	hbSyncWarnAfter = "- Lag warn after: "
	hbSyncPending   = "- Pending writes: "
	hbSyncNextPush  = "- Next push: "
	hbSyncLastError = "- Last sync error: "
)

func writeHeartbeatSync(b *strings.Builder, r *server.SyncStatusRow) {
	if r == nil {
		return
	}
	b.WriteString("\n" + hbSyncHeader + "\n")
	b.WriteString(hbSyncMode + r.Mode + "\n")
	if r.LastPullAt != nil {
		b.WriteString(hbSyncLastPull + r.LastPullAt.UTC().Format(time.RFC3339) + "\n")
	} else {
		b.WriteString(hbSyncLastPull + "never\n")
	}
	if r.LagMs != nil {
		lag := (time.Duration(*r.LagMs) * time.Millisecond).Round(time.Second)
		stale := ""
		if r.Stale {
			stale = " (stale)"
		}
		b.WriteString(hbSyncLag + lag.String() + stale + "\n")
	}
	b.WriteString(hbSyncWarnAfter + (time.Duration(r.LagWarnAfterMs) * time.Millisecond).String() + "\n")
	fmt.Fprintf(b, "%s%d (urgent %d, batched %d, retrying %d, degraded %d, rate limited %d)\n", hbSyncPending,
		r.PendingWrites, r.PendingUrgent, r.PendingBatched, r.PendingRetrying, r.PendingDegraded, r.PendingRateLimited)
	if r.PendingWrites > 0 && r.NextPushAt != nil {
		b.WriteString(hbSyncNextPush + r.NextPushAt.UTC().Format(time.RFC3339) + "\n")
	}
	if e := r.LastError; e != nil {
		fmt.Fprintf(b, "%s%s at %s: %s\n", hbSyncLastError, e.Kind, e.At.UTC().Format(time.RFC3339), e.Message)
	}
}

// heartbeatSync is the "## Sync" section as doctor reads it back.
type heartbeatSync struct {
	Mode       string
	LastPullAt time.Time // zero: never
	WarnAfter  time.Duration
	Pending    string // the "Pending writes" line's value, verbatim
	NextPush   string
	LastError  string
}

// parseHeartbeatSync reads the "## Sync" section; ok is false when the
// heartbeat has none (a daemon predating #117, or a snapshot without one).
func parseHeartbeatSync(content string) (hs heartbeatSync, ok bool) {
	in := false
	for line := range strings.Lines(content) {
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "## ") {
			in = line == hbSyncHeader
			ok = ok || in
			continue
		}
		if !in {
			continue
		}
		switch {
		case strings.HasPrefix(line, hbSyncMode):
			hs.Mode = strings.TrimPrefix(line, hbSyncMode)
		case strings.HasPrefix(line, hbSyncLastPull):
			hs.LastPullAt, _ = time.Parse(time.RFC3339, strings.TrimPrefix(line, hbSyncLastPull))
		case strings.HasPrefix(line, hbSyncWarnAfter):
			hs.WarnAfter, _ = time.ParseDuration(strings.TrimPrefix(line, hbSyncWarnAfter))
		case strings.HasPrefix(line, hbSyncPending):
			hs.Pending = strings.TrimPrefix(line, hbSyncPending)
		case strings.HasPrefix(line, hbSyncNextPush):
			hs.NextPush = strings.TrimPrefix(line, hbSyncNextPush)
		case strings.HasPrefix(line, hbSyncLastError):
			hs.LastError = strings.TrimPrefix(line, hbSyncLastError)
		}
	}
	return hs, ok
}
