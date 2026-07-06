package skills

import (
	"sync"
	"time"
)

// HostInventory is the projection of one SSH host's capability surface.
// Phase-2 ships the type + cache shell; the actual SSH-side `find` is
// integrated in a follow-up so this layer doesn't have to ship its own
// `ssh` binary invocation logic (the existing `internal/agent/ssh.go`
// helpers are the right home for that).
type HostInventory struct {
	Host   string
	Skills []Skill
	Hooks  []HookEntry
	// Stale is true when the cached value was returned because the latest
	// SSH probe failed or the entry is past its TTL but the most recent
	// probe hasn't completed yet.
	Stale     bool
	FetchedAt time.Time
}

// HostScanner is the per-host SSH capability cache (T-103). Entries auto-
// expire after 5 minutes; on cache miss or expiry the caller invokes
// `Refresh(host, scanFn)` to re-fetch.
type HostScanner struct {
	mu    sync.Mutex
	ttl   time.Duration
	cache map[string]HostInventory
}

// NewHostScanner returns a HostScanner with the given TTL. A zero TTL
// defaults to 5 minutes — the design-draft target.
func NewHostScanner(ttl time.Duration) *HostScanner {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &HostScanner{
		ttl:   ttl,
		cache: make(map[string]HostInventory),
	}
}

// Get returns the cached HostInventory if present and fresh, otherwise nil.
// Callers should call Refresh on a nil return.
func (s *HostScanner) Get(host string) *HostInventory {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[host]
	if !ok {
		return nil
	}
	if time.Since(entry.FetchedAt) > s.ttl {
		// Mark as stale rather than evicting — callers can show last-good
		// while a fresh scan runs.
		entry.Stale = true
	}
	return &entry
}

// Refresh runs scanFn for the host and stores the result on success. On
// scanFn failure, the previous cached entry is preserved with Stale=true.
//
// scanFn is the integration seam for the actual `ssh <host> find …` invocation,
// which the orchestrator-side adapter implements via `internal/agent/ssh.go`.
func (s *HostScanner) Refresh(host string, scanFn func(host string) (*HostInventory, error)) error {
	inv, err := scanFn(host)
	if err != nil {
		s.mu.Lock()
		if existing, ok := s.cache[host]; ok {
			existing.Stale = true
			s.cache[host] = existing
		}
		s.mu.Unlock()
		return err
	}
	if inv == nil {
		return nil
	}
	inv.FetchedAt = time.Now()
	inv.Stale = false
	s.mu.Lock()
	s.cache[host] = *inv
	s.mu.Unlock()
	return nil
}

// Snapshot returns a defensive copy of the entire cache. Used by the
// inventory-scan path to fold remote capability surfaces in without
// extra plumbing.
func (s *HostScanner) Snapshot() map[string]HostInventory {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]HostInventory, len(s.cache))
	for k, v := range s.cache {
		out[k] = v
	}
	return out
}
