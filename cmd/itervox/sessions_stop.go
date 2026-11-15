package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// Stopping sessions from the Sessions panel (POST /api/v1/sessions/{id}/stop).
//
//   - A tmux session itervox started (or re-adopted): the CLI's interrupt
//     keys through tmuxctl, a wait of up to stopWait for the session to
//     end, then `tmux kill-session` if it is still there. force skips
//     straight to kill-session.
//   - A hand-started session in a controllable tmux pane
//     (sessions.tmux_control): the interrupt keys only — itervox never
//     kills a tmux session or pane it did not start. Force is refused.
//   - Any other hand-started session: refused (not_controllable); the
//     panel tells the operator to stop it in its own terminal.
//
// Everything here runs on the HTTP handler's goroutine; ownership changes
// reach the event loop only through EndTmuxSession.

const (
	defaultStopWait = 5 * time.Second
	stopPollEvery   = 150 * time.Millisecond
	// claudeInterruptGap separates Claude Code's two Ctrl-C presses: the
	// first clears the prompt and arms "press Ctrl-C again to exit".
	claudeInterruptGap = 400 * time.Millisecond
)

// Stop modes and outcomes (logged, fed and returned).
const (
	stopModeGraceful = "graceful"
	stopModeForce    = "force"

	stopOutcomeStopped     = "stopped"
	stopOutcomeKilled      = "killed"
	stopOutcomeInterrupted = "interrupted"
)

// interruptKeys are the keys that ask backend's interactive CLI to quit:
// Claude Code exits on a second Ctrl-C, Codex on one.
func interruptKeys(backend string) []string {
	if backend == "claude" {
		return []string{"C-c", "C-c"}
	}
	return []string{"C-c"}
}

func (t *tmuxSessions) stopWaitOrDefault() time.Duration {
	if t.stopWait > 0 {
		return t.stopWait
	}
	return defaultStopWait
}

// stop stops session id (see the file comment).
func (t *tmuxSessions) stop(id string, force bool) (server.SessionStopResult, error) {
	if own, ok := t.orch.FindTmuxSession(id); ok {
		if !t.detect.refresh().Available {
			return server.SessionStopResult{}, t.unavailable()
		}
		if force {
			return t.result(id, stopOutcomeKilled, t.killOwned(own, stopModeForce))
		}
		return t.stopOwned(own)
	}
	ext, ok := t.orch.TranscriptSession(id)
	if !ok {
		return server.SessionStopResult{}, server.ErrSessionNotFound
	}
	if force {
		return server.SessionStopResult{}, server.ErrSessionNotOwned
	}
	return t.stopExternal(ext)
}

func (t *tmuxSessions) result(id, outcome string, err error) (server.SessionStopResult, error) {
	if err != nil {
		return server.SessionStopResult{}, err
	}
	return server.SessionStopResult{ID: id, Outcome: outcome}, nil
}

// stopOwned interrupts one of itervox's sessions and kills it if it is
// still there after the wait.
func (t *tmuxSessions) stopOwned(own orchestrator.TmuxSession) (server.SessionStopResult, error) {
	wait := t.stopWaitOrDefault()
	ctx, cancel := context.WithTimeout(context.Background(), wait+3*tmuxctl.DefaultTimeout)
	defer cancel()
	pane, _, err := t.target(ctx, own.ID)
	switch {
	case errors.Is(err, server.ErrPaneGone): // no pane to interrupt: make sure it is gone
		return t.result(own.ID, stopOutcomeKilled, t.killOwned(own, stopModeForce))
	case err != nil:
		return server.SessionStopResult{}, err
	}
	if err := t.pressInterrupt(ctx, own.ID, pane, own.Backend); err != nil {
		return server.SessionStopResult{}, err
	}
	gone := t.waitFor(ctx, wait, func() bool {
		has, err := t.tmux().HasSession(ctx, own.ID)
		return err == nil && !has
	})
	if !gone {
		return t.result(own.ID, stopOutcomeKilled, t.killOwned(own, stopModeGraceful))
	}
	t.ended(own, stopModeGraceful, stopOutcomeStopped)
	return server.SessionStopResult{ID: own.ID, Outcome: stopOutcomeStopped}, nil
}

// stopExternal presses the interrupt keys in a hand-started session's
// controllable pane and reports whether its CLI quit. Never kills.
func (t *tmuxSessions) stopExternal(ext extsessions.Session) (server.SessionStopResult, error) {
	wait := t.stopWaitOrDefault()
	ctx, cancel := context.WithTimeout(context.Background(), wait+3*tmuxctl.DefaultTimeout)
	defer cancel()
	pane, _, err := t.target(ctx, ext.ID)
	switch {
	case errors.Is(err, errNotControllable):
		return server.SessionStopResult{}, server.ErrNotControllable
	case err != nil:
		return server.SessionStopResult{}, err
	}
	if err := t.pressInterrupt(ctx, ext.ID, pane, ext.Backend); err != nil {
		return server.SessionStopResult{}, err
	}
	quit := t.waitFor(ctx, wait, func() bool {
		procs, err := t.listProcesses()
		if err != nil {
			return false
		}
		_, alive := extsessions.FindProcess(procs, ext.PID)
		return !alive
	})
	outcome := stopOutcomeInterrupted
	if quit {
		outcome = stopOutcomeStopped
	}
	t.stopped(ext.ID, ext.Backend, stopModeGraceful, outcome)
	return server.SessionStopResult{ID: ext.ID, Outcome: outcome}, nil
}

// pressInterrupt sends backend's interrupt keys into pane, audited.
func (t *tmuxSessions) pressInterrupt(ctx context.Context, id string, pane tmuxctl.Pane, backend string) error {
	for i, key := range interruptKeys(backend) {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(claudeInterruptGap):
			}
		}
		if err := t.tmux().SendKey(ctx, pane.ID, key); err != nil {
			if i > 0 && errors.Is(err, tmuxctl.ErrNoSuchPane) {
				return nil // the CLI quit on the earlier key: its pane closed
			}
			return tmuxWriteError(err)
		}
		audit(id, pane, "key", len(key))
	}
	return nil
}

// waitFor polls done until it holds or wait elapses.
func (t *tmuxSessions) waitFor(ctx context.Context, wait time.Duration, done func() bool) bool {
	deadline := time.Now().Add(wait)
	for {
		if done() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(stopPollEvery):
		}
	}
}

// killOwned runs tmux kill-session on one of itervox's sessions and
// forgets it.
func (t *tmuxSessions) killOwned(own orchestrator.TmuxSession, mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxctl.DefaultTimeout)
	defer cancel()
	if err := t.tmux().KillSession(ctx, own.ID); err != nil && !errors.Is(err, tmuxctl.ErrNoSuchPane) {
		return fmt.Errorf("cmd/itervox: tmux kill-session: %w", err)
	}
	t.ended(own, mode, stopOutcomeKilled)
	return nil
}

// ended forgets one of itervox's sessions once tmux no longer runs it.
func (t *tmuxSessions) ended(own orchestrator.TmuxSession, mode, outcome string) {
	t.orch.EndTmuxSession(own.ID)
	t.guard.release(own.ID) // #204
	t.stopped(own.ID, own.Backend, mode, outcome)
}

// stopped audits a stop: a log line and a feed event (never pane content).
func (t *tmuxSessions) stopped(id, backend, mode, outcome string) {
	slog.Info("sessions: session stopped", "session_id", id, "backend", backend, "mode", mode, "outcome", outcome)
	t.orch.RecordFeedEvent(orchestrator.FeedEvent{Kind: orchestrator.FeedSessionStopped, Identifier: id,
		Payload: orchestrator.FeedPayload{Backend: backend, Outcome: outcome, Reason: mode, Via: "dashboard"}})
}
