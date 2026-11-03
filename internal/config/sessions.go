package config

import (
	"log/slog"
	"slices"
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
	// TmuxControl lets the Sessions panel type into hand-started sessions
	// that run inside a tmux pane (sessions.tmux_control, #178): screen
	// capture, text and allowlisted keys through internal/tmuxctl. Off by
	// default: anyone holding the dashboard token could otherwise type
	// into your terminals. Sessions itervox starts in tmux itself ("New
	// session") are controllable either way.
	TmuxControl bool
	// SteeringPhrases are the one-click steering chips (#218) of the
	// Sessions composer and the issue steer box. Default
	// DefaultSteeringPhrases; an empty list shows none.
	SteeringPhrases []SteeringPhrase
}

// parseSessionsConfig reads the sessions: block. An unknown scope falls
// back to the project scope, the private one, with a warning.
func parseSessionsConfig(m map[string]any) SessionsConfig {
	c := SessionsConfig{
		External:            boolField(m, "external", true),
		ExternalWindowHours: positiveIntField(m, "external_window_hours", DefaultSessionsExternalWindowHours),
		ExternalScope:       strings.TrimSpace(strField(m, "external_scope", SessionsScopeProject)),
		TmuxControl:         boolField(m, "tmux_control", false),
		SteeringPhrases:     parseSteeringPhrases(m),
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

// SteeringPhrase is one one-click steering chip of the Sessions composer and
// the issue steer box (#218): its label and the operator message it sends.
type SteeringPhrase struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

// Steering phrase bounds (#218): a chip's label and message, and the list.
const (
	maxSteeringPhrases       = 20
	maxSteeringLabelBytes    = 40
	maxSteeringMessageBytes  = 1024
	steeringPhrasesConfigKey = "steering_phrases"
)

// DefaultSteeringPhrases are the chips offered when sessions.steering_phrases
// is not set (#218): pstack's principle names, each with a short message.
var DefaultSteeringPhrases = []SteeringPhrase{
	{Label: "Prove it works", Message: "Prove it works: run the tests or the command that exercises this change and show me the output before you call it done."},
	{Label: "Fix the root cause", Message: "Fix the root cause, not the symptom: explain why it fails, then change the code that causes it."},
	{Label: "Smallest change", Message: "Make the smallest change that solves the problem. Drop anything the issue does not need."},
	{Label: "Reproduce first", Message: "Reproduce the problem first: write a failing test or a command that shows it, then fix it."},
	{Label: "Explain the number", Message: "Explain the number: say where each figure you report comes from and how you measured it."},
	{Label: "Subtract before you add", Message: "Subtract before you add: look for code, config or steps you can remove before adding new ones."},
}

// parseSteeringPhrases reads sessions.steering_phrases: a list whose entries
// are {label, message} maps, or bare strings (the default phrase of that
// label, else a chip whose label is also its message). Absent: the
// defaults; an empty list: no chips. Malformed or oversized entries are
// dropped with a warning; at most 20 are kept.
func parseSteeringPhrases(m map[string]any) []SteeringPhrase {
	v, ok := m[steeringPhrasesConfigKey]
	if !ok || v == nil {
		return slices.Clone(DefaultSteeringPhrases)
	}
	raw, isList := v.([]any)
	if !isList {
		slog.Warn("config: sessions.steering_phrases must be a list; using the defaults", "value", v)
		return slices.Clone(DefaultSteeringPhrases)
	}
	out := []SteeringPhrase{}
	for _, item := range raw {
		p, ok := steeringPhraseFrom(item)
		if !ok {
			slog.Warn("config: sessions.steering_phrases entry ignored (needs a label of at most 40 bytes and a message of at most 1 KiB)", "entry", item)
			continue
		}
		if len(out) == maxSteeringPhrases {
			slog.Warn("config: sessions.steering_phrases has more than 20 entries; the rest are ignored")
			break
		}
		out = append(out, p)
	}
	return out
}

func steeringPhraseFrom(item any) (SteeringPhrase, bool) {
	var p SteeringPhrase
	switch t := item.(type) {
	case string:
		label := strings.TrimSpace(t)
		p = SteeringPhrase{Label: label, Message: label}
		for _, d := range DefaultSteeringPhrases {
			if strings.EqualFold(d.Label, label) {
				p = d
			}
		}
	case map[string]any:
		label, _ := t["label"].(string)
		message, _ := t["message"].(string)
		p = SteeringPhrase{Label: strings.TrimSpace(label), Message: strings.TrimSpace(message)}
	default:
		return SteeringPhrase{}, false
	}
	if p.Label == "" || p.Message == "" || len(p.Label) > maxSteeringLabelBytes || len(p.Message) > maxSteeringMessageBytes {
		return SteeringPhrase{}, false
	}
	return p, true
}
