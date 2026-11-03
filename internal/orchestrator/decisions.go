package orchestrator

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// Decision log (#218, pstack's /show-me-your-work). A run appends one
// tab-separated line per significant decision to run.decisions_path, next
// to its handoff file:
//
//	.itervox/handoff/<run timestamp>_<role>.decisions.tsv
//	<time>\t<decision>\t<why>\t<alternative considered>
//
// The issue detail view reads the decision logs of every run in the
// issue's worktree through GET /api/v1/issues/{identifier}/decisions. The
// files are agent-written, so reading is defensive: bounded file size,
// rows and field length, every field redacted, malformed lines skipped and
// counted. Nothing here touches State; Decisions runs on the caller's
// goroutine (an HTTP handler) and never on the event loop.

// DecisionsFileSuffix ends a run's decision log, which shares its handoff
// file's name stem.
const DecisionsFileSuffix = ".decisions.tsv"

// Decision-log bounds: bytes read per file, rows kept per run, runs listed
// (newest kept), and bytes kept per field.
const (
	maxDecisionsFileBytes  = 64 * 1024
	maxDecisionRowsPerRun  = 200
	maxDecisionRuns        = 50
	maxDecisionFieldBytes  = 500
	maxDecisionTimeBytes   = 40
	decisionsHeaderColumn0 = "time"
)

// decisionsPathFor is a run's decision log path for its handoff path:
// <stem>.decisions.tsv next to <stem>.md.
func decisionsPathFor(handoffRelPath string) string {
	return strings.TrimSuffix(handoffRelPath, ".md") + DecisionsFileSuffix
}

// DecisionRow is one parsed decision.
type DecisionRow struct {
	// Time is the row's time as written (bounded); At is it parsed as
	// RFC 3339, zero when it is not.
	Time        string
	At          time.Time
	Decision    string
	Why         string
	Alternative string
}

// DecisionRun is the decision log of one run.
type DecisionRun struct {
	// Path is relative to the worktree.
	Path string
	// RunTimestamp and Role come from the file name.
	RunTimestamp string
	Role         string
	Decisions    []DecisionRow
	// Truncated: the file had more than maxDecisionRowsPerRun rows or
	// bytes than were read. Skipped counts malformed lines.
	Truncated bool
	Skipped   int
}

// ParseDecisionsTSV reads a decision log: one decision per line, fields
// separated by tabs (time, decision, why, alternative). Blank lines, '#'
// comments and a "time" header row are ignored; a line without a decision
// is skipped and counted. At most maxDecisionRowsPerRun rows are kept.
func ParseDecisionsTSV(r io.Reader) (rows []DecisionRow, truncated bool, skipped int) {
	lr := &io.LimitedReader{R: r, N: maxDecisionsFileBytes + 1}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 0, 4096), maxDecisionsFileBytes+1)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if strings.EqualFold(strings.TrimSpace(f[0]), decisionsHeaderColumn0) {
			continue
		}
		if len(f) < 2 || strings.TrimSpace(f[1]) == "" {
			skipped++
			continue
		}
		if len(rows) == maxDecisionRowsPerRun {
			truncated = true
			break
		}
		for len(f) < 4 {
			f = append(f, "")
		}
		row := DecisionRow{
			Time:        decisionField(f[0], maxDecisionTimeBytes),
			Decision:    decisionField(f[1], maxDecisionFieldBytes),
			Why:         decisionField(f[2], maxDecisionFieldBytes),
			Alternative: decisionField(strings.Join(f[3:], " "), maxDecisionFieldBytes),
		}
		if at, err := time.Parse(time.RFC3339, strings.TrimSpace(f[0])); err == nil {
			row.At = at.UTC()
		}
		rows = append(rows, row)
	}
	if sc.Err() != nil || lr.N <= 0 {
		truncated = true
	}
	return rows, truncated, skipped
}

// decisionField redacts, trims and bounds one agent-written field.
func decisionField(s string, n int) string {
	return boundUTF8(strings.TrimSpace(logging.RedactString(strings.TrimSpace(s))), n)
}

// Decisions lists the decision logs in identifier's worktree, oldest run
// first (the ISO 8601 name prefix orders them); at most maxDecisionRuns,
// the newest kept. Nil when there is no worktree. Reads files only.
func (o *Orchestrator) Decisions(identifier string) []DecisionRun {
	path, err := o.worktreePath(identifier)
	if err != nil {
		return nil
	}
	return readDecisionRuns(filepath.Join(path, HandoffDirRelPath))
}

// readDecisionRuns parses every *.decisions.tsv in dir.
func readDecisionRuns(dir string) []DecisionRun {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), DecisionsFileSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	names = names[max(0, len(names)-maxDecisionRuns):]
	runs := make([]DecisionRun, 0, len(names))
	for _, n := range names {
		run := DecisionRun{Path: HandoffDirRelPath + "/" + n}
		stem := strings.TrimSuffix(n, DecisionsFileSuffix)
		run.RunTimestamp, run.Role, _ = strings.Cut(stem, "_")
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		run.Decisions, run.Truncated, run.Skipped = ParseDecisionsTSV(f)
		_ = f.Close()
		runs = append(runs, run)
	}
	return runs
}
