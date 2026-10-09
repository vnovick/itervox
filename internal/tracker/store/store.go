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
//
// A sync is a full pull: FetchCandidateIssues plus FetchIssuesByStates for
// each view. Incremental pulls are #114. The store is written atomically after
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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/domain"
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
	// SyncInterval is how often Run re-pulls.
	SyncInterval time.Duration
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

	synced     bool
	syncedAt   time.Time
	lastErr    error
	active     []string
	issues     map[string]domain.Issue
	candidates []string
	views      map[string]*view
	// gen counts local changes; touched records the gen of each issue's last
	// local change, so a pull that started before it does not undo it.
	gen     int64
	touched map[string]int64
}

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
// tracker.ProjectManager exactly when up does (Linear does, GitHub does not).
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
	if batches && projects {
		return &linearStore{s}, s, nil
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// A cold pull may already have been made by a reader that got
		// there first; it is the one full pull a cold start makes.
		err := s.sync(ctx, cold)
		cold = false
		if err != nil && ctx.Err() == nil {
			slog.Warn("tracker store: sync failed, serving the last pull", "error", err)
		}
		t.Reset(s.cfg.SyncInterval)
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

// ensureSynced pulls once if the store has no data yet.
func (s *Store) ensureSynced(ctx context.Context) error {
	s.mu.Lock()
	ok := s.synced
	s.mu.Unlock()
	if ok {
		return nil
	}
	return s.sync(ctx, true)
}

// sync pulls every view. With onlyIfUnsynced it does nothing when another
// caller pulled while this one waited, which is what makes a cold start one
// pull however many readers race it.
func (s *Store) sync(ctx context.Context, onlyIfUnsynced bool) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	s.mu.Lock()
	if onlyIfUnsynced && s.synced {
		s.mu.Unlock()
		return nil
	}
	startGen := s.gen
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
		s.lastErr = err
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	issues := make(map[string]domain.Issue, len(cands))
	ids := func(list []domain.Issue) []string {
		out := make([]string, 0, len(list))
		for _, iss := range list {
			issues[iss.ID] = iss
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
	// A local change made after this pull began is newer than what it
	// read: put it back.
	for id, g := range s.touched {
		if g <= startGen {
			delete(s.touched, id)
			continue
		}
		if iss, ok := old[id]; ok {
			s.placeLocked(iss)
		}
	}
	s.synced, s.syncedAt, s.lastErr = true, s.now(), nil
	s.mu.Unlock()

	s.persist()
	return nil
}

// fileLocked is the persisted form of the current data. Caller holds mu.
func (s *Store) fileLocked() []byte {
	f := storeFile{
		Version:    fileVersion,
		Scope:      s.cfg.Scope,
		SyncedAt:   s.syncedAt,
		Candidates: s.candidates,
		Issues:     s.issues,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(s.candidates), nil
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
	s.mu.Lock()
	if v, ok := s.views[key]; ok {
		v.LastRead = s.now()
		out := s.listLocked(v.IDs)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	s.mu.Lock()
	startGen := s.gen
	s.mu.Unlock()
	issues, err := s.up.FetchIssuesByStates(ctx, stateNames)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, iss := range issues {
		// A local change made while this fetch ran is newer than it.
		if s.touched[iss.ID] > startGen {
			continue
		}
		s.placeLocked(iss)
	}
	// Registered last: the tracker said these issues match this state set,
	// whatever their state is named (GitHub's "closed").
	v := &view{States: slices.Clone(stateNames), LastRead: s.now()}
	for _, iss := range issues {
		v.IDs = append(v.IDs, iss.ID)
	}
	s.views[key] = v
	return issues, nil
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
func (s *Store) CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error) {
	iss, err := s.up.CreateIssue(ctx, sourceIssueID, title, body, stateName)
	if err == nil && iss != nil {
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
