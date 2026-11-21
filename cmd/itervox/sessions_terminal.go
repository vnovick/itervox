package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// The live terminal (#298): a `tmux attach-session` client in a PTY the
// daemon owns (internal/tmuxctl), behind the server's ticketed WebSocket.
// It reaches exactly the panes the screen and keys endpoints reach
// (tmuxSessions.target): itervox's own tmux sessions, and a hand-started
// session only in a matched pane with sessions.tmux_control. The server has
// checked the scope and sessions.terminal_write; a read-write attach is
// refused here too when the setting is off. Each attach and detach is
// logged and fed (terminal_attached / terminal_detached: the mode and the
// caller, never a byte of the terminal).

// Terminal modes, as fed and logged.
const (
	terminalModeRead  = "read_only"
	terminalModeWrite = "read_write"
)

// attachTarget resolves the tmux target of a session's live terminal.
func (t *tmuxSessions) attachTarget(ctx context.Context, id string) (string, string, error) {
	if !tmuxctl.AttachSupported {
		return "", "", server.ErrTerminalUnsupported
	}
	pane, handled, err := t.target(ctx, id)
	switch {
	case !handled:
		return "", "", server.ErrSessionNotFound
	case errors.Is(err, errNotControllable):
		return "", "", server.ErrNotControllable
	case err != nil:
		return "", "", err
	}
	if own, ok := t.orch.FindTmuxSession(id); ok {
		target, err := tmuxctl.AttachTarget(own.ID, "")
		return target, own.Backend, err
	}
	backend := ""
	if ext, ok := t.orch.TranscriptSession(id); ok {
		backend = ext.Backend
	}
	target, err := tmuxctl.AttachTarget("", pane.ID)
	return target, backend, err
}

// checkTerminal reports whether the session's terminal can be attached.
func (t *tmuxSessions) checkTerminal(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	_, _, err := t.attachTarget(ctx, id)
	return err
}

// attachTerminal starts the attach and records it.
func (t *tmuxSessions) attachTerminal(req server.TerminalAttachRequest) (server.TerminalConn, error) {
	if req.Write && !t.terminalWrite {
		return nil, server.ErrTerminalWriteDisabled // the server refuses it first; a second check
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	target, backend, err := t.attachTarget(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	// A read-only client must not size the window for everyone else: it
	// attaches with ignore-size where this tmux takes client flags (3.2+).
	mode := tmuxctl.AttachMode{ReadOnly: !req.Write}
	if mode.ReadOnly {
		version := t.detect.refresh().Version
		if mode.IgnoreSize = tmuxctl.ClientFlagsSupported(version); !mode.IgnoreSize {
			oldTmuxSizeOnce.Do(func() {
				slog.Warn("sessions: this tmux has no client flags (tmux 3.2+); a read-only live terminal may resize the session's window to the browser panel",
					"version", version)
			})
		}
	}
	a, err := t.tmux().Attach(ctx, target, mode, req.Cols, req.Rows)
	if err != nil {
		return nil, tmuxWriteError(err)
	}
	fedMode := terminalModeRead
	if req.Write {
		fedMode = terminalModeWrite
	}
	c := &terminalConn{Attachment: a, t: t, id: req.ID, backend: backend, mode: fedMode, actor: req.Actor, start: time.Now()}
	slog.Info("sessions: terminal attached", "session_id", req.ID, "mode", fedMode, "actor", req.Actor)
	t.orch.RecordFeedEvent(orchestrator.FeedEvent{Kind: orchestrator.FeedTerminalAttached, Identifier: req.ID,
		Payload: orchestrator.FeedPayload{Backend: backend, Reason: fedMode, Via: req.Actor}})
	return c, nil
}

// oldTmuxSizeOnce logs the "no client flags" warning once per process.
var oldTmuxSizeOnce sync.Once

// terminalConn is one attachment; Close detaches once and records it.
type terminalConn struct {
	*tmuxctl.Attachment
	t                        *tmuxSessions
	id, backend, mode, actor string
	start                    time.Time
	once                     sync.Once
}

// Close detaches (the session keeps running) and records the detach.
func (c *terminalConn) Close() error { return c.CloseWithReason("") }

// CloseWithReason detaches and records the detach with reason as its
// outcome: why the daemon ended the terminal (its token lost access), ""
// for an ordinary detach.
func (c *terminalConn) CloseWithReason(reason string) error {
	err := c.Attachment.Close()
	c.once.Do(func() {
		slog.Info("sessions: terminal detached", "session_id", c.id, "mode", c.mode, "actor", c.actor,
			"seconds", int(time.Since(c.start).Seconds()), "reason", reason)
		c.t.orch.RecordFeedEvent(orchestrator.FeedEvent{Kind: orchestrator.FeedTerminalDetached, Identifier: c.id,
			Payload: orchestrator.FeedPayload{Backend: c.backend, Reason: c.mode, Via: c.actor, Outcome: reason}})
	})
	if err != nil {
		return fmt.Errorf("cmd/itervox: detach terminal: %w", err)
	}
	return nil
}
