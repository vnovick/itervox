package orchestrator

import "sync"

// setPauseReason records why an identifier is paused. Nil-map safe so a State
// assembled outside the loader (tests, older persisted state) cannot panic the
// event loop for the sake of a diagnostic field.
func setPauseReason(state *State, identifier, reason string) {
	if state == nil || identifier == "" || reason == "" {
		return
	}
	if state.PauseReasons == nil {
		state.PauseReasons = make(map[string]string, 1)
	}
	state.PauseReasons[identifier] = reason
}

// clearPauseReason drops the reason when an identifier is unpaused. It must be
// called everywhere PausedIdentifiers is deleted from: a reason outliving its
// pause would report a resumed issue as still paused-for-a-reason, and the map
// would grow for the daemon's lifetime.
func clearPauseReason(state *State, identifier string) {
	if state == nil {
		return
	}
	delete(state.PauseReasons, identifier)
}

// transitionFailedIDs marks identifiers whose completion-state write failed,
// so the event loop can tell that apart from a deliberate user cancel.
//
// The worker cannot set the reason itself: it runs on a worker goroutine and
// State belongs to the event loop (CLAUDE.md's single-goroutine rule). It
// therefore signals through this set, exactly as it already does for user
// cancellation, and the event loop translates it into a reason.
type transitionFailedSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (t *transitionFailedSet) mark(identifier string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ids == nil {
		t.ids = make(map[string]struct{}, 1)
	}
	t.ids[identifier] = struct{}{}
}

// take reports whether identifier was marked, consuming the mark. Consuming
// rather than peeking keeps a stale mark from re-labelling a later, genuine
// user cancel of the same issue.
func (t *transitionFailedSet) take(identifier string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.ids[identifier]
	delete(t.ids, identifier)
	return ok
}

// markTransitionFailed is called from the worker goroutine.
func (o *Orchestrator) markTransitionFailed(identifier string) { o.transitionFailed.mark(identifier) }

// takeTransitionFailed is called from the event loop.
func (o *Orchestrator) takeTransitionFailed(identifier string) bool {
	return o.transitionFailed.take(identifier)
}
