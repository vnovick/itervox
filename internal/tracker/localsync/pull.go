package localsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// The pull side of a cycle: which remote issues to read, and which fields
// have a remote write still queued.

// pull reads the remote issues that may have changed since the last cycle,
// with their comments. It returns them by ID, the cursor to keep after a
// clean cycle, and whether this was a full pull.
func (s *Syncer) pull(ctx context.Context, now time.Time) (map[string]domain.Issue, time.Time, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	inc, isInc := s.cfg.Remote.(tracker.IncrementalFetcher)
	full := !isInc || s.lg.Cursor.IsZero() || now.Sub(s.lg.LastFullAt) >= s.cfg.FullInterval
	var listed []domain.Issue
	var err error
	if full {
		listed, err = s.cfg.Remote.FetchIssuesByStates(cctx, s.cfg.PullStates)
	} else {
		listed, _, err = inc.FetchIssuesUpdatedSince(cctx, s.lg.Cursor.Add(-cursorOverlap))
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("local sync: pull from %s: %w", s.cfg.Kind, err)
	}
	cursor := s.lg.Cursor
	seen := map[string]bool{}
	var want []string
	for _, is := range listed {
		seen[is.ID] = true
		if is.UpdatedAt != nil && is.UpdatedAt.After(cursor) {
			cursor = *is.UpdatedAt
		}
		l := s.lg.byRemoteID(is.ID)
		switch {
		case l == nil && !s.inPullStates(is.State):
			continue // not linked and not to be pulled
		case l != nil && l.Detached:
			continue
		case l != nil && is.UpdatedAt != nil && !is.UpdatedAt.After(l.RemoteUpdatedAt):
			continue // unchanged since the last cycle read it
		}
		want = append(want, is.ID)
	}
	if full {
		// A linked issue the listing left out may have moved out of the
		// pulled states; read it so that move reaches its file.
		for _, l := range s.lg.Links {
			if l.RemoteID != "" && !l.Detached && !seen[l.RemoteID] {
				want = append(want, l.RemoteID)
			}
		}
	}
	details, err := s.fetchDetails(ctx, want)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	for _, is := range details {
		if is.UpdatedAt != nil && is.UpdatedAt.After(cursor) {
			cursor = *is.UpdatedAt
		}
	}
	return details, cursor, full, nil
}

// fetchDetails reads issues with their comments, batched when the tracker
// can. An issue missing from the answer is skipped this cycle.
func (s *Syncer) fetchDetails(ctx context.Context, ids []string) (map[string]domain.Issue, error) {
	out := map[string]domain.Issue{}
	if len(ids) == 0 {
		return out, nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if b, ok := s.cfg.Remote.(tracker.DetailBatcher); ok {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		got, err := b.FetchIssueDetailsByIDs(cctx, ids)
		if err != nil {
			return nil, fmt.Errorf("local sync: read issues from %s: %w", s.cfg.Kind, err)
		}
		for _, is := range got {
			out[is.ID] = is
		}
		return out, nil
	}
	for _, id := range ids {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		is, err := s.cfg.Remote.FetchIssueDetail(cctx, id)
		cancel()
		var nf *tracker.NotFoundError
		switch {
		case errors.As(err, &nf):
			continue
		case err != nil:
			return nil, fmt.Errorf("local sync: read %s from %s: %w", id, s.cfg.Kind, err)
		case is != nil:
			out[is.ID] = *is
		}
	}
	return out, nil
}

func (s *Syncer) inPullStates(state string) bool {
	return slices.ContainsFunc(s.cfg.PullStates, func(p string) bool {
		return strings.EqualFold(strings.TrimSpace(p), strings.TrimSpace(state))
	})
}

// pendingFields lists, per remote issue ID, the fields with a write still
// queued: the remote does not show them yet, so they are not compared.
func (s *Syncer) pendingFields() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	mark := func(id, field string) {
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][field] = true
	}
	for _, e := range s.cfg.Outbox.Snapshot() {
		switch e.Kind {
		case kindUpdateState:
			mark(e.IssueID, fieldState)
		case kindUpdateFields:
			for _, f := range patchFields(e.Payload) {
				mark(e.IssueID, f)
			}
		}
	}
	return out
}
