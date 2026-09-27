package orchestrator

import "log/slog"

// warnIfQuestionStuck raises the input-required wait from Debug to a single
// Warn when the entry's question is still in the outbox and has become
// Degraded (CORE-123). The outbox never gives up on a failed write and its
// Due is per-issue FIFO head-only, so a question the tracker keeps rejecting
// (a locked or deleted issue, a revoked token scope) blocks the reply and the
// completion transition queued behind it until an operator discards it. It is
// deliberately NOT dropped automatically: a 4xx is also what an operator-
// fixable cause (token scope, permissions) returns, and dropping would lose
// the write for good. What the operator lacked was the signal.
//
// Runs on the event loop only; QuestionStuckWarned is event-loop state.
func (o *Orchestrator) warnIfQuestionStuck(identifier string, entry *InputRequiredEntry) {
	if entry == nil || entry.QuestionStuckWarned || entry.QuestionCommentKey == "" || o.outbox == nil {
		return
	}
	for _, queued := range o.outbox.Snapshot() {
		if queued.CommentKey != entry.QuestionCommentKey {
			continue
		}
		if !queued.Degraded() {
			return
		}
		entry.QuestionStuckWarned = true
		slog.Warn("orchestrator: input-required question is stuck in the outbox; "+
			"the issue's later tracker writes are waiting behind it — retry or discard it from the Outbox panel",
			"identifier", identifier, "outbox_id", queued.ID, "attempts", queued.Attempts,
			"last_error", queued.LastError)
		return
	}
}
