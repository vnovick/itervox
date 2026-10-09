package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// Profile prompt reference validation (#86): MISSING_SKILL_REF,
// MISSING_SUBAGENT_REF and USER_SCOPE_REF_ON_SSH.
//
// A profile's SOUL.md / INSTRUCTIONS.md is free text, so only forms that
// ordinary prose does not produce are checked:
//
//	@agent-<name>                   a subagent (Claude Code's @-mention), at a word start
//	`<name>` skill / skills         backticked name(s), then the word "skill(s)" on the same line
//	`<name>` subagent / subagents   backticked name(s), then the word "subagent(s)" on the same line
//
// A list such as "`a`, `b`, and `c` skills" (separators , & / ; + and or,
// in any combination) names every item. Names follow Claude Code's naming
// rule (lower-case letters, digits, hyphens, starting with a letter),
// optionally prefixed `<plugin>:`, and are matched against the inventory
// case-insensitively. "skill"/"subagent" must end a word (not "skill-level")
// and must not be followed by a word that makes it a noun modifier
// ("skills directory", "subagent file", …). Fenced code blocks (``` or ~~~
// at the start of a line) are ignored. Slash commands (`/name`) are
// deliberately not checked: they also name built-in commands and file paths.

const refNamePattern = "[a-z][a-z0-9-]*(?::[a-z][a-z0-9-]*)?"

// refListSep is one or more list separators between backticked names.
const refListSep = "(?:[ \\t]*(?:,|&|/|;|\\+|\\band\\b|\\bor\\b)[ \\t]*)+"

// refNounModifiers follow "skill(s)"/"subagent(s)" when the phrase describes
// files or configuration rather than naming one.
const refNounModifiers = "(?:directory|directories|dir|dirs|folder|folders|file|files|module|modules|option|options|setting|settings|path|paths|list|lists|format|level|name|names|field|fields|section|sections|header|headers|config|configuration|schema|tree|table)"

var (
	refAgentMentionRe = regexp.MustCompile(`(?:^|[\s(\[{,;"'])@agent-(` + refNamePattern + `)`)
	refNamedListRe    = regexp.MustCompile("(`" + refNamePattern + "`(?:" + refListSep + "`" + refNamePattern + "`)*)[ \\t]+(?i:(skills?|subagents?))(?:[^\\w-]|$)")
	refNounAfterRe    = regexp.MustCompile("^(?i)[ \\t]+" + refNounModifiers + "\\b")
	refBacktickedRe   = regexp.MustCompile("`(" + refNamePattern + ")`")
	refFenceLineRe    = regexp.MustCompile("^[ ]{0,3}(```|~~~)")
)

// blankFencedBlocks replaces the content of Markdown fenced code blocks
// (opened by ``` or ~~~ at the start of a line, up to three spaces of
// indent, and closed by the same marker) with spaces, keeping newlines so
// positions stay stable. An unclosed fence runs to the end of the text, as
// in Markdown.
func blankFencedBlocks(text string) string {
	lines := strings.SplitAfter(text, "\n")
	fence := ""
	for i, line := range lines {
		m := refFenceLineRe.FindStringSubmatch(line)
		switch {
		case fence == "" && m != nil:
			fence = m[1]
		case fence != "" && m != nil && m[1] == fence:
			fence = ""
		case fence == "":
			continue
		}
		lines[i] = strings.Map(func(r rune) rune {
			if r == '\n' {
				return r
			}
			return ' '
		}, line)
	}
	return strings.Join(lines, "")
}

// PromptRef is one explicit skill or subagent reference found in a profile
// prompt.
type PromptRef struct {
	Kind string // "skill" | "subagent"
	Name string
}

// ExtractPromptRefs returns the explicit references in text, de-duplicated,
// in order of first appearance.
func ExtractPromptRefs(text string) []PromptRef {
	text = blankFencedBlocks(text)
	type hit struct {
		pos        int
		kind, name string
	}
	var hits []hit
	for _, m := range refAgentMentionRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[2], "subagent", text[m[2]:m[3]]})
	}
	for _, m := range refNamedListRe.FindAllStringSubmatchIndex(text, -1) {
		if refNounAfterRe.MatchString(text[m[5]:]) {
			continue // "`x` skills directory" describes files, not a skill
		}
		kind := "skill"
		if strings.HasPrefix(strings.ToLower(text[m[4]:m[5]]), "subagent") {
			kind = "subagent"
		}
		list := text[m[2]:m[3]]
		for _, n := range refBacktickedRe.FindAllStringSubmatchIndex(list, -1) {
			hits = append(hits, hit{m[2] + n[2], kind, list[n[2]:n[3]]})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	var out []PromptRef
	seen := map[string]bool{}
	for _, h := range hits {
		key := h.kind + "|" + strings.ToLower(h.name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, PromptRef{Kind: h.kind, Name: h.name})
	}
	return out
}

// RefBackendDefaults are the workflow-level agent settings a profile
// inherits: agent.command and agent.backend.
type RefBackendDefaults struct {
	Command string
	Backend string
}

// commandBinaryBackend is the backend a command's binary runs ("" for an
// unrecognised wrapper), ignoring any backend hint.
func commandBinaryBackend(command string) string {
	if hinted, rest := config.ParseBackendHint(command); hinted != "" {
		command = rest
	}
	if base := filepath.Base(config.FirstCommandToken(command)); config.IsSupportedBackend(base) {
		return base
	}
	return ""
}

// profileBackend mirrors the orchestrator's dispatch resolver
// (internal/orchestrator/dispatch_resolve.go resolveDispatchTarget) for the
// static part of the decision: the default command's backend, then
// agent.backend, then the profile's command (which replaces the command and
// its backend), then the profile's backend. A requested backend is honoured
// only when the command's binary is unrecognised or already matches; a
// conflicting request keeps the binary's backend. An undetermined backend
// runs on the default (Claude) runner. Per-issue pins and rate-limit
// switches are runtime decisions and are not modelled.
func profileBackend(p config.AgentProfile, d RefBackendDefaults) string {
	cmd := d.Command
	backend := config.BackendFromCommand(cmd)
	// Values are used exactly as configured, like the resolver: a padded
	// " codex" becomes a hint ParseBackendHint rejects, and the run falls
	// back to the Claude runner.
	request := func(b string) {
		if b == "" {
			return
		}
		if bin := commandBinaryBackend(cmd); bin != "" && bin != b {
			backend = bin
			return
		}
		backend = b
	}
	request(d.Backend)
	if p.Command != "" {
		cmd = p.Command
		backend = config.BackendFromCommand(cmd)
	}
	request(p.Backend)
	if backend != "codex" {
		return "claude"
	}
	return backend
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
func ValidateProfileRefs(inv *Inventory, profiles map[string]config.AgentProfile, defaults RefBackendDefaults, sshHosts []string) []InventoryIssue {
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
		backend := profileBackend(p, defaults)
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
