package orchestrator

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
)

// SetMaxRetriesCfg / MaxRetriesCfg under -race: same invariant as the input-required
// and pr_opened registries. A negative value clamps to 0 (the orchestrator's
// "unlimited" sentinel).
func TestMaxRetriesCfg_RaceSafeAndClamps(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.SetMaxRetriesCfg(-3)
	assert.Equal(t, 0, o.MaxRetriesCfg(), "negative clamps to 0 (unlimited)")

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for i := range 100 {
				o.SetMaxRetriesCfg(i % 7)
				_ = o.MaxRetriesCfg()
			}
		})
	}
	wg.Wait()
	v := o.MaxRetriesCfg()
	assert.GreaterOrEqual(t, v, 0)
	assert.LessOrEqual(t, v, 6)
}

// SetMaxSwitchesPerIssuePerWindowCfg + SetSwitchWindowHoursCfg under -race.
// Mirrors MaxRetriesCfg test — concurrent writers/readers must not race
// on the cfg fields. Negative cap clamps to 0, hours <= 0 normalises to 6.
// Gap §4.6.
func TestSwitchCapsCfg_RaceSafe(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.SetMaxSwitchesPerIssuePerWindowCfg(-3)
	assert.Equal(t, 0, o.MaxSwitchesPerIssuePerWindowCfg(), "negative cap clamps to 0")
	o.SetSwitchWindowHoursCfg(0)
	assert.Equal(t, 6, o.SwitchWindowHoursCfg(), "0 hours normalises to 6")

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for i := range 100 {
				o.SetMaxSwitchesPerIssuePerWindowCfg(i % 5)
				_ = o.MaxSwitchesPerIssuePerWindowCfg()
				o.SetSwitchWindowHoursCfg((i % 12) + 1)
				_ = o.SwitchWindowHoursCfg()
			}
		})
	}
	wg.Wait()
	cap := o.MaxSwitchesPerIssuePerWindowCfg()
	assert.GreaterOrEqual(t, cap, 0)
	assert.LessOrEqual(t, cap, 4)
	hours := o.SwitchWindowHoursCfg()
	assert.GreaterOrEqual(t, hours, 1)
	assert.LessOrEqual(t, hours, 12)
}

// SetFailedStateCfg / FailedStateCfg under -race. Empty string is the "pause"
// sentinel and must round-trip cleanly.
func TestFailedStateCfg_RaceSafeAndPauseRoundtrip(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.SetFailedStateCfg("Backlog")
	assert.Equal(t, "Backlog", o.FailedStateCfg())
	o.SetFailedStateCfg("")
	assert.Equal(t, "", o.FailedStateCfg(), "empty = pause sentinel must round-trip")

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				o.SetFailedStateCfg("Failed")
				_ = o.FailedStateCfg()
				o.SetFailedStateCfg("")
				_ = o.FailedStateCfg()
			}
		})
	}
	wg.Wait()
}
