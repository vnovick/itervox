package store

import (
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
)

// Pending local writes (#115). With the write-ahead outbox batching tracker
// writes for a push interval, a state move Itervox made can sit in the
// outbox for a while before the tracker sees it. The store lays those moves
// over its copy on every list read, so the board, the orchestrator's tick and
// every other list reader see the issue where Itervox put it at once, and a
// restart (which reloads the outbox) sees it again.
//
// The overlay is read-only and lives only in the answer: the store's own
// copy stays the tracker's (TrackerCopy). That split is load-bearing. The
// outbox reconciles a queued move against the tracker's state ("already
// applied" drops it, "a person moved it since" drops it too); comparing it
// against the overlaid state would always read "already applied" and drop
// every queued move before it was pushed.

// SetPendingStates installs the source of the queued, unsent state moves:
// fn returns the target state by issue ID (outbox.Outbox.PendingStates). It
// is called once per list read, never under the store's lock. nil removes
// the overlay.
func (s *Store) SetPendingStates(fn func() map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = fn
}

// pendingStates returns the queued state moves, or nil.
func (s *Store) pendingStates() map[string]string {
	s.mu.Lock()
	fn := s.pending
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// TrackerCopy returns the store's copy of the issue with id as the tracker
// last reported it (or as an Itervox write that reached the tracker left
// it), without the pending overlay. found is false when the store does not
// hold the issue.
func (s *Store) TrackerCopy(id string) (iss domain.Issue, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, found = s.issues[id]
	return iss, found
}

// overlayLocked lists ids (a candidate list or a view whose member states
// are states) with pending laid over: an issue with a queued move shows the
// move's target state, leaves the list when that state is not a member, and
// joins it from outside when it is. An issue whose queued target is its
// tracker state keeps the membership the tracker reported. Caller holds mu.
func (s *Store) overlayLocked(ids []string, pending map[string]string, states []string) []domain.Issue {
	if len(pending) == 0 {
		return s.listLocked(ids)
	}
	out := make([]domain.Issue, 0, len(ids))
	listed := make(map[string]bool, len(ids))
	for _, id := range ids {
		iss, ok := s.issues[id]
		if !ok {
			continue
		}
		listed[id] = true
		if target, queued := pending[id]; queued && !strings.EqualFold(target, iss.State) {
			if !containsFold(states, target) {
				continue
			}
			iss.State = target
		}
		out = append(out, iss)
	}
	joined := make([]string, 0)
	for id, target := range pending {
		if listed[id] || !containsFold(states, target) {
			continue
		}
		if iss, ok := s.issues[id]; ok && !strings.EqualFold(target, iss.State) {
			joined = append(joined, id)
		}
	}
	slices.Sort(joined) // map order is random; keep answers stable
	for _, id := range joined {
		iss := s.issues[id]
		iss.State = pending[id]
		out = append(out, iss)
	}
	return out
}
