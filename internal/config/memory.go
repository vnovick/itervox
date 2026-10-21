package config

// Memory defaults (#202).
const (
	// DefaultMemoryTokenBudget is the most tokens a run's `## Memory` block
	// may take.
	DefaultMemoryTokenBudget = 2000
	// DefaultMemoryPromptShareTokens is the most tokens the rules block and
	// the memory block together add to a prompt (the V3 prompt budget):
	// memory gets what the rules leave, up to token_budget. Rules are never
	// cut.
	DefaultMemoryPromptShareTokens = 8000
)

// MemoryConfig is the memory: block (#202): managed memory injected into
// each run. Read once at startup (load-time config, not in the cfgMu
// allowlist); the memory files themselves are read again for every run.
type MemoryConfig struct {
	// Enabled turns the run-time memory block on or off. Off, runs get no
	// memory; the Memory page can still edit the files. Default true.
	Enabled bool
	// TokenBudget is the most tokens the memory block may take. Pinned
	// entries are always included; the rest fill what is left in rank
	// order. Default 2000.
	TokenBudget int
	// PromptShareTokens caps rules plus memory together: the memory budget
	// shrinks by the rules block's size so the two never exceed it (pinned
	// entries excepted). Default 8000.
	PromptShareTokens int
}

// parseMemoryConfig reads the memory: block. Zero or negative budgets fall
// back to the defaults (use enabled: false to turn memory off).
func parseMemoryConfig(m map[string]any) MemoryConfig {
	return MemoryConfig{
		Enabled:           boolField(m, "enabled", true),
		TokenBudget:       positiveIntField(m, "token_budget", DefaultMemoryTokenBudget),
		PromptShareTokens: positiveIntField(m, "prompt_share_tokens", DefaultMemoryPromptShareTokens),
	}
}

// MemoryBudget is the token budget of a run's memory block once its rules
// block (rulesTokens) has taken its share: TokenBudget, lowered so rules
// plus memory stay within PromptShareTokens, never below zero.
func (m MemoryConfig) MemoryBudget(rulesTokens int) int {
	return max(0, min(m.TokenBudget, m.PromptShareTokens-rulesTokens))
}
