package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
)

// The "cross-review" preset (#79): the implementer runs on the chosen runner
// and every successful run is reviewed by two read-only reviewers, one on
// each backend — "Claude writes, Codex reviews" with `--runner claude`, the
// reverse with `--runner codex`. Each reviewer is a separate agent run.

// presetCrossReview is the --preset value.
const presetCrossReview = "cross-review"

// otherRunner is the backend the cross reviewer runs on.
func otherRunner(runner string) string {
	if runner == "codex" {
		return "claude"
	}
	return "codex"
}

// crossReviewerProfile is the reviewer profile on the other backend.
func crossReviewerProfile(runner string) string {
	return "reviewer-" + otherRunner(runner)
}

// lookPath is exec.LookPath, replaceable in tests.
var lookPath = exec.LookPath

// offerCrossReviewPreset asks, on a terminal, whether to use the preset when
// both agent CLIs are installed (the cross reviewer needs the other one).
// No terminal, or either CLI missing, means no question and no preset.
func offerCrossReviewPreset(runner string, terminal bool, in *bufio.Reader, out io.Writer) string {
	if !terminal {
		return ""
	}
	if _, err := lookPath("claude"); err != nil {
		return ""
	}
	if _, err := lookPath("codex"); err != nil {
		return ""
	}
	writer, reviewer := "Claude", "Codex"
	if runner == "codex" {
		writer, reviewer = "Codex", "Claude"
	}
	q := fmt.Sprintf("Use the cross-review preset (%s writes; %s and %s review every run, read-only; each review is a separate agent run)?", writer, reviewer, writer)
	if confirmPrompt(in, out, false, q) {
		return presetCrossReview
	}
	return ""
}
