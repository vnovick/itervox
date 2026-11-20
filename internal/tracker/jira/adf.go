package jira

import (
	"fmt"
	"regexp"
	"slices"
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

// adfToText prints an Atlassian Document Format tree as Markdown. For the
// subset textToADF writes (headings, **strong**, _em_, `code`, links, fenced
// code, "- " and "1. " lists, "> " quotes) it gives back exactly the text that
// was written, so Itervox recognises its own comments. Rich text a person
// wrote in Jira reads as the same Markdown. Blocks are separated by one blank
// line. A node it does not know gives the text of its children, or nothing.
// A string is returned as is; any other non-map value gives "". Trailing
// whitespace is trimmed.
func adfToText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	node, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimRight(blockText(node), " \t\r\n")
}

// blockText prints one block node and everything in it.
func blockText(node map[string]any) string {
	switch node["type"] {
	case "paragraph":
		return inlineText(children(node))
	case "heading":
		level := min(max(intAttr(node, "level", 1), 1), 6)
		return strings.Repeat("#", level) + " " + inlineText(children(node))
	case "bulletList", "orderedList":
		return listText(node)
	case "codeBlock":
		var code strings.Builder
		for _, ch := range children(node) {
			s, _ := ch["text"].(string) // a code block holds unmarked text only
			code.WriteString(s)
		}
		if code.Len() > 0 {
			code.WriteString("\n")
		}
		return "```" + strAttr(node, "language") + "\n" + code.String() + "```"
	case "blockquote":
		lines := strings.Split(blocksText(children(node), "\n\n"), "\n")
		for i, l := range lines {
			if l == "" {
				lines[i] = ">"
			} else {
				lines[i] = "> " + l
			}
		}
		return strings.Join(lines, "\n")
	case "rule":
		return "---"
	case "text", "hardBreak", "mention", "emoji", "inlineCard":
		return inlineText([]map[string]any{node})
	default: // doc, panel, table, expand and any other node
		return blocksText(children(node), "\n\n")
	}
}

// blocksText prints block nodes joined by sep, leaving out the ones that
// print nothing (media, an empty paragraph).
func blocksText(nodes []map[string]any, sep string) string {
	var parts []string
	for _, n := range nodes {
		if s := blockText(n); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

// listText prints a list, one "- " or "1. " line per item. The lines inside an
// item (a nested list, a hard break) are indented under the item's marker.
func listText(node map[string]any) string {
	n := intAttr(node, "order", 1)
	var lines []string
	for _, item := range children(node) {
		marker := "- "
		if node["type"] == "orderedList" {
			marker = fmt.Sprintf("%d. ", n)
			n++
		}
		text := blocksText(children(item), "\n")
		lines = append(lines, marker+strings.ReplaceAll(text, "\n", "\n"+strings.Repeat(" ", len(marker))))
	}
	return strings.Join(lines, "\n")
}

// mark is how one ADF mark prints in Markdown.
type mark struct{ open, close string }

var (
	strongMark = mark{"**", "**"}
	emMark     = mark{"_", "_"}
	codeMark   = mark{"`", "`"}
)

// inlineText prints inline nodes. A mark opens where it starts and closes
// where it ends, so text a person styled reads as **bold _both_ bold**, not
// as one pair of marks per text node.
func inlineText(nodes []map[string]any) string {
	var sb strings.Builder
	var open []mark
	closeTo := func(n int) {
		for len(open) > n {
			sb.WriteString(open[len(open)-1].close)
			open = open[:len(open)-1]
		}
	}
	for _, node := range nodes {
		switch node["type"] {
		case "text":
			s, _ := node["text"].(string)
			want := marksOf(node, s)
			// Keep the marks that stay open. ADF cannot put strong or em on
			// code, so a code node inside bold text keeps the bold open.
			keep := 0
			for keep < len(open) && (slices.Contains(want, open[keep]) ||
				slices.Contains(want, codeMark) && (open[keep] == strongMark || open[keep] == emMark)) {
				keep++
			}
			closeTo(keep)
			for _, m := range want {
				if !slices.Contains(open, m) {
					sb.WriteString(m.open)
					open = append(open, m)
				}
			}
			sb.WriteString(s)
		case "hardBreak":
			closeTo(0)
			sb.WriteString("\n")
		case "mention", "emoji":
			closeTo(0)
			sb.WriteString(strAttr(node, "text"))
		case "inlineCard":
			closeTo(0)
			sb.WriteString(strAttr(node, "url"))
		default: // status, date and any other inline node
			closeTo(0)
			sb.WriteString(inlineText(children(node)))
		}
	}
	closeTo(0)
	return sb.String()
}

// marksOf returns the Markdown marks of a text node, in the node's order.
// Marks Markdown has no form for (strike, underline, colour) are left out. A
// link whose address is its text, or that has no address, prints as the text.
func marksOf(node map[string]any, text string) []mark {
	var out []mark
	list, _ := node["marks"].([]any)
	for _, m := range list {
		am, _ := m.(map[string]any)
		switch am["type"] {
		case "strong":
			out = append(out, strongMark)
		case "em":
			out = append(out, emMark)
		case "code":
			out = append(out, codeMark)
		case "link":
			if href := strAttr(am, "href"); href != "" && href != text {
				out = append(out, mark{"[", "](" + href + ")"})
			}
		}
	}
	return out
}

// children returns the node's content entries that are nodes.
func children(node map[string]any) []map[string]any {
	list, _ := node["content"].([]any)
	var out []map[string]any
	for _, ch := range list {
		if m, ok := ch.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// strAttr returns the string attribute key of node, or "".
func strAttr(node map[string]any, key string) string {
	attrs, _ := node["attrs"].(map[string]any)
	s, _ := attrs[key].(string)
	return s
}

// intAttr returns the number attribute key of node, or def. JSON gives
// numbers as float64; documents built in Go hold an int.
func intAttr(node map[string]any, key string, def int) int {
	attrs, _ := node["attrs"].(map[string]any)
	switch v := attrs[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}
