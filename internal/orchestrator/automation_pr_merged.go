package orchestrator

import (
	"regexp"
)

// PRMergedAutomation is the compiled, event-loop-ready form of a `pr_merged`
// automation rule (P1). Fires when an itervox-managed PR transitions to
// MERGED. Mirrors PROpenedAutomation in shape so SetPRMergedAutomations can
// piggyback on the generic registry helpers.
type PRMergedAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
}

// SetPRMergedAutomations installs the compiled pr_merged rule slice under
// automationsMu. Generic helper sibling of SetPROpenedAutomations.
func (o *Orchestrator) SetPRMergedAutomations(rules []PRMergedAutomation) {
	setAutomationRegistry(o, &o.prMergedAutomations, rules)
}

// snapPRMergedAutomations returns a lock-free copy of the registered
// pr_merged automation rules.
func (o *Orchestrator) snapPRMergedAutomations() []PRMergedAutomation {
	return snapAutomationRegistry(o, &o.prMergedAutomations)
}
