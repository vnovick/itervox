package agent

import "sync"

// sshStrictHostMu guards strictHostDefault and strictHostByHost. The orchestrator
// updates these at startup and on config reload (under cfgMu); runners read
// them on every SSH command construction.
var (
	sshStrictHostMu      sync.RWMutex
	strictHostDefault    = "accept-new" // TOFU: pin on first contact, reject on mismatch
	strictHostByHost     map[string]string
	validStrictHostModes = map[string]struct{}{
		"yes":        {},
		"no":         {},
		"ask":        {},
		"accept-new": {},
		"off":        {},
	}
)

// SetSSHStrictHostDefault configures the default StrictHostKeyChecking mode
// applied to every SSH-hosted runner command unless overridden per-host.
//
// Valid modes (per `man ssh_config`):
//   - "accept-new" — TOFU; pin on first contact, reject on mismatch (default; recommended)
//   - "yes"        — strict; reject any unknown or changed host key
//   - "no" / "off" — permissive; accept any host key (insecure; matches pre-T-32 behavior)
//   - "ask"        — interactive; prompt on first contact (incompatible with BatchMode)
//
// Unknown mode strings are ignored — caller is expected to validate at config-load.
func SetSSHStrictHostDefault(mode string) {
	if _, ok := validStrictHostModes[mode]; !ok {
		return
	}
	sshStrictHostMu.Lock()
	strictHostDefault = mode
	sshStrictHostMu.Unlock()
}

// SetSSHStrictHostOverrides replaces the per-host override map. A nil or
// empty map clears all overrides (every host falls back to the default).
func SetSSHStrictHostOverrides(byHost map[string]string) {
	cleaned := make(map[string]string, len(byHost))
	for host, mode := range byHost {
		if _, ok := validStrictHostModes[mode]; ok {
			cleaned[host] = mode
		}
	}
	sshStrictHostMu.Lock()
	strictHostByHost = cleaned
	sshStrictHostMu.Unlock()
}

// sshStrictHostMode returns the StrictHostKeyChecking mode for the given host,
// preferring per-host override over the package default.
func sshStrictHostMode(host string) string {
	sshStrictHostMu.RLock()
	defer sshStrictHostMu.RUnlock()
	if mode, ok := strictHostByHost[host]; ok {
		return mode
	}
	return strictHostDefault
}

// sshStrictHostOption returns the `-o StrictHostKeyChecking=<mode>` argv pair
// for the given host. Use as `cmd.Args = append(cmd.Args, sshStrictHostOption(host)...)`
// or splice into an exec.Command(...) call.
func sshStrictHostOption(host string) []string {
	return []string{"-o", "StrictHostKeyChecking=" + sshStrictHostMode(host)}
}
