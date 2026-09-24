package agent

// SetValidateCLIShellFallback toggles the interactive-login-shell fallback
// used by validateCLI. Tests disable it so PATH manipulation is sufficient
// to exercise the "not found" path without the user's real ~/.zshrc
// re-adding the tool onto PATH.
//
// It returns the previous value so the caller can restore it.
func SetValidateCLIShellFallback(v bool) (prev bool) {
	prev = validateCLIShellFallback
	validateCLIShellFallback = v
	return prev
}

// ItervoxAgentEnv / ItervoxAgentExportPrefix expose the marker helpers to the
// blackbox agent_test package.
func ItervoxAgentEnv(extra ...string) []string { return itervoxAgentEnv(extra...) }
func ItervoxAgentExportPrefix() string         { return itervoxAgentExportPrefix() }
