package config

import "fmt"

// CORE-054 — declarative agent.backend_fallback.
//
// When a backend is limited (the orchestrator's per-(backend, host) circuit
// breaker, CORE-053), dispatch maps the issue's profile to its counterpart on
// the next healthy backend of Chain via ProfileMap, keeps the issue there for
// at least MinDwellMinutes, and switches it back according to SwitchBack.
//
// The block is read at config load only: a WORKFLOW.md edit applies through
// the normal reload, but no settings API mutates it, so it is NOT in the
// cfgMu allowlist (cfg_mu_audit_test.go::AllowedMutableCfgFields).

// Backend fallback switch-back policies.
const (
	// BackendFallbackSwitchBackAtReset reverts the override once the minimum
	// dwell has elapsed AND the original backend's breaker is no longer
	// limited (its published reset, or the default cooldown, has passed).
	BackendFallbackSwitchBackAtReset = "at_reset"
	// BackendFallbackSwitchBackOnSuccess reverts on the first successful run
	// that ends after the minimum dwell.
	BackendFallbackSwitchBackOnSuccess = "on_success"
	// BackendFallbackSwitchBackManual never reverts automatically (the
	// operator pins a backend, or agent.switch_revert_hours applies).
	BackendFallbackSwitchBackManual = "manual"
)

// Backend fallback on_unmapped policies.
const (
	// BackendFallbackUnmappedHold holds an issue whose profile has no
	// profile_map entry with the backend_limited reason until the breaker
	// closes.
	BackendFallbackUnmappedHold = "hold"
	// BackendFallbackUnmappedBackendHint requests the chain backend for the
	// issue's own command. It only takes effect for wrapper commands; a
	// command whose binary is claude or codex is never paired with the other
	// backend (CORE-115), so such an issue is held instead.
	BackendFallbackUnmappedBackendHint = "backend_hint"
)

// BackendFallbackDefaultProfileKey is the profile_map key for issues that
// run without a profile (agent.command). A profile literally named "default"
// is the same key.
const BackendFallbackDefaultProfileKey = "default"

// Default values for agent.backend_fallback.
const (
	DefaultBackendFallbackCooldownMinutes = 15
	DefaultBackendFallbackMinDwellMinutes = 30
	// MaxBackendFallbackCooldownMinutes bounds default_cooldown_minutes (one
	// day): the breaker caps an unknown-reset hold at 24h anyway (M3-close).
	MaxBackendFallbackCooldownMinutes = 24 * 60
)

// BackendFallbackConfig is agent.backend_fallback.
type BackendFallbackConfig struct {
	// Enabled turns declarative fallback on. True when the block is present
	// unless `enabled: false`; false when the block is absent.
	Enabled bool
	// Chain is the backend preference order (default [claude, codex]).
	Chain []string
	// ProfileMap maps a source profile name (or "default" for agent.command)
	// to its counterpart profile per backend: {coder: {codex: coder-codex}}.
	ProfileMap map[string]map[string]string
	// OnUnmapped is "hold" (default) or "backend_hint".
	OnUnmapped string
	// DefaultCooldownMinutes is how long a backend stays limited when the
	// vendor did not publish a reset time (default 15). Also used by the
	// breaker when the block is absent.
	DefaultCooldownMinutes int
	// MinDwellMinutes is the minimum time an issue stays on the fallback
	// before it may switch back (default 30). It never blocks a further hop
	// when the fallback itself becomes limited.
	MinDwellMinutes int
	// SwitchBack is "at_reset" (default), "on_success" or "manual".
	SwitchBack string
}

// defaultBackendFallback returns the values used when the block is absent.
func defaultBackendFallback() BackendFallbackConfig {
	return BackendFallbackConfig{
		Chain:                  []string{"claude", "codex"},
		ProfileMap:             map[string]map[string]string{},
		OnUnmapped:             BackendFallbackUnmappedHold,
		DefaultCooldownMinutes: DefaultBackendFallbackCooldownMinutes,
		MinDwellMinutes:        DefaultBackendFallbackMinDwellMinutes,
		SwitchBack:             BackendFallbackSwitchBackAtReset,
	}
}

// parseBackendFallback reads agent.backend_fallback. The block is a map, or
// a boolean shorthand: `false` (and null/absent) leaves fallback off, `true`
// enables it with the defaults. Any other shape, or a mistyped key, is an
// error — M3-close BH-M3-1: `backend_fallback: false`, "off" or
// {enabled: "false"} used to be read as an empty map and silently ENABLE
// fallback with the default chain. Values are otherwise kept as written so
// ValidateBackendFallback can reject them with a precise message.
func parseBackendFallback(agent map[string]any) (BackendFallbackConfig, error) {
	out := defaultBackendFallback()
	raw, present := agent["backend_fallback"]
	if !present || raw == nil {
		return out, nil
	}
	if b, ok := raw.(bool); ok {
		out.Enabled = b
		return out, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return out, fmt.Errorf("config: agent.backend_fallback must be a map or a boolean, got %T (%v)", raw, raw)
	}
	out.Enabled = true
	if v, ok := m["enabled"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return out, fmt.Errorf("config: agent.backend_fallback.enabled must be a boolean, got %T (%v)", v, v)
		}
		out.Enabled = b
	}
	if v, ok := m["chain"]; ok && v != nil {
		list, isList := v.([]any)
		if !isList {
			return out, fmt.Errorf("config: agent.backend_fallback.chain must be a list of backends, got %T (%v)", v, v)
		}
		out.Chain = make([]string, 0, len(list))
		for _, item := range list {
			s, isStr := item.(string)
			if !isStr {
				return out, fmt.Errorf("config: agent.backend_fallback.chain entries must be strings, got %T (%v)", item, item)
			}
			out.Chain = append(out.Chain, s)
		}
	}
	for key, dst := range map[string]*string{"on_unmapped": &out.OnUnmapped, "switch_back": &out.SwitchBack} {
		if v, ok := m[key]; ok && v != nil {
			s, isStr := v.(string)
			if !isStr {
				return out, fmt.Errorf("config: agent.backend_fallback.%s must be a string, got %T (%v)", key, v, v)
			}
			*dst = s
		}
	}
	for key, dst := range map[string]*int{"default_cooldown_minutes": &out.DefaultCooldownMinutes, "min_dwell_minutes": &out.MinDwellMinutes} {
		if v, ok := m[key]; ok && v != nil {
			n, isInt := toInt(v)
			if !isInt {
				return out, fmt.Errorf("config: agent.backend_fallback.%s must be an integer, got %T (%v)", key, v, v)
			}
			*dst = n
		}
	}
	if v, ok := m["profile_map"]; ok && v != nil {
		pm, isMap := v.(map[string]any)
		if !isMap {
			return out, fmt.Errorf("config: agent.backend_fallback.profile_map must be a map, got %T (%v)", v, v)
		}
		for source, targets := range pm {
			tm, isMap := targets.(map[string]any)
			if !isMap {
				return out, fmt.Errorf("config: agent.backend_fallback.profile_map.%s must be a map of backend: profile, got %T (%v)", source, targets, targets)
			}
			row := make(map[string]string, len(tm))
			for backend, target := range tm {
				s, isStr := target.(string)
				if !isStr {
					return out, fmt.Errorf("config: agent.backend_fallback.profile_map.%s.%s must be a profile name, got %T (%v)", source, backend, target, target)
				}
				row[backend] = s
			}
			out.ProfileMap[source] = row
		}
	}
	return out, nil
}

// Lookup returns the counterpart of profile (""=agent.command) on backend,
// or "" when none is mapped.
func (b BackendFallbackConfig) Lookup(profile, backend string) string {
	if profile == "" {
		profile = BackendFallbackDefaultProfileKey
	}
	return b.ProfileMap[profile][backend]
}

// CooldownMinutes is DefaultCooldownMinutes, or the built-in default when it
// is not positive.
func (b BackendFallbackConfig) CooldownMinutes() int {
	if b.DefaultCooldownMinutes > 0 {
		return b.DefaultCooldownMinutes
	}
	return DefaultBackendFallbackCooldownMinutes
}
