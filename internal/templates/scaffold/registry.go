// Package scaffold owns the named init presets the `itervox init --template`
// flag selects between. The minimum-viable shape is a name registry; the
// presets currently produce the same output as the default scaffold, with
// future PRs filling in the per-template differentiation. todolist4 A.1.
package scaffold

import "slices"

// KnownTemplates are the accepted --template values. minimal is the default.
var KnownTemplates = []string{
	"minimal",
	"full",
	"rate-limit-fallback",
	"pr-review",
	"daily-qa",
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
