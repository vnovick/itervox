package agent

import (
	"regexp"
	"strings"
)

var (
	choiceListLinePattern = regexp.MustCompile(`(?m)^\s*(?:\d+[.)]|[-*])\s+\S`)
)

// Cue strength (CORE-136/158/159). Every cue is a substring match, so a cue
// that is ordinary prose in a finished run's summary ("got approval and
// merged", "the handler will now respond with a 404", "ran the suite before
// proceeding") would halt a successful run if it scored the full two points
// on its own. Only direct asks — second-person requests and first-person
// "I am blocked on you" statements — are strong (+2). Bare cues are weak
// (+1) and need a second signal (a trailing question, reply options, another
// weak cue). Imperative reply instructions ("Reply with ...", `Type "x"`,
// "Pick one") are strong only where they open a sentence, which is how they
// read as an instruction to the human; mid-sentence they are descriptive
// ("changed the field type 'int'") and weak.

// blockingPromptCues are direct asks for the human to choose the next action.
var blockingPromptCues = []string{
	"what would you like",
	"how would you like",
	"what should i do",
	"should i ",
	"do you want me to",
	"would you like me to",
}

// weakPromptCues describe a choice without asking for one ("documented which
// one of the two paths runs first").
var weakPromptCues = []string{
	"which option",
	"which one",
	"which path",
}

// imperativePromptCues ask the human to choose when they open a sentence
// ("Pick one:") and are descriptive otherwise ("lets users choose one repo").
var imperativePromptCues = []string{
	"pick one",
	"choose one",
	"select one",
	"select an option",
}

// confirmationCues are direct requests for confirmation or approval.
var confirmationCues = []string{
	"please confirm",
	"can you confirm",
	"could you confirm",
	"awaiting confirmation",
	"i need approval",
	"please approve",
}

// imperativeConfirmationCues tell the human how to reply when they open a
// sentence (`Type "discard" to confirm.`, "Reply with yes or no.") and are
// descriptive otherwise ("the handler will now respond with a 404").
var imperativeConfirmationCues = []string{
	"reply with",
	"respond with",
	"type '",
	`type "`,
	"send the word",
	"approve this",
}

// weakConfirmationCues: bare "confirm" and "approval" are ordinary prose in a
// finished run's summary ("I confirmed all tests pass", "got approval and
// merged the PR", "the deploy job now requires approval") and match every
// inflection and compound ("confirmation required", "needs approval"). They
// score one point, so a second signal is needed; direct requests ("please
// confirm", "i need approval", `type "x" to confirm`, "need your approval")
// stay strong (CORE-158, CORE-159).
var weakConfirmationCues = []string{
	"confirm",
	"approval",
}

// blockingContinuationCues are first-person statements that the agent is
// blocked on the human.
var blockingContinuationCues = []string{
	"need your input",
	"need your decision",
	"need your confirmation",
	"need your approval",
	"need your permission",
	"i need permission",
	"waiting for your",
	"blocked on your",
	"go-ahead to continue",
	"go-ahead to proceed",
	"reply to continue",
	"reply to proceed",
	"respond to continue",
	"respond to proceed",
}

// weakContinuationCues are continuation words that are ordinary prose in a
// finished run's summary ("so the worker is able to continue", "ran the suite
// before proceeding", "stubbed the tracker so I can continue testing") and
// only suggest a blocking prompt. They score one point, so on their own they
// never cross the NeedsInput threshold; a second signal (a trailing question,
// reply options) is required (CORE-136). "before i proceed" also matched
// "before I proceeded".
var weakContinuationCues = []string{
	"to continue",
	"to proceed",
	"before proceeding",
	"before i continue",
	"before i proceed",
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

	if containsAny(lower, blockingPromptCues) || containsAtSentenceStart(lower, imperativePromptCues) {
		score += 2
		reasons = append(reasons, "asks the human to choose the next action")
	} else if containsAny(lower, weakPromptCues) || containsAny(lower, imperativePromptCues) {
		score++
		reasons = append(reasons, "mentions a choice")
	}
	if containsAny(lower, confirmationCues) || containsAtSentenceStart(lower, imperativeConfirmationCues) {
		score += 2
		reasons = append(reasons, "asks for explicit confirmation or approval")
	} else if containsAny(lower, weakConfirmationCues) || containsAny(lower, imperativeConfirmationCues) {
		score++
		reasons = append(reasons, "mentions confirmation or approval")
	}
	if containsAny(lower, blockingContinuationCues) {
		score += 2
		reasons = append(reasons, "states that the agent is waiting before it can continue")
	} else if containsAny(lower, weakContinuationCues) {
		score++
		reasons = append(reasons, "mentions continuing or proceeding")
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
		containsAny(lower, weakPromptCues) ||
		containsAny(lower, imperativePromptCues) ||
		containsAny(lower, confirmationCues) ||
		containsAny(lower, imperativeConfirmationCues) ||
		containsAny(lower, weakConfirmationCues) ||
		containsAny(lower, blockingContinuationCues) ||
		containsAny(lower, weakContinuationCues) ||
		endsWithQuestion(original)
}

func isChoiceListParagraph(paragraph string) bool {
	return choiceListLinePattern.MatchString(paragraph)
}

func isPromptLeadIn(paragraph string) bool {
	lower := normalizeDetectorText(paragraph)
	return containsAny(lower, blockingPromptCues) ||
		containsAtSentenceStart(lower, imperativePromptCues) ||
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

// containsAtSentenceStart reports whether any cue occurs where a sentence
// begins: at the start of text, or after sentence punctuation or a line
// break, skipping spaces and markdown/quote openers. That is where an
// imperative ("Reply with ...", `Type "x"`) addresses the reader.
func containsAtSentenceStart(text string, cues []string) bool {
	for _, cue := range cues {
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], cue)
			if i < 0 {
				break
			}
			at := from + i
			if sentenceStartsAt(text, at) {
				return true
			}
			from = at + 1
		}
	}
	return false
}

func sentenceStartsAt(text string, at int) bool {
	before := strings.TrimRight(text[:at], " \t*_>`\"'(-")
	if before == "" {
		return true
	}
	switch before[len(before)-1] {
	case '.', '!', '?', ':', ';', '\n':
		return true
	}
	return false
}
