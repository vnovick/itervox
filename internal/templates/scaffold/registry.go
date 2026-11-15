// Package scaffold owns the named starter templates the `itervox init
// --template` flag selects between (#121). The registry holds each template's
// name and its one-line summary; cmd/itervox renders the workflow and the
// profile files each template writes.
package scaffold

import "slices"

// The starter template names.
const (
	Minimal           = "minimal"
	Full              = "full"
	RateLimitFallback = "rate-limit-fallback"
	PRReview          = "pr-review"
	DailyQA           = "daily-qa"
)

// KnownTemplates are the accepted --template values, in help order.
var KnownTemplates = []string{
	Minimal,
	Full,
	RateLimitFallback,
	PRReview,
	DailyQA,
}

// summaries is the one-line description `itervox init --help` and the CLI
// reference print for each template.
var summaries = map[string]string{
	Minimal:           "one implementer profile, no reviewer and no automations",
	Full:              "implementer, reviewer, input-responder and deps-analyzer; auto-review chain; input-required and failed-run automations",
	RateLimitFallback: "implementer on each backend; agent.backend_fallback plus a rate_limited automation that switches to the other backend",
	PRReview:          "implementer, a run reviewer and a pull-request reviewer; auto-review plus pr_opened and \"/review\" comment automations",
	DailyQA:           "implementer and a qa profile; a daily cron automation runs QA on issues awaiting review",
}

// IsKnown reports whether name is one of the shipped template presets.
func IsKnown(name string) bool {
	return slices.Contains(KnownTemplates, name)
}

// Names returns a fresh copy of the known template list.
func Names() []string {
	out := make([]string, len(KnownTemplates))
	copy(out, KnownTemplates)
	return out
}

// Summary returns the one-line description of a known template, or "".
func Summary(name string) string {
	return summaries[name]
}
