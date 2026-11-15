package tmuxctl

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Session tags: tmux user options itervox sets on the sessions it starts,
// so a daemon that restarts can tell its own sessions from same-named ones
// (internal/tmuxctl only sets and lists them; deciding what to adopt is the
// caller's).
const (
	OptProject  = "@itervox-project" // absolute directory of the WORKFLOW.md
	OptBackend  = "@itervox-backend" // "claude" / "codex"
	OptProfile  = "@itervox-profile" // the profile it started with, if any
	OptCwd      = "@itervox-cwd"     // the folder it started in
	OptGuarded  = "@itervox-guarded" // "1" when guardrails were prepared for it
	sessionSep  = paneSep            // the same implausible separator
	sessionCols = 7                  // fields of sessionFormat
)

var optionNameRe = regexp.MustCompile(`^@itervox-[a-z]+$`)

// SessionInfo is one tmux session with itervox's tags (empty when unset).
type SessionInfo struct {
	Name     string
	Created  time.Time
	Project  string
	Backend  string
	Profile  string
	Guarded  bool
	StartCwd string
}

// The free-form path fields come last; a path that happens to contain the
// separator misparses into a project that matches nothing (fail safe).
const sessionFormat = "#{session_name}" + sessionSep + "#{session_created}" + sessionSep + "#{" + OptBackend + "}" +
	sessionSep + "#{" + OptProfile + "}" + sessionSep + "#{" + OptGuarded + "}" + sessionSep +
	"#{" + OptProject + "}" + sessionSep + "#{" + OptCwd + "}"

// SetSessionOptions sets itervox's tags on the session named exactly name.
// Only @itervox-* names are accepted; empty values are skipped.
func (c Client) SetSessionOptions(ctx context.Context, name string, opts map[string]string) error {
	if !ValidSessionName(name) {
		return ErrBadTarget
	}
	for k, v := range opts {
		if !optionNameRe.MatchString(k) || strings.ContainsAny(v, "\n\r") {
			return ErrBadTarget
		}
	}
	for _, k := range []string{OptProject, OptBackend, OptProfile, OptCwd, OptGuarded} {
		if v := opts[k]; v != "" {
			if _, err := c.run(ctx, "", "set-option", "-t", "="+name+":", k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListSessions lists every session of the server with itervox's tags. No
// tmux binary or no running server is an empty list, not an error.
func (c Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	out, err := c.run(ctx, "", "list-sessions", "-F", sessionFormat)
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoSuchPane) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseSessions(out), nil
}

func parseSessions(out []byte) []SessionInfo {
	var list []SessionInfo
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, sessionSep, sessionCols)
		if len(f) != sessionCols || !ValidSessionName(f[0]) {
			continue
		}
		s := SessionInfo{Name: f[0], Backend: f[2], Profile: f[3], Guarded: f[4] == "1", Project: f[5], StartCwd: f[6]}
		if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil && sec > 0 {
			s.Created = time.Unix(sec, 0)
		}
		list = append(list, s)
	}
	return list
}

// HasSession reports whether the session named exactly name exists.
func (c Client) HasSession(ctx context.Context, name string) (bool, error) {
	if !ValidSessionName(name) {
		return false, ErrBadTarget
	}
	_, err := c.run(ctx, "", "has-session", "-t", "="+name)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNoSuchPane):
		return false, nil
	default:
		return false, err
	}
}
