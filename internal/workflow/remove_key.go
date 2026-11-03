package workflow

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// RemoveFrontMatterKeys deletes each front-matter key named by a dotted path
// ("agent.max_retries") from the WORKFLOW.md at path, in one locked write
// (#129: a dashboard save moves a layered key out of WORKFLOW.md and into
// .itervox/settings.jsonc). Only the lines of the removed keys change; every
// other line, comment and the key order stay byte-identical. A key that is
// absent is a no-op, and when the front matter ends up unchanged nothing is
// written. extra mutators run after the removals, in the same write.
//
// Like ApplyAndWriteFrontMatter, the write is registered as a self-write
// (CORE-116): the caller applies the saved value in memory, so this process's
// watcher does not reload for it.
func RemoveFrontMatterKeys(path string, dottedPaths []string, extra ...Mutator) error {
	return applyAndWrite(path, true, true, append([]Mutator{MutateRemoveKeys(dottedPaths...)}, extra...))
}

// MutateRemoveKeys returns a Mutator that removes every key in dottedPaths.
// See MutateRemoveKey.
func MutateRemoveKeys(dottedPaths ...string) Mutator {
	return func(frontLines []string) ([]string, error) {
		var err error
		for _, p := range dottedPaths {
			if frontLines, err = MutateRemoveKey(p)(frontLines); err != nil {
				return nil, err
			}
		}
		return frontLines, nil
	}
}

// MutateRemoveKey returns a Mutator that removes the key at dottedPath from
// block-style YAML front matter: the key's own line and its value's
// continuation lines (a nested map, a block list, a block scalar). Comment and
// blank lines are removed only when they sit inside the value; a comment
// above the key or after its value stays. The parent mapping is left in
// place even when the key was its last entry. An absent key is a no-op.
//
// The result is re-parsed: if the key is still set (it was written in flow
// style, "agent: {max_retries: 3}"), the mutator fails rather than letting a
// save be silently overridden by a key it could not remove.
func MutateRemoveKey(dottedPath string) Mutator {
	parts := strings.Split(dottedPath, ".")
	return func(frontLines []string) ([]string, error) {
		if dottedPath == "" || slices.Contains(parts, "") {
			return nil, fmt.Errorf("workflow remove key: %q is not a dotted key path", dottedPath)
		}
		out := frontLines
		start, end, parentIndent := 0, len(frontLines), -1
		for depth, part := range parts {
			idx := findChildKey(frontLines, start, end, parentIndent, part)
			if idx < 0 {
				break
			}
			keyIndent := lineIndent(frontLines[idx])
			valueEnd := valueEndLine(frontLines, idx, end, keyIndent)
			if depth == len(parts)-1 {
				out = make([]string, 0, len(frontLines)-(valueEnd-idx))
				out = append(out, frontLines[:idx]...)
				out = append(out, frontLines[valueEnd:]...)
				break
			}
			start, end, parentIndent = idx+1, valueEnd, keyIndent
		}
		if err := checkKeyAbsent(out, parts); err != nil {
			return nil, fmt.Errorf("workflow remove key %s: %w", dottedPath, err)
		}
		return out, nil
	}
}

// findChildKey returns the line index, within [start, end), of the mapping
// key name that is a direct child of the parent at parentIndent (-1 for the
// top level), or -1. Direct children are the content lines at the indent of
// the first content line in the range.
func findChildKey(lines []string, start, end, parentIndent int, name string) int {
	childIndent := -1
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		ind := lineIndent(lines[i])
		if ind <= parentIndent {
			return -1
		}
		if childIndent < 0 {
			childIndent = ind
		}
		if ind != childIndent {
			continue
		}
		if m := keyLineRE.FindStringSubmatch(lines[i]); m != nil && m[2] == name {
			return i
		}
	}
	return -1
}

// valueEndLine returns the index one past the last line of the value of the
// key at line idx (indent keyIndent), bounded by end. A value line is one
// indented deeper than the key, or a "- " list item at the key's own indent
// (YAML's compact block-sequence form). Blank and comment lines count only
// when a value line follows them.
func valueEndLine(lines []string, idx, end, keyIndent int) int {
	last := idx
	for j := idx + 1; j < end; j++ {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		ind := lineIndent(lines[j])
		if ind > keyIndent || (ind == keyIndent && (trimmed == "-" || strings.HasPrefix(trimmed, "- "))) {
			last = j
			continue
		}
		break
	}
	return last + 1
}

func lineIndent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// checkKeyAbsent parses the front matter and fails if the key path is set.
func checkKeyAbsent(frontLines []string, parts []string) error {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(frontLines, "\n")), &doc); err != nil {
		return fmt.Errorf("parse front matter: %w", err)
	}
	var cur any = doc
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[p]; !ok {
			return nil
		}
	}
	return fmt.Errorf("the key is still set (flow-style YAML?); remove it from WORKFLOW.md by hand")
}
