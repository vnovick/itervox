package local

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/domain"
)

// The issue file format (#85):
//
//	---
//	title: Add a --json flag to `report`
//	state: Todo
//	priority: 2
//	labels: [cli]
//	blocked_by: [ITX-3]
//	branch: itervox/itx-4
//	created: 2026-10-09T12:00:00Z
//	updated: 2026-10-09T12:30:00Z
//	---
//
//	The description, in Markdown.
//
//	## Comments
//
//	### 2026-10-09T12:05:00Z — alex
//
//	A comment body.
//
// The file name (without .md) is the issue identifier. `## Comments` is the
// last level-2 heading of that name that is followed by at least one comment;
// each comment starts with a `### <RFC 3339 time> — <author>` line. Text
// between the heading and the first comment is kept. Inside a comment body a
// line that would read as that heading or as a comment header is written
// with a leading backslash (Markdown shows it unchanged) and read back
// without it, so no comment text can change the file's structure. Front
// matter keys Itervox does not know are kept.

// commentsHeading starts the comment section.
const commentsHeading = "## Comments"

// commentHeaderRe matches a comment's first line.
var commentHeaderRe = regexp.MustCompile(`^### (\S+) — (.+)$`)

// issueFile is one parsed issue file.
type issueFile struct {
	Title     string
	State     string
	Priority  *int
	Labels    []string
	BlockedBy []string
	Branch    string
	Created   *time.Time
	Updated   *time.Time
	Extra     map[string]any // unknown front matter keys, kept on rewrite
	Body      string         // the description
	// CommentsPreamble is text between `## Comments` and the first comment.
	CommentsPreamble string
	Comments         []fileComment
}

type fileComment struct {
	At     time.Time
	Author string
	Body   string
}

// knownKeys are the front matter keys issueFile reads.
var knownKeys = map[string]bool{"title": true, "state": true, "priority": true, "labels": true,
	"blocked_by": true, "branch": true, "created": true, "updated": true}

// parseIssueFile parses an issue file. Any problem is an error naming it;
// the caller keeps the last good version.
func parseIssueFile(data []byte) (issueFile, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return issueFile{}, errors.New("missing front matter (the file must start with a --- line)")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	var front, body string
	switch {
	case end >= 0:
		front, body = rest[:end], rest[end+len("\n---\n"):]
	case strings.HasSuffix(rest, "\n---"):
		front, body = strings.TrimSuffix(rest, "\n---"), ""
	default:
		return issueFile{}, errors.New("front matter is not closed (no --- line after it)")
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(front), &raw); err != nil {
		return issueFile{}, fmt.Errorf("front matter: %w", err)
	}
	f := issueFile{Extra: map[string]any{}}
	for k, v := range raw {
		if !knownKeys[k] {
			f.Extra[k] = v
		}
	}
	var ok bool
	if f.Title, ok = stringKey(raw, "title"); !ok || strings.TrimSpace(f.Title) == "" {
		return issueFile{}, errors.New("front matter needs a title")
	}
	if f.State, ok = stringKey(raw, "state"); !ok || strings.TrimSpace(f.State) == "" {
		return issueFile{}, errors.New("front matter needs a state")
	}
	if v, has := raw["priority"]; has && v != nil {
		p, isInt := v.(int)
		if !isInt {
			return issueFile{}, fmt.Errorf("priority must be a number, got %v", v)
		}
		f.Priority = &p
	}
	var err error
	if f.Labels, err = listKey(raw, "labels"); err != nil {
		return issueFile{}, err
	}
	if f.BlockedBy, err = listKey(raw, "blocked_by"); err != nil {
		return issueFile{}, err
	}
	f.Branch, _ = stringKey(raw, "branch")
	if f.Created, err = timeKey(raw, "created"); err != nil {
		return issueFile{}, err
	}
	if f.Updated, err = timeKey(raw, "updated"); err != nil {
		return issueFile{}, err
	}
	f.Body, f.CommentsPreamble, f.Comments = splitComments(strings.TrimLeft(body, "\n"))
	return f, nil
}

func stringKey(m map[string]any, key string) (string, bool) {
	v, has := m[key]
	if !has || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case int, float64, bool:
		return fmt.Sprint(t), true
	}
	return "", false
}

func listKey(m map[string]any, key string) ([]string, error) {
	v, has := m[key]
	if !has || v == nil {
		return nil, nil
	}
	items, isList := v.([]any)
	if !isList {
		if s, isStr := v.(string); isStr { // a single value is a one-item list
			return []string{s}, nil
		}
		return nil, fmt.Errorf("%s must be a list, got %v", key, v)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s := strings.TrimSpace(fmt.Sprint(it))
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func timeKey(m map[string]any, key string) (*time.Time, error) {
	v, has := m[key]
	if !has || v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case time.Time:
		return &t, nil
	case string:
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(t))
		if err != nil {
			return nil, fmt.Errorf("%s must be an RFC 3339 time, got %q", key, t)
		}
		return &parsed, nil
	}
	return nil, fmt.Errorf("%s must be an RFC 3339 time, got %v", key, v)
}

// isStructural reports whether line, outside a code fence, would read as
// the comments heading or a comment header.
func isStructural(line string) bool {
	l := strings.TrimRight(line, " ")
	if l == commentsHeading {
		return true
	}
	if m := commentHeaderRe.FindStringSubmatch(l); m != nil {
		_, err := time.Parse(time.RFC3339, m[1])
		return err == nil
	}
	return false
}

// escapeStructure adds a backslash to each line outside a fence that is a
// structural line after any leading backslashes; unescapeStructure removes
// one, so any text round-trips.
func escapeStructure(text string) string {
	return mapUnfenced(text, func(l string) string {
		if isStructural(strings.TrimLeft(l, `\`)) {
			return `\` + l
		}
		return l
	})
}

func unescapeStructure(text string) string {
	return mapUnfenced(text, func(l string) string {
		if strings.HasPrefix(l, `\`) && isStructural(strings.TrimLeft(l, `\`)) {
			return l[1:]
		}
		return l
	})
}

func mapUnfenced(text string, f func(string) string) string {
	lines := strings.Split(text, "\n")
	inFence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			lines[i] = f(l)
		}
	}
	return strings.Join(lines, "\n")
}

// commentHeaderAt parses line as a comment header.
func commentHeaderAt(line string) (fileComment, bool) {
	m := commentHeaderRe.FindStringSubmatch(strings.TrimRight(line, " "))
	if m == nil {
		return fileComment{}, false
	}
	at, err := time.Parse(time.RFC3339, m[1])
	if err != nil {
		return fileComment{}, false
	}
	return fileComment{At: at, Author: strings.TrimSpace(m[2])}, true
}

// splitComments separates the description, the text before the first
// comment, and the comments. A `## Comments` heading with no comment after
// it is part of the description.
func splitComments(body string) (desc, preamble string, comments []fileComment) {
	lines := strings.Split(body, "\n")
	// unfenced[i] is true when line i is outside a code fence.
	unfenced := make([]bool, len(lines))
	inFence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			continue
		}
		unfenced[i] = !inFence
	}
	idx := -1
	for i := len(lines) - 1; i >= 0 && idx < 0; i-- {
		if !unfenced[i] || strings.TrimRight(lines[i], " ") != commentsHeading {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if _, ok := commentHeaderAt(lines[j]); ok && unfenced[j] {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return strings.TrimRight(body, "\n"), "", nil
	}
	desc = strings.TrimRight(strings.Join(lines[:idx], "\n"), "\n")
	var cur *fileComment
	var buf []string
	flush := func() {
		text := unescapeStructure(strings.Trim(strings.Join(buf, "\n"), "\n"))
		if cur == nil {
			preamble = text
		} else {
			cur.Body = text
			comments = append(comments, *cur)
		}
		buf = nil
	}
	// A header inside a code fence belongs to the comment; render closes any
	// fence a comment leaves open.
	inFence = false
	for _, l := range lines[idx+1:] {
		if !inFence {
			if c, ok := commentHeaderAt(l); ok {
				flush()
				cur = &c
				continue
			}
		}
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
		}
		buf = append(buf, l)
	}
	flush()
	return desc, preamble, comments
}

// render serialises f in the canonical layout.
func (f issueFile) render() []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	writeKV := func(key string, v any) {
		node := map[string]any{key: v}
		out, _ := yaml.Marshal(node)
		b.Write(out)
	}
	writeKV("title", f.Title)
	writeKV("state", f.State)
	if f.Priority != nil {
		writeKV("priority", *f.Priority)
	}
	if len(f.Labels) > 0 {
		b.WriteString("labels: [" + strings.Join(quoteAll(f.Labels), ", ") + "]\n")
	}
	if len(f.BlockedBy) > 0 {
		b.WriteString("blocked_by: [" + strings.Join(quoteAll(f.BlockedBy), ", ") + "]\n")
	}
	if f.Branch != "" {
		writeKV("branch", f.Branch)
	}
	if f.Created != nil {
		b.WriteString("created: " + f.Created.UTC().Format(time.RFC3339) + "\n")
	}
	if f.Updated != nil {
		b.WriteString("updated: " + f.Updated.UTC().Format(time.RFC3339) + "\n")
	}
	keys := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeKV(k, f.Extra[k])
	}
	b.WriteString("---\n")
	if f.Body != "" {
		b.WriteString("\n" + f.Body + "\n")
	}
	if len(f.Comments) > 0 {
		b.WriteString("\n" + commentsHeading + "\n")
		if f.CommentsPreamble != "" {
			b.WriteString("\n" + escapeStructure(f.CommentsPreamble) + "\n")
		}
		for _, c := range f.Comments {
			author := strings.TrimSpace(strings.ReplaceAll(c.Author, "\n", " "))
			fmt.Fprintf(&b, "\n### %s — %s\n\n%s\n", c.At.UTC().Format(time.RFC3339), author, closeFences(escapeStructure(c.Body)))
		}
	}
	return b.Bytes()
}

// closeFences closes a code fence a comment left open, so it cannot hide
// the next comment's header.
func closeFences(text string) string {
	open := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			open = !open
		}
	}
	if open {
		return text + "\n```"
	}
	return text
}

// quoteAll renders list items as YAML flow scalars.
func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		node := &yaml.Node{Kind: yaml.ScalarNode, Value: it}
		s, _ := yaml.Marshal(node)
		out[i] = strings.TrimSpace(string(s))
	}
	return out
}

// toIssue converts a parsed file into a domain issue. Blocker states and
// branches are filled in by the tracker, which knows the other issues.
func (f issueFile) toIssue(identifier string) domain.Issue {
	is := domain.Issue{ID: identifier, Identifier: identifier, Title: f.Title, State: f.State,
		Priority: f.Priority, CreatedAt: f.Created, UpdatedAt: f.Updated}
	if f.Body != "" {
		d := f.Body
		is.Description = &d
	}
	if f.Branch != "" {
		b := f.Branch
		is.BranchName = &b
	}
	if len(f.Labels) > 0 {
		is.Labels = make([]string, len(f.Labels))
		for i, l := range f.Labels {
			is.Labels[i] = strings.ToLower(l)
		}
	}
	for i, c := range f.Comments {
		at := c.At
		is.Comments = append(is.Comments, domain.Comment{
			ID:         fmt.Sprintf("%s-c%d", identifier, i+1),
			Body:       c.Body,
			CreatedAt:  &at,
			AuthorID:   c.Author,
			AuthorName: c.Author,
		})
	}
	return is
}
