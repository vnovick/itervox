package rules

import (
	"regexp"
	"strings"
)

// blockIntro opens the rendered `## Rules` block.
const blockIntro = "The operator set these rules for every run. A must rule is a requirement. " +
	"A should rule is a strong default: depart from it only with a reason you state in your handoff."

// Block renders the set as the `## Rules` prompt block, must rules first. ""
// for an empty set.
func (s Set) Block() string {
	if len(s.Rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Rules\n\n")
	b.WriteString(blockIntro)
	if must := s.list(SeverityMust); must != "" {
		b.WriteString("\n\n### Must\n\n")
		b.WriteString(must)
	}
	if should := s.list(SeverityShould); should != "" {
		b.WriteString("\n\n### Should\n\n")
		b.WriteString(should)
	}
	return b.String()
}

// list renders the rules of one severity as a Markdown list, "" when there
// are none.
func (s Set) list(sev Severity) string {
	var lines []string
	for _, r := range s.Rules {
		if r.Severity == sev {
			lines = append(lines, "- "+strings.ReplaceAll(r.Text, "\n", "\n  ")+" ("+r.ID+")")
		}
	}
	return strings.Join(lines, "\n")
}

// Binding is the Liquid `rules` object. `{{ rules }}` prints the whole
// `## Rules` block; `{{ rules.must }}` and `{{ rules.should }}` print just
// that severity's list; `rules.ids` and `rules.hash` describe the set. A
// template that prints `rules` places the rules itself, and the daemon then
// adds no block of its own.
type Binding struct {
	Block  string   `liquid:"block"`
	Must   string   `liquid:"must"`
	Should string   `liquid:"should"`
	IDs    []string `liquid:"ids"`
	Hash   string   `liquid:"hash"`
}

// String is what `{{ rules }}` prints.
func (b Binding) String() string { return b.Block }

// Binding returns the set's Liquid binding.
func (s Set) Binding() Binding {
	return Binding{
		Block:  s.Block(),
		Must:   s.list(SeverityMust),
		Should: s.list(SeverityShould),
		IDs:    s.IDs(),
		Hash:   s.Hash(),
	}
}

// placesRulesRe matches a Liquid output tag that prints the rules object or
// one of its fields: `{{ rules }}`, `{{- rules.must -}}`, `{{ rules | strip }}`.
var placesRulesRe = regexp.MustCompile(`\{\{-?\s*rules\s*(?:[.\[|}-]|$)`)

// TemplatePlacesRules reports whether any of the templates prints the rules
// itself, in which case no automatic `## Rules` block is added.
func TemplatePlacesRules(templates ...string) bool {
	for _, t := range templates {
		if placesRulesRe.MatchString(t) {
			return true
		}
	}
	return false
}
