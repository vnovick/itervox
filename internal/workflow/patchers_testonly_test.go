package workflow

// Test-only WORKFLOW.md patchers (#129). Since dashboard and TUI saves of the
// layered sections (agent, polling, server, workspace) go to
// .itervox/settings.jsonc and only REMOVE keys from WORKFLOW.md
// (RemoveFrontMatterKeys), no production code writes these agent:/workspace:
// keys into WORKFLOW.md any more. The helpers stay here because this
// package's tests use them as ordinary locked, self-registering writers
// (watcher, self-write, cross-process lock and indent tests).

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

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
	unlock, err := lockForPath(path)
	if err != nil {
		return err
	}
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
	return writeLocked(path, data, []byte(patched), true)
}

// PatchAgentBoolField sets a boolean key under the agent: block of the YAML front matter.
// If the key already exists it is updated in place; if it does not exist it is appended
// inside the agent: block. Setting enabled=false removes the key entirely.
//
// Routed through ApplyAndWriteFrontMatter (CORE-006) so concurrent callers
// for the same path (e.g. an HTTP settings PUT racing the TUI) are
// serialized by editMu instead of racing a bare read-modify-write.
func PatchAgentBoolField(path, key string, enabled bool) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentBoolField(key, enabled))
}

// PatchWorkspaceBoolField sets a boolean key under the workspace: block of the YAML front matter.
// Behaves identically to PatchAgentBoolField but targets the workspace: block.
// There is no MutateWorkspaceBoolField export (no caller needs to compose it
// with other mutators today); it calls the package-private
// mutateBlockBoolField directly instead of MutateAgentBoolField, which is
// hardcoded to the agent: block.
func PatchWorkspaceBoolField(path, key string, enabled bool) error {
	return ApplyAndWriteFrontMatter(path, mutateBlockBoolField("workspace", key, enabled))
}

// PatchAgentStringField sets or removes a string key under the agent: block of the YAML front matter.
// If the key already exists it is updated in place; if value == "" the key is removed.
//
// Routed through ApplyAndWriteFrontMatter (CORE-006); see PatchAgentBoolField.
func PatchAgentStringField(path, key, value string) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentStringField(key, value))
}

// MutateAgentStringField sets a string-valued key inside the agent: block.
// Empty value removes the key. Inserts a new key under agent: if missing.
func MutateAgentStringField(key, value string) Mutator {
	return mutateBlockStringField("agent", key, value)
}

// MutateAgentBoolField sets a boolean key inside the agent: block.
// enabled=false removes the key (matching PatchAgentBoolField semantics).
func MutateAgentBoolField(key string, enabled bool) Mutator {
	return mutateBlockBoolField("agent", key, enabled)
}

// MutateAgentStringSliceField sets a string-slice key inside the agent:
// block. Empty slice removes the key (matching PatchAgentStringSliceField).
func MutateAgentStringSliceField(key string, values []string) Mutator {
	return mutateBlockStringSliceField("agent", key, values)
}

// MutateAgentStringMapField sets a string-map key inside the agent: block.
// Empty/nil map removes the key (matching PatchAgentStringMapField).
func MutateAgentStringMapField(key string, values map[string]string) Mutator {
	return mutateBlockStringMapField("agent", key, values)
}

// mutateBlockStringField, mutateBlockBoolField, mutateBlockStringSliceField
// and mutateBlockStringMapField all derive their indent unit from
// detectBlockIndent (loader.go) rather than a hardcoded two-space prefix —
// a WORKFLOW.md whose front matter uses 4-space indent (yaml.v3's default
// serialisation) must keep that indent on every key these helpers write or
// remove, matching the sibling lines already in the block (CORE-006).
func mutateBlockStringField(block, key, value string) Mutator {
	return func(frontLines []string) ([]string, error) {
		blockLine, blockEnd := findBlockRange(frontLines, block)
		if blockLine < 0 {
			// Allow setting on a missing block by inserting a new top-level entry
			// is out of scope here — callers either pre-populate the block or
			// use ApplyAndWriteFrontMatter directly.
			if value == "" {
				return frontLines, nil
			}
			return nil, fmt.Errorf("workflow mutate: %q block not found", block)
		}
		indent := detectBlockIndent(frontLines, blockLine)
		keyPrefix := indent + key + ":"
		out := make([]string, 0, len(frontLines))
		out = append(out, frontLines[:blockLine+1]...)
		written := false
		for _, line := range frontLines[blockLine+1 : blockEnd] {
			if strings.HasPrefix(line, keyPrefix) {
				if value == "" {
					continue
				}
				out = append(out, indent+key+": "+strconv.Quote(value))
				written = true
				continue
			}
			out = append(out, line)
		}
		if !written && value != "" {
			out = append(out, indent+key+": "+strconv.Quote(value))
		}
		out = append(out, frontLines[blockEnd:]...)
		return out, nil
	}
}

func mutateBlockBoolField(block, key string, enabled bool) Mutator {
	return func(frontLines []string) ([]string, error) {
		blockLine, blockEnd := findBlockRange(frontLines, block)
		if blockLine < 0 {
			if !enabled {
				return frontLines, nil
			}
			return nil, fmt.Errorf("workflow mutate: %q block not found", block)
		}
		indent := detectBlockIndent(frontLines, blockLine)
		keyPrefix := indent + key + ":"
		out := make([]string, 0, len(frontLines))
		out = append(out, frontLines[:blockLine+1]...)
		written := false
		for _, line := range frontLines[blockLine+1 : blockEnd] {
			if strings.HasPrefix(line, keyPrefix) {
				if !enabled {
					continue // remove the key
				}
				out = append(out, indent+key+": true")
				written = true
				continue
			}
			out = append(out, line)
		}
		if !written && enabled {
			out = append(out, indent+key+": true")
		}
		out = append(out, frontLines[blockEnd:]...)
		return out, nil
	}
}

func mutateBlockStringSliceField(block, key string, values []string) Mutator {
	return func(frontLines []string) ([]string, error) {
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("marshal %q: %w", key, err)
		}
		blockLine, _ := findBlockRange(frontLines, block)
		indent := detectBlockIndent(frontLines, blockLine)
		keyPrefix := indent + key + ":"
		// Locate existing key inside block.
		keyFound := -1
		if blockLine >= 0 {
			for j := blockLine + 1; j < len(frontLines); j++ {
				line := frontLines[j]
				if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
					break
				}
				if strings.HasPrefix(line, keyPrefix) {
					keyFound = j
					break
				}
			}
		}
		out := make([]string, 0, len(frontLines)+1)
		switch {
		case keyFound >= 0 && len(values) == 0:
			out = append(out, frontLines[:keyFound]...)
			out = append(out, frontLines[keyFound+1:]...)
		case keyFound >= 0:
			out = append(out, frontLines[:keyFound]...)
			out = append(out, indent+key+": "+string(encoded))
			out = append(out, frontLines[keyFound+1:]...)
		case len(values) > 0:
			insertAt := len(frontLines)
			if blockLine >= 0 {
				insertAt = blockLine + 1
			}
			out = append(out, frontLines[:insertAt]...)
			out = append(out, indent+key+": "+string(encoded))
			out = append(out, frontLines[insertAt:]...)
		default:
			return frontLines, nil
		}
		return out, nil
	}
}

func mutateBlockStringMapField(block, key string, values map[string]string) Mutator {
	return func(frontLines []string) ([]string, error) {
		blockLine, _ := findBlockRange(frontLines, block)
		indent := detectBlockIndent(frontLines, blockLine)
		childIndent := indent + indent
		headerLine := indent + key + ":"
		// Locate existing key block.
		blockStart := -1
		blockEndLocal := -1
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
			blockEndLocal = j
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

		out := make([]string, 0, len(frontLines)+len(replacement))
		switch {
		case blockStart >= 0:
			out = append(out, frontLines[:blockStart]...)
			out = append(out, replacement...)
			out = append(out, frontLines[blockEndLocal:]...)
		case len(replacement) > 0:
			insertAt := len(frontLines)
			if blockLine >= 0 {
				insertAt = blockLine + 1
			}
			out = append(out, frontLines[:insertAt]...)
			out = append(out, replacement...)
			out = append(out, frontLines[insertAt:]...)
		default:
			return frontLines, nil
		}
		return out, nil
	}
}

// findBlockRange locates the start (inclusive) and end (exclusive) line
// indices of a top-level YAML block (e.g. "agent:", "tracker:") inside the
// front matter. Returns (-1, len) when the block isn't present.
func findBlockRange(frontLines []string, block string) (start, end int) {
	start = -1
	end = len(frontLines)
	for i, line := range frontLines {
		if line != block+":" {
			continue
		}
		start = i
		for j := i + 1; j < len(frontLines); j++ {
			next := frontLines[j]
			if next == "" {
				continue
			}
			if next[0] != ' ' {
				end = j
				break
			}
		}
		break
	}
	return start, end
}

// PatchReviewerConfig atomically rewrites agent.reviewer_profile and
// agent.auto_review inside the YAML front matter. Empty profile removes the
// reviewer_profile key. autoReview=false removes auto_review.
func PatchReviewerConfig(path, profile string, autoReview bool) error {
	return ApplyAndWriteFrontMatter(path, MutateReviewerConfig(profile, autoReview))
}

// MutateReviewerConfig returns a Mutator that rewrites the reviewer_profile
// and auto_review keys inside the agent: block. See PatchReviewerConfig.
func MutateReviewerConfig(profile string, autoReview bool) Mutator {
	return func(frontLines []string) ([]string, error) {
		agentLine := -1
		agentEnd := len(frontLines)
		for i, line := range frontLines {
			if line != "agent:" {
				continue
			}
			agentLine = i
			for j := i + 1; j < len(frontLines); j++ {
				next := frontLines[j]
				if next == "" {
					continue
				}
				if next[0] != ' ' {
					agentEnd = j
					break
				}
			}
			break
		}
		if agentLine < 0 {
			return nil, fmt.Errorf("agent block not found")
		}

		block := make([]string, 0, agentEnd-agentLine-1+2)
		for _, line := range frontLines[agentLine+1 : agentEnd] {
			if strings.HasPrefix(line, "  reviewer_profile:") || strings.HasPrefix(line, "  auto_review:") {
				continue
			}
			block = append(block, line)
		}
		if profile != "" {
			block = append(block, "  reviewer_profile: "+strconv.Quote(profile))
		}
		if autoReview {
			block = append(block, "  auto_review: true")
		}

		newFrontLines := make([]string, 0, len(frontLines)-((agentEnd-agentLine-1)-len(block)))
		newFrontLines = append(newFrontLines, frontLines[:agentLine+1]...)
		newFrontLines = append(newFrontLines, block...)
		newFrontLines = append(newFrontLines, frontLines[agentEnd:]...)
		return newFrontLines, nil
	}
}

// PatchAgentMaxRetries atomically rewrites agent.max_retries in the YAML front
// matter. The value is always written even if it equals the parser default —
// operators may have intentionally pinned the default and a settings PUT
// should be self-evident in the file. The agent block must exist; this is
// guaranteed by config.Load() which fails earlier if it does not.
func PatchAgentMaxRetries(path string, n int) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentIntField("max_retries", n))
}

// MutateAgentIntField returns a Mutator that sets a single integer key inside
// the agent: block. Existing occurrences are replaced; if the key is missing
// it is appended. This is the int-shaped sibling of MutateReviewerConfig.
func MutateAgentIntField(key string, value int) Mutator {
	prefix := "  " + key + ":"
	return func(frontLines []string) ([]string, error) {
		agentLine := -1
		agentEnd := len(frontLines)
		for i, line := range frontLines {
			if line != "agent:" {
				continue
			}
			agentLine = i
			for j := i + 1; j < len(frontLines); j++ {
				next := frontLines[j]
				if next == "" {
					continue
				}
				if next[0] != ' ' {
					agentEnd = j
					break
				}
			}
			break
		}
		if agentLine < 0 {
			return nil, fmt.Errorf("agent block not found")
		}

		block := make([]string, 0, agentEnd-agentLine)
		replaced := false
		for _, line := range frontLines[agentLine+1 : agentEnd] {
			if strings.HasPrefix(line, prefix) {
				block = append(block, "  "+key+": "+strconv.Itoa(value))
				replaced = true
				continue
			}
			block = append(block, line)
		}
		if !replaced {
			block = append(block, "  "+key+": "+strconv.Itoa(value))
		}

		newFrontLines := make([]string, 0, len(frontLines)-((agentEnd-agentLine-1)-len(block)))
		newFrontLines = append(newFrontLines, frontLines[:agentLine+1]...)
		newFrontLines = append(newFrontLines, block...)
		newFrontLines = append(newFrontLines, frontLines[agentEnd:]...)
		return newFrontLines, nil
	}
}

// MutateIntField rewrites the first occurrence of `key: <int>` inside the
// front matter, preserving any inline comment. Errors when the key is
// missing — this matches PatchIntField's strict-key semantics.
func MutateIntField(key string, n int) Mutator {
	return func(frontLines []string) ([]string, error) {
		for i, line := range frontLines {
			m := keyLineRE.FindStringSubmatch(line)
			if m == nil || m[2] != key {
				continue
			}
			oldVal := m[4]
			comment := ""
			if ci := strings.Index(oldVal, " #"); ci >= 0 {
				comment = " " + strings.TrimSpace(oldVal[ci+1:])
				comment = " #" + comment[2:]
			}
			frontLines[i] = m[1] + m[2] + m[3] + strconv.Itoa(n) + comment
			return frontLines, nil
		}
		return nil, fmt.Errorf("workflow mutate: key %q not found in front matter", key)
	}
}
