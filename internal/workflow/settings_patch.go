package workflow

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Mutator transforms the YAML front-matter lines of a WORKFLOW.md file. It
// must be a pure function of its input: no I/O, no shared state. Mutators
// compose — ApplyAndWriteFrontMatter runs them in sequence, threading the
// output of one as the input of the next, and writes the result once.
type Mutator func(frontLines []string) ([]string, error)

// editMu serializes concurrent edits to the same WORKFLOW.md path. Without
// this, two HTTP handler goroutines hitting the same file (e.g. the user
// editing automations in one tab while another tab edits profiles) could
// read the same starting bytes and have one write clobber the other's
// changes after rename. Keyed by absolute path; unbounded growth is bounded
// by "number of WORKFLOW.md files this daemon has ever touched", which is
// effectively 1.
var editMu sync.Map // path -> *sync.Mutex

func lockForPath(path string) func() {
	muIface, _ := editMu.LoadOrStore(path, &sync.Mutex{})
	mu := muIface.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ApplyAndWriteFrontMatter reads the file at path, splits it into front
// matter and body, runs each mutator in sequence on the front-matter lines,
// reassembles the file, and writes it back atomically. If any mutator
// returns an error, the file is left untouched.
//
// Concurrent calls for the same path are serialized via editMu so that
// multi-tab editors cannot lose writes to read-modify-write races.
func ApplyAndWriteFrontMatter(path string, mutators ...Mutator) error {
	if len(mutators) == 0 {
		return nil
	}
	unlock := lockForPath(path)
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow apply: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow apply: no front matter in %s", path)
	}

	for i, m := range mutators {
		next, err := m(frontLines)
		if err != nil {
			return fmt.Errorf("workflow apply: mutator %d: %w", i, err)
		}
		frontLines = next
	}

	return writeFrontMatter(path, frontLines, bodyLines)
}

// PatchReviewerConfig atomically rewrites agent.reviewer_profile and
// agent.auto_review inside the YAML front matter. Empty profile removes the
// reviewer_profile key. autoReview=false removes auto_review.
func PatchReviewerConfig(path, profile string, autoReview bool) error {
	return ApplyAndWriteFrontMatter(path, MutateReviewerConfig(profile, autoReview))
}

// MutateReviewerConfig returns a Mutator that rewrites the reviewer_profile
// and auto_review keys inside the agent: block. See PatchReviewerConfig.
func MutateReviewerConfig(profile string, autoReview bool) Mutator {
	return func(frontLines []string) ([]string, error) {
		agentLine := -1
		agentEnd := len(frontLines)
		for i, line := range frontLines {
			if line != "agent:" {
				continue
			}
			agentLine = i
			for j := i + 1; j < len(frontLines); j++ {
				next := frontLines[j]
				if next == "" {
					continue
				}
				if next[0] != ' ' {
					agentEnd = j
					break
				}
			}
			break
		}
		if agentLine < 0 {
			return nil, fmt.Errorf("agent block not found")
		}

		block := make([]string, 0, agentEnd-agentLine-1+2)
		for _, line := range frontLines[agentLine+1 : agentEnd] {
			if strings.HasPrefix(line, "  reviewer_profile:") || strings.HasPrefix(line, "  auto_review:") {
				continue
			}
			block = append(block, line)
		}
		if profile != "" {
			block = append(block, "  reviewer_profile: "+strconv.Quote(profile))
		}
		if autoReview {
			block = append(block, "  auto_review: true")
		}

		newFrontLines := make([]string, 0, len(frontLines)-((agentEnd-agentLine-1)-len(block)))
		newFrontLines = append(newFrontLines, frontLines[:agentLine+1]...)
		newFrontLines = append(newFrontLines, block...)
		newFrontLines = append(newFrontLines, frontLines[agentEnd:]...)
		return newFrontLines, nil
	}
}

// PatchTrackerStates atomically rewrites tracker.active_states,
// tracker.terminal_states, and tracker.completion_state inside the YAML front
// matter. Missing keys are inserted inside the tracker block.
func PatchTrackerStates(path string, active, terminal []string, completion string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow patch tracker states: read %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	frontLines, bodyLines := splitFrontMatter(content)
	if frontLines == nil {
		return fmt.Errorf("workflow patch tracker states: no front matter in %s", path)
	}

	trackerLine := -1
	trackerEnd := len(frontLines)
	for i, line := range frontLines {
		if line != "tracker:" {
			continue
		}
		trackerLine = i
		for j := i + 1; j < len(frontLines); j++ {
			next := frontLines[j]
			if next == "" {
				continue
			}
			if next[0] != ' ' {
				trackerEnd = j
				break
			}
		}
		break
	}
	if trackerLine < 0 {
		return fmt.Errorf("workflow patch tracker states: tracker block not found in %s", path)
	}

	block := make([]string, 0, trackerEnd-trackerLine-1+3)
	for _, line := range frontLines[trackerLine+1 : trackerEnd] {
		switch {
		case strings.HasPrefix(line, "  active_states:"):
			continue
		case strings.HasPrefix(line, "  terminal_states:"):
			continue
		case strings.HasPrefix(line, "  completion_state:"):
			continue
		default:
			block = append(block, line)
		}
	}
	block = append(block,
		"  active_states: "+marshalStringSliceInline(active),
		"  terminal_states: "+marshalStringSliceInline(terminal),
		"  completion_state: "+strconv.Quote(completion),
	)

	newFrontLines := make([]string, 0, len(frontLines)-((trackerEnd-trackerLine-1)-len(block)))
	newFrontLines = append(newFrontLines, frontLines[:trackerLine+1]...)
	newFrontLines = append(newFrontLines, block...)
	newFrontLines = append(newFrontLines, frontLines[trackerEnd:]...)
	return writeFrontMatter(path, newFrontLines, bodyLines)
}

// PatchAgentMaxRetries atomically rewrites agent.max_retries in the YAML front
// matter. The value is always written even if it equals the parser default —
// operators may have intentionally pinned the default and a settings PUT
// should be self-evident in the file. The agent block must exist; this is
// guaranteed by config.Load() which fails earlier if it does not.
func PatchAgentMaxRetries(path string, n int) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentIntField("max_retries", n))
}

// MutateAgentIntField returns a Mutator that sets a single integer key inside
// the agent: block. Existing occurrences are replaced; if the key is missing
// it is appended. This is the int-shaped sibling of MutateReviewerConfig.
func MutateAgentIntField(key string, value int) Mutator {
	prefix := "  " + key + ":"
	return func(frontLines []string) ([]string, error) {
		agentLine := -1
		agentEnd := len(frontLines)
		for i, line := range frontLines {
			if line != "agent:" {
				continue
			}
			agentLine = i
			for j := i + 1; j < len(frontLines); j++ {
				next := frontLines[j]
				if next == "" {
					continue
				}
				if next[0] != ' ' {
					agentEnd = j
					break
				}
			}
			break
		}
		if agentLine < 0 {
			return nil, fmt.Errorf("agent block not found")
		}

		block := make([]string, 0, agentEnd-agentLine)
		replaced := false
		for _, line := range frontLines[agentLine+1 : agentEnd] {
			if strings.HasPrefix(line, prefix) {
				block = append(block, "  "+key+": "+strconv.Itoa(value))
				replaced = true
				continue
			}
			block = append(block, line)
		}
		if !replaced {
			block = append(block, "  "+key+": "+strconv.Itoa(value))
		}

		newFrontLines := make([]string, 0, len(frontLines)-((agentEnd-agentLine-1)-len(block)))
		newFrontLines = append(newFrontLines, frontLines[:agentLine+1]...)
		newFrontLines = append(newFrontLines, block...)
		newFrontLines = append(newFrontLines, frontLines[agentEnd:]...)
		return newFrontLines, nil
	}
}

// PatchTrackerFailedState atomically rewrites tracker.failed_state in the YAML
// front matter. Empty value removes the key entirely (operator chose
// "Pause (do not move)" in the UI). Existing occurrences are replaced.
func PatchTrackerFailedState(path, state string) error {
	return ApplyAndWriteFrontMatter(path, MutateTrackerStringField("failed_state", state))
}

// MutateTrackerStringField returns a Mutator that sets or removes a single
// string key inside the tracker: block. Empty value removes; non-empty
// value writes the quoted value. Existing occurrences are stripped first.
func MutateTrackerStringField(key, value string) Mutator {
	prefix := "  " + key + ":"
	return func(frontLines []string) ([]string, error) {
		trackerLine := -1
		trackerEnd := len(frontLines)
		for i, line := range frontLines {
			if line != "tracker:" {
				continue
			}
			trackerLine = i
			for j := i + 1; j < len(frontLines); j++ {
				next := frontLines[j]
				if next == "" {
					continue
				}
				if next[0] != ' ' {
					trackerEnd = j
					break
				}
			}
			break
		}
		if trackerLine < 0 {
			return nil, fmt.Errorf("tracker block not found")
		}

		block := make([]string, 0, trackerEnd-trackerLine)
		for _, line := range frontLines[trackerLine+1 : trackerEnd] {
			if strings.HasPrefix(line, prefix) {
				continue
			}
			block = append(block, line)
		}
		if value != "" {
			block = append(block, "  "+key+": "+strconv.Quote(value))
		}

		newFrontLines := make([]string, 0, len(frontLines)-((trackerEnd-trackerLine-1)-len(block)))
		newFrontLines = append(newFrontLines, frontLines[:trackerLine+1]...)
		newFrontLines = append(newFrontLines, block...)
		newFrontLines = append(newFrontLines, frontLines[trackerEnd:]...)
		return newFrontLines, nil
	}
}

func writeFrontMatter(path string, frontLines, bodyLines []string) error {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(strings.Join(frontLines, "\n"))
	b.WriteString("\n---\n")
	b.WriteString(strings.Join(bodyLines, "\n"))
	if len(bodyLines) > 0 && bodyLines[len(bodyLines)-1] != "" {
		b.WriteString("\n")
	}
	return atomicfs.WriteFile(path, []byte(b.String()), 0o644)
}
