//go:build linux

package agent

// platformIsolationMechanisms lists Linux's sandboxes in preference order
// (#258): bubblewrap confines by mounts and handles every deny path
// exactly; Landlock is the fallback where bwrap is missing or user
// namespaces are disabled.
func platformIsolationMechanisms() []IsolationMechanism {
	return []IsolationMechanism{newBubblewrap(), newLandlock()}
}
