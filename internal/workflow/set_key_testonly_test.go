package workflow

// The generic front-matter key writer below was production code until #239
// moved every key it wrote (tracker.*, dependencies.*) to the layered
// settings files, written by config.SetSettings. Its tests are kept (comment,
// order, indentation and refusal coverage, and the per-field patcher
// wrappers in testonly_test.go) through this test-only copy.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// KeyValue is one front-matter key SetFrontMatterKeys sets: Key is a dotted
// path ("tracker.failed_state"); a nil Value removes the key.
type KeyValue struct {
	Key   string
	Value any
}

// SetFrontMatterKeys sets each key in WORKFLOW.md's front matter, in one
// locked write (#158). It is the one generic writer for the WORKFLOW.md keys
// that are not layered settings (tracker.*, dependencies.*); the layered ones
// go to the JSONC settings files through config.SetSettings. Only the lines
// of the keys it sets change; comments, key order and every other line stay
// as they were. When nothing changes, nothing is written.
//
// Like ApplyAndWriteFrontMatter, the write is registered as a self-write
// (CORE-116): every daemon caller applies the same value in memory.
func SetFrontMatterKeys(path string, values ...KeyValue) error {
	return applyAndWrite(path, true, true, []Mutator{MutateSetKeys(values...)})
}

// MutateSetKeys returns a Mutator that applies MutateSetKey for each value,
// in order.
func MutateSetKeys(values ...KeyValue) Mutator {
	return func(frontLines []string) ([]string, error) {
		var err error
		for _, kv := range values {
			if frontLines, err = MutateSetKey(kv.Key, kv.Value)(frontLines); err != nil {
				return nil, err
			}
		}
		return frontLines, nil
	}
}

// MutateSetKey returns a Mutator that sets the key at dottedPath in
// block-style YAML front matter to value, or removes it when value is nil
// (MutateRemoveKey). An existing key is rewritten in place, keeping an inline
// comment on a scalar; a missing key is appended to its parent mapping, and
// missing parents are created, at the indentation the file already uses.
// Strings are written double-quoted; lists and maps in flow style (JSON).
//
// The result is re-parsed and must hold exactly value at dottedPath: a front
// matter the edit would leave unparseable (a duplicate block header the
// line scan did not recognise, CORE-125) or whose key it could not reach (a
// flow-style parent) is refused and the input is left unchanged.
func MutateSetKey(dottedPath string, value any) Mutator {
	if value == nil {
		return MutateRemoveKey(dottedPath)
	}
	parts := strings.Split(dottedPath, ".")
	return func(frontLines []string) ([]string, error) {
		if dottedPath == "" || slices.Contains(parts, "") {
			return nil, fmt.Errorf("workflow set key: %q is not a dotted key path", dottedPath)
		}
		rendered, err := renderYAMLValue(value)
		if err != nil {
			return nil, fmt.Errorf("workflow set key %s: %w", dottedPath, err)
		}
		out, err := setKeyLines(frontLines, parts, rendered)
		if err != nil {
			return nil, fmt.Errorf("workflow set key %s: %w", dottedPath, err)
		}
		if err := checkKeyValue(out, parts, value); err != nil {
			return nil, fmt.Errorf("workflow set key %s: %w", dottedPath, err)
		}
		return out, nil
	}
}

func setKeyLines(lines []string, parts []string, rendered string) ([]string, error) {
	unit := indentUnit(lines)
	start, end, parentIndent := 0, len(lines), -1
	for depth, part := range parts {
		idx := findChildKey(lines, start, end, parentIndent, part)
		last := depth == len(parts)-1
		if idx >= 0 {
			keyIndent := lineIndent(lines[idx])
			valueEnd := valueEndLine(lines, idx, end, keyIndent)
			m := keyLineRE.FindStringSubmatch(lines[idx])
			oldValue, comment := splitInlineComment(m[4])
			if last {
				line := m[1] + part + ": " + rendered
				if comment != "" && valueEnd == idx+1 {
					line += " " + comment
				}
				return splice(lines, idx, valueEnd, line), nil
			}
			if oldValue != "" && oldValue != "{}" {
				return nil, fmt.Errorf("%s is not a block mapping", strings.Join(parts[:depth+1], "."))
			}
			if oldValue == "{}" {
				lines = splice(lines, idx, idx+1, m[1]+part+":"+suffix(comment))
			}
			start, end, parentIndent = idx+1, valueEnd, keyIndent
			continue
		}
		indent := childIndent(lines, start, end, parentIndent, unit)
		added := make([]string, 0, len(parts)-depth)
		for _, p := range parts[depth : len(parts)-1] {
			added = append(added, strings.Repeat(" ", indent)+p+":")
			indent += unit
		}
		added = append(added, strings.Repeat(" ", indent)+parts[len(parts)-1]+": "+rendered)
		return splice(lines, end, end, added...), nil
	}
	return lines, nil
}

func splice(lines []string, from, to int, insert ...string) []string {
	out := make([]string, 0, len(lines)-(to-from)+len(insert))
	out = append(out, lines[:from]...)
	out = append(out, insert...)
	return append(out, lines[to:]...)
}

func suffix(comment string) string {
	if comment == "" {
		return ""
	}
	return " " + comment
}

// splitInlineComment splits a key line's value into the value and a trailing
// " # comment". A '#' inside a quoted value is not a comment.
func splitInlineComment(v string) (value, comment string) {
	inSingle, inDouble := false, false
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '\\' && inDouble:
			i++
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '#' && !inSingle && !inDouble && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t'):
			return strings.TrimSpace(v[:i]), v[i:]
		}
	}
	return strings.TrimSpace(v), ""
}

// childIndent is the indent of the children of the mapping that spans
// [start, end) under a key at parentIndent: the indent of its first content
// line, or one unit deeper than the parent when it has none.
func childIndent(lines []string, start, end, parentIndent, unit int) int {
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if ind := lineIndent(lines[i]); ind > parentIndent {
			return ind
		}
		break
	}
	if parentIndent < 0 {
		return 0
	}
	return parentIndent + unit
}

// indentUnit is the file's indentation step: the smallest indent of a nested
// mapping key, or two spaces when nothing is nested yet.
func indentUnit(lines []string) int {
	unit := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		if ind := lineIndent(l); ind > 0 && (unit == 0 || ind < unit) {
			unit = ind
		}
	}
	if unit == 0 {
		return len(defaultBlockIndent)
	}
	return unit
}

// renderYAMLValue writes value as one line of YAML: strings double-quoted,
// lists and maps as JSON (valid YAML flow style), other scalars plain.
func renderYAMLValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool, int, int64, float64:
		b, err := yaml.Marshal(v)
		return strings.TrimSpace(string(b)), err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// checkKeyValue parses the front matter and fails unless the key path holds
// want.
func checkKeyValue(frontLines []string, parts []string, want any) error {
	doc, err := parseFrontMatter("front matter edit", frontLines)
	if err != nil {
		return fmt.Errorf("the edit would leave the front matter unparseable: %w", err)
	}
	var cur any = doc
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return fmt.Errorf("the key could not be set (flow-style YAML?); edit WORKFLOW.md by hand")
		}
		cur = m[p]
	}
	if !sameJSON(cur, want) {
		return fmt.Errorf("the key could not be set (flow-style YAML?); edit WORKFLOW.md by hand")
	}
	return nil
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
