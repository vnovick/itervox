package github

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// blockerPhraseRe matches the phrase set the spec (docs/superpowers/specs/
// 2026-08-05-tracker-edge-widening-design.md, "GitHub body patterns")
// recognizes as introducing a hard blocker reference list.
var blockerPhraseRe = regexp.MustCompile(`(?i)\b(?:blocked\s+(?:by|on)|depends\s+(?:on|upon)|requires|waiting\s+(?:on|for))\b`)

// blockerRefRe matches a same-repo issue reference ("#N") anchored at the
// start of the remaining text. owner/repo#N forms never match this regex
// because they do not start with "#" — see blockerCrossRepoRe, which
// recognizes and skips them instead.
var blockerRefRe = regexp.MustCompile(`^#(\d+)\b`)

// blockerCrossRepoRe matches a cross-repo issue reference ("owner/repo#N")
// anchored at the start of the remaining text. Per the widened grammar
// (Task 3, item 2 of the wave-2 polish plan / gh issue #53 deferral
// "Cross-repo ref mid-list silently terminates the list"), a cross-repo
// token mid-list is consumed and skipped — the list continues past it —
// rather than terminating the list the way an unrecognized token does.
var blockerCrossRepoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+#\d+\b`)

// blockerSepUnitRe matches a single separator unit between references in a
// reference list: whitespace (including newlines), a comma, an ampersand, or
// the word "and".
var blockerSepUnitRe = regexp.MustCompile(`(?i)^(?:\s+|,|&|and\b)`)

// blockerBulletRe matches a Markdown bullet-list marker introducing the next
// reference on a new line: an optional indent, then "-" or "*", then
// required horizontal whitespace. Part of the widened colon-form grammar
// (Task 3, item 1): "Depends on:\n- #3\n- #4" continues the reference list
// across bullet-shaped lines.
var blockerBulletRe = regexp.MustCompile(`^[ \t]*[-*][ \t]+`)

// normalizeIssue converts a raw GitHub REST API issue map to a domain.Issue.
// derivedState is the computed state string (from label/closed logic).
// Returns nil if required fields are missing.
func normalizeIssue(raw map[string]any, derivedState string) *domain.Issue {
	numberRaw, ok := raw["number"]
	if !ok {
		return nil
	}
	number, ok := tracker.ToIntVal(numberRaw)
	if !ok {
		return nil
	}
	title, _ := raw["title"].(string)
	if title == "" {
		return nil
	}

	id := strconv.Itoa(number)
	identifier := fmt.Sprintf("#%d", number)

	issue := &domain.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      title,
		State:      derivedState,
		Labels:     extractLabels(raw),
		BlockedBy:  extractBlockers(raw),
		CreatedAt:  tracker.ParseTime(raw["created_at"]),
		UpdatedAt:  tracker.ParseTime(raw["updated_at"]),
	}

	if body, ok := raw["body"].(string); ok && body != "" {
		issue.Description = &body
	}
	if htmlURL, ok := raw["html_url"].(string); ok && htmlURL != "" {
		issue.URL = &htmlURL
	}
	// Priority: map p0–p3 labels to integers 0–3; nil otherwise
	if prio := priorityFromLabels(issue.Labels); prio >= 0 {
		issue.Priority = &prio
	}
	// branch_name: always nil for GitHub
	issue.BranchName = nil

	return issue
}

func extractLabels(raw map[string]any) []string {
	labelsRaw, ok := raw["labels"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(labelsRaw))
	for _, l := range labelsRaw {
		label, ok := l.(map[string]any)
		if !ok {
			continue
		}
		name, ok := label["name"].(string)
		if !ok || name == "" {
			continue
		}
		result = append(result, strings.ToLower(name))
	}
	return result
}

// extractBlockers scans the issue body for hard blocker phrases (see
// blockerPhraseRe) and, for each phrase match, walks the following
// reference list — "#N" tokens separated by whitespace, commas, "and", or
// "&". Deliberate acceptance (per spec): casual phrasing like "requires #5
// to be reviewed" WILL match; this is documented behavior, not a bug. Bare
// "#N" with no preceding phrase is never matched. Refs are deduped by
// number across all phrase matches, and self-references (the issue's own
// number) are dropped.
//
// Widened grammar (Task 3 of the wave-2 polish plan / gh issue #53
// deferrals):
//
//   - Colon form: the phrase may be followed by an optional ":" before the
//     reference list, e.g. "Depends on: #3".
//   - Multi-line bullet lists: after the phrase (with or without a colon),
//     the list may continue across a single newline onto a bullet-shaped
//     line ("- #N" / "* #N", optionally indented), e.g.
//     "Depends on:\n- #3\n- #4". A blank line (two or more consecutive
//     newlines) stops the list — prose resuming after a blank line is not
//     part of the reference list, and neither is a non-bullet, non-ref line.
//   - Cross-repo mid-list skip: an "owner/repo#N" token (see
//     blockerCrossRepoRe) is consumed and skipped rather than terminating
//     the list, e.g. "depends on #3, foo/bar#4, #5" yields #3 and #5. A
//     cross-repo token with nothing recognizable after it still ends the
//     list with whatever refs were already found (same as any other
//     unrecognized trailing token).
//
// Any other token that is neither a separator, a "#N" reference, nor a
// cross-repo reference stops the phrase's reference list.
func extractBlockers(raw map[string]any) []domain.BlockerRef {
	body, ok := raw["body"].(string)
	if !ok || body == "" {
		return nil
	}

	selfNum := ""
	if numberRaw, ok := raw["number"]; ok {
		if n, ok := tracker.ToIntVal(numberRaw); ok {
			selfNum = strconv.Itoa(n)
		}
	}

	seen := make(map[string]bool)
	var result []domain.BlockerRef

	for _, loc := range blockerPhraseRe.FindAllStringIndex(body, -1) {
		pos := loc[1]
		// Optional colon directly after the phrase, e.g. "Depends on: #3".
		if pos < len(body) && body[pos] == ':' {
			pos++
		}
		for pos < len(body) {
			newPos, ok := consumeBlockerListSeparator(body, pos)
			if !ok {
				// A blank line inside the separator run ends the list.
				break
			}
			pos = newPos

			if refMatch := blockerRefRe.FindStringSubmatch(body[pos:]); refMatch != nil {
				num := refMatch[1]
				pos += len(refMatch[0])
				if num == selfNum || seen[num] {
					continue
				}
				seen[num] = true
				id := num
				ident := "#" + num
				result = append(result, domain.BlockerRef{
					ID:         &id,
					Identifier: &ident,
				})
				continue
			}

			if crossMatch := blockerCrossRepoRe.FindString(body[pos:]); crossMatch != "" {
				// owner/repo#N: consumed and skipped, list continues past it.
				pos += len(crossMatch)
				continue
			}

			// Next token is neither a same-repo ref nor a cross-repo ref —
			// stop this phrase's reference list.
			break
		}
	}

	return result
}

// consumeBlockerListSeparator advances pos across a run of list separators
// (whitespace, comma, "&", "and") allowing at most one newline, optionally
// followed by a Markdown bullet marker ("- " / "* ", optionally indented).
// Returns ok=false (and the original pos) when the run crosses a blank line
// — two or more consecutive newlines — which ends the reference list per the
// widened colon-form grammar (see extractBlockers).
func consumeBlockerListSeparator(body string, pos int) (int, bool) {
	start := pos
	newlines := 0
	for pos < len(body) {
		if sepLoc := blockerSepUnitRe.FindStringIndex(body[pos:]); sepLoc != nil {
			unit := body[pos : pos+sepLoc[1]]
			newlines += strings.Count(unit, "\n")
			if newlines >= 2 {
				return start, false
			}
			pos += sepLoc[1]
			continue
		}
		if bulletLoc := blockerBulletRe.FindStringIndex(body[pos:]); bulletLoc != nil {
			pos += bulletLoc[1]
			continue
		}
		break
	}
	return pos, true
}

func priorityFromLabels(labels []string) int {
	for _, l := range labels {
		switch l {
		case "p0":
			return 0
		case "p1":
			return 1
		case "p2":
			return 2
		case "p3":
			return 3
		}
	}
	return -1
}

// deriveState computes the Itervox state string for a GitHub issue.
// Closed issues: prefer a matching terminal label if present, otherwise return
// the first configured terminal state (so the reconciler treats it as terminal
// regardless of which label the user applied or whether they applied one at all).
// Open issues: first matching active or terminal label wins.
// Open issues with no matching label return "" (not eligible).
func deriveState(raw map[string]any, activeStates, terminalStates []string) string {
	ghState, _ := raw["state"].(string)
	labels := extractLabels(raw)
	if strings.ToLower(ghState) == "closed" {
		// Prefer a terminal label if the user applied one (e.g. "done", "cancelled").
		for _, label := range labels {
			for _, terminal := range terminalStates {
				if strings.EqualFold(label, terminal) {
					return terminal
				}
			}
		}
		// No matching terminal label — fall back to the first configured terminal
		// state so the reconciler still recognises this as a terminal event.
		if len(terminalStates) > 0 {
			return terminalStates[0]
		}
		return "closed"
	}
	// Check active labels first
	for _, label := range labels {
		for _, active := range activeStates {
			if strings.EqualFold(label, active) {
				return active
			}
		}
	}
	// Check terminal labels
	for _, label := range labels {
		for _, terminal := range terminalStates {
			if strings.EqualFold(label, terminal) {
				return terminal
			}
		}
	}
	return ""
}
