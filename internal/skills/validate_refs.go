package skills

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// Profile prompt reference validation (#86): MISSING_SKILL_REF,
// MISSING_SUBAGENT_REF and USER_SCOPE_REF_ON_SSH.
//
// A profile's SOUL.md / INSTRUCTIONS.md is free text, so only explicit,
// unambiguous reference forms are checked — plain prose mentioning a word
// never warns:
//
//	@agent-<name>              a subagent (Claude Code's @-mention form)
//	`/<name>`                  a skill invoked as a slash command, in backticks
//	`<name>` skill             a skill named in backticks, then the word "skill"
//	`<name>` subagent|agent    a subagent named in backticks, then "subagent"/"agent"
//
// Names are matched case-insensitively. A plugin skill or agent also resolves
// as `<plugin>:<name>`.

var (
	refAgentMentionRe = regexp.MustCompile(`(?i)(?:^|[^\w@])@agent-([a-z0-9][a-z0-9_.:-]*)`)
	refSlashSkillRe   = regexp.MustCompile("`/([a-zA-Z0-9][a-zA-Z0-9_.:-]*)`")
	refNamedRe        = regexp.MustCompile("(?i)`([a-z0-9][a-z0-9_.:-]*)`\\s+(skill|subagent|agent)s?\\b")
)

// PromptRef is one explicit skill or subagent reference found in a profile
// prompt.
type PromptRef struct {
	Kind string // "skill" | "subagent"
	Name string
}

// ExtractPromptRefs returns the explicit references in text, de-duplicated,
// in order of first appearance.
func ExtractPromptRefs(text string) []PromptRef {
	var out []PromptRef
	seen := map[string]bool{}
	add := func(kind, name string) {
		name = strings.TrimRight(name, ".:-")
		key := kind + "|" + strings.ToLower(name)
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, PromptRef{Kind: kind, Name: name})
	}
	type hit struct {
		pos        int
		kind, name string
	}
	var hits []hit
	for _, m := range refAgentMentionRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[2], "subagent", text[m[2]:m[3]]})
	}
	for _, m := range refSlashSkillRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[2], "skill", text[m[2]:m[3]]})
	}
	for _, m := range refNamedRe.FindAllStringSubmatchIndex(text, -1) {
		kind := "skill"
		if w := strings.ToLower(text[m[4]:m[5]]); w == "subagent" || w == "agent" {
			kind = "subagent"
		}
		hits = append(hits, hit{m[2], kind, text[m[2]:m[3]]})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	for _, h := range hits {
		add(h.kind, h.name)
	}
	return out
}

// profileBackend returns "codex" or "claude" for a profile: the explicit
// backend wins, otherwise a command naming codex means Codex.
func profileBackend(p config.AgentProfile) string {
	if b := strings.ToLower(strings.TrimSpace(p.Backend)); b != "" {
		return b
	}
	if fields := strings.Fields(p.Command); len(fields) > 0 && strings.Contains(strings.ToLower(fields[0]), "codex") {
		return "codex"
	}
	return "claude"
}

// refTarget is one resolvable name with where it comes from.
type refTarget struct {
	sources map[string]bool // "project" | "user" | "plugin:x" | "system" ...
}

func (t *refTarget) add(source string) {
	if t.sources == nil {
		t.sources = map[string]bool{}
	}
	t.sources[source] = true
}

// onlyOutsideRepo reports whether every source is outside the repository
// (user home or a plugin), i.e. nothing travels with the repo to a remote host.
func (t *refTarget) onlyOutsideRepo() bool {
	for s := range t.sources {
		if s == "project" {
			return false
		}
	}
	return len(t.sources) > 0
}

// resolvableNames indexes the inventory's skills (per backend) and subagents
// by lower-cased name, plus `<plugin>:<name>` for plugin entries.
func resolvableNames(inv *Inventory, backend string) (skills, agents map[string]*refTarget) {
	skills, agents = map[string]*refTarget{}, map[string]*refTarget{}
	put := func(m map[string]*refTarget, name, source string) {
		k := strings.ToLower(name)
		if m[k] == nil {
			m[k] = &refTarget{}
		}
		m[k].add(source)
	}
	skillFits := func(provider string) bool {
		switch provider {
		case "", "shared":
			return true
		default:
			return provider == backend
		}
	}
	for _, s := range inv.Skills {
		if skillFits(s.Provider) {
			put(skills, s.Name, s.Source)
		}
	}
	for _, p := range inv.Plugins {
		if !skillFits(p.Provider) {
			continue
		}
		for _, s := range p.Skills {
			put(skills, s.Name, "plugin:"+p.Name)
			put(skills, p.Name+":"+s.Name, "plugin:"+p.Name)
		}
	}
	if backend == "claude" {
		for _, a := range inv.Subagents {
			put(agents, a.Name, a.Source)
			if plugin, ok := strings.CutPrefix(a.Source, "plugin:"); ok {
				put(agents, plugin+":"+a.Name, a.Source)
			}
		}
	}
	return skills, agents
}

// ValidateProfileRefs checks every profile's SOUL.md and INSTRUCTIONS.md for
// explicit skill and subagent references and returns one issue per missing
// reference, plus an info issue when a reference resolves only outside the
// repository while SSH hosts are configured. Profiles are visited in name
// order so the output is stable.
func ValidateProfileRefs(inv *Inventory, profiles map[string]config.AgentProfile, sshHosts []string) []InventoryIssue {
	if inv == nil || len(profiles) == 0 {
		return nil
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)

	var issues []InventoryIssue
	for _, name := range names {
		p := profiles[name]
		backend := profileBackend(p)
		skillIdx, agentIdx := resolvableNames(inv, backend)
		for _, ref := range ExtractPromptRefs(p.Soul + "\n" + p.Instructions) {
			idx := skillIdx
			if ref.Kind == "subagent" {
				idx = agentIdx
			}
			target := idx[strings.ToLower(ref.Name)]
			if target == nil {
				issues = append(issues, missingRefIssue(name, backend, ref))
				continue
			}
			if len(sshHosts) > 0 && target.onlyOutsideRepo() {
				issues = append(issues, InventoryIssue{
					ID:       "USER_SCOPE_REF_ON_SSH",
					Severity: "info",
					Title:    fmt.Sprintf("Profile %q references %s %q that only exists outside the repository", name, ref.Kind, ref.Name),
					Description: fmt.Sprintf("%s %q was found only in user or plugin scope on this machine. Agents on SSH hosts (%s) see the repository's .claude/ directory but not this machine's home directory or plugins, so the %s may be missing there. Commit it under .claude/ to make it travel with the repo.",
						capitalize(ref.Kind), ref.Name, strings.Join(sshHosts, ", "), ref.Kind),
					Affected: []string{name, ref.Name},
				})
			}
		}
	}
	return issues
}

func missingRefIssue(profile, backend string, ref PromptRef) InventoryIssue {
	if ref.Kind == "subagent" {
		desc := fmt.Sprintf("Profile %q's prompt references subagent %q, but no subagent with that name was found in .claude/agents (project or user) or in an installed plugin.", profile, ref.Name)
		if backend == "codex" {
			desc = fmt.Sprintf("Profile %q runs on Codex, which has no subagents, but its prompt references subagent %q.", profile, ref.Name)
		}
		return InventoryIssue{
			ID:          "MISSING_SUBAGENT_REF",
			Severity:    "warn",
			Title:       fmt.Sprintf("Profile %q references unknown subagent %q", profile, ref.Name),
			Description: desc,
			Affected:    []string{profile, ref.Name},
		}
	}
	where := ".claude/skills (project or user) or an installed Claude plugin"
	if backend == "codex" {
		where = "the Codex skill directories"
	}
	return InventoryIssue{
		ID:          "MISSING_SKILL_REF",
		Severity:    "warn",
		Title:       fmt.Sprintf("Profile %q references unknown skill %q", profile, ref.Name),
		Description: fmt.Sprintf("Profile %q (%s) references skill %q, but no skill with that name was found in %s.", profile, backend, ref.Name, where),
		Affected:    []string{profile, ref.Name},
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
