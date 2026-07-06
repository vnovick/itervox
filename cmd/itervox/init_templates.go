package main

import "github.com/vnovick/itervox/internal/templates/scaffold"

// IsKnownTemplateName re-exports scaffold.IsKnown for the init flag-handling
// path. todolist4 A.1.
func IsKnownTemplateName(name string) bool { return scaffold.IsKnown(name) }

// KnownTemplateNames returns the human-friendly list of supported template
// presets so flag errors can tell operators what's accepted.
func KnownTemplateNames() []string { return scaffold.Names() }
