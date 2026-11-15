package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Quota pools (agent.profiles.<name>.pool).
//
// A pool names a separate quota for a backend: a profile that points a known
// agent CLI at another model server (`ANTHROPIC_BASE_URL=http://127.0.0.1:8000
// claude`) still runs the claude backend, but does not share Anthropic's
// usage limit. The orchestrator keys its circuit breaker (CORE-053) on the
// backend lane — the backend plus its pool, written "claude/local" — and
// agent.backend_fallback.chain and the profile_map backend keys may name a
// lane. A profile without a pool runs the plain backend lane ("claude"), so
// every key, and backend_health.json, is unchanged for it.

// BackendPoolSeparator separates a backend from its pool in a lane.
const BackendPoolSeparator = "/"

var poolNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidPoolName reports whether pool is a valid pool name: lower-case
// letters, digits and '-'.
func ValidPoolName(pool string) bool {
	return poolNamePattern.MatchString(pool)
}

// BackendLane is the lane for backend in pool: "claude", or "claude/local".
func BackendLane(backend, pool string) string {
	if pool == "" {
		return backend
	}
	return backend + BackendPoolSeparator + pool
}

// SplitBackendLane splits a lane into its backend and pool ("" when none).
func SplitBackendLane(lane string) (backend, pool string) {
	backend, pool, _ = strings.Cut(lane, BackendPoolSeparator)
	return backend, pool
}

// ValidateBackendLane checks that lane names a supported backend and, when
// it has a pool part, a valid pool name.
func ValidateBackendLane(lane string) error {
	backend, pool := SplitBackendLane(lane)
	if !IsSupportedBackend(backend) {
		return fmt.Errorf("unknown backend %q (must be %s, optionally followed by /<pool>)", backend, SupportedBackendsText())
	}
	if strings.Contains(lane, BackendPoolSeparator) && !ValidPoolName(pool) {
		return fmt.Errorf("pool %q in %q must match [a-z0-9-]+", pool, lane)
	}
	return nil
}

// ProfileRunsLane is ProfileRunsBackend plus the profile's pool, "" when the
// backend is unknown.
func ProfileRunsLane(profile AgentProfile, defaultCommand string) string {
	backend := ProfileRunsBackend(profile, defaultCommand)
	if backend == "" {
		return ""
	}
	return BackendLane(backend, profile.Pool)
}

// profilePoolField reads agent.profiles.<name>.pool: absent or empty means
// no pool; anything else must be a valid pool name.
func profilePoolField(m map[string]any, name string) (string, error) {
	raw, ok := m["pool"]
	if !ok || raw == nil {
		return "", nil
	}
	s, isStr := raw.(string)
	if !isStr {
		return "", fmt.Errorf("config: agent.profiles.%s.pool must be a string, got %T (%v)", name, raw, raw)
	}
	pool := strings.TrimSpace(s)
	if pool != "" && !ValidPoolName(pool) {
		return "", fmt.Errorf("config: agent.profiles.%s.pool %q must match [a-z0-9-]+ (lower-case letters, digits and -)", name, pool)
	}
	return pool, nil
}
