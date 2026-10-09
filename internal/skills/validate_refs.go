package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// Profile prompt reference validation (#86): MISSING_SKILL_REF,
// MISSING_SUBAGENT_REF and USER_SCOPE_REF_ON_SSH.
//
// A profile's SOUL.md / INSTRUCTIONS.md is free text, so only forms that
// ordinary prose does not produce are checked:
//
//	@agent-<name>                   a subagent (Claude Code's @-mention)
//	`<name>` skill / skills         backticked name(s), then the word "skill(s)" on the same line
//	`<name>` subagent / subagents   backticked name(s), then the word "subagent(s)" on the same line
//
// "@agent-" counts at the start of the text or after whitespace or one of
// ( [ { , ; " ' ` * _ < > — so "`@agent-x`", "**@agent-x**" and
// "<@agent-x>" are references, while "me@agent-x.com" and URLs are not
// ("_" and "*" count only when they do not follow a letter or digit). A
// name directly followed by a letter, digit or non-ASCII character, by an
// underscore that continues the word ("@agent-foo_bar"), or ending in a
// hyphen is not a valid name and is skipped rather than truncated.
//
// A list such as "`a`, `b`, and `c` skills" (separators , & / ; + and or,
// in any combination, on one line) names every item. Only a contiguous run
// of backticked names directly before the noun counts: "`a` and the `b`
// skills" names only b, and "(or `b`)", "etc.", lists wrapped across lines
// and table cells are not followed. Names follow Claude Code's naming rule
// (lower-case letters, digits, hyphens, starting with a letter), optionally
// prefixed `<plugin>:`, and are matched against the inventory
// case-insensitively. "skill"/"subagent" must end a word (not
// "skill-level") and must not be followed by a word that makes it a noun
// modifier ("skills directory", "subagent file", …). Fenced code blocks are
// ignored (see blankFencedBlocks). Slash commands (`/name`) are
// deliberately not checked: they also name built-in commands and file
// paths. The list and mention limits only ever leave a reference
// unchecked. Fence detection approximates Markdown containers (see
// openFence): text that only looks like a fence in a list item or quote can
// hide or expose a reference that a full Markdown parser would treat
// differently.

const refNamePattern = "[a-z][a-z0-9-]*(?::[a-z][a-z0-9-]*)?"

// refListSep is one or more list separators between backticked names.
const refListSep = "(?:[ \\t]*(?:,|&|/|;|\\+|\\band\\b|\\bor\\b)[ \\t]*)+"

// refNounModifiers follow "skill(s)"/"subagent(s)" when the phrase describes
// files or configuration rather than naming one.
const refNounModifiers = "(?:directory|directories|dir|dirs|folder|folders|file|files|module|modules|option|options|setting|settings|path|paths|list|lists|format|level|name|names|field|fields|section|sections|header|headers|config|configuration|schema|tree|table)"

var (
	refAgentMentionRe = regexp.MustCompile("(?:^|[\\s(\\[{,;\"'`*_<>])@agent-(" + refNamePattern + ")")
	refNamedListRe    = regexp.MustCompile("(`" + refNamePattern + "`(?:" + refListSep + "`" + refNamePattern + "`)*)[ \\t]+(?i:(skills?|subagents?))(?:[^\\w-]|$)")
	refNounAfterRe    = regexp.MustCompile("^(?i)[ \\t]+" + refNounModifiers + "\\b")
	refBacktickedRe   = regexp.MustCompile("`(" + refNamePattern + ")`")
)

// refListMarkerRe matches a list-item marker and the spaces after it.
var refListMarkerRe = regexp.MustCompile(`^(?:[-*+]|[0-9]{1,9}[.)])(?: +|$)`)

// refFence is an open fenced code block.
type refFence struct {
	char   byte // '`' or '~'
	n      int  // length of the opening run
	quotes int  // blockquote depth the fence sits in
	base   int  // content column of its list item (0 outside a list)
}

// leadingSpaces counts a line's indentation (a tab advances to the next
// multiple of four) and returns the rest of the line.
func leadingSpaces(s string) (int, string) {
	col := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		default:
			return col, s[i:]
		}
	}
	return col, ""
}

// stripQuotes removes up to limit blockquote markers (all of them when
// limit < 0) and returns how many it removed.
func stripQuotes(line string, limit int) (int, string) {
	n := 0
	for limit < 0 || n < limit {
		ind, rest := leadingSpaces(line)
		if ind > 3 || !strings.HasPrefix(rest, ">") {
			break
		}
		line = strings.TrimPrefix(rest[1:], " ")
		n++
	}
	return n, line
}

// fenceRun reports whether s starts with a fence marker — three or more of
// the same "`" or "~" — and returns its character, length and the rest.
func fenceRun(s string) (char byte, n int, rest string, ok bool) {
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return 0, 0, "", false
	}
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return s[0], n, s[n:], n >= 3
}

// openFence returns the fence a line opens, if any. It is lenient about
// containers: a fence counts after blockquote markers, a list-item marker
// and any indentation, so fences in list items and quotes are found without
// tracking the list structure. A fence indented four or more columns is
// taken to be inside a list item whose content starts at that column.
func openFence(line string) (*refFence, bool) {
	quotes, rest := stripQuotes(line, -1)
	col, rest := leadingSpaces(rest)
	inList := col >= 4
	if m := refListMarkerRe.FindString(rest); m != "" {
		rest = rest[len(m):]
		var more int
		more, rest = leadingSpaces(rest)
		col += len(m) + more
		inList = true
	}
	char, n, info, ok := fenceRun(rest)
	if !ok || (char == '`' && strings.Contains(info, "`")) {
		return nil, false
	}
	f := &refFence{char: char, n: n, quotes: quotes}
	if inList {
		f.base = col
	}
	return f, true
}

// blankFencedBlocks replaces the content of Markdown fenced code blocks with
// spaces, keeping newlines so positions stay stable. It follows CommonMark's
// fence rules: a fence is a run of three or more "`" or "~" (a backtick
// fence's info string may not contain a backtick) and is closed by a run of
// the same character at least as long, indented at most three columns
// within its container, with nothing else on the line. A fence also ends
// with its container: a line with fewer blockquote markers, or a non-blank
// line indented less than its list item's content. An unclosed fence runs
// to the end of the text. Container detection is approximate (see
// openFence); the package doc lists what that can get wrong.
func blankFencedBlocks(text string) string {
	lines := strings.SplitAfter(text, "\n")
	var open *refFence
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		if open != nil {
			quotes, rest := stripQuotes(body, open.quotes)
			ind, content := leadingSpaces(rest)
			ended := quotes < open.quotes || (open.base > 0 && content != "" && ind < open.base)
			if !ended {
				if char, n, after, ok := fenceRun(content); ok && ind-open.base <= 3 &&
					char == open.char && n >= open.n && strings.TrimSpace(after) == "" {
					open = nil
				}
				lines[i] = blankLine(line)
				continue
			}
			open = nil // the container ended; this line is ordinary text again
		}
		if f, ok := openFence(body); ok {
			open = f
			lines[i] = blankLine(line)
		}
	}
	return strings.Join(lines, "")
}

func blankLine(line string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		return ' '
	}, line)
}

// mentionNameContinues reports whether the text right after an @agent-
// name continues it with characters a name cannot hold: a letter, digit or
// non-ASCII character, or underscores followed by one ("_" or "__" alone
// may close emphasis, as in "_@agent-x_").
func mentionNameContinues(after string) bool {
	rest := strings.TrimLeft(after, "_")
	return rest != "" && (isNameByte(rest[0]) || rest[0] >= 0x80)
}

// isNameByte reports whether c is an ASCII letter or digit.
func isNameByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
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
		if mentionNameContinues(text[m[3]:]) || strings.HasSuffix(text[m[2]:m[3]], "-") {
			continue // "@agent-foo_bar" / "@agent-fooBar" / "@agent-foo-" is not a valid name
		}
		if m[0] > 0 && (text[m[0]] == '_' || text[m[0]] == '*') && isNameByte(text[m[0]-1]) {
			continue // "ops_@agent-corp.com", ".../a_@agent-x": inside a word, not emphasis
		}
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

// ProfileBackend returns the runner ("claude" or "codex") a profile runs on.
// It replays the orchestrator's dispatch resolver
// (internal/orchestrator/dispatch_resolve.go resolveDispatchTarget) for the
// static part of the decision and then routes the resulting runner command
// the way agent.MultiRunner does, so the answer is the runner that would
// actually execute: the default command, then agent.backend, then the
// profile's command (which replaces the command), then the profile's
// backend. A requested backend is honoured only when the command's binary
// is unrecognised or already matches; a conflicting request keeps the
// binary. MultiRunner routes by agent.BackendFromCommand of the runner
// command and sends anything other than codex to the default (Claude)
// runner. Per-issue pins and rate-limit switches are runtime decisions and
// are not modelled.
func ProfileBackend(p config.AgentProfile, d RefBackendDefaults) string {
	cmd := d.Command
	runner := cmd
	request := func(b string) {
		if b == "" {
			return
		}
		if bin := commandBinaryBackend(cmd); bin != "" && bin != b {
			runner = stripBackendHint(cmd)
			return
		}
		runner = agent.CommandWithBackendHint(cmd, b)
	}
	request(d.Backend)
	if p.Command != "" {
		cmd = p.Command
		runner = cmd
	}
	request(p.Backend)
	if agent.BackendFromCommand(runner) == "codex" {
		return "codex"
	}
	return "claude"
}

// stripBackendHint returns command without a leading backend hint (the
// resolver's helper of the same name).
func stripBackendHint(command string) string {
	if hinted, rest := config.ParseBackendHint(command); hinted != "" {
		return rest
	}
	return command
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
	projectPlugins := map[string]bool{}
	for _, p := range inv.Plugins {
		if p.Source == "project" {
			projectPlugins[p.Name] = true
		}
	}
	put := func(m map[string]*refTarget, name, source string) {
		// A plugin installed in the repository travels with it like any
		// other project file.
		if plugin, ok := strings.CutPrefix(source, "plugin:"); ok && projectPlugins[plugin] {
			source = "project"
		}
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
		backend := ProfileBackend(p, defaults)
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
