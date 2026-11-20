package jira

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// textToADF converts Markdown text to an Atlassian Document Format document,
// so that Jira shows it as rich text. It splits the text into blocks at blank
// lines. A block becomes a heading ("# " to "###### "), a fenced code block, a
// "- " or "1. " list, a "> " quote or a paragraph, with **strong**, _em_,
// `code` and [text](https://...) links inside. A block is converted only when
// adfToText reads the result back as exactly that block; any other block is
// written as plain text, as plainTextToADF writes it. So for text with one
// blank line between blocks and no trailing whitespace,
// adfToText(textToADF(text)) == text. The daemon recognises its own comments
// by that text.
func textToADF(text string) map[string]any {
	content := []any{}
	for _, block := range markdownBlocks(text) {
		if node := richBlock(block); node != nil && blockText(node) == block {
			content = append(content, node)
		} else {
			content = append(content, plainParagraphs(block)...)
		}
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

// plainTextToADF writes text as plain paragraphs, the way the adapter wrote
// all text before Markdown support. A block of lines becomes one paragraph,
// with a hard break between its lines. Blank lines only separate blocks: an
// empty paragraph for each one makes Jira show the text double-spaced.
func plainTextToADF(text string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": plainParagraphs(text)}
}

func plainParagraphs(text string) []any {
	content := []any{}
	var block []any
	flush := func() {
		if len(block) > 0 {
			content = append(content, map[string]any{"type": "paragraph", "content": block})
			block = nil
		}
	}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			flush()
			continue
		}
		if len(block) > 0 {
			block = append(block, map[string]any{"type": "hardBreak"})
		}
		block = append(block, map[string]any{"type": "text", "text": line})
	}
	flush()
	return content
}

// markdownBlocks splits text at blank lines. A fenced code block stays one
// block even when it holds blank lines.
func markdownBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	var blocks []string
	for i := 0; i < len(lines); {
		if lines[i] == "" {
			i++
			continue
		}
		end := fenceEnd(lines, i)
		if end < 0 {
			for end = i; end+1 < len(lines) && lines[end+1] != ""; end++ {
			}
		}
		blocks = append(blocks, strings.Join(lines[i:end+1], "\n"))
		i = end + 1
	}
	return blocks
}

// fenceEnd returns the index of the line that closes the code fence opened at
// lines[i], or -1. The closing line is the first "```" after the opening line,
// and a blank line or the end of the text must follow it.
func fenceEnd(lines []string, i int) int {
	if _, ok := fenceLanguage(lines[i]); !ok {
		return -1
	}
	for j := i + 1; j < len(lines); j++ {
		if lines[j] == "```" {
			if j+1 == len(lines) || lines[j+1] == "" {
				return j
			}
			return -1
		}
	}
	return -1
}

// fenceLanguage reports whether line opens a code fence: "```" and an
// optional language name of letters, digits and + # . _ - characters.
func fenceLanguage(line string) (string, bool) {
	lang, ok := strings.CutPrefix(line, "```")
	if !ok {
		return "", false
	}
	for _, r := range lang {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("+#._-", r) {
			return "", false
		}
	}
	return lang, true
}

// richBlock converts one block to an ADF node: the construct its lines make,
// else a paragraph. It returns nil for an empty code fence, because a code
// block must hold text.
func richBlock(block string) map[string]any {
	lines := strings.Split(block, "\n")
	last := len(lines) - 1
	if lang, ok := fenceLanguage(lines[0]); ok && last > 0 && lines[last] == "```" && !slices.Contains(lines[1:last], "```") {
		code := strings.Join(lines[1:last], "\n")
		if code == "" {
			return nil
		}
		node := adfNode("codeBlock", textNode(code, nil))
		if lang != "" {
			node["attrs"] = map[string]any{"language": lang}
		}
		return node
	}
	if level, text := headingLine(lines[0]); level > 0 && last == 0 {
		node := adfNode("heading", inline(text, nil)...)
		node["attrs"] = map[string]any{"level": level}
		return node
	}
	if items, ok := allLines(lines, bulletItem); ok {
		return list("bulletList", items)
	}
	if items, ok := allLines(lines, numberedItem); ok {
		node := list("orderedList", items)
		if first, _ := strconv.Atoi(lines[0][:strings.IndexByte(lines[0], '.')]); first != 1 {
			node["attrs"] = map[string]any{"order": first}
		}
		return node
	}
	if quoted, ok := allLines(lines, quoteLine); ok {
		return adfNode("blockquote", paragraph(quoted))
	}
	return paragraph(lines)
}

// allLines applies item to every line and returns the texts it gives, or
// false when a line does not match.
func allLines(lines []string, item func(string) (string, bool)) ([]string, bool) {
	out := make([]string, len(lines))
	for i, line := range lines {
		text, ok := item(line)
		if !ok {
			return nil, false
		}
		out[i] = text
	}
	return out, true
}

// headingLine returns the level and text of a heading line: one to six "#",
// a space and text. The level is 0 for any other line.
func headingLine(line string) (int, string) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	text, ok := strings.CutPrefix(line[level:], " ")
	if level == 0 || level > 6 || !ok || text == "" {
		return 0, ""
	}
	return level, text
}

// bulletItem returns the text of a "- " list line.
func bulletItem(line string) (string, bool) {
	text, ok := strings.CutPrefix(line, "- ")
	return text, ok && text != ""
}

// numberedItem returns the text of a numbered list line: one to nine digits,
// ". " and text.
func numberedItem(line string) (string, bool) {
	num, text, ok := strings.Cut(line, ". ")
	if !ok || num == "" || len(num) > 9 || strings.Trim(num, "0123456789") != "" || text == "" {
		return "", false
	}
	return text, true
}

// quoteLine returns the text of a "> " quote line.
func quoteLine(line string) (string, bool) { return strings.CutPrefix(line, "> ") }

// list makes a list of kind with one paragraph per item.
func list(kind string, items []string) map[string]any {
	content := make([]any, len(items))
	for i, item := range items {
		content[i] = adfNode("listItem", paragraph([]string{item}))
	}
	return adfNode(kind, content...)
}

// paragraph makes one paragraph of lines, with a hard break between them.
func paragraph(lines []string) map[string]any {
	var content []any
	for i, line := range lines {
		if i > 0 {
			content = append(content, map[string]any{"type": "hardBreak"})
		}
		content = append(content, inline(line, nil)...)
	}
	return adfNode("paragraph", content...)
}

func adfNode(kind string, content ...any) map[string]any {
	return map[string]any{"type": kind, "content": content}
}

func textNode(text string, marks []any) map[string]any {
	node := map[string]any{"type": "text", "text": text}
	if len(marks) > 0 {
		node["marks"] = slices.Clone(marks)
	}
	return node
}

// inline converts one line of Markdown to ADF inline nodes. marks are the
// marks of the span the line is inside, none at the top. Characters that do
// not make a complete span stay text, as typed.
func inline(s string, marks []any) []any {
	var out []any
	start := 0
	for i := 0; i < len(s); i++ {
		nodes, end := span(s, i, marks)
		if nodes == nil {
			continue
		}
		if i > start {
			out = append(out, textNode(s[start:i], marks))
		}
		out = append(out, nodes...)
		start, i = end, end-1
	}
	if start < len(s) {
		out = append(out, textNode(s[start:], marks))
	}
	return out
}

// span converts the span that starts at s[i]: `code`, **strong**, _em_ or
// [text](https://...). It returns the nodes and the index after the span, or
// nil when no span starts at i.
func span(s string, i int, marks []any) ([]any, int) {
	rest := s[i:]
	switch {
	case rest[0] == '`':
		// One backtick on each side of code text that has no backtick.
		n := strings.IndexByte(rest[1:], '`')
		end := i + 1 + n
		if n <= 0 || i > 0 && s[i-1] == '`' || end+1 < len(s) && s[end+1] == '`' {
			return nil, 0
		}
		return []any{textNode(s[i+1:end], codeMarks(marks))}, end + 1
	case strings.HasPrefix(rest, "**"):
		n := strings.Index(rest[2:], "**")
		if n <= 0 || isSpace(rest[2]) || isSpace(rest[1+n]) {
			return nil, 0
		}
		return styled(rest[2:2+n], marks, "strong"), i + n + 4
	case rest[0] == '_' && (i == 0 || !isWordByte(s[i-1])):
		n := emEnd(rest)
		if n < 0 {
			return nil, 0
		}
		return styled(rest[1:n], marks, "em"), i + n + 1
	case rest[0] == '[':
		text, href, n := link(rest)
		if n == 0 {
			return nil, 0
		}
		return inline(text, withMark(marks, map[string]any{"type": "link", "attrs": map[string]any{"href": href}})), i + n
	}
	return nil, 0
}

// styled converts the text of a strong or em span. It refuses a span that
// opens with code: ADF cannot mark code strong or em, so that span would not
// read back as typed.
func styled(text string, marks []any, kind string) []any {
	nodes := inline(text, withMark(marks, map[string]any{"type": kind}))
	if first := nodes[0].(map[string]any); hasMark(first["marks"], "code") {
		return nil
	}
	return nodes
}

// emEnd returns the index in rest of the "_" that closes the italic span rest
// opens, or -1. As in Markdown, an underscore inside a word (snake_case)
// neither opens nor closes it, and the text must not start or end with a
// space. A line full of openers with no closer makes the search quadratic: a
// 32 KB line of "_a " takes about 0.1 s.
func emEnd(rest string) int {
	if len(rest) < 3 || isSpace(rest[1]) || rest[1] == '_' {
		return -1
	}
	for k := 2; k < len(rest); k++ {
		if rest[k] == '_' && !isSpace(rest[k-1]) && (k+1 == len(rest) || !isWordByte(rest[k+1])) {
			return k
		}
	}
	return -1
}

// link parses "[text](address)" at the start of rest and returns its length,
// or 0. The address must be an http or https URL with a host, without spaces
// or parentheses.
func link(rest string) (text, href string, n int) {
	textEnd := strings.Index(rest, "](")
	if textEnd < 2 {
		return "", "", 0
	}
	hrefEnd := strings.IndexByte(rest[textEnd+2:], ')')
	if hrefEnd < 0 {
		return "", "", 0
	}
	text, href = rest[1:textEnd], rest[textEnd+2:textEnd+2+hrefEnd]
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" ||
		strings.ContainsAny(href, " \t(") {
		return "", "", 0
	}
	return text, href, textEnd + 2 + hrefEnd + 1
}

// codeMarks returns the marks of inline code inside a span with marks. ADF
// lets code combine only with a link, so strong and em are dropped.
func codeMarks(marks []any) []any {
	out := slices.DeleteFunc(slices.Clone(marks), func(m any) bool { return m.(map[string]any)["type"] != "link" })
	return append(out, map[string]any{"type": "code"})
}

func withMark(marks []any, m map[string]any) []any { return append(slices.Clip(marks), m) }

// hasMark reports whether the mark list holds a mark of kind.
func hasMark(marks any, kind string) bool {
	list, _ := marks.([]any)
	return slices.ContainsFunc(list, func(m any) bool { return m.(map[string]any)["type"] == kind })
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

// isWordByte reports whether b is part of a word: an ASCII letter or digit,
// an underscore, or a byte of a non-ASCII character.
func isWordByte(b byte) bool {
	return b == '_' || b >= 0x80 || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9'
}
