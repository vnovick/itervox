package main

import (
	"context"
	"errors"
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// dispatchAdapter backs the #213 feed and dispatch pause endpoints with the
// orchestrator. The feed is read from the published snapshot (never cfgMu);
// a pause or resume is a round-trip through the event loop.
type dispatchAdapter struct {
	orch *orchestrator.Orchestrator
}

func (d dispatchAdapter) Feed(since int64, limit int) server.FeedPage {
	return feedPageRow(d.orch.Feed(since, limit))
}

func (d dispatchAdapter) PauseDispatch(ctx context.Context, reason string, until time.Time) (*server.DispatchPauseRow, error) {
	p, err := d.orch.PauseDispatch(ctx, reason, until)
	return dispatchPauseRow(p), mapDispatchPauseErr(err)
}

func (d dispatchAdapter) ResumeDispatch(ctx context.Context) (*server.DispatchPauseRow, error) {
	p, err := d.orch.ResumeDispatch(ctx)
	return dispatchPauseRow(p), mapDispatchPauseErr(err)
}

func mapDispatchPauseErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, orchestrator.ErrPauseUntilPast):
		return server.ErrPauseUntilPast
	default:
		return server.ErrBusy
	}
}

// dispatchPauseRow is the snapshot / API row of the pause: nil when dispatch
// is not paused.
func dispatchPauseRow(p orchestrator.DispatchPause) *server.DispatchPauseRow {
	if !p.Active {
		return nil
	}
	row := &server.DispatchPauseRow{Reason: p.Reason, PausedAt: p.PausedAt}
	if !p.Until.IsZero() {
		until := p.Until
		row.Until = &until
	}
	return row
}

func feedPageRow(p orchestrator.FeedPage) server.FeedPage {
	out := server.FeedPage{
		Events:  make([]server.FeedEvent, 0, len(p.Events)),
		NextSeq: p.NextSeq, HeadSeq: p.HeadSeq, OldestSeq: p.OldestSeq,
		Truncated: p.Truncated, HasMore: p.HasMore,
	}
	for _, ev := range p.Events {
		out.Events = append(out.Events, server.FeedEvent{
			Seq: ev.Seq, TS: ev.TS, Kind: ev.Kind, Identifier: ev.Identifier,
			Payload: server.FeedPayload(ev.Payload),
		})
	}
	return out
}
