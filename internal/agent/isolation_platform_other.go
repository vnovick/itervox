//go:build !linux && !darwin

package agent

// platformIsolationMechanisms is empty: no sandbox is supported here
// (#258), so a profile asking for process isolation does not start.
func platformIsolationMechanisms() []IsolationMechanism { return nil }
