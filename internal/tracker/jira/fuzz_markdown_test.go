package jira_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/tracker/jira"
)

// normalizeBlankLines is what the writer cannot keep, as before: leading
// blank lines, runs of blank lines (one blank line separates blocks) and
// trailing whitespace.
func normalizeBlankLines(s string) string {
	s = strings.TrimLeft(strings.TrimRight(s, " \t\r\n"), "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// FuzzMarkdownRoundTrip checks, for any text, that the document the writer
// makes is valid ADF and reads back as the same text.
func FuzzMarkdownRoundTrip(f *testing.F) {
	for _, tc := range itervoxTexts {
		f.Add(tc.text)
	}
	f.Add(everyConstruct)
	for _, seed := range []string{
		"", "\n", "**", "****", "** **", "__x__", "_", "`", "``", "```", "[", "[]()", "[x](", "[x](https://)",
		"*_`[x](https://e.example)`_*", "**a _b `c` d_ e**", "_**x**_", "**[a](https://e.example)**",
		"# ", "#", "- ", "1. ", "0. zero", "999999999999. big", "> ", ">", "```\n\n```", "```go\n```",
		"a\n\n\n\nb", "  \n\t\n", "\r\n\r\n", "\x00\xff\xfe", "🤖 **Agent needs your input**\n\n---\n_x_",
		"- a\n- b\n\n1. c\n2. d\n\n> e\n\n```\nf\n```", strings.Repeat("**_`[", 200),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		text := normalizeBlankLines(raw)
		doc := jira.TextToADF(text)
		if got := jira.ADFToText(doc); got != text {
			t.Fatalf("round trip changed the text\n in: %q\nout: %q", text, got)
		}
		if problem := invalidADF(doc); problem != "" {
			t.Fatalf("invalid ADF for %q: %s", text, problem)
		}
	})
}

// invalidADF returns why doc breaks an ADF rule the writer must keep, or "".
// The rules are from developer.atlassian.com/cloud/jira/platform/apis/document.
func invalidADF(node map[string]any) string {
	content, _ := node["content"].([]any)
	switch node["type"] {
	case "text":
		if s, _ := node["text"].(string); s == "" {
			return "empty text node"
		}
		var types []string
		marks, _ := node["marks"].([]any)
		for _, m := range marks {
			types = append(types, m.(map[string]any)["type"].(string))
		}
		if slices.Contains(types, "code") && slices.ContainsFunc(types, func(t string) bool { return t != "code" && t != "link" }) {
			return "code mark combined with " + strings.Join(types, ",")
		}
		slices.Sort(types)
		if len(slices.Compact(types)) != len(types) {
			return "repeated mark"
		}
	case "heading":
		if l, _ := node["attrs"].(map[string]any)["level"].(int); l < 1 || l > 6 {
			return "heading level out of range"
		}
	case "codeBlock":
		for _, c := range content {
			if c.(map[string]any)["type"] != "text" || c.(map[string]any)["marks"] != nil {
				return "codeBlock holds more than unmarked text"
			}
		}
	case "listItem":
		if len(content) == 0 || content[0].(map[string]any)["type"] != "paragraph" {
			return "list item without a paragraph"
		}
	case "paragraph", "bulletList", "orderedList", "blockquote":
		if len(content) == 0 {
			return "empty " + node["type"].(string)
		}
	}
	for _, c := range content {
		if problem := invalidADF(c.(map[string]any)); problem != "" {
			return problem
		}
	}
	return ""
}
