package localsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// Cycle runs one sync cycle (see the package doc). Exported for tests and
// for callers that drive the sync themselves; Run calls it on its own
// schedule. Not safe to call concurrently with Run or another Cycle.
func (s *Syncer) Cycle(ctx context.Context) error {
	now := s.now()
	remote, cursor, full, err := s.pull(ctx, now)
	if err != nil {
		return err
	}
	locals, err := s.cfg.Local.SyncIssues()
	if err != nil {
		return fmt.Errorf("local sync: read the issue files: %w", err)
	}
	c := &cycle{s: s, ctx: ctx, now: now, remote: remote, pending: s.pendingFields()}
	present := map[string]bool{}
	for _, si := range locals {
		present[si.Identifier] = true
		if si.Remote != "" {
			c.remoteKeys = append(c.remoteKeys, si.Remote)
		}
	}
	s.detachMissing(present)

	var errs []error
	for _, si := range locals {
		if err := c.issue(si); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", si.Identifier, err))
		}
	}
	errs = append(errs, c.pullNew()...)
	if len(errs) == 0 {
		// Advance only after a clean cycle: a change the cycle could not
		// apply must still be listed by the next incremental pull.
		s.lg.Cursor = cursor
		if full {
			s.lg.LastFullAt = now
		}
	}
	s.saveLedger()
	if c.wroteLocal && s.cfg.OnLocalChange != nil {
		s.cfg.OnLocalChange()
	}
	return errors.Join(errs...)
}

// cycle is one Cycle's working state.
type cycle struct {
	s          *Syncer
	ctx        context.Context
	now        time.Time
	remote     map[string]domain.Issue // remote ID → changed issue, with comments
	pending    map[string]map[string]bool
	remoteKeys []string // every `remote:` value in the files
	wroteLocal bool
}

// detachMissing detaches the links whose file is gone: the remote issue is
// left as it is and not pulled back into a new file.
func (s *Syncer) detachMissing(present map[string]bool) {
	for id, l := range s.lg.Links {
		if present[id] || l.Detached {
			continue
		}
		if l.RemoteID == "" {
			if l.CreateKey == "" {
				delete(s.lg.Links, id)
			}
			continue // the create still lands; push.go detaches it then
		}
		slog.Info("local sync: issue file removed; its remote issue is left as it is",
			"identifier", id, "remote", l.RemoteIdentifier)
		l.Detached = true
	}
}

// linked reports whether localID is linked to a remote issue.
func (s *Syncer) linked(localID string) bool {
	l := s.lg.Links[localID]
	return l != nil && l.RemoteID != "" && !l.Detached
}

// localOf maps a remote issue ID to its local identifier.
func (s *Syncer) localOf(remoteID string) (string, bool) {
	if l := s.lg.byRemoteID(remoteID); l != nil && !l.Detached {
		return l.Local, true
	}
	return "", false
}

// remoteIDs maps local identifiers to remote issue IDs, leaving out the
// unlinked ones.
func (s *Syncer) remoteIDs(localIDs []string) []string {
	out := []string{}
	for _, id := range localIDs {
		if s.linked(id) {
			out = append(out, s.lg.Links[id].RemoteID)
		}
	}
	return out
}

// issue syncs one local issue file.
func (c *cycle) issue(si local.SyncIssue) error {
	s := c.s
	l := s.lg.Links[si.Identifier]
	if l != nil && l.Detached && l.RemoteID != "" {
		l.Detached = false // the file is back (a checkout, an undo): sync it again
	}
	switch {
	case l == nil && si.Remote == "":
		return c.createRemote(si)
	case l == nil || (si.Remote != "" && !strings.EqualFold(si.Remote, l.RemoteIdentifier) && l.CreateKey == ""):
		return c.adopt(si)
	case l.CreateKey != "" || l.Detached:
		return nil // the create has not landed yet / not synced any more
	}
	r, fetched := c.remote[l.RemoteID]
	if si.Remote == "" {
		// The `remote:` key was removed from the file: put it back, the
		// link is in the ledger.
		ident := l.RemoteIdentifier
		if _, err := s.cfg.Local.ApplySync(si.Identifier, local.SyncEdit{Remote: &ident}); err != nil {
			return err
		}
	}
	return c.syncFields(si, l, r, fetched, false)
}

// adopt links a file that names its remote issue (`remote:`) but has no
// ledger entry: a cloned repository, a lost ledger, a renamed file. Both
// sides' existing comments are taken as already synced; for each field
// that differs, the more recent side wins without a conflict note.
func (c *cycle) adopt(si local.SyncIssue) error {
	s := c.s
	cctx, cancel := context.WithTimeout(c.ctx, callTimeout)
	r, err := s.cfg.Remote.FetchIssueByIdentifier(cctx, si.Remote)
	cancel()
	var nf *tracker.NotFoundError
	if errors.As(err, &nf) || (err == nil && r == nil) {
		slog.Warn("local sync: the file's remote issue was not found; not syncing it", "identifier", si.Identifier, "remote", si.Remote)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read remote %s: %w", si.Remote, err)
	}
	if other := s.lg.byRemoteID(r.ID); other != nil && other.Local != si.Identifier {
		if _, stillThere := s.lg.Links[other.Local]; stillThere && !other.Detached {
			slog.Warn("local sync: two files name the same remote issue; syncing the first",
				"identifier", si.Identifier, "other", other.Local, "remote", si.Remote)
			return nil
		}
		delete(s.lg.Links, other.Local) // the file was renamed
	}
	l := newLink(si.Identifier)
	l.RemoteID, l.RemoteIdentifier = r.ID, remoteIdentifier(*r)
	for _, cm := range si.Comments {
		l.LocalComments[localCommentHash(cm)] = true
	}
	for _, cm := range r.Comments {
		l.RemoteComments[cm.ID] = true
	}
	s.lg.Links[si.Identifier] = l
	return c.syncFields(si, l, *r, true, true)
}

// createRemote queues the creation of a remote issue for a new file.
func (c *cycle) createRemote(si local.SyncIssue) error {
	s := c.s
	lv := localValues(si, s.linked)
	draft := tracker.IssueDraft{Title: lv.Title, Description: lv.Description, State: s.dl.remoteState(lv.State),
		Labels: si.Labels, BlockedBy: s.remoteIDs(lv.BlockedBy)}
	if lv.Priority != nil {
		p := s.dl.remotePriority(lv.Priority)
		draft.Priority = &p
	}
	key, err := s.enqueueCreate(si.Identifier, draft)
	if err != nil {
		return err
	}
	l := newLink(si.Identifier)
	l.CreateKey = key
	for _, f := range syncedFields {
		l.setBase(f, lv.hash(f), c.now)
	}
	s.lg.Links[si.Identifier] = l
	slog.Info("local sync: creating the remote issue", "identifier", si.Identifier, "tracker", s.cfg.Kind)
	return nil
}

// syncFields carries field and comment changes between a linked file and
// its remote issue. fetched is false when the remote issue did not change
// since the last cycle (it is then not compared). adopting suppresses
// conflict notes.
func (c *cycle) syncFields(si local.SyncIssue, l *link, r domain.Issue, fetched, adopting bool) error {
	s := c.s
	lv := localValues(si, s.linked)
	var rv values
	if fetched {
		rv = s.dl.remoteValues(r, s.localOf)
	}
	pend := c.pending[l.RemoteID]
	localAt, remoteAt := si.ModTime, timeOr(r.UpdatedAt, c.now)

	var (
		edit       local.SyncEdit
		editBases  = map[string]string{}
		patch      tracker.IssueFieldsPatch
		newState   string
		pushBases  = map[string]string{}
		notes      []string
		editLocal  bool
		takeRemote = func(f string) {
			editLocal = true
			editBases[f] = rv.hash(f)
			c.applyRemote(&edit, f, rv, si)
		}
		pushLocal = func(f string) {
			pushBases[f] = lv.hash(f)
			if f == fieldState {
				newState = s.dl.remoteState(lv.State)
				return
			}
			c.addToPatch(&patch, f, lv, si)
		}
	)
	for _, f := range syncedFields {
		base, hasBase := l.Fields[f]
		lh := lv.hash(f)
		lc := !hasBase || lh != base.Hash
		rc, rh := false, ""
		if fetched && rv.known[f] && !pend[f] {
			rh = rv.hash(f)
			rc = !hasBase || rh != base.Hash
		}
		switch {
		case !lc && !rc:
		case lc && !rc:
			if pend[f] && !hasBase {
				continue // adopting while a write is queued: wait for it
			}
			pushLocal(f)
		case !lc && rc:
			takeRemote(f)
		case lh == rh:
			l.setBase(f, lh, c.now) // both sides made the same edit
		case remoteAt.After(localAt):
			takeRemote(f)
			if hasBase && !adopting {
				notes = append(notes, conflictNote(f, "remote", rv, lv, remoteAt, localAt))
			}
		default:
			pushLocal(f)
			if hasBase && !adopting {
				notes = append(notes, conflictNote(f, "local", lv, rv, localAt, remoteAt))
			}
		}
	}

	// Comments: the file's new comments go to the remote; the remote's new
	// comments come into the file. The sync's own posts are recognised by
	// their keys and never come back.
	var pushComments []local.SyncComment
	for _, cm := range si.Comments {
		if !l.LocalComments[localCommentHash(cm)] {
			pushComments = append(pushComments, cm)
		}
	}
	pulledHashes := map[string]bool{}
	var pulledIDs []string
	if fetched {
		for _, rc := range r.Comments {
			if l.RemoteComments[rc.ID] {
				continue
			}
			pulledIDs = append(pulledIDs, rc.ID)
			if l.isEcho(rc) || isMarkerComment(rc.Body) {
				continue
			}
			cm := pulledComment(rc, c.now)
			edit.Comments = append(edit.Comments, cm)
			pulledHashes[localCommentHash(cm)] = true
		}
	}
	for _, n := range notes {
		cm := local.SyncComment{At: c.now, Author: local.ItervoxAuthor, Body: n}
		edit.Comments = append(edit.Comments, cm)
		pulledHashes[localCommentHash(cm)] = true // a local note, never pushed
	}

	if editLocal || len(edit.Comments) > 0 {
		if _, err := s.cfg.Local.ApplySync(si.Identifier, edit); err != nil {
			return err
		}
		c.wroteLocal = true
		for f, h := range editBases {
			l.setBase(f, h, c.now)
		}
		for h := range pulledHashes {
			l.LocalComments[h] = true
		}
		for _, n := range notes {
			slog.Info("local sync: conflict", "identifier", si.Identifier, "note", n)
		}
	}
	for _, id := range pulledIDs {
		l.RemoteComments[id] = true
	}

	if err := s.enqueueEdits(l, patch, newState, r.State); err != nil {
		return err
	}
	for f, h := range pushBases {
		l.setBase(f, h, c.now)
	}
	for _, cm := range pushComments {
		key, err := s.enqueueComment(l, cm)
		if err != nil {
			return err
		}
		l.PushedKeys[key] = true
		l.LocalComments[localCommentHash(cm)] = true
	}
	if fetched && r.UpdatedAt != nil {
		l.RemoteUpdatedAt = *r.UpdatedAt
	}
	return nil
}

// pullNew writes a new file for each changed remote issue in a pulled state
// that no file is linked to.
func (c *cycle) pullNew() []error {
	s := c.s
	ids := make([]string, 0, len(c.remote))
	for id := range c.remote {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range ids {
		r := c.remote[id]
		if s.lg.byRemoteID(id) != nil || !s.inPullStates(r.State) ||
			slices.ContainsFunc(c.remoteKeys, func(k string) bool { return strings.EqualFold(k, r.Identifier) }) {
			continue
		}
		if err := c.pullIssue(r); err != nil {
			errs = append(errs, fmt.Errorf("pull %s: %w", r.Identifier, err))
		}
	}
	return errs
}

// pullIssue writes a new file for remote issue r and links it.
func (c *cycle) pullIssue(r domain.Issue) error {
	s := c.s
	rv := s.dl.remoteValues(r, s.localOf)
	si := local.SyncIssue{Title: rv.Title, State: rv.State, Description: rv.Description, Priority: rv.Priority,
		Labels: rv.Labels, BlockedBy: rv.BlockedBy, Remote: remoteIdentifier(r)}
	l := newLink("")
	for _, rc := range r.Comments {
		l.RemoteComments[rc.ID] = true
		if isMarkerComment(rc.Body) {
			continue
		}
		si.Comments = append(si.Comments, pulledComment(rc, c.now))
	}
	created, err := s.cfg.Local.CreateSyncIssue(si)
	if err != nil {
		return err
	}
	c.wroteLocal = true
	l.Local = created.Identifier
	l.RemoteID, l.RemoteIdentifier = r.ID, remoteIdentifier(r)
	if r.UpdatedAt != nil {
		l.RemoteUpdatedAt = *r.UpdatedAt
	}
	lv := localValues(created, s.linked)
	for _, f := range syncedFields {
		h := lv.hash(f)
		if rv.known[f] {
			h = rv.hash(f)
		}
		l.setBase(f, h, c.now)
	}
	for _, cm := range created.Comments {
		l.LocalComments[localCommentHash(cm)] = true
	}
	s.lg.Links[created.Identifier] = l
	slog.Info("local sync: pulled a remote issue into a new file", "identifier", created.Identifier, "remote", r.Identifier)
	return nil
}
