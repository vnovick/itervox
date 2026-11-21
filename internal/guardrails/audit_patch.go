package guardrails

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// AuditDeletedTests reads `git diff --name-status -z --no-renames`: a test file
// that is deleted (a rename is a deletion plus an addition) is a finding.
func AuditDeletedTests(status []byte) []AuditFinding {
	var out []AuditFinding
	fields := bytes.Split(status, []byte{0})
	for i := 0; i+1 < len(fields); i += 2 {
		st, p := string(fields[i]), string(fields[i+1])
		if st == "D" && isTestFile(p) {
			out = append(out, AuditFinding{Rule: "test_deleted", Path: p, Message: "test file deleted or renamed"})
		}
	}
	return out
}

// AuditContext is the number of context lines the audit diff carries
// around each change (`git diff -U20`), so a hunk rule can see what a
// change sits inside (an array opened a few lines above, an ESLint rules
// block). Every audit diff uses it: `referee audit`, and AuditRange for
// the pre-push lane reasons and the Lanes view.
const AuditContext = 20

// hunk is one change: a run of removed and added lines with no unchanged
// line between them (what a -U0 hunk holds), plus the head-side lines of
// the diff hunk ahead of it.
type hunk struct {
	firstNew       int
	removed, added []string
	// before is the head version's lines (context and added) of the same
	// diff hunk ahead of this change, oldest first: at most AuditContext
	// unchanged lines, plus any earlier changes between them.
	before []string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// AuditPatch applies the line rules and every hunk rule (coverage
// threshold, eslint --max-warnings, test paths ignored, ESLint rules set
// to 0) to a patch, with or without context lines. Context lines count in
// the head version's line numbers and are never findings themselves; a
// "+++ " line inside a hunk is an added line, not a file header.
func AuditPatch(patch []byte) ([]AuditFinding, error) {
	var out []AuditFinding
	var file string
	inHunk := false
	newLine := 0      // the head line number of the next context or added line
	var head []string // the current diff hunk's head-side lines so far
	var cur *hunk     // the change being read (nil between changes)
	flush := func() {
		if cur != nil && file != "" {
			out = appendHunkFindings(out, file, *cur)
		}
		cur = nil
	}
	change := func() {
		if cur == nil {
			cur = &hunk{firstNew: newLine, before: slices.Clone(head)}
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			file, inHunk = "", false
		case !inHunk && strings.HasPrefix(line, "+++ "):
			file = patchPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			flush()
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("unexpected hunk header %q", line)
			}
			newLine, _ = strconv.Atoi(m[1])
			head, inHunk = nil, true
		case inHunk && strings.HasPrefix(line, " "):
			flush()
			head = append(head, line[1:])
			newLine++
		case inHunk && strings.HasPrefix(line, "-"):
			change()
			cur.removed = append(cur.removed, line[1:])
		case inHunk && strings.HasPrefix(line, "+"):
			change()
			text := line[1:]
			lineNo := newLine
			newLine++
			cur.added = append(cur.added, text)
			head = append(head, text)
			if file == "" {
				continue
			}
			out = appendLineFindings(out, file, lineNo, text)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read diff: %w", err)
	}
	flush()
	return out, nil
}

// appendHunkFindings appends every hunk rule's findings on one change.
func appendHunkFindings(out []AuditFinding, file string, h hunk) []AuditFinding {
	for _, rule := range hunkRules {
		out = append(out, rule(file, h)...)
	}
	return out
}

// appendLineFindings appends the line rules' findings on one added line.
func appendLineFindings(out []AuditFinding, file string, lineNo int, text string) []AuditFinding {
	for _, r := range lineRules {
		if r.Files(file) && r.match(text) {
			out = append(out, AuditFinding{Rule: r.ID, Path: file, Line: lineNo, Message: r.Message, Text: text})
		}
	}
	return out
}

// patchPath is the repository path of a "+++ b/path" line ("" for
// /dev/null: a deleted file has no added lines).
func patchPath(s string) string {
	s = strings.TrimSuffix(s, "\t")
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}
