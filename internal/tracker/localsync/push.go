package localsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// Remote writes go through the sync's write-ahead outbox: queued durably
// (with the coalesced push of #115: within a push interval an issue's field
// edits merge and only its last state move is sent), then delivered by Push,
// which retries a failed write with backoff and never drops one.

const (
	kindUpdateState   = outbox.KindUpdateState
	kindCreateComment = outbox.KindCreateComment
	kindCreateIssue   = outbox.KindCreateIssue
	kindUpdateFields  = outbox.KindUpdateFields
)

// createPayload is a create_issue entry's payload.
type createPayload struct {
	Local string             `json:"local"`
	Draft tracker.IssueDraft `json:"draft"`
}

// patchFields names the fields a queued update_fields payload sets.
func patchFields(payload string) []string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &m) != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Syncer) enqueueCreate(localID string, draft tracker.IssueDraft) (string, error) {
	key, err := outbox.NewCommentKey()
	if err != nil {
		return "", err
	}
	draft.Key = key
	payload, err := json.Marshal(createPayload{Local: localID, Draft: draft})
	if err != nil {
		return "", fmt.Errorf("local sync: %w", err)
	}
	err = s.cfg.Outbox.Enqueue(outbox.Entry{Kind: kindCreateIssue, IssueID: "local:" + localID, Identifier: localID,
		Payload: string(payload), CommentKey: key})
	return key, err
}

// enqueueEdits queues a field patch and a state move for l's remote issue.
func (s *Syncer) enqueueEdits(l *link, patch tracker.IssueFieldsPatch, state, fromState string) error {
	if !patch.Empty() {
		payload, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("local sync: %w", err)
		}
		if err := s.cfg.Outbox.Enqueue(outbox.Entry{Kind: kindUpdateFields, IssueID: l.RemoteID,
			Identifier: l.RemoteIdentifier, Payload: string(payload)}); err != nil {
			return err
		}
	}
	if state != "" {
		return s.cfg.Outbox.Enqueue(outbox.Entry{Kind: kindUpdateState, IssueID: l.RemoteID,
			Identifier: l.RemoteIdentifier, TargetState: state, FromState: fromState})
	}
	return nil
}

// enqueueComment queues a file comment for l's remote issue and returns its
// idempotency key.
func (s *Syncer) enqueueComment(l *link, c local.SyncComment) (string, error) {
	key, err := outbox.NewCommentKey()
	if err != nil {
		return "", err
	}
	body := pushedCommentBody(c)
	if body == "" {
		return key, nil
	}
	return key, s.cfg.Outbox.Enqueue(outbox.Entry{Kind: kindCreateComment, IssueID: l.RemoteID,
		Identifier: l.RemoteIdentifier, Body: body, CommentKey: key})
}

// Push delivers the due remote writes, each issue's in order, and stops as
// soon as the remote tracker's rate-limit gate is open (the rest wait,
// untouched). Not safe to call concurrently with Run or Cycle.
func (s *Syncer) Push(ctx context.Context) {
	gate := tracker.SharedRateLimitGate()
	attempted := map[string]bool{}
	for {
		progressed := false
		for _, e := range s.cfg.Outbox.Due(s.now()) {
			if attempted[e.ID] || ctx.Err() != nil {
				continue
			}
			if _, open := gate.OpenUntil(s.cfg.Kind); open {
				return
			}
			attempted[e.ID] = true
			progressed = true
			s.deliver(ctx, e)
		}
		if !progressed {
			return
		}
	}
}

// deliver sends one queued write and records the outcome.
func (s *Syncer) deliver(ctx context.Context, e outbox.Entry) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var err error
	switch e.Kind {
	case kindUpdateState:
		err = s.cfg.Remote.UpdateIssueState(cctx, e.IssueID, e.TargetState)
	case kindUpdateFields:
		var patch tracker.IssueFieldsPatch
		if err = json.Unmarshal([]byte(e.Payload), &patch); err == nil {
			err = s.editor.UpdateIssueFields(cctx, e.IssueID, patch)
		}
	case kindCreateComment:
		err = s.deliverComment(cctx, e)
	case kindCreateIssue:
		err = s.deliverCreate(cctx, e)
	default:
		err = fmt.Errorf("local sync: queued write has unknown kind %q", e.Kind)
	}
	if errors.Is(err, errAlreadyDelivered) {
		err = nil
	}
	if err != nil {
		s.cfg.Outbox.MarkFailed(e.ID, err, s.now())
		slog.Warn("local sync: remote write failed, will retry", "kind", e.Kind, "identifier", e.Identifier,
			"attempts", e.Attempts+1, "error", err)
		return
	}
	s.cfg.Outbox.MarkFlushed(e.ID)
}

var errAlreadyDelivered = errors.New("local sync: already delivered")

// deliverComment posts a comment, first asking whether an earlier attempt
// already landed when the tracker can answer (the outbox flusher's rule).
func (s *Syncer) deliverComment(ctx context.Context, e outbox.Entry) error {
	ic, ok := s.cfg.Remote.(tracker.IdempotentCommenter)
	if !ok {
		_, err := s.cfg.Remote.CreateComment(ctx, e.IssueID, e.Body)
		return err
	}
	if e.Attempts+e.RateLimitedAttempts > 0 {
		_, found, err := ic.FindCommentByKey(ctx, e.IssueID, e.CommentKey)
		if err != nil {
			return fmt.Errorf("local sync: comment lookup failed, deferring: %w", err)
		}
		if found {
			return errAlreadyDelivered
		}
	}
	_, err := ic.CreateCommentWithKey(ctx, e.IssueID, e.CommentKey, e.Body)
	return err
}

// deliverCreate creates the remote issue for a new file, then links them
// and records the remote identifier in the file.
func (s *Syncer) deliverCreate(ctx context.Context, e outbox.Entry) error {
	var p createPayload
	if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
		return fmt.Errorf("local sync: bad create payload: %w", err)
	}
	var issue *domain.Issue
	if e.Attempts+e.RateLimitedAttempts > 0 {
		found, ok, err := s.editor.FindIssueByKey(ctx, p.Draft.Key)
		if err != nil {
			return fmt.Errorf("local sync: issue lookup failed, deferring: %w", err)
		}
		if ok {
			issue = found
		}
	}
	if issue == nil {
		created, err := s.editor.CreateScopedIssue(ctx, p.Draft)
		if err != nil {
			return err
		}
		issue = created
	}
	s.linkCreated(p.Local, p.Draft.Key, *issue)
	return nil
}

// linkCreated records a landed create in the ledger and the file.
func (s *Syncer) linkCreated(localID, key string, issue domain.Issue) {
	l := s.lg.Links[localID]
	if l == nil || l.CreateKey != key {
		l = newLink(localID) // the ledger was lost meanwhile: link afresh
	}
	l.CreateKey = ""
	l.RemoteID, l.RemoteIdentifier = issue.ID, remoteIdentifier(issue)
	if issue.UpdatedAt != nil {
		l.RemoteUpdatedAt = *issue.UpdatedAt
	}
	s.lg.Links[localID] = l
	ident := l.RemoteIdentifier
	if _, err := s.cfg.Local.ApplySync(localID, local.SyncEdit{Remote: &ident}); err != nil {
		var nf *tracker.NotFoundError
		if errors.As(err, &nf) {
			l.Detached = true
			slog.Info("local sync: remote issue created for a file that was removed since", "identifier", localID, "remote", ident)
		} else {
			// The link is in the ledger; the next cycle writes the key.
			slog.Warn("local sync: cannot record the remote issue in the file yet", "identifier", localID, "remote", ident, "error", err)
		}
	} else if s.cfg.OnLocalChange != nil {
		s.cfg.OnLocalChange()
	}
	slog.Info("local sync: remote issue created", "identifier", localID, "remote", ident)
	s.saveLedger()
}
