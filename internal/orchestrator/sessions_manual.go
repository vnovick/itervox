package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/extsessions"
)

// Turns of sessions continued under itervox (#178 step 3). The loop owns
// ManualSession; one goroutine per turn runs the resumed CLI and reports
// back with EventManualTurnExited. See sessions_external.go.

// manualTurn is what one turn's goroutine needs (copied, never shared).
type manualTurn struct {
	ID         string
	ResumeID   string
	Backend    string
	Cwd        string
	Command    string
	Permission string
	Prompt     string
	Path       string // the transcript the turn resumes
}

// startManualTurn marks m running and starts one resumed turn with prompt.
// Event loop only.
func (o *Orchestrator) startManualTurn(ctx context.Context, m *ManualSession, prompt string) {
	m.Status = SessionStatusRunning
	m.LastError = ""
	t := manualTurn{
		ID: m.ID, ResumeID: m.ResumeID, Backend: m.Backend, Cwd: m.Cwd,
		Command: m.Command, Permission: m.Permission, Prompt: prompt, Path: m.Path,
	}
	key := manualKey(m.ID)
	o.workersWg.Add(key)
	go func() {
		defer o.workersWg.Done(key)
		defer RecoverGoroutine("manual-session-turn", key, func() {
			o.sendEventBounded(OrchestratorEvent{Type: EventManualTurnExited, ManualTurn: &ManualTurnResult{
				ID: t.ID, Failed: true, Error: "the turn panicked; see the daemon log",
			}}, key)
		})
		o.runManualTurn(ctx, t)
	}()
}

// runManualTurn runs one resumed turn and reports it. Worker goroutine:
// sends events only.
func (o *Orchestrator) runManualTurn(ctx context.Context, t manualTurn) {
	res := ManualTurnResult{ID: t.ID}
	log := slog.With("session_id", t.ResumeID, "manual_session", true)
	if err := agent.ValidatePromptSize(t.Backend, t.Prompt); err != nil {
		res.Failed, res.Error = true, err.Error()
	} else {
		sid := t.ResumeID
		result, err := o.runner.RunTurn(ctx, log, nil, &sid, t.Prompt, t.Cwd, t.Command, "", "",
			o.cfg.Agent.ReadTimeoutMs, o.cfg.Agent.TurnTimeoutMs, agent.ParsePermissionMode(t.Permission))
		res.SessionID, res.LastText = result.SessionID, result.LastText
		if result.SessionID != "" && result.SessionID != t.ResumeID && t.Backend == extsessions.BackendClaude {
			// The CLI moved the conversation to a new session file.
			if p := filepath.Join(filepath.Dir(t.Path), result.SessionID+".jsonl"); fileExists(p) {
				res.Path = p
			}
		}
		switch {
		case err != nil:
			res.Failed, res.Error = true, err.Error()
		case result.Failed:
			res.Failed, res.Error = true, result.FailureText
		}
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventManualTurnExited, ManualTurn: &res}:
	case <-ctx.Done():
	}
}

// applyManualTurnExited records a finished turn and starts the next one
// when operator messages are waiting. Event loop only.
func (o *Orchestrator) applyManualTurnExited(ctx context.Context, state *State, r *ManualTurnResult) {
	if r == nil {
		return
	}
	m := state.ManualSessions[r.ID]
	if m == nil {
		return
	}
	m.Status = SessionStatusIdle
	m.TurnCount++
	if r.LastText != "" {
		m.LastEvent = abbreviateRedacted(r.LastText)
	}
	if r.Failed {
		m.LastError = abbreviateRedacted(r.Error)
	}
	if r.SessionID != "" && r.SessionID != m.ResumeID {
		// The CLI moved the conversation to a new session: resume that one
		// next, and show its transcript when the turn found it.
		m.ResumeID = r.SessionID
		if r.Path != "" {
			m.Path = r.Path
		}
	}
	slog.Info("orchestrator: manual session turn finished",
		"session_id", m.ID, "resume_id", m.ResumeID, "failed", r.Failed, "turns", m.TurnCount)
	o.deliverQueuedManualMessages(ctx, state, m)
}

// deliverQueuedManualMessages starts a turn with the messages queued for m
// while it was busy, or drops them while draining. Event loop only.
func (o *Orchestrator) deliverQueuedManualMessages(ctx context.Context, state *State, m *ManualSession) {
	key := manualKey(m.ID)
	list := state.SessionMessages[key]
	var texts []string
	for i := range list {
		if list[i].Status != SessionMessageQueued {
			continue
		}
		if state.Draining {
			list[i].Status, list[i].Reason = SessionMessageDropped, "the daemon is draining"
			continue
		}
		list[i].Status, list[i].Delivery, list[i].DeliveredAt = SessionMessageDelivered, SteeringResume, time.Now()
		texts = append(texts, list[i].Text)
	}
	if len(texts) > 0 {
		o.startManualTurn(ctx, m, strings.Join(texts, "\n\n"))
	}
}

// applyManualSessionMessage records an operator message for a continued
// session: it starts a turn when the session is idle and is queued while
// a turn runs. Reports whether msg addressed a continued session. Event
// loop only.
func (o *Orchestrator) applyManualSessionMessage(ctx context.Context, state *State, key string, msg SessionMessage) bool {
	m := findManualSession(*state, key)
	if m == nil {
		return false
	}
	msg.Identifier, msg.SessionID = manualKey(m.ID), m.ID
	switch {
	case state.Draining:
		msg.Status, msg.Reason = SessionMessageDropped, "the daemon is draining"
	case m.Status == SessionStatusIdle:
		msg.Status, msg.Delivery, msg.DeliveredAt = SessionMessageDelivered, SteeringResume, time.Now()
		o.startManualTurn(ctx, m, msg.Text)
	default:
		msg.Status = SessionMessageQueued
		if n := countQueued(state.SessionMessages[msg.Identifier]); n >= steerInboxSize {
			msg.Status, msg.Reason = SessionMessageDropped, fmt.Sprintf("too many undelivered messages (%d)", steerInboxSize)
		}
	}
	appendSessionMessage(state, msg)
	slog.Info("orchestrator: operator message for continued session",
		"session_id", m.ID, "message_id", msg.ID, "status", string(msg.Status))
	return true
}

func countQueued(list []SessionMessage) int {
	n := 0
	for _, m := range list {
		if m.Status == SessionMessageQueued {
			n++
		}
	}
	return n
}

// findManualSession returns the continued session addressed by key: the
// ID it was discovered with, or the ID it resumes now.
func findManualSession(state State, key string) *ManualSession {
	if key == "" {
		return nil
	}
	if m := state.ManualSessions[key]; m != nil {
		return m
	}
	for _, m := range state.ManualSessions {
		if m != nil && m.ResumeID == key {
			return m
		}
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
