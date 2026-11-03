package config

import (
	"errors"
	"fmt"
	"strings"
)

// Defaults for tracker.sync (#118).
const (
	DefaultTrackerSyncIntervalMs     = 60000
	DefaultTrackerSyncFullIntervalMs = 3600000
)

// TrackerConnection is how Itervox reaches a Linear or GitHub tracker: the
// keys tracker: and tracker.sync: share, parsed by parseTrackerConnection
// with the same defaults in both places.
type TrackerConnection struct {
	Kind            string
	Endpoint        string
	APIKey          string
	ProjectSlug     string
	ActiveStates    []string
	TerminalStates  []string
	BacklogStates   []string
	CompletionState string
}

// parseTrackerConnection reads kind, endpoint, api_key ($VAR resolved),
// project_slug, active_states, terminal_states, backlog_states and
// completion_state from m. The Linear endpoint default applies only to
// kind linear, so GitHub's client picks its own.
func parseTrackerConnection(m map[string]any) TrackerConnection {
	c := TrackerConnection{Kind: strField(m, "kind", "")}
	defaultEndpoint := ""
	if c.Kind == "linear" {
		defaultEndpoint = "https://api.linear.app/graphql"
	}
	c.Endpoint = strField(m, "endpoint", defaultEndpoint)
	c.APIKey = resolveSecret(strField(m, "api_key", ""))
	c.ProjectSlug = strField(m, "project_slug", "")
	c.ActiveStates = strSliceField(m, "active_states", []string{"Todo", "In Progress"})
	c.TerminalStates = strSliceField(m, "terminal_states", []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"})
	defaultBacklog := []string{}
	if c.Kind == "linear" || c.Kind == "local" {
		defaultBacklog = []string{"Backlog"}
	}
	c.BacklogStates = strSliceField(m, "backlog_states", defaultBacklog)
	c.CompletionState = strField(m, "completion_state", "")
	return c
}

// TrackerSyncConfig is tracker.sync (#118): two-way sync between the local
// issue files (tracker.kind: local) and a Linear project or GitHub
// repository. Read once at startup.
type TrackerSyncConfig struct {
	// Enabled is set when tracker.sync is present (validation then requires
	// a kind).
	Enabled bool
	// TrackerConnection is the remote tracker, with the keys tracker: uses
	// for it (kind, api_key, project_slug, endpoint, and the remote's own
	// active/terminal/backlog/completion state names).
	TrackerConnection
	// StateMap maps local state names to remote ones (state_map); a state
	// not in it has the same name on both sides.
	StateMap map[string]string
	// PullStates are the remote states whose issues are pulled into new
	// local files (pull_states); default the remote active and backlog
	// states, and the completion state.
	PullStates []string
	// IntervalMs is how often the sync runs (interval_ms, default 60000);
	// a local file edit also starts one within seconds.
	IntervalMs int
	// FullSyncIntervalMs is how often a pull lists every issue instead of
	// the changes since the last one (full_sync_interval_ms, default
	// 3600000).
	FullSyncIntervalMs int
}

// parseTrackerSync reads tracker.sync.
func parseTrackerSync(tracker map[string]any) TrackerSyncConfig {
	m := nestedMap(tracker, "sync")
	if len(m) == 0 {
		return TrackerSyncConfig{}
	}
	s := TrackerSyncConfig{TrackerConnection: parseTrackerConnection(m)}
	s.Enabled = true
	s.StateMap = stringMapField(m, "state_map", nil)
	defaultPull := append(append([]string{}, s.ActiveStates...), s.BacklogStates...)
	if s.CompletionState != "" {
		defaultPull = append(defaultPull, s.CompletionState)
	}
	s.PullStates = strSliceField(m, "pull_states", defaultPull)
	s.IntervalMs = positiveIntField(m, "interval_ms", DefaultTrackerSyncIntervalMs)
	s.FullSyncIntervalMs = positiveIntField(m, "full_sync_interval_ms", DefaultTrackerSyncFullIntervalMs)
	return s
}

// validateTrackerSync checks tracker.sync at load.
func validateTrackerSync(t TrackerConfig) error {
	s := t.Sync
	if !s.Enabled {
		return nil
	}
	switch {
	case t.Kind != "local":
		return fmt.Errorf("config: tracker.sync is for tracker.kind: local, not %q", t.Kind)
	case s.Kind != "linear" && s.Kind != "github":
		return fmt.Errorf("config: tracker.sync.kind must be linear or github, got %q", s.Kind)
	case s.APIKey == "":
		return errors.New("config: tracker.sync.api_key is empty (set it, e.g. api_key: $LINEAR_API_KEY, and put the value in .itervox/.env)")
	case strings.TrimSpace(s.ProjectSlug) == "" && s.Kind == "github":
		return errors.New("config: tracker.sync.project_slug is required for github (owner/repo)")
	case strings.TrimSpace(s.ProjectSlug) == "":
		return errors.New("config: tracker.sync.project_slug is required for linear (the project new issues are created in)")
	}
	return nil
}
