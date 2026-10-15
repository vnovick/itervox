package config

import (
	"log/slog"
	"strings"
)

// Sessions panel discovery scopes (sessions.external_scope).
const (
	// SessionsScopeProject lists only hand-started sessions whose working
	// directory is inside the project (the WORKFLOW.md directory, plus
	// sessions.external_roots).
	SessionsScopeProject = "project"
	// SessionsScopeAll lists every recent session of the daemon's user.
	SessionsScopeAll = "all"
)

// DefaultSessionsExternalWindowHours is how recent a hand-started session's
// last write must be for the Sessions panel to list it.
const DefaultSessionsExternalWindowHours = 24

// SessionsConfig is the sessions: block (#178 step 3): discovery of Claude
// Code and Codex sessions started by hand on the daemon's host. Read-only
// after startup.
type SessionsConfig struct {
	// External turns discovery of hand-started sessions on or off.
	// Default true.
	External bool
	// ExternalWindowHours: only sessions written to within this many hours
	// are listed. Default 24.
	ExternalWindowHours int
	// ExternalScope is SessionsScopeProject (default) or SessionsScopeAll.
	// The project scope keeps sessions from unrelated directories private.
	ExternalScope string
	// ExternalRoots are extra directories (besides the WORKFLOW.md
	// directory) whose sessions the project scope lists.
	ExternalRoots []string
}

// parseSessionsConfig reads the sessions: block. An unknown scope falls
// back to the project scope, the private one, with a warning.
func parseSessionsConfig(m map[string]any) SessionsConfig {
	c := SessionsConfig{
		External:            boolField(m, "external", true),
		ExternalWindowHours: positiveIntField(m, "external_window_hours", DefaultSessionsExternalWindowHours),
		ExternalScope:       strings.TrimSpace(strField(m, "external_scope", SessionsScopeProject)),
	}
	for _, r := range strSliceField(m, "external_roots", nil) {
		if r = strings.TrimSpace(r); r != "" {
			c.ExternalRoots = append(c.ExternalRoots, resolvePathValue(r, r))
		}
	}
	switch c.ExternalScope {
	case SessionsScopeProject, SessionsScopeAll:
	default:
		slog.Warn("config: sessions.external_scope unrecognized, using the project scope",
			"value", c.ExternalScope, "default", SessionsScopeProject)
		c.ExternalScope = SessionsScopeProject
	}
	return c
}
