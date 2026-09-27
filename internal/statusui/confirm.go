package statusui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// CORE-020 — y/N confirm for the destructive D (discard paused) and S (stop
// running) keys. The pending action lives in Model.confirmKind/confirmID; the
// key handlers in model.go arm it and resolveConfirm answers it.

// discardTargetDescription states, from the configured tracker states, what
// the orchestrator will do with a stopped or discarded issue. It mirrors
// Orchestrator.asyncDiscardAndTransition: the target is the first backlog
// state, else the first active state; the claim is released either way, so
// the issue is re-dispatched when the target is active or the tracker move
// fails. Never promises manual-only recovery (CORE-020).
func (m Model) discardTargetDescription() string {
	switch {
	case len(m.cfg.BacklogStates) > 0:
		return fmt.Sprintf("moved to %q (re-dispatch possible if the move fails)", m.cfg.BacklogStates[0])
	case len(m.cfg.TodoStates) > 0:
		return fmt.Sprintf("moved to %q — an active state, so it will be re-dispatched", m.cfg.TodoStates[0])
	default:
		return "left in its current state and may be re-dispatched"
	}
}

// resolveConfirm consumes the key that answers a pending D/S confirm. It
// returns false only for ctrl+c, which cancels the confirm and then falls
// through to the normal quit handling.
func (m *Model) resolveConfirm(msg tea.KeyMsg) bool {
	kind, id := m.confirmKind, m.confirmID
	m.confirmKind, m.confirmID = "", ""
	verb := "discarded"
	if kind == "stop" {
		verb = "stopped"
	}
	switch msg.String() {
	case "y", "Y":
	case "ctrl+c":
		return false
	default:
		m.killMsg = "↩ Cancelled — nothing " + verb
		return true
	}
	if m.cfg.TerminateIssue == nil {
		return true
	}
	ok := m.cfg.TerminateIssue(id)
	switch {
	case kind == "stop" && ok:
		m.killMsg = "✕ Stopped " + id
	case kind == "stop":
		m.killMsg = "✗ Could not stop " + id
	case ok:
		m.killMsg = "✕ Discarded " + id
		if m.pausedCursor >= len(m.paused)-1 {
			m.inPausedSection = false
			m.pausedCursor = 0
		}
	default:
		m.killMsg = "✗ Could not discard " + id
	}
	return true
}
