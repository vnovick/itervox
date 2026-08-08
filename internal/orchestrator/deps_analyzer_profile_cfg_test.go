package orchestrator_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestResolveDepsAnalyzerProfileCfgAtomicRead pins wave-2 polish Task 4 / #52:
// ResolveDepsAnalyzerProfileCfg must return the same triple that calling
// DepsAnalyzerProfileCfg() followed by AgentProfileCfg(name) would, but from
// a single cfgMu critical section instead of two.
func TestResolveDepsAnalyzerProfileCfgAtomicRead(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"analyzer": {Command: "claude"},
	}
	cfg.Agent.DepsAnalyzerProfile = "analyzer"
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)

	name, profile, ok := orch.ResolveDepsAnalyzerProfileCfg()
	require.True(t, ok)
	assert.Equal(t, "analyzer", name)
	assert.Equal(t, "claude", profile.Command)

	// Matches the two-call equivalent.
	wantName := orch.DepsAnalyzerProfileCfg()
	wantProfile, wantOK := orch.AgentProfileCfg(wantName)
	assert.Equal(t, wantName, name)
	assert.Equal(t, wantProfile, profile)
	assert.Equal(t, wantOK, ok)
}

// TestResolveDepsAnalyzerProfileCfgEmptyName covers the disabled-analyzer
// case: an empty configured profile name resolves to ok=false without
// touching cfg.Agent.Profiles (mirrors DepsAnalyzerProfileCfg's "empty means
// disabled" contract).
func TestResolveDepsAnalyzerProfileCfgEmptyName(t *testing.T) {
	cfg := baseConfig()
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)

	name, profile, ok := orch.ResolveDepsAnalyzerProfileCfg()
	assert.False(t, ok)
	assert.Equal(t, "", name)
	assert.Equal(t, config.AgentProfile{}, profile)
}

// TestResolveDepsAnalyzerProfileCfgUnknownProfile covers a configured name
// that no longer resolves against cfg.Agent.Profiles (e.g. deleted between a
// SetDepsAnalyzerProfileCfg validation and this read).
func TestResolveDepsAnalyzerProfileCfgUnknownProfile(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.DepsAnalyzerProfile = "ghost"
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)

	name, _, ok := orch.ResolveDepsAnalyzerProfileCfg()
	assert.False(t, ok)
	assert.Equal(t, "ghost", name)
}
