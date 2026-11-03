// Package localsync is the two-way sync between the local issue files
// (tracker.kind: local, internal/tracker/local) and a Linear project or
// GitHub repository (#118, tracker.sync).
//
// A Syncer runs in its own goroutine, off the orchestrator's event loop. It
// talks to the local tracker (through its sync surface, local/sync.go) and
// to the remote tracker only; it never touches orchestrator.State. A cycle:
//
//   - pulls the remote issues that changed since the last cycle (the
//     incremental changes listing, #114, between periodic full pulls);
//   - creates a remote issue for each new local file, and writes the
//     remote identifier into the file's front matter (`remote: ENG-42`);
//   - pulls each new remote issue into a new local file;
//   - for each linked issue, compares title, description, state, labels,
//     priority and blockers on both sides with the value both last agreed
//     on (the sync ledger, ledger.go) and carries a change across; when
//     both sides changed a field, the more recent edit wins and the
//     conflict is recorded as a comment in the file;
//   - copies new comments both ways.
//
// Writes to the remote go through the sync's own write-ahead outbox (with
// the coalesced push of #115), delivered by the same goroutine (push.go).
// The sync's own writes do not come back as edits: a written value becomes
// the field's agreed value at once, a field with a write still queued is
// not compared, and the comments it posted are recognised by their
// idempotency keys.
package localsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// Defaults for Config.
const (
	DefaultInterval     = 60 * time.Second
	DefaultFullInterval = time.Hour
	// pushTick is how often queued remote writes are checked for being due.
	pushTick = 5 * time.Second
	// nudgeDelay lets a burst of local edits settle into one cycle.
	nudgeDelay = 2 * time.Second
	// cursorOverlap re-asks for a little before the cursor: remote clocks
	// and listing lag can put an update just behind the newest one seen.
	cursorOverlap = time.Minute
	callTimeout   = 30 * time.Second
)

// Config configures a Syncer.
type Config struct {
	// Kind is the remote tracker: "linear" or "github".
	Kind string
	// Local is the local issue tracker.
	Local *local.Tracker
	// Remote is the remote tracker; it must implement tracker.IssueEditor.
	Remote tracker.Tracker
	// Outbox queues the writes to the remote (its own file, not the
	// orchestrator's outbox).
	Outbox *outbox.Outbox
	// LedgerPath is the sync ledger file; empty keeps it in memory.
	LedgerPath string
	// StateMap maps local state names to remote ones; a state not in it has
	// the same name on both sides.
	StateMap map[string]string
	// PullStates are the remote states whose unlinked issues are pulled
	// into new local files (remote names).
	PullStates []string
	// StateLabels are the remote state names; on GitHub labels with these
	// names are the issue's state, not labels to sync.
	StateLabels []string
	// Interval is the cycle interval (DefaultInterval when zero);
	// FullInterval how often a pull is a full one (DefaultFullInterval).
	Interval     time.Duration
	FullInterval time.Duration
	// OnLocalChange is called after a cycle wrote local files, so the
	// orchestrator polls at once (the sync's own writes are not file-watch
	// changes).
	OnLocalChange func()
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

// Status is the sync's state for the sync status (#117).
type Status struct {
	// SyncedAt is when the last cycle completed; zero before the first.
	SyncedAt time.Time
	// Cursor is the newest remote update time seen.
	Cursor time.Time
	// LastErr is the newest failed cycle's error (classify it before
	// showing it), nil after a good cycle; LastErrAt when it happened and
	// Failures the failed cycles since the last good one.
	LastErr   error
	LastErrAt time.Time
	Failures  int
	Interval  time.Duration
	// Linked is the number of linked issues.
	Linked int
}

// Syncer syncs the local issue files with a remote tracker.
type Syncer struct {
	cfg    Config
	editor tracker.IssueEditor
	dl     dialect
	now    func() time.Time
	nudge  chan struct{}

	// lg is owned by the Run goroutine (and tests calling Cycle/Push
	// directly); statusMu guards only status.
	lg       *ledger
	statusMu sync.Mutex
	status   Status
}

// New returns a Syncer. It fails when the remote tracker cannot create and
// edit issues.
func New(cfg Config) (*Syncer, error) {
	if cfg.Local == nil || cfg.Remote == nil || cfg.Outbox == nil {
		return nil, errors.New("local sync: Local, Remote and Outbox are required")
	}
	ed, ok := cfg.Remote.(tracker.IssueEditor)
	if !ok {
		return nil, fmt.Errorf("local sync: the %s tracker cannot create and edit issues", cfg.Kind)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.FullInterval <= 0 {
		cfg.FullInterval = DefaultFullInterval
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Syncer{cfg: cfg, editor: ed, now: now, nudge: make(chan struct{}, 1),
		dl:     dialect{kind: cfg.Kind, toRemoteState: cfg.StateMap, stateLabels: cfg.StateLabels},
		lg:     loadLedger(cfg.LedgerPath),
		status: Status{Interval: cfg.Interval}}
	s.status.Cursor = s.lg.Cursor
	return s, nil
}

// Nudge asks for a cycle soon (a local file changed). Never blocks.
func (s *Syncer) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Status reports the sync's state. Safe from any goroutine.
func (s *Syncer) Status() Status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// Outbox is the sync's remote write queue (for the sync status).
func (s *Syncer) Outbox() *outbox.Outbox { return s.cfg.Outbox }

// Run cycles until ctx ends: at once, then every Interval or soon after a
// Nudge, pushing due remote writes in between.
func (s *Syncer) Run(ctx context.Context) {
	cycle := time.NewTimer(0)
	defer cycle.Stop()
	push := time.NewTicker(pushTick)
	defer push.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cycle.C:
			s.runCycle(ctx)
			s.Push(ctx)
			cycle.Reset(s.cfg.Interval)
		case <-s.nudge:
			cycle.Reset(nudgeDelay)
		case <-push.C:
			s.Push(ctx)
		case <-s.cfg.Outbox.Wake():
			s.Push(ctx)
		}
	}
}

// runCycle runs one cycle and records its outcome in the status.
func (s *Syncer) runCycle(ctx context.Context) {
	err := s.Cycle(ctx)
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	now := s.now()
	s.status.Cursor = s.lg.Cursor
	s.status.Linked = s.linkedCount()
	if err != nil {
		s.status.LastErr, s.status.LastErrAt = err, now
		s.status.Failures++
		slog.Warn("local sync: cycle failed; retrying at the next one", "error", err, "failures", s.status.Failures)
		return
	}
	s.status.SyncedAt, s.status.LastErr, s.status.Failures = now, nil, 0
}

func (s *Syncer) linkedCount() int {
	n := 0
	for _, l := range s.lg.Links {
		if l.RemoteID != "" && !l.Detached {
			n++
		}
	}
	return n
}

func (s *Syncer) saveLedger() {
	if err := s.lg.save(s.cfg.LedgerPath); err != nil {
		slog.Warn("local sync: cannot save the sync ledger", "error", err)
	}
}
