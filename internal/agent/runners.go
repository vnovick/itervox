package agent

import "github.com/vnovick/itervox/internal/config"

// BackendRunners returns a fresh runner for every backend except claude,
// keyed by backend name: the map NewMultiRunner routes by. Every place
// that builds a MultiRunner uses it, so a new backend is wired once.
func BackendRunners() map[string]Runner {
	return map[string]Runner{
		config.BackendCodex:    NewCodexRunner(),
		config.BackendOpenCode: NewOpenCodeRunner(),
		config.BackendCursor:   NewCursorRunner(),
		config.BackendCustom:   NewCustomRunner(),
	}
}

// NewBackendMultiRunner is the daemon's runner: Claude by default, every
// other supported backend by its name (BackendRunners).
func NewBackendMultiRunner() *MultiRunner {
	return NewMultiRunner(NewClaudeRunner(), BackendRunners())
}
