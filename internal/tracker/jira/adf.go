package jira

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// contentLimit is the most text Jira Cloud stores in a comment body or an
// issue description. Atlassian states 32,767 characters for both
// (https://support.atlassian.com/atlassian-cloud/kb/commentbodycharacterlimitexceededexception-no-message-or-the-entered-text-is-too-long-it-exceeds-the-allowed-limit-of-32-767-characters-error-message-in-jcma/)
// but not how they are counted. Measured on a live Jira Cloud site on
// 2026-11-20, with documents built by textToADF:
//
//   - The count is UTF-16 code units: 32,767 ASCII letters, accented letters
//     or CJK characters are stored, 32,768 are refused; 16,383 emoji (outside
//     the BMP, 2 units each) are stored, 16,384 are refused; one emoji and
//     32,765 letters (32,766 code points, 32,767 units) are stored, one emoji
//     and 32,766 letters (32,767 code points, 32,768 units) are refused. Bytes
//     do not count: 32,767 CJK characters are 98,301 bytes.
//   - A hard break counts 1 and a break between two paragraphs counts 2: with
//     999 of either, the last stored text was 32,767 minus 999, or minus 1,998.
//   - Markdown and HTML characters count 1 each (32,767 of "*", "{", "\",
//     "<", "&", "_", "[", "|" or "-" are stored).
//   - The size of the document does not count (a comment of 7,999 characters
//     in a 216 KB document was stored).
//   - Over the limit, a comment gets 400 {"errorMessages":
//     ["CONTENT_LIMIT_EXCEEDED"],"errors":{"comment":"CONTENT_LIMIT_EXCEEDED"}};
//     a description gets 400 with errors.description CONTENT_LIMIT_EXCEEDED on
//     edit and "The entered text is too long. It exceeds the allowed limit of
//     32767 characters." on create.
const contentLimit = 32767

// itervoxMarkerPrefix starts every hidden marker line the daemon appends to
// its own comments (tracker.ManagedCommentMarker, tracker.CommentKeyMarker).
const itervoxMarkerPrefix = "<!-- itervox:"

// blankRuns matches the blank lines textToADF drops: a run of three or more
// line feeds is stored as one paragraph break, the same as two.
var blankRuns = regexp.MustCompile(`\n{3,}`)

// utf16Len returns the length of s in UTF-16 code units, the unit Jira counts.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// storedLen returns what Jira counts for text sent through textToADF: its
// UTF-16 units, where a line feed is a hard break (1) and a blank line is a
// paragraph break (2). Line feeds at the start and end, and blank lines after
// the first in a run, count nothing because textToADF drops them.
func storedLen(text string) int {
	return utf16Len(blankRuns.ReplaceAllString(strings.Trim(text, "\n"), "\n\n"))
}

// fitContentLimit returns text unchanged when Jira can store it. Otherwise it
// keeps the start of the text, cut on a character boundary and at the end of
// a line when one ends in the second half of the room, then one line that
// says how much was cut, then the trailing Itervox marker lines exactly as
// they were. The cut counts with utf16Len, which is never less than what
// Jira counts, so the result always fits.
func fitContentLimit(text string) string {
	if storedLen(text) <= contentLimit {
		return text
	}
	body, tail := splitTrailingMarkers(text)
	note := func(cut int) string {
		return fmt.Sprintf("\n\n[Shortened by Itervox: %d characters cut to fit Jira's %d-character limit.]", cut, contentLimit)
	}
	total := utf16Len(body)
	// The note for total is at least as long as the final note.
	room := contentLimit - utf16Len(tail) - utf16Len(note(total))
	end, n := len(body), 0
	for i, r := range body {
		if n += utf16.RuneLen(r); n > room {
			end = i
			break
		}
	}
	kept := body[:end]
	if nl := strings.LastIndexByte(kept, '\n'); nl >= len(kept)/2 {
		kept = kept[:nl]
	}
	kept = strings.TrimRight(kept, " \t\r\n")
	return kept + note(total-utf16Len(kept)) + tail
}

// splitTrailingMarkers splits text before its last lines when they are blank
// lines and Itervox marker lines with at least one marker; tail keeps them
// byte for byte, with the line feed before them. Without a marker, or when
// the marker lines would take more than half the limit, tail is empty.
func splitTrailingMarkers(text string) (body, tail string) {
	end := len(text)
	for end > 0 {
		start := strings.LastIndexByte(text[:end], '\n') + 1
		line := strings.TrimSpace(text[start:end])
		if line != "" && (!strings.HasPrefix(line, itervoxMarkerPrefix) || !strings.HasSuffix(line, "-->")) {
			break
		}
		end = max(start-1, 0)
	}
	if tail = text[end:]; !strings.Contains(tail, itervoxMarkerPrefix) || utf16Len(tail) > contentLimit/2 {
		return text, ""
	}
	return text[:end], tail
}

// adfToText flattens an Atlassian Document Format tree to plain text. Blocks
// (paragraphs, headings, lists, code blocks) are separated by one blank line,
// as in Markdown. A string is returned as is; any other non-map value gives
// "". Trailing whitespace is trimmed.
func adfToText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	node, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	writeADF(&sb, node)
	return strings.TrimRight(sb.String(), " \t\r\n")
}

// writeADF appends the text of one ADF node and its children to sb.
func writeADF(sb *strings.Builder, node map[string]any) {
	attr := func(key string) string {
		attrs, _ := node["attrs"].(map[string]any)
		s, _ := attrs[key].(string)
		return s
	}
	switch node["type"] {
	case "text":
		s, _ := node["text"].(string)
		sb.WriteString(s)
		if href := linkHref(node); href != "" && href != s {
			sb.WriteString(" (" + href + ")")
		}
	case "hardBreak":
		sb.WriteString("\n")
	case "paragraph", "heading":
		writeADFChildren(sb, node)
		sb.WriteString("\n\n")
	case "bulletList", "orderedList":
		writeADFChildren(sb, node)
		sb.WriteString("\n")
	case "listItem":
		var item strings.Builder
		writeADFChildren(&item, node)
		sb.WriteString("- " + strings.TrimRight(item.String(), "\n") + "\n")
	case "codeBlock":
		var code strings.Builder
		writeADFChildren(&code, node)
		sb.WriteString("```\n" + code.String() + "\n```\n\n")
	case "mention", "emoji":
		sb.WriteString(attr("text"))
	case "inlineCard":
		sb.WriteString(attr("url"))
	default: // doc, panel, and any unknown node
		writeADFChildren(sb, node)
	}
}

// linkHref returns the address of the first link mark on a text node, or "".
func linkHref(node map[string]any) string {
	marks, _ := node["marks"].([]any)
	for _, m := range marks {
		mark, _ := m.(map[string]any)
		if mark["type"] == "link" {
			return str(nested(mark, "attrs", "href"))
		}
	}
	return ""
}

func writeADFChildren(sb *strings.Builder, node map[string]any) {
	children, _ := node["content"].([]any)
	for _, ch := range children {
		if m, ok := ch.(map[string]any); ok {
			writeADF(sb, m)
		}
	}
}

// textToADF wraps plain text in an Atlassian Document Format document, so
// Markdown typed by an agent is stored as typed. A block of lines becomes one
// paragraph, with a hard break between its lines. Blank lines only separate
// blocks: an empty paragraph for each one makes Jira show the text
// double-spaced.
func textToADF(text string) map[string]any {
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
	return map[string]any{"type": "doc", "version": 1, "content": content}
}
