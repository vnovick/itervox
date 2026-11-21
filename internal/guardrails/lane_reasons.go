package guardrails

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
)

// Lane reasons: why a push needs a person, concretely, with file and line.
// A held human-lane push carries them in its ApprovalRequest, and the
// daemon shows them wherever the approval appears (the dashboard card, the
// tracker comment, Slack, `itervox watch`) and in the Lanes view:
//
//	.github/workflows/ci.yml:10 adds `continue-on-error: true` (failure ignored …)
//	.github/workflows/deploy.yml matched .github/workflows/** → human
//
// Two sources today: the audit rules (AuditFinding, the referee's table)
// over the pushed diff, and the lane classification (the pattern that put
// each path in its lane). ReasonToolBehaviour is a typed slot for a third
// ("a tool's behaviour changed") that nothing produces yet.

// Reason kinds.
const (
	// ReasonFinding: an audit rule matched an added line (or a deleted
	// test file). Rule is the audit rule id, Line the head-version line.
	ReasonFinding = "finding"
	// ReasonLaneRule: the path matched a lane pattern. Rule is the pattern,
	// Lane the lane it put the path in.
	ReasonLaneRule = "lane_rule"
	// ReasonToolBehaviour is reserved for a tool whose behaviour changed
	// (a later comparison of what the checks did before and after). It is
	// accepted and shown, so a newer hook's reason is never dropped.
	ReasonToolBehaviour = "tool_behaviour"
)

// Reason limits: how many a request carries and the daemon keeps, how many
// a message lists before "and K more", and the longest a text field gets.
const (
	MaxLaneReasons      = 20
	MaxLaneReasonsShown = 8
	maxReasonFieldBytes = 200
)

// LaneReason is one concrete reason a change is in its lane.
type LaneReason struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Lane    string `json:"lane,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message,omitempty"`
	// Text is the added line a finding matched (trimmed, redacted,
	// clipped). It is a hunk line, so it needs operator scope: String and
	// Markdown never include it (they reach the tracker comment, Slack and
	// viewer clients), and the server withholds it below operator.
	Text string `json:"text,omitempty"`
}

// IsReasonKind reports whether kind is a reason kind the daemon shows.
func IsReasonKind(kind string) bool {
	return kind == ReasonFinding || kind == ReasonLaneRule || kind == ReasonToolBehaviour
}

// Location is "path" or "path:line".
func (r LaneReason) Location() string {
	if r.Line > 0 {
		return r.Path + ":" + strconv.Itoa(r.Line)
	}
	return r.Path
}

// String is the reason as one line of plain text: location, rule message
// and rule id, never the added line itself (Text needs operator scope).
func (r LaneReason) String() string {
	loc := r.Location()
	switch r.Kind {
	case ReasonFinding:
		if r.Rule != "" {
			return loc + ": " + r.Message + " [" + r.Rule + "]"
		}
		return loc + ": " + r.Message
	case ReasonLaneRule:
		if r.Rule == "" {
			return loc + ": " + r.Message
		}
		return loc + " matched " + r.Rule + " → " + r.Lane
	}
	return loc + ": " + r.Message
}

// Markdown is String for a Markdown reader (the tracker comment): the
// location and the rule in code spans (a reason field never holds a
// backtick, cleanReasonField turns them into quotes) and the message
// escaped. Never the added line itself.
func (r LaneReason) Markdown() string {
	loc := "`" + r.Location() + "`"
	switch r.Kind {
	case ReasonFinding:
		if r.Rule != "" {
			return loc + ": " + EscapeMarkdown(r.Message) + " (`" + r.Rule + "`)"
		}
		return loc + ": " + EscapeMarkdown(r.Message)
	case ReasonLaneRule:
		if r.Rule == "" {
			return loc + ": " + EscapeMarkdown(r.Message)
		}
		return loc + " matched `" + r.Rule + "` → " + EscapeMarkdown(r.Lane)
	}
	return loc + ": " + EscapeMarkdown(r.Message)
}

// markdownEscaper escapes the characters Markdown (CommonMark, GitHub and
// Linear comments) gives a meaning inside a line: emphasis, code, links,
// HTML, tables, strike-through, mentions. A reason never starts a line, so
// block markers ("#", "-", "1.") need nothing.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", `\<`, ">", `\>`, "|", `\|`, "~", `\~`, "@", `\@`)

// EscapeMarkdown escapes s so a Markdown renderer shows it as typed (no
// emphasis, links, HTML, mentions or headings).
func EscapeMarkdown(s string) string { return markdownEscaper.Replace(s) }

// LaneReasons builds the reasons for a classified push: the findings first
// (they name a line), then one lane-rule reason per path above auto, human
// before second_opinion. findings may be cut short: findingsTotal is how
// many there were in all (at least len(findings)). Only the first
// MaxLaneReasons reasons are built and returned; total is how many there
// were. Sanitized (see SanitizeLaneReasons).
func LaneReasons(r LaneResult, findings []AuditFinding, findingsTotal int) ([]LaneReason, int) {
	total := max(findingsTotal, len(findings)) + len(r.Human) + len(r.SecondOpinion)
	out := make([]LaneReason, 0, min(total, MaxLaneReasons))
	for _, f := range findings[:min(len(findings), MaxLaneReasons)] {
		out = append(out, FindingReason(f))
	}
	for _, lane := range []struct {
		name  string
		paths []string
	}{{LaneHuman, r.Human}, {LaneSecondOpinion, r.SecondOpinion}} {
		for _, p := range lane.paths[:min(len(lane.paths), max(MaxLaneReasons-len(out), 0))] {
			rule := r.Rules[p]
			reason := LaneReason{Kind: ReasonLaneRule, Path: p, Lane: lane.name, Rule: rule}
			if rule == "" { // the stand-in of a push whose files could not be listed
				reason.Message = "held: the changed files could not be listed"
			}
			out = append(out, reason)
		}
	}
	return SanitizeLaneReasons(out, total)
}

// FindingReason is the reason an audit finding gives.
func FindingReason(f AuditFinding) LaneReason {
	return LaneReason{Kind: ReasonFinding, Path: f.Path, Line: f.Line, Rule: f.Rule,
		Message: f.Message, Text: strings.TrimSpace(f.Text)}
}

// SanitizeLaneReasons bounds and redacts reasons from any source (the hook
// runs in the agent's environment, so the daemon sanitizes again): unknown
// kinds and empty paths are dropped, every text field is redacted and
// clipped, and at most MaxLaneReasons are kept. total is how many there
// were (at least the number kept).
func SanitizeLaneReasons(in []LaneReason, total int) ([]LaneReason, int) {
	out := make([]LaneReason, 0, min(len(in), MaxLaneReasons))
	for _, r := range in {
		if len(out) == MaxLaneReasons {
			break
		}
		if !IsReasonKind(r.Kind) {
			continue
		}
		r.Path = cleanReasonField(r.Path)
		if r.Path == "" {
			continue
		}
		r.Line = max(r.Line, 0)
		if r.Lane != "" && r.Lane != LaneHuman && r.Lane != LaneSecondOpinion && r.Lane != LaneAuto {
			r.Lane = LaneHuman // an unknown lane ranks as human (LaneRank)
		}
		r.Rule, r.Message, r.Text = cleanReasonField(r.Rule), cleanReasonField(r.Message), cleanReasonField(r.Text)
		out = append(out, r)
	}
	return out, max(total, len(out))
}

// cleanReasonField redacts secrets, flattens control characters (C0, DEL
// and C1) and clips one text field at a rune boundary.
func cleanReasonField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.ReplaceAll(logging.RedactString(s), "`", "'")
	if len(s) <= maxReasonFieldBytes {
		return s
	}
	cut := maxReasonFieldBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// LaneReasonLines are up to n reasons as text lines, with an "and K more"
// line when total is larger.
func LaneReasonLines(reasons []LaneReason, total, n int) []string {
	shown := reasons[:min(len(reasons), max(n, 0))]
	lines := make([]string, 0, len(shown)+1)
	for _, r := range shown {
		lines = append(lines, r.String())
	}
	if more := max(total, len(reasons)) - len(shown); more > 0 {
		lines = append(lines, "and "+strconv.Itoa(more)+" more")
	}
	return lines
}

// findingKey identifies a finding for de-duplication.
type findingKey struct {
	rule, path string
	line       int
}

// findingSet collects findings each (rule, path, line) once, in order:
// the first limit are kept, and total counts every distinct one. Linear in
// the findings it is given (a map, never a scan of what it kept).
type findingSet struct {
	seen  map[findingKey]struct{}
	kept  []AuditFinding
	limit int
	total int
}

func newFindingSet(limit int) *findingSet {
	return &findingSet{seen: map[findingKey]struct{}{}, limit: limit}
}

// add adds the findings on paths (all of them when paths is nil).
func (s *findingSet) add(findings []AuditFinding, paths map[string]struct{}) {
	for _, f := range findings {
		if paths != nil {
			if _, ok := paths[f.Path]; !ok {
				continue
			}
		}
		k := findingKey{f.Rule, f.Path, f.Line}
		if _, dup := s.seen[k]; dup {
			continue
		}
		s.seen[k] = struct{}{}
		s.total++
		if len(s.kept) < s.limit {
			s.kept = append(s.kept, f)
		}
	}
}

// pathSet is paths as a set.
func pathSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		set[p] = struct{}{}
	}
	return set
}
