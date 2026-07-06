package orchestrator

import (
	"regexp"
	"strings"
)

// TrackerCommentAutomation is the compiled, event-loop-ready form of a
// `tracker_comment_added` automation rule. The body filter (BodyContainsAny /
// BodyRegex) lets the daemon pre-filter comments before any agent runs, so a
// merge-bot only wakes on its trigger phrase and not on every reviewer chat
// comment.
type TrackerCommentAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
	BodyContainsAny []string
	BodyRegex       *regexp.Regexp
}

// SetTrackerCommentAutomations installs the compiled tracker_comment_added
// rule slice under automationsMu. Generic helper sibling of the other
// per-trigger Set helpers.
func (o *Orchestrator) SetTrackerCommentAutomations(rules []TrackerCommentAutomation) {
	setAutomationRegistry(o, &o.trackerCommentAutomations, rules)
}

// SnapTrackerCommentAutomations returns a lock-free copy of the registered
// tracker_comment_added automation rules. Exposed for the body-filter dispatch
// helper called from the tracker poll-comment ingestion path.
func (o *Orchestrator) SnapTrackerCommentAutomations() []TrackerCommentAutomation {
	return snapAutomationRegistry(o, &o.trackerCommentAutomations)
}

// FilterTrackerCommentAutomationsByBody returns the subset of registered
// tracker_comment_added rules whose body filter accepts the given comment
// body. Called from the orchestrator's comment ingestion path so a comment
// that does not match any operator-configured body filter never produces an
// automation dispatch.
func (o *Orchestrator) FilterTrackerCommentAutomationsByBody(body string) []TrackerCommentAutomation {
	rules := o.SnapTrackerCommentAutomations()
	if len(rules) == 0 {
		return nil
	}
	out := rules[:0:0]
	for _, rule := range rules {
		if commentBodyMatchesFilter(body, rule.BodyContainsAny, rule.BodyRegex) {
			out = append(out, rule)
		}
	}
	return out
}

// commentBodyMatchesFilter reports whether body satisfies the body_contains /
// body_regex pair on a tracker_comment_added automation. Empty filters always
// match. When both are set, BOTH must match (AND). Empty body still matches an
// empty filter set (preserves "no filter == match all" semantics).
func commentBodyMatchesFilter(body string, containsAny []string, bodyRegex *regexp.Regexp) bool {
	hasContains := len(containsAny) > 0
	hasRegex := bodyRegex != nil
	if !hasContains && !hasRegex {
		return true
	}
	if hasContains {
		lower := strings.ToLower(body)
		matched := false
		for _, needle := range containsAny {
			if strings.Contains(lower, strings.ToLower(needle)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if hasRegex && !bodyRegex.MatchString(body) {
		return false
	}
	return true
}
