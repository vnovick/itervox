// Package store is the local tracker store (#113): a copy of the tracked
// issues, kept under .itervox/, that serves the list reads the orchestrator,
// dashboard and API make on every tick, so they cost no tracker requests and
// keep working through a tracker outage. A background sync refreshes it.
//
// The store wraps a tracker adapter and is itself a tracker.Tracker, so
// everything above it is unchanged:
//
//   - List reads (FetchCandidateIssues, FetchIssuesByStates) are answered
//     from the store. A state set the store has not pulled before is fetched
//     once and then kept as a view.
//   - Reads by issue ID (FetchIssueStatesByIDs, FetchIssueDetail,
//     FetchIssueByIdentifier, batched detail) go to the tracker: they decide
//     whether a running worker continues, a retry fires or a cancelled
//     issue's worker stops, and carry the comments an agent must see fresh.
//     Their results refresh the store's copy.
//   - Writes go to the tracker. A successful state, branch or create write is
//     applied to the store and saved at once, so the next tick, and a
//     restart, see it; the next sync is authoritative.
//   - A state move queued in the write-ahead outbox and not pushed yet
//     (#115) is laid over every list read (SetPendingStates, pending.go), so
//     the board sees it at once; the store's own copy stays the tracker's.
//
// A full pull is FetchCandidateIssues plus FetchIssuesByStates for each
// view. Between full pulls, an adapter that implements
// tracker.IncrementalFetcher is asked only for the issues updated since the
// last pull (#114): one paginated request, whose results are placed by state
// (an issue closed or moved out of the active states leaves the candidate
// list; one that was trashed, archived or lost its tracked state leaves the
// store). The cursor is the newest update time the tracker reported, so
// clocks never need to agree; it is saved with the store, and a failed pull
// keeps it and retries with backoff. A full pull still runs every
// Config.FullSyncInterval to catch what an update listing cannot show (an
// issue moved to another project or repository, a deleted GitHub issue).
// The store is written atomically after
// every successful sync and loaded at startup, so a restart serves at once;
// only a cold start (no file, or one for another tracker scope) pulls before
// the first read is answered, and it pulls exactly once however many readers
// are waiting.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
)

// fileVersion is the on-disk format version; a file with another version is
// ignored (a cold start).
const fileVersion = 1

// viewTTLSyncs is how many sync intervals an unread view is kept for. A view
// nobody reads stops costing a request per sync; reading it again re-fetches
// it once.
const viewTTLSyncs = 10

// Config configures a Store.
type Config struct {
	// Path is the store file, normally .itervox/tracker_store.json.
	Path string
	// Scope identifies the tracker and project the file was pulled from. A
	// file with another scope is discarded, so a changed project_slug,
	// endpoint or active-state list never serves the old project's issues.
	Scope string
	// ActiveStates are the states FetchCandidateIssues returns; a write that
	// moves an issue into or out of one updates the candidate list at once.
	ActiveStates []string
	// Views are the state sets pulled on every sync from the start, e.g. the
	// dashboard's backlog+active+terminal list, so a cold start's one pull
	// covers them.
	Views [][]string
	// SyncInterval is how often Run re-pulls: tracker.store.sync_interval_ms,
	// or the webhook safety-net interval when webhooks are on (#131).
	SyncInterval time.Duration
	// FullSyncInterval is how often a pull is a full one even when the
	// adapter can pull incrementally (#114): tracker.store.full_sync_interval_ms
	// (zero: DefaultFullSyncInterval).
	FullSyncInterval time.Duration
	// NudgeDelay is how long Run waits after RequestSync before pulling
	// (zero: DefaultNudgeDelay).
	NudgeDelay time.Duration
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// view is one state set the store answers FetchIssuesByStates for.
type view struct {
	States   []string
	IDs      []string
	LastRead time.Time
	// configured views (Config.Views) are never dropped for being unread:
	// a restart must find them in the file.
	configured bool
}

// storeFile is the persisted form.
type storeFile struct {
	Version    int
	Scope      string
	SyncedAt   time.Time
	Candidates []string
	Views      []*view
	Issues     map[string]domain.Issue
	// Cursor and FullAt (#114): the newest update time seen and when the
	// last full pull ran. A file without them makes the next pull full.
	Cursor time.Time `json:",omitzero"`
	FullAt time.Time `json:",omitzero"`
}

// Store is the local tracker store. Build it with Wrap.
type Store struct {
	up  tracker.Tracker
	cfg Config
	now func() time.Time

	// syncMu serialises pulls; fileMu serialises file writes (so an older
	// snapshot never lands after a newer one); mu guards everything below.
	// No tracker call is made under mu.
	syncMu sync.Mutex
	fileMu sync.Mutex
	mu     sync.Mutex

	synced   bool
	syncedAt time.Time
	lastErr  error
	// lastErrAt and failures describe the run of failed pulls since the last
	// good one (#117): when the newest failed and how many failed in a row.
	lastErrAt  time.Time
	failures   int
	active     []string
	issues     map[string]domain.Issue
	candidates []string
	// fullNext makes the next pull a full one (RequestSync).
	fullNext bool
	views    map[string]*view
	// gen counts local changes; touched records the gen of each issue's last
	// local change, so a pull that started before it does not undo it.
	gen     int64
	touched map[string]int64
	// removed marks touched issues that were removed locally (#131), so a
	// pull that read one before the removal leaves it out.
	removed map[string]struct{}
	// scope counts runtime scope changes (active states, project filter): a
	// pull that started under an older scope is discarded, not committed.
	scope int64

	// nudge asks Run for a pull soon (RequestSync, #131); buffered so a
	// request made while one is pending coalesces with it.
	nudge chan struct{}

	// cursor is the newest UpdatedAt the tracker reported (#114): the next
	// incremental pull asks for the issues updated since. Zero means the
	// next pull is full. fullAt is when the last full pull committed.
	cursor time.Time
	fullAt time.Time

	// pending returns the state moves queued for push and not yet sent
	// (#115), by issue ID; nil when nothing queues writes. See pending.go.
	pending func() map[string]string
}

// DefaultFullSyncInterval is Config.FullSyncInterval when unset.
const DefaultFullSyncInterval = time.Hour

// The adapters' optional interfaces the base Store forwards. Wrap refuses an
// adapter without them, so a type assertion on the store answers exactly as
// it would on the adapter.
type baseUpstream interface {
	tracker.Tracker
	tracker.RateLimiter
	tracker.IdempotentCommenter
	tracker.StateListSetter
}

// Wrap returns a store over up, loading cfg.Path when it holds this scope.
// The result also implements tracker.DetailBatcher and
// tracker.ProjectManager exactly when up does (Linear does, GitHub does not),
// and tracker.ScopeManager when up also does (#182), and
// tracker.FilterManager when up does (#198: Linear and GitHub).
// It fails for an adapter without the base optional interfaces (the local
// and memory trackers, which need no store).
func Wrap(up tracker.Tracker, cfg Config) (tracker.Tracker, *Store, error) {
	if _, ok := up.(baseUpstream); !ok {
		return nil, nil, fmt.Errorf("store: %T is not a remote tracker adapter", up)
	}
	if cfg.SyncInterval <= 0 {
		return nil, nil, errors.New("store: sync interval must be positive")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Store{
		up:      up,
		cfg:     cfg,
		now:     now,
		active:  slices.Clone(cfg.ActiveStates),
		issues:  map[string]domain.Issue{},
		views:   map[string]*view{},
		touched: map[string]int64{},
		removed: map[string]struct{}{},
		nudge:   make(chan struct{}, 1),
	}
	s.load()
	for _, states := range cfg.Views {
		if len(states) == 0 {
			continue
		}
		v, ok := s.views[viewKey(states)]
		if !ok {
			v = &view{States: slices.Clone(states), LastRead: now()}
			s.views[viewKey(states)] = v
			// A configured view the file lacks has no data: pull before
			// serving, as on a cold start.
			s.synced = false
		}
		v.configured = true
	}
	_, batches := up.(tracker.DetailBatcher)
	_, projects := up.(tracker.ProjectManager)
	_, filtered := up.(tracker.FilterManager)
	if batches && projects {
		if _, scoped := up.(tracker.ScopeManager); scoped && filtered {
			return &scopedStore{&linearStore{s}, filterForward{s}}, s, nil
		}
		return &linearStore{s}, s, nil
	}
	if filtered {
		// #198: GitHub's tracker filter.
		return &filteredStore{s, filterForward{s}}, s, nil
	}
	return s, s, nil
}

// load reads the store file; anything unreadable or for another scope is a
// cold start.
func (s *Store) load() {
	raw, err := os.ReadFile(s.cfg.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("tracker store: unreadable, pulling from the tracker", "path", s.cfg.Path, "error", err)
		}
		return
	}
	var f storeFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != fileVersion || f.Scope != s.cfg.Scope {
		slog.Info("tracker store: file is for another tracker or format, pulling from the tracker", "path", s.cfg.Path)
		return
	}
	if f.Issues != nil {
		s.issues = f.Issues
	}
	s.candidates = f.Candidates
	for _, v := range f.Views {
		if v != nil && len(v.States) > 0 {
			v.LastRead = s.now()
			s.views[viewKey(v.States)] = v
		}
	}
	s.syncedAt = f.SyncedAt
	s.cursor, s.fullAt = f.Cursor, f.FullAt
	s.synced = true
}

// viewKey is the order- and case-insensitive key of a state set.
func viewKey(states []string) string {
	k := make([]string, 0, len(states))
	for _, st := range states {
		k = append(k, strings.ToLower(strings.TrimSpace(st)))
	}
	slices.Sort(k)
	return strings.Join(slices.Compact(k), "\x00")
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

// Run keeps the store fresh until ctx ends: a cold store pulls at once, a
// loaded one when its data is a sync interval old, then every interval.
func (s *Store) Run(ctx context.Context) {
	s.mu.Lock()
	wait := s.cfg.SyncInterval - s.now().Sub(s.syncedAt)
	cold := !s.synced
	if cold {
		wait = 0
	}
	s.mu.Unlock()
	wait = max(wait, 0)
	t := time.NewTimer(wait)
	defer t.Stop()
	var lastNudged time.Time
	failures := 0
	for {
		trigger := metrics.StorePullInterval
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if cold {
				trigger = metrics.StorePullCold
			}
		case <-s.nudge:
			// A webhook named an issue the store cannot place on its own.
			// Wait a moment so a burst of deliveries costs one pull.
			// Pulls a webhook asks for are at least MinNudgeGap apart, so
			// a busy team's deliveries for issues outside the project
			// cannot spend the rate limit on pulls.
			trigger = metrics.StorePullWebhook
			delay := max(s.nudgeDelay(), MinNudgeGap-time.Since(lastNudged))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			lastNudged = time.Now()
			select { // a nudge made during the wait is covered by this pull
			case <-s.nudge:
			default:
			}
		}
		// A cold pull may already have been made by a reader that got
		// there first; it is the one full pull a cold start makes.
		err := s.sync(ctx, cold, trigger)
		next := s.cfg.SyncInterval
		if err != nil && ctx.Err() == nil {
			failures++
			next = retryDelay(failures, s.cfg.SyncInterval)
			slog.Warn("tracker store: sync failed, serving the last pull", "error", err, "retry_in", next)
		} else {
			cold = false
			failures = 0
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(next)
	}
}

// RetryBase is the wait before the first retry of a failed pull (#114);
// each further failure doubles it, up to the sync interval. The cursor is
// kept, so the retry asks for everything since the last good pull.
var RetryBase = 5 * time.Second

func retryDelay(failures int, interval time.Duration) time.Duration {
	return min(RetryBase<<min(failures-1, 16), interval)
}

// MinNudgeGap is the least time between two pulls RequestSync asks for.
var MinNudgeGap = 30 * time.Second

// DefaultNudgeDelay is how long Run waits after RequestSync before pulling,
// so a burst of webhook deliveries costs one pull.
const DefaultNudgeDelay = 2 * time.Second

func (s *Store) nudgeDelay() time.Duration {
	if s.cfg.NudgeDelay > 0 {
		return s.cfg.NudgeDelay
	}
	return DefaultNudgeDelay
}

// RequestSync asks Run for a full pull soon (#131): a webhook named an
// issue the store does not hold, so it cannot tell on its own whether the
// issue belongs to the tracked project, or the webhook queue overflowed and
// its removals were dropped. The pull is a full one even when an
// incremental pull is due (#114): a changes listing does not show a
// deleted or transferred GitHub issue. Requests made before the pull
// coalesce.
func (s *Store) RequestSync() {
	s.mu.Lock()
	s.fullNext = true
	s.mu.Unlock()
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// RefreshIssues re-reads the issues with ids by ID, with no list request,
// and applies them (#131): a webhook said they changed. Through an adapter
// that can check its own scope (tracker.ScopedFetcher) an in-scope issue is
// placed whether or not the store held it, and a held issue that is no
// longer in scope (moved to another project, deleted) is dropped; an issue
// out of scope costs nothing more. Otherwise held issues are re-read one by
// one and the others are returned as unknown, for a full pull to place. A
// failed read is returned after everything else is applied.
//
// A read is older than any local change made while it was in flight (a
// state write through the store, a Forget): such an issue is left as the
// local change made it, and a read made under a scope since replaced is
// discarded.
func (s *Store) RefreshIssues(ctx context.Context, ids []string) (unknown []string, err error) {
	if sf, ok := s.up.(tracker.ScopedFetcher); ok {
		startGen, startScope := s.generation()
		got, ferr := sf.FetchIssuesInScope(ctx, ids)
		if ferr != nil {
			return nil, ferr
		}
		inScope := make(map[string]bool, len(got))
		s.mu.Lock()
		if s.scope != startScope {
			s.mu.Unlock()
			return nil, nil
		}
		for _, iss := range got {
			inScope[iss.ID] = true
			if s.touched[iss.ID] <= startGen {
				s.upsertLocked(iss)
			}
		}
		for _, id := range ids {
			if !inScope[id] && s.touched[id] <= startGen {
				s.removeLocked(id)
			}
		}
		s.mu.Unlock()
		s.persist()
		return nil, nil
	}
	var known []string
	s.mu.Lock()
	for _, id := range ids {
		if _, ok := s.issues[id]; ok {
			known = append(known, id)
		} else {
			unknown = append(unknown, id)
		}
	}
	s.mu.Unlock()
	changed := false
	for _, id := range known {
		startGen, startScope := s.generation()
		iss, ferr := s.up.FetchIssueDetail(ctx, id)
		if ferr != nil {
			err = errors.Join(err, ferr)
			continue
		}
		if iss == nil {
			continue
		}
		s.mu.Lock()
		if s.scope == startScope && s.touched[id] <= startGen {
			s.upsertLocked(*iss)
			changed = true
		}
		s.mu.Unlock()
	}
	if changed {
		s.persist()
	}
	return unknown, err
}

// generation is the local-change generation and the scope now: a read that
// started at them is older than any local change with a higher generation.
func (s *Store) generation() (gen, scope int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen, s.scope
}

// upsertLocked records iss as a local change: a pull that started before it
// does not undo it. Caller holds mu.
func (s *Store) upsertLocked(iss domain.Issue) {
	s.gen++
	s.touched[iss.ID] = s.gen
	delete(s.removed, iss.ID)
	s.placeLocked(iss)
}

// removeLocked drops the issue with id as a local change, remembered so a
// pull that read it before the removal does not bring it back. The removal
// is remembered even when the store does not hold the issue yet: a pull in
// flight (the first one, or one that finds a new issue) may be about to add
// it. Caller holds mu.
func (s *Store) removeLocked(id string) {
	s.gen++
	s.touched[id] = s.gen
	s.removed[id] = struct{}{}
	s.dropLocked(id)
}

// dropLocked takes id out of the issues, the candidates and every view.
// Caller holds mu.
func (s *Store) dropLocked(id string) {
	delete(s.issues, id)
	s.candidates = setMember(s.candidates, id, false)
	for _, v := range s.views {
		v.IDs = setMember(v.IDs, id, false)
	}
}

// Forget removes the issue with id from the store: it was deleted or moved
// out of the tracked repository (#131).
func (s *Store) Forget(id string) {
	s.mu.Lock()
	_, ok := s.issues[id]
	s.removeLocked(id)
	s.mu.Unlock()
	if ok {
		s.persist()
	}
}

// Upstream returns the adapter the store wraps, for callers that need an
// adapter-specific interface the store does not forward (GitHub's
// repository-comment reads for comment commands).
func (s *Store) Upstream() tracker.Tracker { return s.up }

// Status reports when the store last pulled successfully and the error of
// the last failed pull since (nil when the last pull succeeded).
func (s *Store) Status() (syncedAt time.Time, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncedAt, s.lastErr
}

// SyncStatus is the store's sync state (#117), read by the snapshot, the
// HEARTBEAT and through them `itervox doctor`.
type SyncStatus struct {
	// SyncedAt is when the store last pulled successfully (loaded from the
	// store file after a restart); zero when it never has.
	SyncedAt time.Time
	// Cursor is the newest update time the tracker reported (#114); zero
	// before the first full pull.
	Cursor time.Time
	// LastErr is the error of the newest failed pull, nil when the last pull
	// succeeded. It is the tracker adapter's error: callers must classify it
	// (tracker.ClassifyError) before showing it, never print it raw.
	LastErr error
	// LastErrAt is when LastErr happened; Failures counts the failed pulls
	// since the last good one.
	LastErrAt time.Time
	Failures  int
	// Interval is the periodic pull interval (Config.SyncInterval).
	Interval time.Duration
}

// SyncStatus reports the store's sync state. Safe from any goroutine.
func (s *Store) SyncStatus() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyncStatus{
		SyncedAt:  s.syncedAt,
		Cursor:    s.cursor,
		LastErr:   s.lastErr,
		LastErrAt: s.lastErrAt,
		Failures:  s.failures,
		Interval:  s.cfg.SyncInterval,
	}
}

// failLocked records a failed pull. Caller holds mu.
func (s *Store) failLocked(err error) {
	s.lastErr, s.lastErrAt = err, s.now()
	s.failures++
}

// ensureSynced pulls once if the store has no data yet.
func (s *Store) ensureSynced(ctx context.Context) error {
	s.mu.Lock()
	ok := s.synced
	s.mu.Unlock()
	if ok {
		return nil
	}
	return s.sync(ctx, true, metrics.StorePullCold)
}

// scopeRetries bounds how often a pull is redone because the scope changed
// while it ran.
const scopeRetries = 3

// errScopeChanged is returned when the scope kept changing under every pull.
var errScopeChanged = errors.New("store: tracker scope changed during the pull; pulling again on the next read")

// sync pulls every view. With onlyIfUnsynced it does nothing when another
// caller pulled while this one waited, which is what makes a cold start one
// pull however many readers race it. A pull overtaken by a scope change is
// discarded and redone under the new scope.
func (s *Store) sync(ctx context.Context, onlyIfUnsynced bool, trigger string) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	for range scopeRetries {
		done, err := s.pullOnce(ctx, onlyIfUnsynced, trigger)
		if done || err != nil {
			return err
		}
	}
	return errScopeChanged
}

// pullOnce is one pull; done is false when the scope changed while it ran
// and nothing was committed. Caller holds syncMu.
func (s *Store) pullOnce(ctx context.Context, onlyIfUnsynced bool, trigger string) (done bool, err error) {
	s.mu.Lock()
	if onlyIfUnsynced && s.synced {
		s.mu.Unlock()
		return true, nil
	}
	metrics.StorePull(trigger)
	startGen, startScope := s.gen, s.scope
	if inc, ok := s.up.(tracker.IncrementalFetcher); ok && s.incrementalDueLocked() {
		since := s.cursor.Add(-CursorOverlap)
		s.mu.Unlock()
		return s.pullChanges(ctx, inc, since, startGen, startScope)
	}
	forced := s.fullNext
	s.fullNext = false // a request made while this pull runs asks for another
	ttl := viewTTLSyncs * s.cfg.SyncInterval
	type pull struct {
		key    string
		states []string
	}
	var pulls []pull
	for key, v := range s.views {
		if !v.configured && s.now().Sub(v.LastRead) > ttl {
			delete(s.views, key)
			continue
		}
		pulls = append(pulls, pull{key, slices.Clone(v.States)})
	}
	s.mu.Unlock()

	cands, err := s.up.FetchCandidateIssues(ctx)
	results := make(map[string][]domain.Issue, len(pulls))
	for _, p := range pulls {
		if err != nil {
			break
		}
		results[p.key], err = s.up.FetchIssuesByStates(ctx, p.states)
	}
	if err != nil {
		s.mu.Lock()
		s.failLocked(err)
		s.fullNext = s.fullNext || forced
		s.mu.Unlock()
		return false, err
	}

	s.mu.Lock()
	if s.scope != startScope {
		s.mu.Unlock()
		return false, nil
	}
	issues := make(map[string]domain.Issue, len(cands))
	var mixed []string // listed twice, in two states
	ids := func(list []domain.Issue) []string {
		out := make([]string, 0, len(list))
		for _, iss := range list {
			// An issue edited between two of this pull's requests is listed
			// twice: keep the newer copy, or the cursor would move past an
			// update the store does not hold.
			prev, ok := issues[iss.ID]
			if ok && !strings.EqualFold(prev.State, iss.State) {
				mixed = append(mixed, iss.ID)
			}
			if !ok || newer(iss, prev) {
				issues[iss.ID] = iss
			}
			out = append(out, iss.ID)
		}
		return out
	}
	candidates := ids(cands)
	for key, list := range results {
		if v, ok := s.views[key]; ok {
			v.IDs = ids(list)
		}
	}
	// Views registered while this pull ran keep the issues they fetched.
	for key, v := range s.views {
		if _, pulled := results[key]; pulled {
			continue
		}
		for _, id := range v.IDs {
			if iss, ok := s.issues[id]; ok {
				issues[id] = iss
			}
		}
	}
	old := s.issues
	s.issues, s.candidates = issues, candidates
	for _, id := range mixed {
		s.memberLocked(issues[id]) // the lists the newer copy's state belongs to
	}
	s.cursor, s.fullAt = newestUpdate(issues), s.now()
	// A local change made after this pull began is newer than what it
	// read: put it back.
	for id, g := range s.touched {
		if g <= startGen {
			delete(s.touched, id)
			delete(s.removed, id)
			continue
		}
		if _, gone := s.removed[id]; gone {
			s.dropLocked(id)
		} else if iss, ok := old[id]; ok {
			s.placeLocked(iss)
		}
	}
	s.synced, s.syncedAt, s.lastErr, s.failures = true, s.now(), nil, 0
	s.mu.Unlock()

	s.persist()
	return true, nil
}

// CursorOverlap is how far before the cursor an incremental pull asks from:
// an update the tracker commits late, or stamps with a time a little before
// one already seen, is still listed. Issues listed again unchanged cost
// nothing (sameListing).
var CursorOverlap = time.Minute

// incrementalDueLocked reports whether the next pull may be incremental
// (#114): the store holds a full pull with a cursor, and the last full pull
// is less than FullSyncInterval old. Caller holds mu.
func (s *Store) incrementalDueLocked() bool {
	full := s.cfg.FullSyncInterval
	if full <= 0 {
		full = DefaultFullSyncInterval
	}
	return s.synced && !s.fullNext && !s.cursor.IsZero() && s.now().Sub(s.fullAt) < full
}

// pullChanges is an incremental pull (#114): the issues updated since the
// cursor are placed by state, the ones gone from the scope are dropped, and
// the cursor moves to the newest update seen. A local change made after the
// pull began is newer than what it read and is kept. Nothing is written when
// nothing changed. Caller holds syncMu.
func (s *Store) pullChanges(ctx context.Context, inc tracker.IncrementalFetcher, since time.Time, startGen, startScope int64) (bool, error) {
	changed, gone, err := inc.FetchIssuesUpdatedSince(ctx, since)
	if err != nil {
		s.mu.Lock()
		s.failLocked(err)
		s.mu.Unlock()
		return false, err
	}
	s.mu.Lock()
	if s.scope != startScope {
		s.mu.Unlock()
		return false, nil
	}
	dirty := false
	cursor := s.cursor
	for _, iss := range changed {
		if iss.UpdatedAt != nil && iss.UpdatedAt.After(cursor) {
			cursor = *iss.UpdatedAt
		}
		if s.touched[iss.ID] > startGen {
			continue
		}
		if cur, ok := s.issues[iss.ID]; ok && sameListing(cur, iss) {
			continue
		}
		s.placeLocked(iss)
		dirty = true
	}
	// After every issue is placed, so a blocked issue listed in the same
	// pull does not overwrite its blocker's newer state. Dependents follow
	// the blocker as the store now holds it: a blocker changed locally while
	// the pull ran keeps its newer state, and so do references to it.
	for _, iss := range changed {
		cur, ok := s.issues[iss.ID]
		if !ok {
			continue
		}
		if s.patchBlockersLocked(cur) {
			dirty = true
		}
	}
	for _, id := range gone {
		if s.touched[id] > startGen {
			continue
		}
		if _, ok := s.issues[id]; ok {
			s.dropLocked(id)
			dirty = true
		}
	}
	for id, g := range s.touched {
		if g <= startGen {
			delete(s.touched, id)
			delete(s.removed, id)
		}
	}
	moved := !cursor.Equal(s.cursor)
	s.cursor = cursor
	s.synced, s.syncedAt, s.lastErr, s.failures = true, s.now(), nil, 0
	s.mu.Unlock()
	if dirty || moved {
		s.persist()
	}
	return true, nil
}

// sameListing reports whether a listing of an issue matches the stored copy.
// Comments are left out: a detail read stores them, a listing never has them.
func sameListing(stored, listed domain.Issue) bool {
	stored.Comments, listed.Comments = nil, nil
	return reflect.DeepEqual(stored, listed)
}

// patchBlockersLocked updates the blocker state other stored issues record
// for changed: an incremental pull returns the blocker, not the issues it
// blocks, whose own update time did not move. Reports whether it changed
// any. Caller holds mu.
func (s *Store) patchBlockersLocked(changed domain.Issue) (any bool) {
	for id, other := range s.issues {
		patched := false
		for i, b := range other.BlockedBy {
			if b.ID == nil || *b.ID != changed.ID || (b.State != nil && *b.State == changed.State) {
				continue
			}
			if !patched {
				other.BlockedBy = slices.Clone(other.BlockedBy)
				patched = true
			}
			st := changed.State
			other.BlockedBy[i].State = &st
		}
		if patched {
			s.issues[id] = other
			any = true
		}
	}
	return any
}

// newer reports whether a was updated after b; an issue without an update
// time is not newer.
func newer(a, b domain.Issue) bool {
	return a.UpdatedAt != nil && (b.UpdatedAt == nil || a.UpdatedAt.After(*b.UpdatedAt))
}

// newestUpdate is the latest UpdatedAt among issues, or zero.
func newestUpdate(issues map[string]domain.Issue) time.Time {
	var newest time.Time
	for _, iss := range issues {
		if iss.UpdatedAt != nil && iss.UpdatedAt.After(newest) {
			newest = *iss.UpdatedAt
		}
	}
	return newest
}

// fileLocked is the persisted form of the current data. Caller holds mu.
func (s *Store) fileLocked() []byte {
	f := storeFile{
		Version:    fileVersion,
		Scope:      s.cfg.Scope,
		SyncedAt:   s.syncedAt,
		Candidates: s.candidates,
		Issues:     s.issues,
		Cursor:     s.cursor,
		FullAt:     s.fullAt,
	}
	for _, v := range s.views {
		f.Views = append(f.Views, v)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		slog.Warn("tracker store: encode failed", "error", err)
		return nil
	}
	return raw
}

// persist writes the current data to the store file (mode 0600: it holds
// issue text). A failed write is logged; the in-memory store keeps serving.
func (s *Store) persist() {
	if s.cfg.Path == "" {
		return
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	s.mu.Lock()
	// Before the first pull the data is partial; a restart must not load it
	// as a complete store.
	var raw []byte
	if s.synced {
		raw = s.fileLocked()
	}
	s.mu.Unlock()
	if raw == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.Path), 0o755); err == nil {
		err = atomicfs.WriteFile(s.cfg.Path, raw, 0o600)
		if err == nil {
			return
		}
		slog.Warn("tracker store: write failed", "path", s.cfg.Path, "error", err)
	}
}

// placeLocked records iss and, when its state changed (or it is new), puts
// it in the candidate list and the views its state belongs to and out of the
// others. An unchanged state keeps the memberships the tracker reported,
// which matters where the adapter's state names differ from its query names
// (GitHub lists closed issues for "closed" but names their state after a
// terminal label). Caller holds mu.
func (s *Store) placeLocked(iss domain.Issue) {
	old, known := s.issues[iss.ID]
	s.issues[iss.ID] = iss
	if known && strings.EqualFold(old.State, iss.State) {
		return
	}
	s.memberLocked(iss)
}

// memberLocked puts iss in the candidate list and the views its state
// belongs to and out of the others. Caller holds mu.
func (s *Store) memberLocked(iss domain.Issue) {
	s.candidates = setMember(s.candidates, iss.ID, containsFold(s.active, iss.State))
	for _, v := range s.views {
		v.IDs = setMember(v.IDs, iss.ID, containsFold(v.States, iss.State))
	}
}

// touchLocked applies a local change to the issue with id, when the store
// has it. Caller holds mu.
func (s *Store) touchLocked(id string, change func(*domain.Issue)) {
	iss, ok := s.issues[id]
	if !ok {
		return
	}
	change(&iss)
	s.gen++
	s.touched[id] = s.gen
	s.placeLocked(iss)
}

func setMember(ids []string, id string, in bool) []string {
	i := slices.Index(ids, id)
	switch {
	case in && i < 0:
		return append(ids, id)
	case !in && i >= 0:
		return slices.Delete(ids, i, i+1)
	}
	return ids
}

// listLocked returns the issues for ids. Caller holds mu.
func (s *Store) listLocked(ids []string) []domain.Issue {
	out := make([]domain.Issue, 0, len(ids))
	for _, id := range ids {
		if iss, ok := s.issues[id]; ok {
			out = append(out, iss)
		}
	}
	return out
}

// FetchCandidateIssues implements tracker.Tracker from the store.
func (s *Store) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	if err := s.ensureSynced(ctx); err != nil {
		return nil, err
	}
	pend := s.pendingStates()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlayLocked(s.candidates, pend, s.active), nil
}

// FetchIssuesByStates implements tracker.Tracker from the store; a state set
// it has not seen is fetched once and kept as a view.
func (s *Store) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	if err := s.ensureSynced(ctx); err != nil {
		return nil, err
	}
	key := viewKey(stateNames)
	pend := s.pendingStates()
	s.mu.Lock()
	if v, ok := s.views[key]; ok {
		v.LastRead = s.now()
		out := s.overlayLocked(v.IDs, pend, v.States)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	s.mu.Lock()
	startGen, startScope := s.gen, s.scope
	s.mu.Unlock()
	metrics.StorePull(metrics.StorePullView)
	issues, err := s.up.FetchIssuesByStates(ctx, stateNames)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != startScope {
		// Fetched under a scope since replaced: answer, but keep nothing.
		return issues, nil
	}
	// movedLocally: Itervox changed the issue while this fetch ran, so the
	// fetch is older than the store's copy.
	movedLocally := func(id string) bool { return s.touched[id] > startGen }
	v := &view{States: slices.Clone(stateNames), LastRead: s.now()}
	for _, iss := range issues {
		if movedLocally(iss.ID) {
			// Membership follows the newer local state.
			if cur, ok := s.issues[iss.ID]; !ok || !containsFold(stateNames, cur.State) {
				continue
			}
		} else {
			s.placeLocked(iss)
		}
		// Otherwise the tracker said it matches this set, whatever its state
		// is named (GitHub's "closed").
		v.IDs = append(v.IDs, iss.ID)
	}
	// Issues Itervox moved into this set while the fetch ran.
	for id := range s.touched {
		if movedLocally(id) && !slices.Contains(v.IDs, id) {
			if cur, ok := s.issues[id]; ok && containsFold(stateNames, cur.State) {
				v.IDs = append(v.IDs, id)
			}
		}
	}
	s.views[key] = v
	return s.overlayLocked(v.IDs, pend, v.States), nil
}

// FetchIssueStatesByIDs implements tracker.Tracker from the tracker (see
// the package doc); the states it returns refresh the store's copies.
func (s *Store) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	issues, err := s.up.FetchIssueStatesByIDs(ctx, issueIDs)
	if err != nil || len(issues) == 0 {
		return issues, err
	}
	s.mu.Lock()
	changed := false
	for _, iss := range issues {
		if cur, ok := s.issues[iss.ID]; ok && !strings.EqualFold(cur.State, iss.State) {
			s.touchLocked(iss.ID, func(c *domain.Issue) { c.State = iss.State })
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.persist()
	}
	return issues, nil
}

// refresh stores a detail read's issue when the store holds it.
func (s *Store) refresh(iss *domain.Issue) {
	if iss == nil {
		return
	}
	s.mu.Lock()
	cur, ok := s.issues[iss.ID]
	moved := ok && !strings.EqualFold(cur.State, iss.State)
	s.touchLocked(iss.ID, func(cur *domain.Issue) { *cur = *iss })
	s.mu.Unlock()
	if moved {
		s.persist()
	}
}

// FetchIssueDetail implements tracker.Tracker from the tracker.
func (s *Store) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	iss, err := s.up.FetchIssueDetail(ctx, issueID)
	if err == nil {
		s.refresh(iss)
	}
	return iss, err
}

// FetchIssueByIdentifier implements tracker.Tracker from the tracker.
func (s *Store) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	iss, err := s.up.FetchIssueByIdentifier(ctx, identifier)
	if err == nil {
		s.refresh(iss)
	}
	return iss, err
}

// UpdateIssueState implements tracker.Tracker; on success the store's copy
// moves too.
func (s *Store) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	if err := s.up.UpdateIssueState(ctx, issueID, stateName); err != nil {
		return err
	}
	s.mu.Lock()
	s.touchLocked(issueID, func(iss *domain.Issue) { iss.State = stateName })
	s.mu.Unlock()
	s.persist()
	return nil
}

// SetIssueBranch implements tracker.Tracker; on success the store's copy
// carries the branch.
func (s *Store) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	if err := s.up.SetIssueBranch(ctx, issueID, branchName); err != nil {
		return err
	}
	s.mu.Lock()
	s.touchLocked(issueID, func(iss *domain.Issue) { iss.BranchName = &branchName })
	s.mu.Unlock()
	s.persist()
	return nil
}

// CreateIssue implements tracker.Tracker; the new issue joins the store.
func (s *Store) CreateIssue(ctx context.Context, sourceIssueID string, in tracker.NewIssue) (*domain.Issue, error) {
	iss, err := s.up.CreateIssue(ctx, sourceIssueID, in)
	// An adapter returns the issue with an error when it was created but a
	// follow-up write (blocked_by_source) failed: it exists, so it joins.
	if iss != nil {
		s.mu.Lock()
		s.gen++
		s.touched[iss.ID] = s.gen
		s.placeLocked(*iss)
		s.mu.Unlock()
		s.persist()
	}
	return iss, err
}

// CreateComment implements tracker.Tracker.
func (s *Store) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	return s.up.CreateComment(ctx, issueID, body)
}

// CreateCommentWithKey implements tracker.IdempotentCommenter.
func (s *Store) CreateCommentWithKey(ctx context.Context, issueID, key, body string) (*domain.Comment, error) {
	return s.up.(tracker.IdempotentCommenter).CreateCommentWithKey(ctx, issueID, key, body)
}

// FindCommentByKey implements tracker.IdempotentCommenter.
func (s *Store) FindCommentByKey(ctx context.Context, issueID, key string) (*domain.Comment, bool, error) {
	return s.up.(tracker.IdempotentCommenter).FindCommentByKey(ctx, issueID, key)
}

// RateLimitSnapshot implements tracker.RateLimiter.
func (s *Store) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	return s.up.(tracker.RateLimiter).RateLimitSnapshot()
}

// SetStateLists implements tracker.StateListSetter. Which issues are
// candidates is the adapter's answer, so the store pulls again before the
// next read.
func (s *Store) SetStateLists(active, terminal []string) {
	s.up.(tracker.StateListSetter).SetStateLists(active, terminal)
	s.invalidate(active)
}

// invalidate makes the next read pull again; active, when non-nil, is the
// new active-state list.
func (s *Store) invalidate(active []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active != nil {
		s.active = slices.Clone(active)
	}
	s.synced = false
	s.scope++
	s.cursor = time.Time{} // another scope: the next pull is full
}

// linearStore is a Store over an adapter that also batches detail reads and
// scopes by project (Linear).
type linearStore struct{ *Store }

// FetchIssueDetailsByIDs implements tracker.DetailBatcher from the tracker.
func (l *linearStore) FetchIssueDetailsByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	issues, err := l.up.(tracker.DetailBatcher).FetchIssueDetailsByIDs(ctx, issueIDs)
	if err == nil {
		for i := range issues {
			l.refresh(&issues[i])
		}
	}
	return issues, err
}

// FetchProjects implements tracker.ProjectManager.
func (l *linearStore) FetchProjects(ctx context.Context) ([]domain.Project, error) {
	return l.up.(tracker.ProjectManager).FetchProjects(ctx)
}

// GetProjectFilter implements tracker.ProjectManager.
func (l *linearStore) GetProjectFilter() []string {
	return l.up.(tracker.ProjectManager).GetProjectFilter()
}

// SetProjectFilter implements tracker.ProjectManager. Another project means
// other issues, so the store pulls again before the next read.
func (l *linearStore) SetProjectFilter(slugs []string) {
	l.up.(tracker.ProjectManager).SetProjectFilter(slugs)
	l.invalidate(nil)
}

// scopedStore is a linearStore over an adapter whose scope combines a
// project, an assignee and labels (#182). The scope itself lives in the
// adapter; another scope means other issues, so SetScope makes the store
// pull again, like SetProjectFilter.
type scopedStore struct {
	*linearStore
	filterForward
}

var _ tracker.ScopeManager = (*scopedStore)(nil)

func (a *scopedStore) sm() tracker.ScopeManager { return a.up.(tracker.ScopeManager) }

// Scope implements tracker.ScopeManager.
func (a *scopedStore) Scope() tracker.Scope { return a.sm().Scope() }

// SetScope implements tracker.ScopeManager.
func (a *scopedStore) SetScope(sc tracker.Scope) {
	a.sm().SetScope(sc)
	a.invalidate(nil)
}

// ResolveUser implements tracker.ScopeManager.
func (a *scopedStore) ResolveUser(ctx context.Context, spec string) (tracker.AssigneeUser, error) {
	return a.sm().ResolveUser(ctx, spec)
}

// ResolveAssignee implements tracker.ScopeManager.
func (a *scopedStore) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	return a.sm().ResolveAssignee(ctx)
}

// ResolvedAssignee implements tracker.ScopeManager.
func (a *scopedStore) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	return a.sm().ResolvedAssignee()
}

// FetchLabels implements tracker.ScopeManager.
func (a *scopedStore) FetchLabels(ctx context.Context) ([]string, error) {
	return a.sm().FetchLabels(ctx)
}

// FetchUsers implements tracker.ScopeManager.
func (a *scopedStore) FetchUsers(ctx context.Context) ([]tracker.AssigneeUser, error) {
	return a.sm().FetchUsers(ctx)
}

// ScopeKey implements tracker.ScopeManager.
func (a *scopedStore) ScopeKey() string { return a.sm().ScopeKey() }

// ScopeLabel implements tracker.ScopeManager.
func (a *scopedStore) ScopeLabel() string { return a.sm().ScopeLabel() }
