package local

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Front matter rewrites keep the file's own layout (#118): a file read from
// disk is rewritten by editing its front matter in place, so key order,
// YAML comments, and the spelling of every value Itervox did not change
// survive. Only a key whose value changed is re-encoded; a new key is added
// at the end, and a key whose value is now empty is removed. A file with no
// front matter to edit (a new issue), or one the editor cannot handle, gets
// the standard layout.

// patchFront returns f's front matter (without the --- lines) as an edit of
// f.front, or ok=false when it cannot be edited in place.
func (f issueFile) patchFront() (string, bool) {
	if strings.TrimSpace(f.front) == "" {
		return "", false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(f.front), &doc); err != nil || doc.Kind != yaml.DocumentNode ||
		len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", false
	}
	m := doc.Content[0]
	old, err := parseFrontValues(f.front)
	if err != nil {
		return "", false
	}

	setStr := func(key, want, was string) {
		if want == was {
			return
		}
		if want == "" {
			removeKey(m, key)
			return
		}
		setKey(m, key, encodeNode(want))
	}
	setStr("title", f.Title, old.Title)
	setStr("state", f.State, old.State)
	if !reflect.DeepEqual(f.Priority, old.Priority) {
		if f.Priority == nil {
			removeKey(m, "priority")
		} else {
			setKey(m, "priority", encodeNode(*f.Priority))
		}
	}
	setList := func(key string, want, was []string) {
		if slices.Equal(want, was) {
			return
		}
		if len(want) == 0 {
			removeKey(m, key)
			return
		}
		setKey(m, key, flowSeq(want))
	}
	setList("labels", f.Labels, old.Labels)
	setList("blocked_by", f.BlockedBy, old.BlockedBy)
	setStr("branch", f.Branch, old.Branch)
	setTime := func(key string, want, was *time.Time) {
		if timesEqual(want, was) {
			return
		}
		if want == nil {
			removeKey(m, key)
			return
		}
		setKey(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: want.UTC().Format(time.RFC3339)})
	}
	setTime("created", f.Created, old.Created)
	setTime("updated", f.Updated, old.Updated)
	setStr("remote", f.Remote, old.Remote)

	for k, was := range old.Extra {
		if _, keep := f.Extra[k]; !keep {
			removeKey(m, k)
		} else if !reflect.DeepEqual(f.Extra[k], was) {
			setKey(m, k, encodeNode(f.Extra[k]))
		}
	}
	extra := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		if _, had := old.Extra[k]; !had {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	for _, k := range extra {
		setKey(m, k, encodeNode(f.Extra[k]))
	}

	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", false
	}
	_ = enc.Close()
	out := b.String()
	if _, err := parseFrontValues(out); err != nil {
		return "", false // never write front matter that would not read back
	}
	return out, true
}

// parseFrontValues reads the front matter keys of a file with only that
// front matter (no title or state check: an edited file may drop neither,
// but the old one is compared as it was).
func parseFrontValues(front string) (issueFile, error) {
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(front), &raw); err != nil {
		return issueFile{}, err
	}
	f := issueFile{Extra: map[string]any{}}
	for k, v := range raw {
		if !knownKeys[k] {
			f.Extra[k] = v
		}
	}
	f.Title, _ = stringKey(raw, "title")
	f.State, _ = stringKey(raw, "state")
	if v, ok := raw["priority"].(int); ok {
		f.Priority = &v
	}
	f.Labels, _ = listKey(raw, "labels")
	f.BlockedBy, _ = listKey(raw, "blocked_by")
	f.Branch, _ = stringKey(raw, "branch")
	f.Remote, _ = stringKey(raw, "remote")
	f.Remote = strings.TrimSpace(f.Remote)
	f.Created, _ = timeKey(raw, "created")
	f.Updated, _ = timeKey(raw, "updated")
	return f, nil
}

func timesEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// encodeNode encodes v as a YAML value node.
func encodeNode(v any) *yaml.Node {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	return &n
}

// flowSeq is a one-line list with every item double-quoted (see flowList).
func flowSeq(items []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it, Style: yaml.DoubleQuotedStyle})
	}
	return seq
}

// setKey replaces key's value in mapping m, keeping the key and the old
// value's line comment, or appends the pair when key is absent.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			value.LineComment = old.LineComment
			value.HeadComment, value.FootComment = old.HeadComment, old.FootComment
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// removeKey drops key from mapping m. A comment above the key moves to the
// next key so it is not lost.
func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		head := m.Content[i].HeadComment
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		if head != "" && i < len(m.Content) {
			next := m.Content[i]
			next.HeadComment = strings.TrimSpace(head + "\n" + next.HeadComment)
		}
		return
	}
}
