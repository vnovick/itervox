package workflow

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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
var editMu sync.Map // path -> chan struct{} (capacity 1: a mutex with a timed acquire)

// lockForPath serializes a WORKFLOW.md read-modify-write both within this
// process (editMu) and across processes (an flock on the sidecar
// ".<base>.lock", CORE-149 — `itervox init --update` and `itervox models
// refresh` are separate processes from the daemon). Lock order is fixed:
// in-process lock first, then the file lock; the returned unlock releases
// them in reverse. Neither lock is reentrant — never call lockForPath (or a
// function that takes it) while holding it for the same path.
//
// Both acquisitions share one deadline, editLockTimeout (BH4): a writer
// queued in-process behind one that is itself waiting on a stuck foreign
// holder gives up at the same moment instead of each waiting a full timeout
// in turn. On timeout the error wraps ErrEditLockBusy and nothing is written.
func lockForPath(path string) (func(), error) {
	deadline := time.Now().Add(editLockTimeout)
	muIface, _ := editMu.LoadOrStore(path, make(chan struct{}, 1))
	mu := muIface.(chan struct{})
	timer := time.NewTimer(editLockTimeout)
	select {
	case mu <- struct{}{}:
		timer.Stop()
	case <-timer.C:
		return nil, editLockBusyError(lockFilePath(path), "another edit in this process")
	}
	unlockFile, err := lockFile(path, deadline)
	if err != nil {
		<-mu
		return nil, err
	}
	return func() {
		unlockFile()
		<-mu
	}, nil
}

// WithEditLock runs fn while holding the same in-process and inter-process
// lock as every patcher in this package. It is for WORKFLOW.md writers that
// cannot be expressed as a front-matter Mutator (the `itervox init --update`
// YAML re-encoders): fn must do its whole read-modify-write inside, and must
// not call any locking function of this package for the same path.
//
// Writes made inside fn are NOT registered as self-writes: if the running
// daemon itself ever used this, its watcher would reload — the safe default
// for a write whose value no in-memory setter applied.
func WithEditLock(path string, fn func() error) error {
	unlock, err := lockForPath(path)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// ApplyAndWriteFrontMatter reads the file at path, splits it into front
// matter and body, runs each mutator in sequence on the front-matter lines,
// reassembles the file, and writes it back atomically. If any mutator
// returns an error, the file is left untouched.
//
// Concurrent calls for the same path are serialized via lockForPath (editMu
// plus the inter-process sidecar flock) so that multi-tab editors and
// separate itervox processes cannot lose writes to read-modify-write races.
//
// The write is registered as a self-write (CORE-116): this process's
// WORKFLOW.md watcher will not reload for it, because every daemon caller
// applies the same value in memory under cfgMu. A caller whose value has
// no in-memory setter must use WriteAndReload instead.
func ApplyAndWriteFrontMatter(path string, mutators ...Mutator) error {
	return applyAndWrite(path, true, mutators)
}

func applyAndWrite(path string, registerSelfWrite bool, mutators []Mutator) error {
	if len(mutators) == 0 {
		return nil
	}
	unlock, err := lockForPath(path)
	if err != nil {
		return err
	}
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

	return writeLocked(path, data, []byte(renderFrontMatter(frontLines, bodyLines)), registerSelfWrite)
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

// MutateTrackerStates returns a Mutator that rewrites tracker.active_states,
// tracker.terminal_states, and tracker.completion_state inside the tracker:
// block. See PatchTrackerStates.
func MutateTrackerStates(active, terminal []string, completion string) Mutator {
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
			return nil, fmt.Errorf("workflow mutate tracker states: tracker block not found")
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
		return newFrontLines, nil
	}
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

// renderFrontMatter reassembles a WORKFLOW.md from front-matter and body
// lines (shared trailing-newline policy for every patcher in this package).
func renderFrontMatter(frontLines, bodyLines []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(strings.Join(frontLines, "\n"))
	b.WriteString("\n---\n")
	b.WriteString(strings.Join(bodyLines, "\n"))
	if len(bodyLines) > 0 && bodyLines[len(bodyLines)-1] != "" {
		b.WriteString("\n")
	}
	return b.String()
}

// writeLocked is the single WORKFLOW.md write primitive of this package. The
// caller holds lockForPath(path) and passes the bytes it read under that lock
// (pre) and the bytes to write (post). After a successful atomic write the
// (pre, post) transition is registered for the watcher's self-write
// suppression (CORE-116) unless registerSelfWrite is false.
func writeLocked(path string, pre, post []byte, registerSelfWrite bool) error {
	if err := atomicfs.WriteFile(path, post, 0o644); err != nil {
		return err
	}
	if registerSelfWrite {
		recordSelfWrite(path, pre, post)
	}
	return nil
}
