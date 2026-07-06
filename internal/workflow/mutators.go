package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file extracts the in-memory front-matter manipulation from each
// Patch* helper in loader.go into composable Mutator values. The Doc API
// (doc.go) chains these into one atomic Save. Existing Patch* callers
// continue to work unchanged — they each delegate to the corresponding
// Mutate* via ApplyAndWriteFrontMatter.

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

// ── Block-level helpers (shared between Patch* and Mutate* paths) ───────────

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
		keyPrefix := "  " + key + ":"
		out := make([]string, 0, len(frontLines))
		out = append(out, frontLines[:blockLine+1]...)
		written := false
		for _, line := range frontLines[blockLine+1 : blockEnd] {
			if strings.HasPrefix(line, keyPrefix) {
				if value == "" {
					continue
				}
				out = append(out, "  "+key+": "+strconv.Quote(value))
				written = true
				continue
			}
			out = append(out, line)
		}
		if !written && value != "" {
			out = append(out, "  "+key+": "+strconv.Quote(value))
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
		keyPrefix := "  " + key + ":"
		out := make([]string, 0, len(frontLines))
		out = append(out, frontLines[:blockLine+1]...)
		written := false
		for _, line := range frontLines[blockLine+1 : blockEnd] {
			if strings.HasPrefix(line, keyPrefix) {
				if !enabled {
					continue // remove the key
				}
				out = append(out, "  "+key+": true")
				written = true
				continue
			}
			out = append(out, line)
		}
		if !written && enabled {
			out = append(out, "  "+key+": true")
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
		keyPrefix := "  " + key + ":"
		blockLine, _ := findBlockRange(frontLines, block)
		// Locate existing key inside block.
		keyFound := -1
		if blockLine >= 0 {
			for j := blockLine + 1; j < len(frontLines); j++ {
				line := frontLines[j]
				if len(line) > 0 && line[0] != ' ' {
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
			out = append(out, "  "+key+": "+string(encoded))
			out = append(out, frontLines[keyFound+1:]...)
		case len(values) > 0:
			insertAt := len(frontLines)
			if blockLine >= 0 {
				insertAt = blockLine + 1
			}
			out = append(out, frontLines[:insertAt]...)
			out = append(out, "  "+key+": "+string(encoded))
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
		// Locate existing key block.
		blockStart := -1
		blockEndLocal := -1
		for i, line := range frontLines {
			if line != "  "+key+":" {
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
				trimmed := strings.TrimLeft(l, " ")
				indent := len(l) - len(trimmed)
				if indent > 2 {
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
			replacement = append(replacement, "  "+key+":")
			keys := make([]string, 0, len(values))
			for mapKey := range values {
				keys = append(keys, mapKey)
			}
			sort.Strings(keys)
			for _, mapKey := range keys {
				replacement = append(replacement, "    "+strconv.Quote(mapKey)+": "+strconv.Quote(values[mapKey]))
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
