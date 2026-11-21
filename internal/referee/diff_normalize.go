package referee

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Normalising probe output: two installs of the same tool print different
// versions, timestamps, durations and paths even when they behave the same.
// Those are replaced by placeholders before the outputs are compared, so a
// diff is about behaviour (what the tool says it does, the flags it takes,
// the questions it asks), not about the version number.

var (
	// ANSI escape sequences: CSI (colours, cursor moves) and OSC (titles,
	// hyperlinks), then any other two-byte escape.
	ansiCSI   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC   = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiOther = regexp.MustCompile(`\x1b[@-Z\\-_]`)

	normalizers = []struct {
		re   *regexp.Regexp
		repl string
	}{
		// Order matters: a timestamp contains things that look like a
		// version or a duration.
		{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?`), "<time>"},
		{regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\b`), "<time>"},
		{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
		{regexp.MustCompile(`\bv?\d+\.\d+(?:\.\d+)+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?\b`), "<version>"},
		{regexp.MustCompile(`\b\d+(?:\.\d+)?\s?(?:ms|µs|us|ns)\b|\b\d+\.\d+\s?s\b`), "<duration>"},
		{regexp.MustCompile(`(?i)\b[0-9a-f]{12,64}\b`), "<hex>"},
		{regexp.MustCompile(`(?:/private)?/(?:tmp|var/folders|var/tmp)/[^\s'"():,]+`), "<tmp>"},
	}

	// promptPatterns are lines that ask the operator to confirm something.
	promptPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)[\[(]\s*y(?:es)?\s*/\s*n(?:o)?\s*[\])]`),
		regexp.MustCompile(`^\s*\?\s+\S`), // inquirer-style "? Are you sure …"
		regexp.MustCompile(`(?i)\b(?:are you sure|do you want to (?:continue|proceed)|continue\?|proceed\?|press (?:enter|return|any key))`),
	}
)

// Bounds on what a report carries about one probe.
const (
	maxDiffLines   = 20  // added / removed lines listed per probe
	maxPromptLines = 10  // new prompts listed per probe
	maxLineBytes   = 200 // a listed line is cut after this many bytes
)

// normalizeOutput turns raw probe output into comparable lines. paths are
// replaced (longest first) by their placeholder before anything else.
func normalizeOutput(raw []byte, paths []pathPlaceholder) []string {
	s := strings.ToValidUTF8(string(raw), "�")
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiOther.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, p := range paths {
		if p.path != "" {
			s = strings.ReplaceAll(s, p.path, p.placeholder)
		}
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		// A carriage return redraws the line (progress bars): keep what
		// was left on screen.
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		for _, n := range normalizers {
			line = n.re.ReplaceAllString(line, n.repl)
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// pathPlaceholder is a path that differs between the two runs (the
// checkout, the fresh HOME) and the placeholder that replaces it.
type pathPlaceholder struct{ path, placeholder string }

// isPrompt reports whether a line asks for a confirmation.
func isPrompt(line string) bool {
	for _, re := range promptPatterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// lineDiff is the lines only head has and the lines only base has, as
// multisets in each side's order: a line printed twice on head and once on
// base is added once. Reordering alone adds and removes nothing.
func lineDiff(base, head []string) (added, removed []string) {
	count := func(lines []string) map[string]int {
		m := map[string]int{}
		for _, l := range lines {
			m[l]++
		}
		return m
	}
	inBase, inHead := count(base), count(head)
	for _, l := range head {
		if inBase[l] > 0 {
			inBase[l]--
			continue
		}
		added = append(added, l)
	}
	for _, l := range base {
		if inHead[l] > 0 {
			inHead[l]--
			continue
		}
		removed = append(removed, l)
	}
	return added, removed
}

// newPrompts is the prompt lines of head that base does not print.
func newPrompts(base, head []string) []string {
	seen := map[string]bool{}
	for _, l := range base {
		if isPrompt(l) {
			seen[l] = true
		}
	}
	var out []string
	for _, l := range head {
		if isPrompt(l) && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// looksLikeQuestion reports whether the last thing a probe printed before
// it timed out reads as a question waiting for an answer.
func looksLikeQuestion(line string) bool {
	line = strings.TrimSpace(line)
	return isPrompt(line) || strings.HasSuffix(line, "?") || strings.HasSuffix(line, ":")
}

// bounded keeps at most n lines, each cut to maxLineBytes and made safe to
// print.
func bounded(lines []string, n int) []string {
	out := []string{}
	for _, l := range lines[:min(len(lines), n)] {
		if len(l) > maxLineBytes {
			cut := maxLineBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut] + "…"
		}
		out = append(out, displayPath(l))
	}
	return out
}
