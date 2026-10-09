package main

import (
	"bytes"
	"errors"
	"fmt"
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
// (for example a bare `agent:` with a null value).
func yamlNodeEnsureMap(m *yaml.Node, key string) *yaml.Node {
	if existing := yamlNodeGet(m, key); existing != nil && existing.Kind == yaml.MappingNode {
		return existing
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	yamlNodeSet(m, key, child)
	return child
}

// yamlNodeMappingEntries returns the key names of a mapping node in order.
func yamlNodeMappingEntries(m *yaml.Node) []string {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	return keys
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

// yamlNodeMappingFor returns the mapping node at `path` (dot-free: one key
// per element) below `root`, or nil when any step is missing or not a
// mapping. Used by tests to inspect the rewritten front matter.
func yamlNodeMappingFor(root *yaml.Node, path ...string) *yaml.Node {
	cur := root
	for _, key := range path {
		cur = yamlNodeGet(cur, key)
		if cur == nil || cur.Kind != yaml.MappingNode {
			return nil
		}
	}
	return cur
}

// frontMatterKeyOrder parses a front-matter text and returns its top-level
// key order; it is a test helper kept next to the production helpers so the
// two cannot drift.
func frontMatterKeyOrder(front string) ([]string, error) {
	_, root, err := parseFrontMatterNode(front)
	if err != nil {
		return nil, fmt.Errorf("parse front matter: %w", err)
	}
	return yamlNodeMappingEntries(root), nil
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
