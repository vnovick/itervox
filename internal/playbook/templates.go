package playbook

import (
	"embed"
	"strings"
)

// templateFS holds the playbooks `itervox init` scaffolds (#218).
//
//go:embed templates/*.md
var templateFS embed.FS

// TemplateNames are the scaffolded playbooks, in the order init lists them.
var TemplateNames = []string{"bug-fix", "feature", "refactor", "perf", "investigation", "docs"}

// Template returns the scaffold content of the named playbook.
func Template(name string) (string, bool) {
	data, err := templateFS.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(data), "\n") + "\n", true
}
