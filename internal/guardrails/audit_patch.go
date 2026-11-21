package guardrails

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
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

// hunk is one -U0 hunk's removed and added lines.
type hunk struct {
	firstNew       int
	removed, added []string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// AuditPatch applies the line rules and the threshold rule to a -U0 patch.
func AuditPatch(patch []byte) ([]AuditFinding, error) {
	var out []AuditFinding
	var file string
	var cur *hunk
	flush := func() {
		if cur != nil && file != "" {
			out = append(out, lowered(file, *cur)...)
		}
		cur = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			file = ""
		case cur == nil && strings.HasPrefix(line, "+++ "):
			file = patchPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			flush()
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("unexpected hunk header %q", line)
			}
			n, _ := strconv.Atoi(m[1])
			cur = &hunk{firstNew: n}
		case cur != nil && strings.HasPrefix(line, "-"):
			cur.removed = append(cur.removed, line[1:])
		case cur != nil && strings.HasPrefix(line, "+"):
			text := line[1:]
			lineNo := cur.firstNew + len(cur.added)
			cur.added = append(cur.added, text)
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
