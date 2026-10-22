package server

import (
	"maps"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
)

// Secret redaction below admin (#214). Tracker API keys and webhook secrets
// are never served by any route (WORKFLOW.md holds $VAR references that are
// resolved in memory only). What can carry a secret in a response is free
// text the operator wrote: a profile or agent command with an inline env
// assignment (`ANTHROPIC_API_KEY=... claude`), a clone URL with credentials,
// and the raw text of a settings file. Viewers and operators get those
// redacted; admins get them as written, because admins edit them.

// redactedValue replaces a secret value.
const redactedValue = "[redacted]"

// redactSecretString redacts env-assignment values, URL credentials and
// every known secret shape (logging.RedactString) from s.
func redactSecretString(s string) string {
	fields := strings.Fields(s)
	changed := false
	for i, f := range fields {
		switch {
		case config.IsEnvAssignment(f):
			name, _, _ := strings.Cut(f, "=")
			fields[i] = name + "=" + redactedValue
			changed = true
		case strings.Contains(f, "://") && strings.Contains(f, "@"):
			if u, err := url.Parse(f); err == nil && u.User != nil {
				u.User = url.User(redactedValue)
				fields[i] = u.String()
				changed = true
			}
		}
	}
	if changed {
		s = strings.Join(fields, " ")
	}
	return logging.RedactString(s)
}

// redactSecretValue applies redactSecretString to every string inside v (a
// decoded JSON value).
func redactSecretValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactSecretString(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactSecretValue(e)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = redactSecretString(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = redactSecretValue(e)
		}
		return out
	}
	return v
}

// snapshotForScope returns snap as a caller of scope may see it: with
// CallerScope set and, below admin, profile commands redacted. snap's maps
// are shared with other callers, so redaction copies them.
func snapshotForScope(snap StateSnapshot, scope apitokens.Scope) StateSnapshot {
	snap.CallerScope = string(scope)
	snap.ProfileDefs = profileDefsForScope(snap.ProfileDefs, scope)
	return snap
}

// profileDefsForScope redacts profile commands below admin.
func profileDefsForScope(defs map[string]ProfileDef, scope apitokens.Scope) map[string]ProfileDef {
	if scope == apitokens.ScopeAdmin || defs == nil {
		return defs
	}
	out := make(map[string]ProfileDef, len(defs))
	maps.Copy(out, defs)
	for name, d := range out {
		d.Command = redactSecretString(d.Command)
		out[name] = d
	}
	return out
}

// settingsLayersForScope redacts the Settings page's layers below admin:
// the raw file text is withheld (it may hold anything) and every value is
// passed through redactSecretValue.
func settingsLayersForScope(view SettingsLayersView, scope apitokens.Scope) SettingsLayersView {
	if scope == apitokens.ScopeAdmin {
		return view
	}
	layers := make([]SettingsLayerView, len(view.Layers))
	for i, l := range view.Layers {
		l.Raw = ""
		l.Redacted = true
		values := make(map[string]any, len(l.Values))
		for k, v := range l.Values {
			values[k] = redactSecretValue(v)
		}
		l.Values = values
		layers[i] = l
	}
	view.Layers = layers
	effective := make([]SettingsEffective, len(view.Effective))
	for i, e := range view.Effective {
		e.Value = redactSecretValue(e.Value)
		effective[i] = e
	}
	view.Effective = effective
	return view
}
