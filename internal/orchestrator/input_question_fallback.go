package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"
)

// EventInputQuestionFallback carries the outcome of a keyless direct post of
// an input-required question whose outbox enqueue failed (CORE-121). Its
// InputRequiredEntry is a result carrier, not a new entry: QuestionCommentKey
// names the round the post belongs to, and QuestionCommentID/Author describe
// the comment the tracker created (empty when Error is set).
const EventInputQuestionFallback EventType = "InputQuestionFallback"

// questionFallbackSendBound bounds how long the fallback goroutine waits to
// hand its result to the event loop. Losing the event is safe — the entry
// keeps its key, which is exactly the "fallback failed" outcome.
const questionFallbackSendBound = 30 * time.Second

// postQuestionKeyless is the recovery for a question the outbox refused
// (persist failure: disk full, read-only filesystem, .itervox removed or
// unwritable). The entry's key is authoritative for reply detection and no
// tracker comment will ever carry it, so without this the question never
// reaches the tracker and a tracker reply never resumes the agent.
//
// The post is network I/O, so it runs off the event loop as a
// commentWg-tracked goroutine and reports back through
// EventInputQuestionFallback; only the event loop updates the entry. It calls
// the tracker directly and keyless: the outbox is what just failed, and a key
// is only safe when a durable retry stands behind it (see
// newInputRequiredCommentKey).
func (o *Orchestrator) postQuestionKeyless(issueID, identifier, key, body string, enqueueErr error) {
	slog.Warn("orchestrator: could not enqueue input-required question, posting it directly without a key",
		"identifier", identifier, "comment_key", key, "error", enqueueErr)
	goSafe(&o.commentWg, "input-required-question-fallback", identifier, func() {
		postCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
		defer cancel()
		result := &InputRequiredEntry{IssueID: issueID, Identifier: identifier, QuestionCommentKey: key}
		comment, err := o.tracker.CreateComment(postCtx, issueID, body)
		if err == nil && (comment == nil || comment.ID == "") {
			err = errors.New("orchestrator: tracker returned no comment id for the fallback question")
		}
		if err == nil {
			result.QuestionCommentID = comment.ID
			result.QuestionAuthorID = comment.AuthorID
			result.QuestionAuthorName = comment.AuthorName
		}
		o.sendQuestionFallback(OrchestratorEvent{
			Type: EventInputQuestionFallback, IssueID: issueID, Identifier: identifier,
			InputRequiredEntry: result, Error: err,
		})
	}, o.withPanicFailure("input-required-question-fallback", identifier, nil))
}

// sendQuestionFallback hands a fallback post's outcome to the event loop.
//
// Shutdown is checked BEFORE the send (BH5). Once Run's context is done the
// loop never reads o.events again, but a buffered channel with room still
// accepts the send, and a select with both cases ready picks one at random —
// so the event could vanish into an unread buffer with nothing logged, while
// the persisted entry kept a key that the real (keyless) comment never
// carries. On shutdown the result is instead logged with the comment id and,
// when the post succeeded, written into the input-required ledger directly.
func (o *Orchestrator) sendQuestionFallback(ev OrchestratorEvent) {
	shutdown := context.Background()
	if p := o.runCtx.Load(); p != nil {
		shutdown = *p
	}
	if shutdown.Err() != nil {
		o.dropQuestionFallbackAtShutdown(ev)
		return
	}
	timer := time.NewTimer(questionFallbackSendBound)
	defer timer.Stop()
	select {
	case o.events <- ev:
	case <-shutdown.Done():
		o.dropQuestionFallbackAtShutdown(ev)
	case <-timer.C:
		slog.Warn("orchestrator: input-required fallback result dropped, event queue full; the question keeps its key",
			"identifier", ev.Identifier, "comment_id", fallbackCommentID(ev), "error", ev.Error)
	}
}

func fallbackCommentID(ev OrchestratorEvent) string {
	if ev.Error != nil || ev.InputRequiredEntry == nil {
		return ""
	}
	return ev.InputRequiredEntry.QuestionCommentID
}

// dropQuestionFallbackAtShutdown handles a fallback result that arrived after
// the event loop stopped reading events. A failed post changes nothing (the
// entry keeps its key, as it would on the loop). A successful post is written
// into the persisted input-required ledger synchronously, so the next
// generation or restart matches replies by the comment id instead of a key no
// tracker comment carries. It waits for Run's loop exit and shutdown flush
// first so the loop's last ledger version cannot land after the correction;
// Run joins commentWg after that point, so the correction lands before Run
// returns and before a reload loads the ledger.
func (o *Orchestrator) dropQuestionFallbackAtShutdown(ev OrchestratorEvent) {
	id := fallbackCommentID(ev)
	if id == "" {
		slog.Warn("orchestrator: input-required fallback result dropped at shutdown; the question keeps its key",
			"identifier", ev.Identifier, "error", ev.Error)
		return
	}
	if p := o.loopExited.Load(); p != nil {
		<-*p
	}
	corrected, err := o.correctPersistedQuestion(ev.Identifier, ev.InputRequiredEntry)
	switch {
	case err != nil:
		slog.Error("orchestrator: input-required fallback posted but the ledger could not be corrected at shutdown; "+
			"the question keeps its key and tracker replies cannot be matched — answer from the dashboard or dismiss",
			"identifier", ev.Identifier, "comment_id", id, "error", err)
	case corrected:
		slog.Warn("orchestrator: input-required fallback result arrived at shutdown; ledger corrected to match the posted comment",
			"identifier", ev.Identifier, "comment_id", id)
	default:
		slog.Warn("orchestrator: input-required fallback result dropped at shutdown; the entry was answered, dismissed or superseded",
			"identifier", ev.Identifier, "comment_id", id)
	}
}

// correctPersistedQuestion applies handleInputQuestionFallback's success
// transition to the persisted input-required ledger: the awaiting entry whose
// key matches loses the key and takes the posted comment's id. It reports
// whether an entry matched. Only valid once the event loop has exited.
func (o *Orchestrator) correctPersistedQuestion(identifier string, res *InputRequiredEntry) (bool, error) {
	o.inputRequiredMu.RLock()
	path := o.inputRequiredFile
	o.inputRequiredMu.RUnlock()
	if path == "" {
		return false, nil
	}
	w := o.ledger(ledgerInputRequired)
	data := w.lastData()
	if data == nil {
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		data = raw
	}
	var disk inputRequiredStateDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return false, err
	}
	entry, ok := disk.Awaiting[identifier]
	if !ok || entry.QuestionCommentKey == "" || entry.QuestionCommentKey != res.QuestionCommentKey {
		return false, nil
	}
	entry.QuestionCommentKey = ""
	entry.QuestionCommentID = res.QuestionCommentID
	entry.QuestionAuthorID = res.QuestionAuthorID
	entry.QuestionAuthorName = res.QuestionAuthorName
	disk.Awaiting[identifier] = entry
	out, err := json.Marshal(disk)
	if err != nil {
		return false, err
	}
	if w.submit(path, out, 0o644) {
		if err := w.drain(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// handleInputQuestionFallback applies a fallback post's outcome on the event
// loop. The key is cleared ONLY when the tracker confirmed the keyless post,
// and then the comment id takes over as the exact match. On failure the key
// stays: clearing it would drop reply detection back to the latest Itervox
// question on the issue, which may be a previous round's, and resume the
// agent with the answer to that older question.
func (o *Orchestrator) handleInputQuestionFallback(state State, ev OrchestratorEvent) State {
	res := ev.InputRequiredEntry
	entry := state.InputRequiredIssues[ev.Identifier]
	if res == nil || entry == nil || entry.QuestionCommentKey == "" || entry.QuestionCommentKey != res.QuestionCommentKey {
		// Answered, dismissed, or superseded by a later round meanwhile.
		return state
	}
	if ev.Error != nil {
		slog.Warn("orchestrator: input-required question was never posted to the tracker; "+
			"tracker replies cannot be matched to it — answer from the dashboard or dismiss",
			"identifier", ev.Identifier, "comment_key", entry.QuestionCommentKey, "error", ev.Error)
		return state
	}
	updated := *entry
	updated.QuestionCommentKey = ""
	updated.QuestionCommentID = res.QuestionCommentID
	updated.QuestionAuthorID = res.QuestionAuthorID
	updated.QuestionAuthorName = res.QuestionAuthorName
	state.InputRequiredIssues[ev.Identifier] = &updated
	slog.Info("orchestrator: input-required question posted directly after the outbox refused it",
		"identifier", ev.Identifier, "comment_id", updated.QuestionCommentID)
	return state
}
