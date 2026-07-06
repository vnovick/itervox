package agent

import (
	"regexp"
	"strings"
)

var (
	choiceListLinePattern = regexp.MustCompile(`(?m)^\s*(?:\d+[.)]|[-*])\s+\S`)
)

var blockingPromptCues = []string{
	"what would you like",
	"how would you like",
	"which option",
	"which one",
	"which path",
	"what should i do",
	"should i ",
	"do you want me to",
	"would you like me to",
	"pick one",
	"choose one",
	"select one",
	"select an option",
}

var confirmationCues = []string{
	"confirm",
	"confirmation",
	"approval",
	"approval required",
	"requires approval",
	"need approval",
	"needs approval",
	"please approve",
	"approve this",
	"reply with",
	"respond with",
	"type '",
	`type "`,
	"send the word",
}

var blockingContinuationCues = []string{
	"need your input",
	"need your decision",
	"need your confirmation",
	"need your approval",
	"waiting for your",
	"blocked on your",
	"before i continue",
	"before proceeding",
	"before i proceed",
	"to continue",
	"to proceed",
	"so i can continue",
	"so i can proceed",
}

var nonBlockingClosers = []string{
	"anything else",
	"does that help",
	"can i help with anything else",
	"want a summary",
	"want me to explain",
	"need anything else",
}

// InputRequiredDecision is the fallback detector's verdict about whether an
// otherwise-successful assistant message is blocked on a human reply.
type InputRequiredDecision struct {
	NeedsInput bool
	Question   string
	Reason     string
}

// DetectInputRequiredFallback inspects the assistant's final output and returns
// whether the turn is blocked waiting for a human decision, confirmation, or
// missing information. This is the deterministic fallback for successful turns
// that did not emit an explicit input-required signal or sentinel.
func DetectInputRequiredFallback(assistantOutput string) InputRequiredDecision {
	text := strings.TrimSpace(stripSentinel(assistantOutput))
	if text == "" {
		return InputRequiredDecision{}
	}

	candidate := extractBlockingCandidate(text)
	if candidate == "" {
		return InputRequiredDecision{}
	}

	lower := normalizeDetectorText(candidate)
	if containsAny(lower, nonBlockingClosers) {
		return InputRequiredDecision{}
	}

	score := 0
	var reasons []string

	if containsAny(lower, blockingPromptCues) {
		score += 2
		reasons = append(reasons, "asks the human to choose the next action")
	}
	if containsAny(lower, confirmationCues) {
		score += 2
		reasons = append(reasons, "asks for explicit confirmation or approval")
	}
	if containsAny(lower, blockingContinuationCues) {
		score += 2
		reasons = append(reasons, "states that the agent is waiting before it can continue")
	}
	if choiceListLinePattern.MatchString(candidate) {
		score++
		reasons = append(reasons, "includes reply options")
	}
	if endsWithQuestion(candidate) {
		score++
		reasons = append(reasons, "ends with a direct human-facing question")
	}

	if score < 2 {
		return InputRequiredDecision{}
	}

	return InputRequiredDecision{
		NeedsInput: true,
		Question:   strings.TrimSpace(candidate),
		Reason:     strings.Join(reasons, "; "),
	}
}

func stripSentinel(text string) string {
	return strings.ReplaceAll(text, InputRequiredSentinel, "")
}

func normalizeDetectorText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(strings.ToLower(text))
	return text
}

func extractBlockingCandidate(text string) string {
	paragraphs := splitDetectorParagraphs(text)
	if len(paragraphs) == 0 {
		return ""
	}

	last := paragraphs[len(paragraphs)-1]
	candidate := []string{last}
	candidateLower := normalizeDetectorText(last)
	if !isLikelyBlockingTail(candidateLower, last) {
		return ""
	}

	for i := len(paragraphs) - 2; i >= 0 && len(candidate) < 3; i-- {
		para := paragraphs[i]
		if para == "" {
			continue
		}
		if isChoiceListParagraph(para) || isPromptLeadIn(para) {
			candidate = append([]string{para}, candidate...)
			continue
		}
		break
	}

	return strings.Join(candidate, "\n\n")
}

func splitDetectorParagraphs(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	paragraphs := make([]string, 0, len(raw))
	for _, para := range raw {
		trimmed := strings.TrimSpace(para)
		if trimmed == "" {
			continue
		}
		paragraphs = append(paragraphs, trimmed)
	}
	return paragraphs
}

func isLikelyBlockingTail(lower, original string) bool {
	return containsAny(lower, blockingPromptCues) ||
		containsAny(lower, confirmationCues) ||
		containsAny(lower, blockingContinuationCues) ||
		endsWithQuestion(original)
}

func isChoiceListParagraph(paragraph string) bool {
	return choiceListLinePattern.MatchString(paragraph)
}

func isPromptLeadIn(paragraph string) bool {
	lower := normalizeDetectorText(paragraph)
	return containsAny(lower, blockingPromptCues) ||
		containsAny(lower, blockingContinuationCues)
}

func endsWithQuestion(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return strings.HasSuffix(line, "?")
	}
	return false
}

func containsAny(text string, cues []string) bool {
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}
