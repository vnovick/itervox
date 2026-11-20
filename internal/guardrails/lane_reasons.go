package guardrails

import (
	"slices"
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
	// Text is the added line a finding matched (trimmed, redacted, clipped).
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

// String is the reason as one line of text.
func (r LaneReason) String() string {
	loc := r.Location()
	switch r.Kind {
	case ReasonFinding:
		if r.Text != "" {
			return loc + " adds `" + r.Text + "` (" + r.Message + ")"
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

// LaneReasons builds the reasons for a classified push: the findings first
// (they name a line), then one lane-rule reason per path above auto, human
// before second_opinion. At most MaxLaneReasons are returned; total is how
// many there were. Sanitized (see SanitizeLaneReasons).
func LaneReasons(r LaneResult, findings []AuditFinding) ([]LaneReason, int) {
	out := make([]LaneReason, 0, len(findings)+len(r.Human)+len(r.SecondOpinion))
	for _, f := range findings {
		out = append(out, FindingReason(f))
	}
	for _, lane := range []struct {
		name  string
		paths []string
	}{{LaneHuman, r.Human}, {LaneSecondOpinion, r.SecondOpinion}} {
		for _, p := range lane.paths {
			rule := r.Rules[p]
			reason := LaneReason{Kind: ReasonLaneRule, Path: p, Lane: lane.name, Rule: rule}
			if rule == "" { // the stand-in of a push whose files could not be listed
				reason.Message = "held: the changed files could not be listed"
			}
			out = append(out, reason)
		}
	}
	return SanitizeLaneReasons(out, len(out))
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

// cleanReasonField redacts secrets, flattens control characters and clips
// one text field at a rune boundary.
func cleanReasonField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
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

// filterFindings keeps the findings on paths (all of them when paths is
// nil), each (rule, path, line) once.
func filterFindings(findings []AuditFinding, paths []string) []AuditFinding {
	out := findings[:0:0]
	for _, f := range findings {
		if paths != nil && !slices.Contains(paths, f.Path) {
			continue
		}
		if slices.ContainsFunc(out, func(o AuditFinding) bool { return o.Rule == f.Rule && o.Path == f.Path && o.Line == f.Line }) {
			continue
		}
		out = append(out, f)
	}
	return out
}
