package main

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Node-tree helpers for `itervox init --update` (#71).
//
// The schema-2 migration used to decode the WORKFLOW.md front matter into a
// map[string]any and re-marshal it, which dropped every comment and sorted
// the keys alphabetically. Operators lost their notes (the GitHub tracker
// label instructions live in comments) and got a diff that touched every
// line. The migration now edits the yaml.v3 Node tree instead: comments,
// key order, scalar styles (block scalars, quoted strings, flow sequences)
// and the file's indent width all survive, and only the migrated keys
// change.

// defaultFrontMatterIndent is the indent width used when the front matter
// has no indented line to learn it from.
const defaultFrontMatterIndent = 2

// parseFrontMatterNode parses the YAML front matter into a document node and
// returns it together with its top-level mapping. An empty front matter
// yields an empty mapping so callers can populate it.
func parseFrontMatterNode(front string) (doc *yaml.Node, root *yaml.Node, err error) {
	doc = &yaml.Node{}
	if err := yaml.Unmarshal([]byte(front), doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		return doc, root, nil
	}
	root = doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		// `---\n~\n---` or a comment-only front matter: treat as empty.
		replacement := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map",
			HeadComment: root.HeadComment, LineComment: root.LineComment, FootComment: root.FootComment}
		doc.Content[0] = replacement
		return doc, replacement, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("front matter is not a YAML mapping")
	}
	return doc, root, nil
}

// detectFrontMatterIndent returns the indent width the front matter already
// uses (the leading-space count of its first indented, non-comment line) so
// the re-encoded file keeps the operator's layout. Falls back to
// defaultFrontMatterIndent when nothing is indented.
func detectFrontMatterIndent(front string) int {
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if n := len(line) - len(trimmed); n > 0 {
			if n > 8 {
				return defaultFrontMatterIndent
			}
			return n
		}
	}
	return defaultFrontMatterIndent
}

// encodeFrontMatterNode renders the document node back to YAML text using the
// given indent width.
func encodeFrontMatterNode(doc *yaml.Node, indent int) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// yamlNodeIndex returns the index of `key` in a mapping node's Content (the
// key node's position), or -1.
func yamlNodeIndex(m *yaml.Node, key string) int {
	if m == nil || m.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// yamlNodeGet returns the value node for `key`, or nil when absent.
func yamlNodeGet(m *yaml.Node, key string) *yaml.Node {
	i := yamlNodeIndex(m, key)
	if i < 0 {
		return nil
	}
	return m.Content[i+1]
}

// yamlNodeSet replaces the value of `key` in place (keeping the key's
// position and comments, and the old value's trailing line comment) or
// appends the pair when the key is absent.
func yamlNodeSet(m *yaml.Node, key string, value *yaml.Node) {
	if i := yamlNodeIndex(m, key); i >= 0 {
		old := m.Content[i+1]
		if value.LineComment == "" {
			value.LineComment = old.LineComment
		}
		m.Content[i+1] = value
		return
	}
	m.Content = append(m.Content, yamlKeyNode(key), value)
}

// yamlNodeInsertFirst puts `key: value` at the top of the mapping, replacing
// any existing entry for the key in place instead (so a file that already
// carries the key keeps its layout).
func yamlNodeInsertFirst(m *yaml.Node, key string, value *yaml.Node) {
	if yamlNodeIndex(m, key) >= 0 {
		yamlNodeSet(m, key, value)
		return
	}
	m.Content = append([]*yaml.Node{yamlKeyNode(key), value}, m.Content...)
}

// yamlNodeDelete removes `key` (and its value) from the mapping. Returns
// whether anything was removed.
func yamlNodeDelete(m *yaml.Node, key string) bool {
	i := yamlNodeIndex(m, key)
	if i < 0 {
		return false
	}
	m.Content = append(m.Content[:i], m.Content[i+2:]...)
	return true
}

// yamlNodeEnsureMap returns the mapping node stored under `key`, creating an
// empty one (appended) when the key is absent or its value is not a mapping
// (for example a bare `agent:` with a null value). A value that is an alias
// of a mapping, or a mapping with merge keys (`<<: *anchor`), is replaced by
// a plain mapping with the same effective entries first: editing the alias
// would edit every other user of the anchor (or, replaced by an empty map,
// lose its values), and a key deleted from a mapping would come back through
// its merge.
func yamlNodeEnsureMap(m *yaml.Node, key string) *yaml.Node {
	if existing := yamlNodeGet(m, key); existing != nil {
		if existing.Kind == yaml.MappingNode && !yamlHasMergeKey(existing) {
			return existing
		}
		if plain, ok := yamlEffectiveMap(existing); ok {
			yamlNodeSet(m, key, plain)
			return plain
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	yamlNodeSet(m, key, child)
	return child
}

// yamlIsMergeKey reports whether k is a YAML merge key (`<<`).
func yamlIsMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "!!merge" || k.Tag == "")
}

func yamlHasMergeKey(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if yamlIsMergeKey(m.Content[i]) {
			return true
		}
	}
	return false
}

// yamlEffectiveMap returns a new mapping node holding n's effective entries:
// an alias is followed, merge keys are expanded (own keys win, then merged
// mappings in order), and every value is a copy, so the result can be edited
// without touching the anchor. ok is false when n is not a mapping.
func yamlEffectiveMap(n *yaml.Node) (*yaml.Node, bool) {
	src := n
	for src != nil && src.Kind == yaml.AliasNode {
		src = src.Alias
	}
	if src == nil || src.Kind != yaml.MappingNode {
		return nil, false
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: src.Style,
		HeadComment: n.HeadComment, LineComment: n.LineComment, FootComment: n.FootComment}
	seen := map[string]bool{}
	var merges []*yaml.Node
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		if yamlIsMergeKey(k) {
			merges = append(merges, v)
			continue
		}
		seen[k.Value] = true
		out.Content = append(out.Content, yamlCopyNode(k), yamlCopyNode(v))
	}
	for _, merge := range merges {
		sources := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			sources = merge.Content
		}
		for _, s := range sources {
			eff, ok := yamlEffectiveMap(s)
			if !ok {
				continue
			}
			for j := 0; j+1 < len(eff.Content); j += 2 {
				if k := eff.Content[j]; !seen[k.Value] {
					seen[k.Value] = true
					out.Content = append(out.Content, k, eff.Content[j+1])
				}
			}
		}
	}
	return out, true
}

// yamlCopyNode deep-copies n. Anchors are dropped from the copy (the
// original keeps them, so aliases elsewhere still resolve), and aliases are
// kept pointing at their original anchors.
func yamlCopyNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Anchor = ""
	if n.Kind != yaml.AliasNode && len(n.Content) > 0 {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			c.Content[i] = yamlCopyNode(child)
		}
	}
	return &c
}

func yamlKeyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

func yamlStringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func yamlIntNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

// topLevelKeyLineRE matches a column-0 mapping key line ("tracker:",
// "agent: {}", "itervox_schema_version: 2").
var topLevelKeyLineRE = regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)

// restoreTopLevelBlankLines re-inserts the blank line that separated
// top-level sections in the original front matter. yaml.v3 drops blank lines
// when it encodes a node tree; without this pass every section boundary
// shows up in the migration diff even though nothing in it changed. A
// comment block directly above a key travels with it: the blank line is
// restored above the block, as the operator wrote it.
func restoreTopLevelBlankLines(originalFront, encoded string) string {
	wantBlank := topLevelKeysPrecededByBlankLine(originalFront)
	if len(wantBlank) == 0 {
		return encoded
	}
	lines := strings.Split(strings.TrimRight(encoded, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(wantBlank))
	for i, line := range lines {
		m := topLevelKeyLineRE.FindStringSubmatch(line)
		if m != nil && wantBlank[m[1]] {
			// Walk back over the key's column-0 comment block so the blank
			// line lands above it.
			insertAt := len(out)
			for insertAt > 0 && strings.HasPrefix(out[insertAt-1], "#") {
				insertAt--
			}
			if insertAt > 0 && out[insertAt-1] != "" && i > 0 {
				out = append(out[:insertAt], append([]string{""}, out[insertAt:]...)...)
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// topLevelKeysPrecededByBlankLine returns the top-level keys whose line (or
// the column-0 comment block directly above it) follows a blank line in the
// original front matter.
func topLevelKeysPrecededByBlankLine(front string) map[string]bool {
	keys := map[string]bool{}
	lines := strings.Split(front, "\n")
	for i, line := range lines {
		m := topLevelKeyLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		j := i - 1
		for j >= 0 && strings.HasPrefix(lines[j], "#") {
			j--
		}
		if j >= 0 && strings.TrimSpace(lines[j]) == "" {
			keys[m[1]] = true
		}
	}
	return keys
}
