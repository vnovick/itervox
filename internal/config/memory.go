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
	// Lessons configures the lessons Itervox proposes (#203).
	Lessons LessonsConfig
}

// parseMemoryConfig reads the memory: block. Zero or negative budgets fall
// back to the defaults (use enabled: false to turn memory off).
func parseMemoryConfig(m map[string]any) MemoryConfig {
	return MemoryConfig{
		Enabled:           boolField(m, "enabled", true),
		TokenBudget:       positiveIntField(m, "token_budget", DefaultMemoryTokenBudget),
		PromptShareTokens: positiveIntField(m, "prompt_share_tokens", DefaultMemoryPromptShareTokens),
		Lessons:           parseLessonsConfig(nestedMap(m, "lessons")),
	}
}

// MemoryBudget is the token budget of a run's memory block once its rules
// block (rulesTokens) has taken its share: TokenBudget, lowered so rules
// plus memory stay within PromptShareTokens, never below zero.
func (m MemoryConfig) MemoryBudget(rulesTokens int) int {
	return max(0, min(m.TokenBudget, m.PromptShareTokens-rulesTokens))
}

// Lesson defaults (#203).
const (
	// DefaultLessonMaxPerIssue is how many lessons one issue may propose
	// within DefaultLessonRateWindowHours.
	DefaultLessonMaxPerIssue = 3
	// DefaultLessonRateWindowHours is the per-issue rate-limit window.
	DefaultLessonRateWindowHours = 24
	// DefaultLessonDraftTokenBudget is the most tokens (input plus output)
	// a lesson-drafting call may use before it is stopped and the template
	// is used instead.
	DefaultLessonDraftTokenBudget = 6000
	// DefaultLessonDraftTimeoutMs bounds a lesson-drafting call.
	DefaultLessonDraftTimeoutMs = 60_000
	// DefaultLessonStaleAfterRuns is how many runs an approved lesson may go
	// unused before the Memory page lists it as not helping.
	DefaultLessonStaleAfterRuns = 20
	// LessonProfileTemplate as memory.lessons.profile drafts every lesson
	// from the template, never calling an agent.
	LessonProfileTemplate = "template"
)

// LessonsConfig is the memory.lessons: block (#203): lessons Itervox
// proposes from corrections and failed-then-succeeded runs. Load-time
// config, not in the cfgMu allowlist.
type LessonsConfig struct {
	// Enabled turns lesson proposals on (they also need memory.enabled).
	// Default true.
	Enabled bool
	// Profile names the agent profile whose backend drafts a lesson's text
	// in a short, budgeted call. Empty: the reviewer profile
	// (agent.reviewer_profile), when one is set. "template", or no profile
	// to use: the lesson is filled in from a template.
	Profile string
	// Scope is the memory file a proposal is written to: "private"
	// (default; the project file when private memory is off) or "project".
	Scope string
	// AutoApprovePrivate / AutoApproveProject make a proposal landing in
	// that scope active at once instead of waiting for review. Both default
	// false; `auto_approve: true` sets both.
	AutoApprovePrivate bool
	AutoApproveProject bool
	// MaxPerIssue / RateWindowHours rate-limit proposals per issue.
	MaxPerIssue     int
	RateWindowHours int
	// DraftTokenBudget / DraftTimeoutMs bound the drafting call: past either
	// the call is stopped and the template is used.
	DraftTokenBudget int
	DraftTimeoutMs   int
	// StaleAfterRuns is how many later runs an approved lesson may go
	// unused before it is listed as not helping.
	StaleAfterRuns int
}

// AutoApprove reports whether a proposal written to scope ("private" or
// "project") is approved at once.
func (l LessonsConfig) AutoApprove(scope string) bool {
	if scope == "project" {
		return l.AutoApproveProject
	}
	return l.AutoApprovePrivate
}

// parseLessonsConfig reads memory.lessons. auto_approve is a bool (both
// scopes) or a map with private / project keys. An unknown scope falls back
// to private; non-positive limits fall back to the defaults.
func parseLessonsConfig(m map[string]any) LessonsConfig {
	l := LessonsConfig{
		Enabled:          boolField(m, "enabled", true),
		Profile:          strField(m, "profile", ""),
		Scope:            strField(m, "scope", "private"),
		MaxPerIssue:      positiveIntField(m, "max_per_issue", DefaultLessonMaxPerIssue),
		RateWindowHours:  positiveIntField(m, "rate_window_hours", DefaultLessonRateWindowHours),
		DraftTokenBudget: positiveIntField(m, "draft_token_budget", DefaultLessonDraftTokenBudget),
		DraftTimeoutMs:   positiveIntField(m, "draft_timeout_ms", DefaultLessonDraftTimeoutMs),
		StaleAfterRuns:   positiveIntField(m, "stale_after_runs", DefaultLessonStaleAfterRuns),
	}
	if l.Scope != "private" && l.Scope != "project" {
		l.Scope = "private"
	}
	if all, ok := m["auto_approve"].(bool); ok {
		l.AutoApprovePrivate = boolField(m, "auto_approve", all)
		l.AutoApproveProject = all
	} else {
		per := nestedMap(m, "auto_approve")
		l.AutoApprovePrivate = boolField(per, "private", false)
		l.AutoApproveProject = boolField(per, "project", false)
	}
	return l
}
