package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/automationdef"
	"gopkg.in/yaml.v3"
)

// ErrorCode identifies the category of a workflow load/parse failure.
type ErrorCode string

// Workflow error code constants returned by Load.
const (
	ErrMissingFile        ErrorCode = "missing_workflow_file"
	ErrParseError         ErrorCode = "workflow_parse_error"
	ErrFrontMatterNotAMap ErrorCode = "workflow_front_matter_not_a_map"
)

// Error is a typed workflow error carrying a code and optional cause.
type Error struct {
	Code  ErrorCode
	Path  string
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Path, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Path)
}

func (e *Error) Unwrap() error { return e.Cause }

// Workflow holds the parsed front matter and prompt template from a WORKFLOW.md file.
type Workflow struct {
	Config         map[string]any
	PromptTemplate string
}

// Load reads and parses a WORKFLOW.md file at the given path.
func Load(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Code: ErrMissingFile, Path: path, Cause: err}
	}
	return parse(path, string(data))
}

func parse(path, content string) (*Workflow, error) {
	frontLines, promptLines := splitFrontMatter(content)

	config, err := parseFrontMatter(path, frontLines)
	if err != nil {
		return nil, err
	}

	prompt := strings.TrimSpace(strings.Join(promptLines, "\n"))
	return &Workflow{Config: config, PromptTemplate: prompt}, nil
}

// splitFrontMatter splits content on --- delimiters.
// Returns front matter lines and prompt body lines.
func splitFrontMatter(content string) (front []string, body []string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, lines
	}
	// Skip the opening "---"
	rest := lines[1:]
	for i, line := range rest {
		if line == "---" {
			return rest[:i], rest[i+1:]
		}
	}
	// Opening --- but no closing ---: treat all as front matter, empty body
	return rest, nil
}

func parseFrontMatter(path string, lines []string) (map[string]any, error) {
	if len(lines) == 0 {
		return map[string]any{}, nil
	}
	raw := strings.Join(lines, "\n")
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}

	var decoded any
	if err := yaml.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, &Error{Code: ErrParseError, Path: path, Cause: err}
	}

	switch v := decoded.(type) {
	case map[string]any:
		return v, nil
	case nil:
		return map[string]any{}, nil
	default:
		return nil, &Error{Code: ErrFrontMatterNotAMap, Path: path}
	}
}

// keyLineRE matches a YAML key-value line like "  max_concurrent_agents: 3"
// and captures the leading whitespace, the key, and the value.
var keyLineRE = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*:\s*)(.*)$`)

// PatchIntField rewrites the first occurrence of `key: <int>` inside the
// YAML front matter of the file at path, replacing the integer value with n.
// The rest of the file (comments, formatting, body) is preserved byte-for-byte.
// Returns an error if the key is not found in the front matter or the file
// cannot be read/written.
//
// T-46 (06.G-01): grabs editMu via lockForPath so concurrent calls for the
// same path serialize. Without this, two callers (e.g. HTTP SetWorkers and
// TUI AdjustWorkers, which both invoke PatchIntField with `max_concurrent_agents`)
// could read the same starting bytes and have one rename clobber the other's
// edit. The Patch{Agent,Workspace,Profiles,Automations}* helpers already
// route through ApplyAndWriteFrontMatter for the same guarantee; this
// function predated that pattern.
func PatchIntField(path, key string, n int) error {
	unlock := lockForPath(path)
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	// Only patch within the front matter (between the first pair of --- markers).
	frontEnd := -1
	if strings.HasPrefix(content, "---\n") {
		idx := strings.Index(content[4:], "\n---")
		if idx >= 0 {
			frontEnd = 4 + idx + 1 // index of the closing '\n---'
		}
	}
	searchRegion := content
	if frontEnd > 0 {
		searchRegion = content[:frontEnd]
	}

	lines := strings.Split(searchRegion, "\n")
	replaced := false
	for i, line := range lines {
		m := keyLineRE.FindStringSubmatch(line)
		if m == nil || m[2] != key {
			continue
		}
		// Preserve everything except the value; strip inline comment from old value.
		oldVal := m[4]
		comment := ""
		if ci := strings.Index(oldVal, " #"); ci >= 0 {
			comment = " " + strings.TrimSpace(oldVal[ci+1:])
			comment = " #" + comment[2:]
		}
		lines[i] = m[1] + m[2] + m[3] + strconv.Itoa(n) + comment
		replaced = true
		break
	}
	if !replaced {
		return fmt.Errorf("workflow patch: key %q not found in front matter of %s", key, path)
	}

	patched := strings.Join(lines, "\n")
	if frontEnd > 0 {
		patched = patched + content[frontEnd:]
	}
	return atomicfs.WriteFile(path, []byte(patched), 0o644)
}

// PatchAgentBoolField sets a boolean key under the agent: block of the YAML front matter.
// If the key already exists it is updated in place; if it does not exist it is appended
// inside the agent: block. Setting enabled=false removes the key entirely.
func PatchAgentBoolField(path, key string, enabled bool) error {
	return patchBlockBoolField(path, "agent", key, enabled)
}

// PatchWorkspaceBoolField sets a boolean key under the workspace: block of the YAML front matter.
// Behaves identically to PatchAgentBoolField but targets the workspace: block.
func PatchWorkspaceBoolField(path, key string, enabled bool) error {
	return patchBlockBoolField(path, "workspace", key, enabled)
}

// defaultBlockIndent matches the convention written by `itervox init` for
// freshly-scaffolded workflows. Used as the fallback when the target block
// has no existing children to learn from.
const defaultBlockIndent = "  "

// detectBlockIndent scans forward from the block-header line for the first
// child entry and returns its leading-whitespace prefix (spaces or tabs).
// Falls back to `defaultBlockIndent` when:
//   - blockLine is negative (block not present yet),
//   - the block is empty,
//   - every child line is blank or a comment.
//
// Without this, the Patch* helpers hardcoded a
// 2-space prefix and silently corrupted any WORKFLOW.md whose front matter
// used 4-space indent (yaml.v3's default serialisation produces 4-space
// nested keys on common Go YAML codecs).
func detectBlockIndent(frontLines []string, blockLine int) string {
	if blockLine < 0 {
		return defaultBlockIndent
	}
	for j := blockLine + 1; j < len(frontLines); j++ {
		line := frontLines[j]
		// Stop at the next top-level key (no leading whitespace).
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return defaultBlockIndent
}

// findBlockHeader returns the index of the line equal to "<block>:", or -1.
func findBlockHeader(frontLines []string, block string) int {
	target := block + ":"
	for i, l := range frontLines {
		if l == target {
			return i
		}
	}
	return -1
}

// findKeyInBlock searches inside the block for a child line whose stripped
// content starts with `key:`. Returns -1 when absent. Honours arbitrary
// indent (so a 4-space-indented file gets matched as cleanly as a 2-space).
func findKeyInBlock(frontLines []string, blockLine int, key string) int {
	if blockLine < 0 {
		return -1
	}
	for j := blockLine + 1; j < len(frontLines); j++ {
		line := frontLines[j]
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			return -1 // next top-level block reached
		}
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, key+":") {
			return j
		}
	}
	return -1
}

// writeFrontMatter is defined in settings_patch.go (shared trailing-newline
// policy for every patcher in this package).

// patchBlockBoolField is the shared implementation used by PatchAgentBoolField
// and PatchWorkspaceBoolField.
func patchBlockBoolField(path, block, key string, enabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch bool: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow patch bool: no front matter in %s", path)
	}

	blockLine := findBlockHeader(frontLines, block)
	indent := detectBlockIndent(frontLines, blockLine)
	keyLine := indent + key + ": "
	keyFound := findKeyInBlock(frontLines, blockLine, key)

	switch {
	case keyFound >= 0 && !enabled:
		frontLines = append(frontLines[:keyFound], frontLines[keyFound+1:]...)
	case keyFound >= 0:
		frontLines[keyFound] = keyLine + "true"
	case enabled:
		frontLines = insertAfterBlockHeader(frontLines, blockLine, keyLine+"true")
	}
	return writeFrontMatter(path, frontLines, bodyLines)
}

// insertAfterBlockHeader inserts `line` immediately after `blockLine`, or at
// the end of frontLines when the block header is absent (defensive — patcher
// callers always pass a valid header in practice).
func insertAfterBlockHeader(frontLines []string, blockLine int, line string) []string {
	insertAt := len(frontLines)
	if blockLine >= 0 {
		insertAt = blockLine + 1
	}
	out := make([]string, 0, len(frontLines)+1)
	out = append(out, frontLines[:insertAt]...)
	out = append(out, line)
	out = append(out, frontLines[insertAt:]...)
	return out
}

// PatchAgentStringField sets or removes a string key under the agent: block of the YAML front matter.
// If the key already exists it is updated in place; if value == "" the key is removed.
func PatchAgentStringField(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch string: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow patch string: no front matter in %s", path)
	}

	blockLine := findBlockHeader(frontLines, "agent")
	indent := detectBlockIndent(frontLines, blockLine)
	keyPrefix := indent + key + ": "
	keyFound := findKeyInBlock(frontLines, blockLine, key)

	switch {
	case keyFound >= 0 && value == "":
		frontLines = append(frontLines[:keyFound], frontLines[keyFound+1:]...)
	case keyFound >= 0:
		frontLines[keyFound] = keyPrefix + strconv.Quote(value)
	case value != "":
		frontLines = insertAfterBlockHeader(frontLines, blockLine, keyPrefix+strconv.Quote(value))
	}
	return writeFrontMatter(path, frontLines, bodyLines)
}

// PatchAgentStringSliceField sets or removes a string-slice key under the
// agent: block of the YAML front matter. Empty values remove the key.
func PatchAgentStringSliceField(path, key string, values []string) error {
	return patchBlockStringSliceField(path, "agent", key, values)
}

func patchBlockStringSliceField(path, block, key string, values []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch string slice: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow patch string slice: no front matter in %s", path)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("workflow patch string slice: marshal %q: %w", key, err)
	}

	blockLine := findBlockHeader(frontLines, block)
	indent := detectBlockIndent(frontLines, blockLine)
	keyLine := indent + key + ": "
	keyFound := findKeyInBlock(frontLines, blockLine, key)

	switch {
	case keyFound >= 0 && len(values) == 0:
		frontLines = append(frontLines[:keyFound], frontLines[keyFound+1:]...)
	case keyFound >= 0:
		frontLines[keyFound] = keyLine + string(encoded)
	case len(values) > 0:
		frontLines = insertAfterBlockHeader(frontLines, blockLine, keyLine+string(encoded))
	}
	return writeFrontMatter(path, frontLines, bodyLines)
}

// PatchAgentStringMapField replaces or removes a string map under the agent:
// block of the YAML front matter. Empty maps remove the key entirely.
func PatchAgentStringMapField(path, key string, values map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch string map: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow patch string map: no front matter in %s", path)
	}

	// Honour the file's existing indent convention.
	// The block header `<indent>key:` and the child entries are written with
	// the indent the rest of the agent: block already uses (so a 4-space
	// workflow stays at 4-space, a 2-space workflow stays at 2-space).
	agentLine := findBlockHeader(frontLines, "agent")
	indent := detectBlockIndent(frontLines, agentLine)
	childIndent := indent + indent
	headerLine := indent + key + ":"

	blockStart := -1
	blockEnd := -1
	for i, line := range frontLines {
		if line != headerLine {
			continue
		}
		blockStart = i
		j := i + 1
		for j < len(frontLines) {
			l := frontLines[j]
			if l == "" {
				j++
				continue
			}
			trimmed := strings.TrimLeft(l, " \t")
			if len(l)-len(trimmed) > len(indent) {
				j++
			} else {
				break
			}
		}
		blockEnd = j
		break
	}

	var replacement []string
	if len(values) > 0 {
		replacement = append(replacement, headerLine)
		keys := make([]string, 0, len(values))
		for mapKey := range values {
			keys = append(keys, mapKey)
		}
		sort.Strings(keys)
		for _, mapKey := range keys {
			replacement = append(replacement, childIndent+strconv.Quote(mapKey)+": "+strconv.Quote(values[mapKey]))
		}
	}

	var newFrontLines []string
	switch {
	case blockStart >= 0:
		newFrontLines = append(newFrontLines, frontLines[:blockStart]...)
		newFrontLines = append(newFrontLines, replacement...)
		newFrontLines = append(newFrontLines, frontLines[blockEnd:]...)
	case len(replacement) > 0:
		newFrontLines = insertLinesAfter(frontLines, agentLine, replacement)
	default:
		return nil
	}
	return writeFrontMatter(path, newFrontLines, bodyLines)
}

// insertLinesAfter inserts a sequence of lines immediately after `at`
// (defensively appends when `at` < 0). Companion to insertAfterBlockHeader
// for callers that need to insert more than one line at once.
func insertLinesAfter(frontLines []string, at int, lines []string) []string {
	insertAt := len(frontLines)
	if at >= 0 {
		insertAt = at + 1
	}
	out := make([]string, 0, len(frontLines)+len(lines))
	out = append(out, frontLines[:insertAt]...)
	out = append(out, lines...)
	out = append(out, frontLines[insertAt:]...)
	return out
}

// ProfileEntry describes one named agent profile for PatchProfilesBlock.
type ProfileEntry struct {
	// Command is the CLI command string (e.g. "claude --model claude-haiku-4-5-20251001").
	// Any leading "command: " prefix typed by the user is stripped automatically.
	Command string
	// Prompt is an optional role description for this sub-agent, shown to the
	// orchestrating agent when agent teams are enabled.
	Prompt string
	// SoulFile is the configured path to the profile SOUL.md file.
	SoulFile string
	// InstructionsFile is the configured path to the profile INSTRUCTIONS.md file.
	InstructionsFile string
	// Backend is an optional explicit runner selection override.
	Backend string
	// Enabled controls whether the profile is selectable and dispatchable.
	// Nil means omit the field from WORKFLOW.md, which defaults to true.
	Enabled *bool
	// AllowedActions is the optional allowlist of daemon-backed agent actions.
	AllowedActions []string
	// CreateIssueState is the target tracker state/column for the create_issue action.
	CreateIssueState string
}

type AutomationTriggerEntry = automationdef.Trigger
type AutomationFilterEntry = automationdef.Filter
type AutomationPolicyEntry = automationdef.Policy
type AutomationEntry = automationdef.Definition

// PatchProfilesBlock replaces (or inserts) the agent.profiles block in the YAML
// front matter of the file at path. profiles maps profile name → ProfileEntry.
// Passing nil or an empty map removes the profiles block entirely.
// The rest of the file (other keys, comments, prompt body) is preserved byte-for-byte.
func PatchProfilesBlock(path string, profiles map[string]ProfileEntry) error {
	return ApplyAndWriteFrontMatter(path, MutateProfilesBlock(profiles))
}

// MutateProfilesBlock returns a Mutator that replaces (or inserts) the
// agent.profiles: block. See PatchProfilesBlock.
func MutateProfilesBlock(profiles map[string]ProfileEntry) Mutator {
	return func(frontLines []string) ([]string, error) {
		// The whole block is emitted at the file's own indentation unit, not a
		// hardcoded 2-space prefix. A WORKFLOW.md whose front matter uses 4-space
		// indent (yaml.v3's default) keeps profiles: at one unit, names at two,
		// fields at three, list items at four. Hardcoding 2-space here used to
		// make this mutator fail to find a 4-space profiles: block and then
		// INSERT a duplicate at 2-space — a duplicate key at inconsistent indent
		// that breaks YAML parsing and freezes the daemon on reload. See
		// detectBlockIndent's comment and TestPatchProfilesBlock_Replace4SpaceNoDuplicate.
		agentLine := findBlockHeader(frontLines, "agent")
		unit := detectBlockIndent(frontLines, agentLine) // per-level indent ("  " or "    ")
		lvl1 := unit                                     // profiles:
		lvl2 := unit + unit                              // profile names
		lvl3 := unit + unit + unit                       // profile fields
		lvl4 := lvl3 + unit                              // allowed_actions list items

		// Locate an existing agent.profiles: block at whatever indent it uses.
		profilesStart, profilesEnd := -1, -1
		if idx := findKeyInBlock(frontLines, agentLine, "profiles"); idx >= 0 {
			profilesStart = idx
			blockIndent := len(frontLines[idx]) - len(strings.TrimLeft(frontLines[idx], " \t"))
			j := idx + 1
			for j < len(frontLines) {
				l := frontLines[j]
				if strings.TrimSpace(l) == "" {
					j++
					continue
				}
				indent := len(l) - len(strings.TrimLeft(l, " \t"))
				if indent > blockIndent {
					j++
				} else {
					break
				}
			}
			profilesEnd = j
		}

		var replacement []string
		if len(profiles) > 0 {
			replacement = append(replacement, lvl1+"profiles:")
			names := make([]string, 0, len(profiles))
			for n := range profiles {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				entry := profiles[name]
				// Strip accidental "command: " prefix users may have typed in the UI.
				cmd := strings.TrimPrefix(entry.Command, "command: ")
				cmd = strings.TrimPrefix(cmd, "command:")
				replacement = append(replacement, lvl2+name+":")
				replacement = append(replacement, lvl3+"command: "+cmd)
				if entry.SoulFile != "" {
					replacement = append(replacement, lvl3+"soul_file: "+entry.SoulFile)
				}
				if entry.InstructionsFile != "" {
					replacement = append(replacement, lvl3+"instructions_file: "+entry.InstructionsFile)
				}
				if entry.Backend != "" {
					replacement = append(replacement, lvl3+"backend: "+entry.Backend)
				}
				if entry.Enabled != nil && !*entry.Enabled {
					replacement = append(replacement, lvl3+"enabled: false")
				}
				if len(entry.AllowedActions) > 0 {
					replacement = append(replacement, lvl3+"allowed_actions:")
					for _, action := range entry.AllowedActions {
						if action == "" {
							continue
						}
						replacement = append(replacement, lvl4+"- "+action)
					}
				}
				if entry.CreateIssueState != "" {
					replacement = append(replacement, lvl3+"create_issue_state: "+strconv.Quote(entry.CreateIssueState))
				}
				if entry.Prompt != "" {
					replacement = append(replacement, lvl3+"prompt: "+strconv.Quote(entry.Prompt))
				}
			}
		}

		var newFrontLines []string
		switch {
		case profilesStart >= 0:
			newFrontLines = append(newFrontLines, frontLines[:profilesStart]...)
			newFrontLines = append(newFrontLines, replacement...)
			newFrontLines = append(newFrontLines, frontLines[profilesEnd:]...)
		case len(profiles) > 0:
			// No existing block: insert after the agent: block (or append if
			// there is no agent: key at all).
			if agentLine < 0 {
				newFrontLines = append(frontLines, replacement...)
			} else {
				agentEnd := len(frontLines)
				for j := agentLine + 1; j < len(frontLines); j++ {
					l := frontLines[j]
					if strings.TrimSpace(l) == "" {
						continue
					}
					if l[0] != ' ' && l[0] != '\t' { // next top-level key
						agentEnd = j
						break
					}
				}
				newFrontLines = append(newFrontLines, frontLines[:agentEnd]...)
				newFrontLines = append(newFrontLines, replacement...)
				newFrontLines = append(newFrontLines, frontLines[agentEnd:]...)
			}
		default:
			// Block not found and profiles is empty: nothing to do.
			return frontLines, nil
		}
		return newFrontLines, nil
	}
}

// PatchAutomationsBlock replaces (or inserts) the top-level automations block in
// the YAML front matter of the file at path. Passing nil or an empty slice
// removes the automations block entirely. Legacy schedules blocks are removed
// when writing automations so the file has a single source of truth.
func PatchAutomationsBlock(path string, automations []AutomationEntry) error {
	return ApplyAndWriteFrontMatter(path, MutateAutomationsBlock(automations))
}

// MutateAutomationsBlock returns a Mutator that replaces (or inserts) the
// top-level automations: block. See PatchAutomationsBlock.
func MutateAutomationsBlock(automations []AutomationEntry) Mutator {
	return func(frontLines []string) ([]string, error) {
		automationsStart := -1
		automationsEnd := -1
		legacySchedulesStart := -1
		legacySchedulesEnd := -1
		for i, line := range frontLines {
			if line != "automations:" && line != "schedules:" {
				continue
			}
			if line == "automations:" {
				automationsStart = i
			} else {
				legacySchedulesStart = i
			}
			j := i + 1
			for j < len(frontLines) {
				l := frontLines[j]
				if l == "" {
					j++
					continue
				}
				trimmed := strings.TrimLeft(l, " ")
				indent := len(l) - len(trimmed)
				if indent > 0 {
					j++
				} else {
					break
				}
			}
			if line == "automations:" {
				automationsEnd = j
			} else {
				legacySchedulesEnd = j
			}
		}

		var replacement []string
		if len(automations) > 0 {
			replacement = append(replacement, "automations:")
			for _, automation := range automations {
				replacement = append(replacement, "  - id: "+automation.ID)
				replacement = append(replacement, "    enabled: "+strconv.FormatBool(automation.Enabled))
				replacement = append(replacement, "    profile: "+automation.Profile)
				if automation.Instructions != "" {
					replacement = append(replacement, "    instructions: "+strconv.Quote(automation.Instructions))
				}
				replacement = append(replacement, "    trigger:")
				replacement = append(replacement, "      type: "+automation.Trigger.Type)
				if automation.Trigger.Cron != "" {
					replacement = append(replacement, "      cron: "+strconv.Quote(automation.Trigger.Cron))
				}
				if automation.Trigger.Timezone != "" {
					replacement = append(replacement, "      timezone: "+strconv.Quote(automation.Trigger.Timezone))
				}
				if automation.Trigger.State != "" {
					replacement = append(replacement, "      state: "+strconv.Quote(automation.Trigger.State))
				}
				filterLines := buildAutomationFilterLines(automation.Filter)
				if len(filterLines) > 0 {
					replacement = append(replacement, "    filter:")
					replacement = append(replacement, filterLines...)
				}
				policyLines := buildAutomationPolicyLines(automation.Trigger.Type, automation.Policy)
				if len(policyLines) > 0 {
					replacement = append(replacement, "    policy:")
					replacement = append(replacement, policyLines...)
				}
			}
		}

		var newFrontLines []string
		switch {
		case automationsStart >= 0:
			newFrontLines = append(newFrontLines, frontLines[:automationsStart]...)
			newFrontLines = append(newFrontLines, replacement...)
			newFrontLines = append(newFrontLines, frontLines[automationsEnd:]...)
		case legacySchedulesStart >= 0:
			newFrontLines = append(newFrontLines, frontLines[:legacySchedulesStart]...)
			newFrontLines = append(newFrontLines, replacement...)
			newFrontLines = append(newFrontLines, frontLines[legacySchedulesEnd:]...)
		case len(automations) > 0:
			newFrontLines = append(frontLines, replacement...)
		default:
			return frontLines, nil
		}
		return newFrontLines, nil
	}
}

func buildAutomationFilterLines(filter AutomationFilterEntry) []string {
	var lines []string
	if filter.MatchMode != "" && filter.MatchMode != "all" {
		lines = append(lines, "      match_mode: "+strconv.Quote(filter.MatchMode))
	}
	if len(filter.States) > 0 {
		lines = append(lines, "      states: "+marshalStringSliceInline(filter.States))
	}
	if len(filter.LabelsAny) > 0 {
		lines = append(lines, "      labels_any: "+marshalStringSliceInline(filter.LabelsAny))
	}
	if filter.IdentifierRegex != "" {
		lines = append(lines, "      identifier_regex: "+strconv.Quote(filter.IdentifierRegex))
	}
	if filter.Limit > 0 {
		lines = append(lines, "      limit: "+strconv.Itoa(filter.Limit))
	}
	if filter.InputContextRegex != "" {
		lines = append(lines, "      input_context_regex: "+strconv.Quote(filter.InputContextRegex))
	}
	if filter.MaxAgeMinutes > 0 {
		lines = append(lines, "      max_age_minutes: "+strconv.Itoa(filter.MaxAgeMinutes))
	}
	return lines
}

func buildAutomationPolicyLines(triggerType string, policy AutomationPolicyEntry) []string {
	var lines []string
	if policy.AutoResume {
		if triggerType == "rate_limited" {
			lines = append(lines, "      auto_switch: true")
		} else {
			lines = append(lines, "      auto_resume: true")
		}
	}
	if policy.SwitchToProfile != "" {
		lines = append(lines, "      switch_to_profile: "+strconv.Quote(policy.SwitchToProfile))
	}
	if policy.SwitchToBackend != "" {
		lines = append(lines, "      switch_to_backend: "+strconv.Quote(policy.SwitchToBackend))
	}
	if policy.CooldownMinutes > 0 {
		lines = append(lines, "      cooldown_minutes: "+strconv.Itoa(policy.CooldownMinutes))
	}
	return lines
}

func marshalStringSliceInline(values []string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(data)
}
