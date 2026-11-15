package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"regexp"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// Re-adoption of itervox's tmux sessions after a daemon restart (#178).
//
// "New session" tags each session it starts with tmux user options
// (tmuxctl.Opt*): the project directory (the WORKFLOW.md's), the backend,
// the profile, the folder, and whether guardrails were prepared. Each
// discovery pass (the first one runs as the daemon starts) lists the
// server's sessions and offers the loop those that are itervox's own for
// THIS project: the name is itervox-<6 hex> AND the project tag is this
// daemon's project directory. A same-named session without the tag, or
// tagged by another project's daemon, is never adopted. The loop decides
// (cap, sessions it already tracks, sessions it just stopped).

// itervoxSessionNameRe is the name "New session" gives (see shortID).
var itervoxSessionNameRe = regexp.MustCompile(`^itervox-[0-9a-f]{6}$`)

// tagSession tags a session itervox just started, so a later daemon
// process for this project can re-adopt it. A failure leaves the session
// running but not re-adoptable after a restart.
func (t *tmuxSessions) tagSession(ctx context.Context, s orchestrator.TmuxSession, guarded bool) {
	opts := map[string]string{
		tmuxctl.OptProject: t.projectRoot, tmuxctl.OptBackend: s.Backend,
		tmuxctl.OptProfile: s.Profile, tmuxctl.OptCwd: s.Cwd,
	}
	if guarded {
		opts[tmuxctl.OptGuarded] = "1"
	}
	if err := t.tmux().SetSessionOptions(ctx, s.ID, opts); err != nil {
		slog.Warn("sessions: cannot tag the tmux session; a restarted daemon will not re-adopt it",
			"session_id", s.ID, "error", err)
	}
}

// adoptableSessions are the listed sessions itervox started for project:
// an itervox-<id> name, a claude/codex backend tag, and a project tag
// naming project. A guarded one has stale guardrails: its token was issued
// by the daemon process that started it.
func adoptableSessions(list []tmuxctl.SessionInfo, project string) []orchestrator.TmuxSession {
	if project == "" {
		return nil
	}
	var out []orchestrator.TmuxSession
	for _, s := range list {
		if !itervoxSessionNameRe.MatchString(s.Name) || !sameDir(s.Project, project) ||
			(s.Backend != "claude" && s.Backend != "codex") {
			continue
		}
		out = append(out, orchestrator.TmuxSession{ID: s.Name, Backend: s.Backend, Profile: s.Profile,
			Cwd: s.StartCwd, StartedAt: s.Created, GuardrailsStale: s.Guarded})
	}
	return out
}

// sameDir reports whether a and b name the same absolute directory.
func sameDir(a, b string) bool {
	if a == "" || b == "" || !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
