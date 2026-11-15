//go:build darwin

package agent

// platformIsolationMechanisms lists macOS's sandbox (#258).
func platformIsolationMechanisms() []IsolationMechanism {
	return []IsolationMechanism{newSandboxExec()}
}
